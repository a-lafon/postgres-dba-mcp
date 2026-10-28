//go:build integration

package health

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// callHealth runs analyze_db_health through an in-memory MCP client against dsn.
func callHealth(t *testing.T, dsn string, healthType *string) (text string, isErr bool) {
	t.Helper()
	cfg, err := config.Parse([]string{dsn}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	reg := server.New(cfg, d, "test")
	Register(reg)

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := reg.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	params := &mcp.CallToolParams{Name: "analyze_db_health"}
	if healthType != nil {
		params.Arguments = map[string]any{"health_type": *healthType}
	}
	res, err := cs.CallTool(ctx, params)
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func str(s string) *string { return &s }

func admin(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func exec(t *testing.T, c *pgx.Conn, sql string, args ...any) {
	t.Helper()
	if _, err := c.Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// setVar lowers or raises a threshold for the duration of the test.
func setVar[T any](t *testing.T, p *T, v T) {
	t.Helper()
	old := *p
	*p = v
	t.Cleanup(func() { *p = old })
}

// makeDeadRows leaves health.bloated with 100 000 live and 40 000 dead rows, and waits for the statistics.
func makeDeadRows(t *testing.T, c *pgx.Conn) {
	t.Helper()
	ctx := context.Background()
	exec(t, c, "TRUNCATE health.bloated")
	// TRUNCATE keeps the dead rows counted by the seed's updates; the counters must restart from zero.
	exec(t, c, "SELECT pg_stat_reset_single_table_counters('health.bloated'::regclass)")
	exec(t, c, "INSERT INTO health.bloated SELECT i, i, 'x' FROM generate_series(1, 140000) AS i")
	exec(t, c, "DELETE FROM health.bloated WHERE id <= 40000")
	var v int
	if err := c.QueryRow(ctx, "SELECT current_setting('server_version_num')::int").Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v >= 150000 {
		exec(t, c, "SELECT pg_stat_force_next_flush()")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var dead int64
		if err := c.QueryRow(ctx, "SELECT n_dead_tup FROM pg_stat_user_tables WHERE schemaname = 'health' AND relname = 'bloated'").Scan(&dead); err != nil {
			t.Fatal(err)
		}
		if dead == 40000 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("statistics not flushed: n_dead_tup = %d", dead)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func hasLine(text, line string) bool {
	for _, l := range strings.Split(text, "\n") {
		if l == line {
			return true
		}
	}
	return false
}

func TestFixtureWithTraps(t *testing.T) {
	a := admin(t, testpg.Admin(t))
	makeDeadRows(t, a)
	exec(t, a, "SELECT pg_create_physical_replication_slot('health_test_slot')")
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), testpg.Admin(t))
		if err == nil {
			_, _ = c.Exec(context.Background(), "SELECT pg_drop_replication_slot('health_test_slot')")
			_ = c.Close(context.Background())
		}
	})

	text, isErr := callHealth(t, testpg.RO(t), nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	for _, line := range []string{
		"index        ! invalid: health.ix_invalid on health.dups",
		"             ! duplicate: health.ix_dup_a (account_id) covered by health.ix_dup_b (account_id, created_at)",
		"             ! duplicate on 3 partitions of health.payment_p: <partition>_customer_id_idx (customer_id) covered by idx_fk_<partition>_customer_id (customer_id); e.g. payment_p_2023",
		"vacuum       ! dead rows: health.bloated 28.6% (40k dead, 100k live), last vacuum never",
		"sequence     ! seqcol.narrow_id_seq (seqcol.narrow.id, integer): 97.8% used, 47M left",
		"             ! health.legacy_id_seq (health.legacy.id, integer): 91.0% used, 193M left",
		"replication  ! inactive slot health_test_slot (physical)",
		"constraint   ! health.orders_nv.amount_positive is NOT VALID",
		"             ! health.child.child_parent_fk referencing health.parent is NOT VALID",
	} {
		if !hasLine(text, line) {
			t.Errorf("missing line %q in:\n%s", line, text)
		}
	}
	for _, re := range []string{
		`(?m)^             ! rarely used \(<= 50 scans, over up to \d+ days? since \d{4}-\d{2}-\d{2} \d{2}:\d{2} UTC\): \d+ indexes, [\d.]+ [kMG]B: .*health\.ix_unused [\d.]+ [kM]B 0 scans`,
		`(?m)^             ! rarely used .*health\.<partition>_customer_id_idx \(3 partitions of health\.payment_p\) [\d.]+ [kM]?B \d+ scans?`,
		`(?m)^connection   OK \(cluster: \d+ total, \d+ idle in transaction\)$`,
		`(?m)^buffer       (OK \(cache hit [\d.]+% (tables|indexes).*\)|! .*)$`,
	} {
		if !regexp.MustCompile(re).MatchString(text) {
			t.Errorf("no match for %s in:\n%s", re, text)
		}
	}
	// The window comes from the server: it starts in the past and its length is not negative.
	if m := regexp.MustCompile(`over up to (-?\d+) days? since (\d{4}-\d{2}-\d{2} \d{2}:\d{2}) UTC`).FindStringSubmatch(text); m == nil {
		t.Errorf("no statistics window in:\n%s", text)
	} else if since, err := time.Parse("2006-01-02 15:04", m[2]); err != nil || since.After(time.Now()) || strings.HasPrefix(m[1], "-") {
		t.Errorf("statistics window %q since %q is not in the past (%v)", m[1], m[2], err)
	}
	if strings.Contains(text, "health.payment_p_2024_customer_id_idx") {
		t.Errorf("a partition index is reported on its own:\n%s", text)
	}
	// Sections come in the fixed order.
	last := -1
	for _, name := range []string{"\nindex ", "\nconnection ", "\nvacuum ", "\nsequence ", "\nreplication ", "\nbuffer ", "\nconstraint "} {
		i := strings.Index("\n"+text, name)
		if i <= last {
			t.Errorf("section %q out of order or missing", name)
		}
		last = i
	}
}

func TestHealthyDatabase(t *testing.T) {
	name := testpg.TempDB(t)
	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), nil)
	if isErr {
		t.Fatalf("error: %s", text)
	}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 7 {
		t.Fatalf("want 7 lines, got:\n%s", text)
	}
	for _, l := range lines {
		if !strings.HasPrefix(l[13:], "OK") {
			t.Errorf("not OK: %q", l)
		}
	}
	for _, line := range []string{"replication  OK (primary, 0 replicas, 0 slots)", "buffer       OK (no cache statistics yet)"} {
		if !hasLine(text, line) {
			t.Errorf("missing %q in:\n%s", line, text)
		}
	}
}

func TestBloatLoweredThreshold(t *testing.T) {
	setVar(t, &bloatMinBytes, 0)
	cfg, err := config.Parse([]string{testpg.RO(t)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	// The cap of 10 could hide the index of health.bloated among the others, so look at the section before the cap.
	var found bool
	err = d.ReadOnly(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		res, err := checkIndex(ctx, tx)
		if err != nil {
			return err
		}
		for _, it := range res.render().items {
			if it.kind == "bloated" && strings.Contains(it.text, " on health.bloated, ~") {
				found = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Error("no bloated item for health.bloated")
	}
}

func TestWraparoundLoweredThreshold(t *testing.T) {
	setVar(t, &xidThreshold, 2146483648)
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	rows, err := a.Query(context.Background(), `
SELECT n.nspname || '.' || c.relname, 2146483648 - GREATEST(AGE(c.relfrozenxid), AGE(t.relfrozenxid))
FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace LEFT JOIN pg_class t ON c.reltoastrelid = t.oid
WHERE c.relkind = 'r' AND (2146483648 - GREATEST(AGE(c.relfrozenxid), AGE(t.relfrozenxid))) < 2146483648
ORDER BY 2, n.nspname, c.relname`)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	var left []int64
	for rows.Next() {
		var n string
		var l int64
		if err := rows.Scan(&n, &l); err != nil {
			t.Fatal(err)
		}
		names, left = append(names, n), append(left, l)
	}
	rows.Close()
	if len(names) < 12 {
		t.Fatalf("only %d tables in an empty database", len(names))
	}

	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), str("vacuum"))
	if isErr {
		t.Fatalf("error: %s", text)
	}
	first := fmt.Sprintf("vacuum       ! wraparound: %s has %s transactions left (threshold %s)", names[0], format.Approx(left[0]), format.Approx(xidThreshold))
	if !strings.HasPrefix(text, first+"\n") {
		t.Errorf("first line want %q, got:\n%s", first, text)
	}
	more := fmt.Sprintf("             ! … and %d more wraparound\n", len(names)-10)
	if !strings.Contains(text, more) {
		t.Errorf("missing %q in:\n%s", more, text)
	}
}

func TestSequenceUnreadableSkipped(t *testing.T) {
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	exec(t, a, "CREATE SEQUENCE public.secret_seq AS integer")
	exec(t, a, "SELECT setval('public.secret_seq', 2000000000)")
	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), str("sequence"))
	if isErr || strings.Contains(text, "secret_seq") || strings.Contains(text, "check failed") {
		t.Errorf("unreadable sequence not skipped (error %v):\n%s", isErr, text)
	}
	// Once readable, the same sequence is reported.
	exec(t, a, "GRANT SELECT ON SEQUENCE public.secret_seq TO pgdba_ro")
	text, _ = callHealth(t, testpg.DSN(testpg.RO(t), name), str("sequence"))
	if !strings.Contains(text, "public.secret_seq (integer): 93.1% used") {
		t.Errorf("readable sequence not reported:\n%s", text)
	}
}

// A descending sequence consumes its range from max toward min.
func TestSequenceDescending(t *testing.T) {
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	for _, s := range []string{"down_quarter", "down_full"} {
		exec(t, a, "CREATE SEQUENCE public."+s+" INCREMENT -1 MINVALUE 1 MAXVALUE 100 START 100")
		exec(t, a, "GRANT SELECT ON SEQUENCE public."+s+" TO pgdba_ro")
	}
	exec(t, a, "SELECT setval('public.down_quarter', 75)")
	exec(t, a, "SELECT setval('public.down_full', 5)")
	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), str("sequence"))
	if isErr || strings.Contains(text, "down_quarter") || !strings.Contains(text, "public.down_full (bigint): 96.0% used, 4 left") {
		t.Errorf("descending sequences misread (error %v):\n%s", isErr, text)
	}
}

// TestBufferCulprits builds disk reads deterministically: VACUUM FULL writes the new heap through the storage layer,
// so its blocks are not in shared_buffers and the first sequential scan reads them from disk.
func TestBufferCulprits(t *testing.T) {
	name := testpg.TempDB(t)
	dsn := testpg.DSN(testpg.Admin(t), name)
	a := admin(t, dsn)
	for _, stmt := range []string{
		"CREATE TABLE big AS SELECT g AS id, repeat('x', 200) AS pad FROM generate_series(1, 20000) g",
		"CREATE TABLE tiny AS SELECT 1 AS id",
		"CREATE TABLE tree (id int, pad text) PARTITION BY RANGE (id)",
		"CREATE TABLE tree_1 PARTITION OF tree FOR VALUES FROM (0) TO (10000)",
		"CREATE TABLE tree_2 PARTITION OF tree FOR VALUES FROM (10000) TO (20000)",
		"INSERT INTO tree SELECT g, repeat('x', 200) FROM generate_series(0, 19999) g",
		"VACUUM FULL big", "VACUUM FULL tiny", "VACUUM FULL tree_1", "VACUUM FULL tree_2",
		"SELECT count(*) FROM big", "SELECT count(*) FROM tiny", "SELECT count(*) FROM tree",
	} {
		exec(t, a, stmt)
	}
	// Statistics reach other sessions when this one ends (and after a delay before PostgreSQL 15).
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The hit rate depends on the inserts above, so the check runs on its own and the line is rendered with a forced rate.
	ro, err := pgx.Connect(context.Background(), testpg.DSN(testpg.RO(t), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close(context.Background()) })
	var got bufferResult
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		tx, err := ro.Begin(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		res, err := checkBuffer(context.Background(), tx)
		_ = tx.Rollback(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got = res.(bufferResult)
		if len(got.Culprits) == 2 || time.Now().After(deadline) {
			break
		}
	}
	// big and tree read about the same number of blocks; tiny reads one, far below 10 %.
	got.Tables = fptr(0.5)
	s := got.render()
	s.name = "buffer"
	text := s.text()
	re := regexp.MustCompile(`^buffer       ! table cache hit 50.0% \(below 95%\): public\.(big|tree) \d+% of disk reads, \d+ seq scans?; public\.(tree|big) \d+% of disk reads, \d+ seq scans?\n`)
	if !re.MatchString(text) || strings.Contains(text, "tiny") || strings.Contains(text, "tree_") {
		t.Errorf("want public.big and public.tree (partitions folded into their root), not tiny: %+v\n%s", got.Culprits, text)
	}
}

// Four tables read about the same number of blocks, so each holds about 25 % of the reads: only three are named.
func TestBufferCulpritsAtMostThree(t *testing.T) {
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	for _, tbl := range []string{"t1", "t2", "t3", "t4"} {
		exec(t, a, "CREATE TABLE "+tbl+" AS SELECT g AS id, repeat('x', 200) AS pad FROM generate_series(1, 20000) g")
		exec(t, a, "VACUUM FULL "+tbl)
		exec(t, a, "SELECT count(*) FROM "+tbl)
	}
	if err := a.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	ro, err := pgx.Connect(context.Background(), testpg.DSN(testpg.RO(t), name))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ro.Close(context.Background()) })
	// Wait until the reads of all four tables are visible, so a missing cap cannot hide behind late statistics.
	for deadline := time.Now().Add(10 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		var n int
		if err := ro.QueryRow(context.Background(), "SELECT count(*) FROM pg_statio_user_tables WHERE heap_blks_read > 0").Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 4 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d tables with reads after 10 s", n)
		}
	}
	tx, err := ro.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	res, err := checkBuffer(context.Background(), tx)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.(bufferResult).Culprits; len(got) != 3 {
		t.Errorf("culprits = %+v, want 3", got)
	}
}

func TestMixedCaseNames(t *testing.T) {
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	exec(t, a, `CREATE SCHEMA "Billing"`)
	exec(t, a, `CREATE TABLE "Billing"."Legacy" ("Id" integer GENERATED BY DEFAULT AS IDENTITY)`)
	exec(t, a, `SELECT setval('"Billing"."Legacy_Id_seq"', 1954210119)`)
	exec(t, a, `GRANT USAGE ON SCHEMA "Billing" TO pgdba_ro`)
	exec(t, a, `GRANT SELECT ON "Billing"."Legacy" TO pgdba_ro`)
	exec(t, a, `GRANT SELECT ON SEQUENCE "Billing"."Legacy_Id_seq" TO pgdba_ro`)
	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), nil)
	if isErr || strings.Contains(text, "! check failed") {
		t.Errorf("error %v:\n%s", isErr, text)
	}
	if want := "sequence     ! Billing.Legacy_Id_seq (Billing.Legacy.Id, integer): 91.0% used, 193M left"; !hasLine(text, want) {
		t.Errorf("missing %q in:\n%s", want, text)
	}
}

func TestFailingCheckIsIsolated(t *testing.T) {
	cfg, err := config.Parse([]string{testpg.RO(t)}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	broken := check{"replication", func(ctx context.Context, tx pgx.Tx) (result, error) {
		_, err := tx.Exec(ctx, "SELECT * FROM pg_catalog.no_such_relation")
		return nil, err
	}}
	checks := []check{allChecks[1], broken, allChecks[6]}
	sections, err := runChecks(context.Background(), d, checks)
	if err != nil {
		t.Fatal(err)
	}
	text := renderReport(sections, 1<<20)
	if !strings.Contains(text, "replication  ! check failed: relation \"pg_catalog.no_such_relation\" does not exist\n") {
		t.Errorf("failure not reported:\n%s", text)
	}
	if !strings.Contains(text, "\nconnection ") && !strings.HasPrefix(text, "connection ") || !strings.Contains(text, "\nconstraint ") {
		t.Errorf("other sections missing:\n%s", text)
	}
}

// A column type can narrow a sequence's range to a single value; the share-used division would then fail,
// and runOne turns that into one "check failed" line for the whole section, hiding every real warning.
// A narrowed range must not be dropped either: a sequence whose range falls entirely past its column
// overflows on its very next value, which is the loudest thing this check has to say.
func TestSequenceSingleValueRange(t *testing.T) {
	name := testpg.TempDB(t)
	a := admin(t, testpg.DSN(testpg.Admin(t), name))
	exec(t, a, "CREATE TABLE public.narrow_one (c smallint)")
	exec(t, a, "CREATE SEQUENCE public.one_value_seq AS bigint MINVALUE 32767 MAXVALUE 100000 START 32767")
	exec(t, a, "ALTER SEQUENCE public.one_value_seq OWNED BY public.narrow_one.c")
	exec(t, a, "SELECT nextval('public.one_value_seq')")
	exec(t, a, "GRANT SELECT ON SEQUENCE public.one_value_seq TO pgdba_ro")
	// A sequence past the end of its column: every nextval overflows it.
	exec(t, a, "CREATE TABLE public.narrow_two (c integer)")
	exec(t, a, "CREATE SEQUENCE public.beyond_seq AS bigint MINVALUE 3000000000 MAXVALUE 4000000000 START 3000000000")
	exec(t, a, "ALTER SEQUENCE public.beyond_seq OWNED BY public.narrow_two.c")
	exec(t, a, "SELECT nextval('public.beyond_seq')")
	exec(t, a, "GRANT SELECT ON SEQUENCE public.beyond_seq TO pgdba_ro")
	// A third sequence, genuinely nearly full: it is what the first one would hide.
	exec(t, a, "CREATE SEQUENCE public.nearly_full AS integer START 2000000000")
	exec(t, a, "SELECT nextval('public.nearly_full')")
	exec(t, a, "GRANT SELECT ON SEQUENCE public.nearly_full TO pgdba_ro")

	text, isErr := callHealth(t, testpg.DSN(testpg.RO(t), name), str("sequence"))
	if isErr || strings.Contains(text, "check failed") {
		t.Fatalf("sequence check failed (error %v):\n%s", isErr, text)
	}
	if !strings.Contains(text, "public.nearly_full (integer): 93.1% used") {
		t.Errorf("the sequence that matters is missing:\n%s", text)
	}
	if !strings.Contains(text, "public.beyond_seq (public.narrow_two.c, integer): 100.0% used") {
		t.Errorf("the sequence that already overflows its column is missing:\n%s", text)
	}
}
