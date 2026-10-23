//go:build integration

package indexadv

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

const (
	eventsQuery = "SELECT * FROM events WHERE account_id = 42 ORDER BY created_at DESC LIMIT 20"
	eventsIndex = "CREATE INDEX ON public.events (account_id, created_at);"
)

// call runs a tool through an in-memory MCP client on the database of dsn.
func call(t *testing.T, dsn, tool string, args map[string]any) (string, bool) {
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func queryTool(t *testing.T, queries ...string) string {
	t.Helper()
	text, isErr := call(t, testpg.RO(t), "analyze_query_indexes", map[string]any{"queries": queries})
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	return text
}

var duration = regexp.MustCompile(`, \d+(\.\d+)? m?s(\n|$)`)

func exec(t *testing.T, dsn string, stmts ...string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	for _, s := range stmts {
		if _, err := c.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}
}

// tempDB creates a database with hypopg and the tables of setup, readable by pgdba_ro, and returns its name.
func tempDB(t *testing.T, hypopg bool, setup ...string) string {
	t.Helper()
	name := testpg.TempDB(t)
	admin := testpg.DSN(testpg.Admin(t), name)
	if hypopg {
		exec(t, admin, "CREATE EXTENSION hypopg")
	}
	exec(t, admin, setup...)
	exec(t, admin, "GRANT USAGE ON SCHEMA public TO pgdba_ro", "GRANT SELECT ON ALL TABLES IN SCHEMA public TO pgdba_ro")
	return name
}

// useOf resolves queries on the database name as the tool does and returns their candidates.
func useOf(t *testing.T, name string, sqls ...string) (columnUse, []candidate) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testpg.DSN(testpg.RO(t), name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var qs []*query
	for i, s := range sqls {
		q := &query{SQL: s, Label: "Q" + strconv.Itoa(i+1), idx: i}
		if q.rels, err = safesql.Relations(s); err != nil {
			t.Fatal(err)
		}
		qs = append(qs, q)
	}
	tables, err := resolve(ctx, tx, qs)
	if err != nil {
		t.Fatal(err)
	}
	qs = bind(qs, tables)
	use, err := collectUse(qs, tables)
	if err != nil {
		t.Fatal(err)
	}
	existing, err := existingIndexes(ctx, tx, use)
	if err != nil {
		t.Fatal(err)
	}
	return use, candidates(use, existing)
}

func columnsOf(use columnUse, table string) map[string]bool {
	out := map[string]bool{}
	for tb, cols := range use {
		if tb.Name == table {
			for c := range cols {
				out[c] = true
			}
		}
	}
	return out
}

func TestRecommendedIndex(t *testing.T) {
	text := queryTool(t, eventsQuery)
	if !strings.Contains(text, eventsIndex) {
		t.Fatalf("missing %q in:\n%s", eventsIndex, text)
	}
	if !strings.HasPrefix(text, "Workload cost ") || !strings.Contains(text, "#,cost_change,size,ddl\n") ||
		!strings.Contains(text, "Per query: Q1 -") {
		t.Errorf("unexpected shape:\n%s", text)
	}
	for _, bad := range []string{"{", "[", "trace", "map["} {
		if strings.Contains(text, bad) {
			t.Errorf("internal structure %q in:\n%s", bad, text)
		}
	}
	if strings.Contains(text, "(created_at, account_id)") {
		t.Errorf("reversed column order in:\n%s", text)
	}
}

// hypopg installed outside the search_path: the reset fails and aborts the transaction, which must surface as an
// error, not as "no candidate lowers the workload cost".
func TestHypopgOutsideSearchPath(t *testing.T) {
	name := tempDB(t, false, "CREATE TABLE events (id int PRIMARY KEY, account_id int)",
		"INSERT INTO events SELECT i, i % 100 FROM generate_series(1, 10000) i", "ANALYZE events",
		"CREATE SCHEMA ext", "CREATE EXTENSION hypopg SCHEMA ext")
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes",
		map[string]any{"queries": []string{"SELECT * FROM events WHERE account_id = 42"}})
	if !isErr || !strings.Contains(text, "hypopg_reset() does not exist") {
		t.Errorf("want a reset error, got (error %v):\n%s", isErr, text)
	}
}

func TestAlreadyOptimal(t *testing.T) {
	text := queryTool(t, "SELECT * FROM events WHERE id = 42")
	if !strings.HasPrefix(text, "No index recommendation: no candidate lowers the workload cost by at least 10%.\nWorkload cost ") {
		t.Errorf("got:\n%s", text)
	}
}

func TestSizeBudget(t *testing.T) {
	small, isErr := call(t, testpg.RO(t), "analyze_query_indexes", map[string]any{"queries": []string{eventsQuery}, "max_index_size_mb": 1})
	if isErr || strings.Contains(small, "CREATE INDEX") {
		t.Errorf("a 1 MB budget must not fit the index (error %v):\n%s", isErr, small)
	}
	big := queryTool(t, eventsQuery)
	if !strings.Contains(big, "CREATE INDEX") {
		t.Errorf("default budget:\n%s", big)
	}
}

func TestSkippedQueries(t *testing.T) {
	text := queryTool(t, "SELECT * FROM events WHERE account_id = 1", "DELETE FROM events")
	want := "Skipped: Q2 (restricted mode: DeleteStmt statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW))"
	if !strings.Contains(text, want) {
		t.Errorf("missing %q in:\n%s", want, text)
	}
	// Q1 stays Q1 and is analyzed.
	if !strings.Contains(text, "Per query: Q1 ") && !strings.Contains(text, "evaluated") {
		t.Errorf("Q1 not analyzed:\n%s", text)
	}
}

func TestCatalogQueryDroppedSilently(t *testing.T) {
	text := queryTool(t, "SELECT * FROM pg_class WHERE relname = 'x'", eventsQuery)
	if strings.Contains(text, "Skipped") || !strings.Contains(text, eventsIndex) {
		t.Errorf("got:\n%s", text)
	}
}

// pg_stat_statements writes interval '1 year' as `interval $1`, which Postgres rejects: the query must be
// analyzed, not skipped with `syntax error at or near "$1"`.
func TestTypedLiteralParams(t *testing.T) {
	for _, q := range []string{
		"SELECT count(*) FROM events WHERE created_at < now() - interval $1",
		"SELECT count(*) FROM events WHERE created_at < now() - interval $1 day to second",
		"SELECT count(*) FROM events WHERE created_at < now() - interval $1 second(3)",
		"SELECT count(*) FROM events WHERE created_at < timestamp with time zone $1 AND id < int4 $2 AND id > numeric(9,2) $3",
	} {
		t.Run(q, func(t *testing.T) {
			text := queryTool(t, q)
			if strings.Contains(text, "Skipped") || !strings.Contains(text, "Workload cost") {
				t.Errorf("got:\n%s", text)
			}
		})
	}
}

func TestWorkloadTypedLiteral(t *testing.T) {
	lowerThresholds(t)
	resetStatements(t)
	repeat(t, testpg.RO(t), 60, "SELECT count(*) FROM events WHERE created_at < now() - interval '1 year'")
	text, isErr := call(t, testpg.RO(t), "analyze_workload_indexes", map[string]any{})
	if isErr {
		t.Fatal(text)
	}
	if strings.Contains(text, "Skipped") || !strings.Contains(text, "Workload cost") {
		t.Errorf("got:\n%s", text)
	}
}

func TestParamsBeyondNine(t *testing.T) {
	text := queryTool(t, "SELECT * FROM events WHERE id IN ($1,$2,$3,$4,$5,$6,$7,$8,$9) AND account_id = $10 AND kind = $11 AND created_at > $12")
	if strings.Contains(text, "Skipped") || !strings.Contains(text, "Workload cost") {
		t.Errorf("got:\n%s", text)
	}
}

// A parameterized query must be priced on its generic plan, not on NULL values folded into the plan.
func TestParamsPriced(t *testing.T) {
	text := queryTool(t, "SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2")
	if !strings.Contains(text, eventsIndex) {
		t.Errorf("missing %q in:\n%s", eventsIndex, text)
	}
}

func TestTwoRuns(t *testing.T) {
	qs := []string{eventsQuery, "SELECT * FROM orders WHERE status = 'pending' AND account_id = 7 ORDER BY created_at"}
	a := duration.ReplaceAllString(queryTool(t, qs...), "$2")
	b := duration.ReplaceAllString(queryTool(t, qs...), "$2")
	if a != b {
		t.Errorf("runs differ:\n%s\n---\n%s", a, b)
	}
	if !strings.Contains(a, eventsIndex) {
		t.Errorf("missing events index:\n%s", a)
	}
}

func TestTimeBudget(t *testing.T) {
	old := timeBudget
	timeBudget = time.Millisecond
	t.Cleanup(func() { timeBudget = old })
	start := time.Now()
	text := queryTool(t, "SELECT * FROM events e JOIN orders o ON o.account_id = e.account_id WHERE e.kind = 'a' AND e.created_at > now() AND o.status = 'x' AND o.amount > 1 AND o.created_at < now()")
	if !strings.Contains(text, budgetLine) {
		t.Errorf("missing budget line:\n%s", text)
	}
	if time.Since(start) > 10*time.Second {
		t.Errorf("took %s", time.Since(start))
	}
}

func TestCandidateColumns(t *testing.T) {
	name := tempDB(t, true,
		"CREATE TABLE a (id int, x int)", "CREATE TABLE b (id int, a_id int, x int)",
		"CREATE TABLE c (id int, b_id int, kind text, big text)",
		"CREATE INDEX ON b (x)",
		"INSERT INTO c SELECT i, i, 'k', repeat('x', 100) FROM generate_series(1, 100) i",
		"ANALYZE")

	t.Run("nested join condition", func(t *testing.T) {
		use, _ := useOf(t, name, "SELECT * FROM a JOIN b ON a.id = b.a_id JOIN c ON c.b_id = b.id WHERE c.kind = 'x'")
		for table, cols := range map[string][]string{"b": {"a_id", "id"}, "c": {"b_id", "kind"}, "a": {"id"}} {
			got := columnsOf(use, table)
			for _, c := range cols {
				if !got[c] {
					t.Errorf("%s.%s missing from %v", table, c, got)
				}
			}
		}
	})
	t.Run("select list columns ignored", func(t *testing.T) {
		use, _ := useOf(t, name, "SELECT x FROM a WHERE id = 1")
		if cols := columnsOf(use, "a"); cols["x"] || !cols["id"] {
			t.Errorf("got %v", cols)
		}
	})
	t.Run("unqualified column beside a subquery", func(t *testing.T) {
		use, _ := useOf(t, name, "SELECT * FROM a JOIN (SELECT id AS bid FROM b) s ON s.bid = a.id WHERE x = 1")
		if cols := columnsOf(use, "a"); !cols["x"] || !cols["id"] {
			t.Errorf("got %v", cols)
		}
	})
	t.Run("existing index excluded", func(t *testing.T) {
		_, cands := useOf(t, name, "SELECT * FROM b WHERE x = 1 AND a_id = 2")
		got := describeAll(cands)
		if fmt.Sprint(got) != "[b(a_id) b(x,a_id)]" {
			t.Errorf("got %v", got)
		}
	})
	t.Run("long text column excluded", func(t *testing.T) {
		use, _ := useOf(t, name, "SELECT * FROM c WHERE big = 'x' AND kind = 'k'")
		if cols := columnsOf(use, "c"); cols["big"] || !cols["kind"] {
			t.Errorf("got %v", cols)
		}
	})
	t.Run("equality before sort column", func(t *testing.T) {
		_, cands := useOf(t, testpgName(t), eventsQuery)
		got := fmt.Sprint(describeAll(cands))
		if !strings.Contains(got, "events(account_id,created_at)") || strings.Contains(got, "events(created_at,account_id)") {
			t.Errorf("got %s", got)
		}
	})
}

// testpgName is the database of the fixture DSN.
func testpgName(t *testing.T) string {
	t.Helper()
	cfg, err := pgx.ParseConfig(testpg.RO(t))
	if err != nil {
		t.Fatal(err)
	}
	return cfg.Database
}

func TestNeverAnalyzed(t *testing.T) {
	name := tempDB(t, true,
		"CREATE TABLE t (id int, x int) WITH (autovacuum_enabled = false)",
		"INSERT INTO t SELECT i, i FROM generate_series(1, 1000) i")
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{"queries": []string{"SELECT * FROM t WHERE x = 1"}})
	if !isErr || text != "ERROR: tables never analyzed: public.t; run ANALYZE on them first" {
		t.Errorf("got %v %q", isErr, text)
	}
	exec(t, testpg.DSN(testpg.Admin(t), name), "ANALYZE t")
	// Before PG 15 the statistics collector reports ANALYZE with a delay.
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		text, isErr = call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{"queries": []string{"SELECT * FROM t WHERE x = 1"}})
		if !isErr || time.Now().After(deadline) {
			break
		}
	}
	if isErr {
		t.Errorf("after ANALYZE: %s", text)
	}
}

func TestHypopgMissing(t *testing.T) {
	name := tempDB(t, false, "CREATE TABLE t (id int)")
	var ver string
	conn, err := pgx.Connect(context.Background(), testpg.RO(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := conn.QueryRow(context.Background(), "SHOW server_version_num").Scan(&ver); err != nil {
		t.Fatal(err)
	}
	n, _ := strconv.Atoi(ver)
	want := fmt.Sprintf("ERROR: index analysis needs the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-%d-hypopg package)", n/10000)
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{"queries": []string{"SELECT * FROM t WHERE id = 1"}})
	if !isErr || text != want {
		t.Errorf("got %v %q, want %q", isErr, text, want)
	}
}

func TestValidationErrors(t *testing.T) {
	ro := testpg.RO(t)
	tests := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"analyze_query_indexes", map[string]any{"queries": []string{}}, "ERROR: Please provide a non-empty list of queries to analyze."},
		{"analyze_query_indexes", map[string]any{"queries": make([]string, 11)}, "ERROR: Please provide a list of up to 10 queries to analyze."},
		{"analyze_workload_indexes", map[string]any{"method": "llm"}, `ERROR: method "llm" is not supported by postgres-dba-mcp; use "dta"`},
		{"analyze_workload_indexes", map[string]any{"max_index_size_mb": 0}, "ERROR: max_index_size_mb must be at least 1"},
	}
	for _, tc := range tests {
		if text, isErr := call(t, ro, tc.tool, tc.args); !isErr || text != tc.want {
			t.Errorf("%s %v: got %v %q", tc.tool, tc.args, isErr, text)
		}
	}
}

// resetStatements clears pg_stat_statements as admin. Safe because integration packages run one at a time.
func resetStatements(t *testing.T) {
	t.Helper()
	exec(t, testpg.Admin(t), "SELECT pg_stat_statements_reset()")
}

func lowerThresholds(t *testing.T) {
	t.Helper()
	oldCalls, oldMean := minCalls, minMeanMs
	minCalls, minMeanMs = 50, 0
	t.Cleanup(func() { minCalls, minMeanMs = oldCalls, oldMean })
}

func repeat(t *testing.T, dsn string, n int, sql string) {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	for range n {
		if _, err := c.Exec(ctx, sql, pgx.QueryExecModeSimpleProtocol); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWorkloadTool(t *testing.T) {
	lowerThresholds(t)
	resetStatements(t)
	repeat(t, testpg.RO(t), 60, eventsQuery)
	text, isErr := call(t, testpg.RO(t), "analyze_workload_indexes", map[string]any{})
	if isErr {
		t.Fatal(text)
	}
	if !strings.Contains(text, eventsIndex) {
		t.Errorf("missing %q in:\n%s", eventsIndex, text)
	}
	if !strings.Contains(text, "Q1 (calls 60, mean ") || !strings.Contains(text, "SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2") {
		t.Errorf("missing workload query line:\n%s", text)
	}
}

func TestWorkloadEmpty(t *testing.T) {
	resetStatements(t)
	text, isErr := call(t, testpg.RO(t), "analyze_workload_indexes", map[string]any{})
	if isErr || text != "No query in pg_stat_statements for this database has calls >= 50 and mean time >= 5 ms." {
		t.Errorf("got %v %q", isErr, text)
	}
}

func TestWorkloadCurrentDatabaseOnly(t *testing.T) {
	lowerThresholds(t)
	resetStatements(t)
	other := tempDB(t, false, "CREATE TABLE t (id int)")
	repeat(t, testpg.DSN(testpg.RO(t), other), 60, "SELECT id FROM t WHERE id = 1 /* other_db_marker */")
	repeat(t, testpg.RO(t), 60, "SELECT id FROM events WHERE id = 1 /* this_db_marker */")

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testpg.RO(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qs, err := loadWorkload(ctx, tx)
	if err != nil {
		t.Fatal(err)
	}
	var this bool
	for _, q := range qs {
		if strings.Contains(q.SQL, "other_db_marker") {
			t.Errorf("query of another database in the workload: %s", q.SQL)
		}
		this = this || strings.Contains(q.SQL, "this_db_marker")
	}
	if !this {
		t.Errorf("query of the current database missing from %d queries", len(qs))
	}
}

// The composite index serves the range and the ORDER BY of the workload as well as the equality, at a planner
// cost the narrow index nearly matches: the advisor must recommend the composite, not (account_id) alone.
func TestCompositePreferred(t *testing.T) {
	text := queryTool(t,
		"SELECT count(*) FROM events WHERE created_at < now() - interval '30 days'",
		"SELECT count(*) FROM public.events WHERE account_id = 42 AND created_at > '2025-02-01'::timestamptz",
		"SELECT id, kind FROM public.events WHERE account_id = 42 AND created_at < '2025-02-01'::timestamptz ORDER BY created_at DESC LIMIT 20")
	if !strings.Contains(text, eventsIndex) || strings.Contains(text, "events (account_id);") {
		t.Errorf("want only %q in:\n%s", eventsIndex, text)
	}
}

// An index after the first one must lower the workload cost by 1 % of the base cost, even if it halves the
// cost of its own query.
func TestSmallGainDropped(t *testing.T) {
	name := tempDB(t, true,
		"CREATE TABLE big (id int, k int, pad text)", "CREATE TABLE tiny (id int, k int)",
		"INSERT INTO big SELECT i, i % 50000, repeat('x', 50) FROM generate_series(1, 200000) i",
		"INSERT INTO tiny SELECT i, i FROM generate_series(1, 2000) i",
		"ANALYZE")
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{
		"queries": []string{"SELECT * FROM big WHERE k = 7", "SELECT * FROM tiny WHERE k = 7"}})
	if isErr {
		t.Fatal(text)
	}
	if !strings.Contains(text, "CREATE INDEX ON public.big (k);") || strings.Contains(text, "\n2,") ||
		!strings.Contains(text, "\nNot recommended") {
		t.Errorf("got:\n%s", text)
	}
}

// A partitioned table has no storage of its own: its size, which weighs every candidate, is that of its partitions.
func TestPartitionedTableRecommended(t *testing.T) {
	name := tempDB(t, true,
		"CREATE TABLE p (id int, k int, pad text) PARTITION BY RANGE (id)",
		"CREATE TABLE p1 PARTITION OF p FOR VALUES FROM (1) TO (100001)",
		"CREATE TABLE p2 PARTITION OF p FOR VALUES FROM (100001) TO (200001)",
		"INSERT INTO p SELECT i, i % 50000, repeat('x', 50) FROM generate_series(1, 200000) i",
		"ANALYZE")
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{
		"queries": []string{"SELECT * FROM p WHERE k = 7"}})
	if isErr || !strings.Contains(text, "CREATE INDEX ON public.p (k);") {
		t.Errorf("got %v:\n%s", isErr, text)
	}
}

// A query on another table that the picks leave unserved is named with the index that would serve it, even
// though that index is refused for its small share of the workload.
func TestUnservedQueryNamed(t *testing.T) {
	name := tempDB(t, true,
		"CREATE TABLE big (id int, k int, pad text)", "CREATE TABLE tiny (id int, k int)",
		"INSERT INTO big SELECT i, i % 50000, repeat('x', 50) FROM generate_series(1, 200000) i",
		"INSERT INTO tiny SELECT i, i FROM generate_series(1, 2000) i",
		"ANALYZE")
	text, isErr := call(t, testpg.DSN(testpg.RO(t), name), "analyze_query_indexes", map[string]any{
		"queries": []string{"SELECT * FROM big WHERE k = 7", "SELECT * FROM tiny WHERE k = 7"}})
	if isErr {
		t.Fatal(text)
	}
	re := regexp.MustCompile(`(?m)^Not recommended, small workload share: CREATE INDEX ON public\.tiny \(k\); Q2 -\d+\.\d%, workload -0\.\d%, [\d.]+ [kM]?B$`)
	if !re.MatchString(text) || strings.Contains(text, "\n2,") {
		t.Errorf("got:\n%s", text)
	}
}
