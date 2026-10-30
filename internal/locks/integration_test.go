//go:build integration

package locks

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// callLocks runs analyze_locks through an in-memory MCP client against dsn.
func callLocks(t *testing.T, dsn string, args ...string) (text string, isErr bool) {
	t.Helper()
	cfg, err := config.Parse(append(args, dsn), func(string) string { return "" })
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "analyze_locks"})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func suffix(t *testing.T) string {
	t.Helper()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b[:])
}

func conn(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

func pid(t *testing.T, c *pgx.Conn) int32 {
	t.Helper()
	var p int32
	if err := c.QueryRow(context.Background(), "SELECT pg_backend_pid()").Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

// contend holds a throwaway table read in one open transaction and makes a second session wait for
// ACCESS EXCLUSIVE on it, which is the lock the report names.
// Both sessions belong to the superuser, so a role without pg_read_all_stats cannot read their details.
// It returns once the wait is visible in pg_locks, and releases everything when the test ends.
func contend(t *testing.T) (table string, holder, waiter int32) {
	t.Helper()
	ctx := context.Background()
	admin := testpg.Admin(t)
	table = "locks_it_" + suffix(t)
	q := pgx.Identifier{table}.Sanitize()

	setup := conn(t, admin)
	if _, err := setup.Exec(ctx, "CREATE TABLE "+q+" (id int)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("drop %s: connect: %v", table, err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(context.Background(), "DROP TABLE IF EXISTS "+q); err != nil {
			t.Errorf("drop %s: %v", table, err)
		}
	})

	hc := conn(t, admin)
	holder = pid(t, hc)
	tx, err := hc.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "SELECT count(*) FROM "+q); err != nil {
		t.Fatal(err)
	}

	wc := conn(t, admin)
	waiter = pid(t, wc)
	// A lock_timeout makes the waiter give up on its own if the holder is never released.
	if _, err := wc.Exec(ctx, "SET lock_timeout = '30s'"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = wc.Exec(context.Background(), "BEGIN; LOCK TABLE "+q+" IN ACCESS EXCLUSIVE MODE")
	}()
	// Releasing the holder first lets the waiter finish, so a failed assertion cannot wedge the run on the
	// table drop that comes after.
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		<-done
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		if err := setup.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE pid = $1 AND NOT granted", waiter).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return table, holder, waiter
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiting session never showed an ungranted lock")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLocksReportsTheChain(t *testing.T) {
	table, holder, waiter := contend(t)
	text, isErr := callLocks(t, testpg.RO(t))
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	want := fmt.Sprintf("%d,", waiter)
	line := findRow(t, text, want)
	fields := strings.Split(line, ",")
	if len(fields) != 8 {
		t.Fatalf("row %q has %d fields", line, len(fields))
	}
	if fields[2] != "relation AccessExclusiveLock on public."+table {
		t.Errorf("lock = %q", fields[2])
	}
	if fields[3] != fmt.Sprint(holder) || fields[4] != fmt.Sprint(holder) {
		t.Errorf("blocking_pid = %q, root_pid = %q, want %d", fields[3], fields[4], holder)
	}
	if !strings.Contains(text, fmt.Sprintf("root pid %d", holder)) {
		t.Errorf("the note does not name the root:\n%s", text)
	}
}

// findRow returns the first line of text starting with prefix.
func findRow(t *testing.T, text, prefix string) string {
	t.Helper()
	for _, l := range strings.Split(text, "\n") {
		if strings.HasPrefix(l, prefix) {
			return l
		}
	}
	t.Fatalf("no row starting with %q in:\n%s", prefix, text)
	return ""
}

func TestLocksWithoutReadAllStats(t *testing.T) {
	table, holder, waiter := contend(t)
	dsn, role := noStatsRole(t)
	text, isErr := callLocks(t, dsn)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	line := findRow(t, text, fmt.Sprintf("%d,", waiter))
	wantRow := fmt.Sprintf(`%d,\N,relation AccessExclusiveLock on public.%s,%d,%d,\N,\N,\N`, waiter, table, holder, holder)
	if line != wantRow {
		t.Errorf("row = %q, want %q", line, wantRow)
	}
	wantNote := "-- session details hidden: grant pg_read_all_stats to " + role + " to read waits, states and query texts"
	if !strings.Contains(text, wantNote) {
		t.Errorf("no note %q in:\n%s", wantNote, text)
	}
}

// noStatsRole creates a login role that is not a member of pg_read_all_stats and returns its DSN and name.
func noStatsRole(t *testing.T) (dsn, role string) {
	t.Helper()
	ctx := context.Background()
	admin := testpg.Admin(t)
	c := conn(t, admin)
	role = "locks_it_" + suffix(t)
	q := pgx.Identifier{role}.Sanitize()
	var dbname string
	if err := c.QueryRow(ctx, "SELECT current_database()").Scan(&dbname); err != nil {
		t.Fatal(err)
	}
	dbq := pgx.Identifier{dbname}.Sanitize()
	if _, err := c.Exec(ctx, "CREATE ROLE "+q+" LOGIN PASSWORD 'pgdba'"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cc, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("drop role %s: connect: %v", role, err)
			return
		}
		defer func() { _ = cc.Close(context.Background()) }()
		for _, stmt := range []string{"REVOKE ALL ON DATABASE " + dbq + " FROM " + q, "DROP ROLE " + q} {
			if _, err := cc.Exec(context.Background(), stmt); err != nil {
				t.Errorf("%s: %v", stmt, err)
			}
		}
	})
	if _, err := c.Exec(ctx, "GRANT CONNECT ON DATABASE "+dbq+" TO "+q); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.User = url.UserPassword(role, "pgdba")
	return u.String(), role
}

// rowConflict makes two sessions update the same row. The waiter then blocks on the holder's transaction id,
// a lock that names no relation: the most common contention there is, and the one the report cannot label.
func rowConflict(t *testing.T) (holder, waiter int32) {
	t.Helper()
	ctx := context.Background()
	admin := testpg.Admin(t)
	table := "locks_it_" + suffix(t)
	q := pgx.Identifier{table}.Sanitize()

	setup := conn(t, admin)
	if _, err := setup.Exec(ctx, "CREATE TABLE "+q+" (id int primary key)"); err != nil {
		t.Fatal(err)
	}
	if _, err := setup.Exec(ctx, "INSERT INTO "+q+" VALUES (1)"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("drop %s: connect: %v", table, err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(context.Background(), "DROP TABLE IF EXISTS "+q); err != nil {
			t.Errorf("drop %s: %v", table, err)
		}
	})

	hc := conn(t, admin)
	holder = pid(t, hc)
	tx, err := hc.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "UPDATE "+q+" SET id = id WHERE id = 1"); err != nil {
		t.Fatal(err)
	}

	wc := conn(t, admin)
	waiter = pid(t, wc)
	if _, err := wc.Exec(ctx, "SET lock_timeout = '30s'"); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = wc.Exec(context.Background(), "BEGIN; UPDATE "+q+" SET id = id WHERE id = 1")
	}()
	t.Cleanup(func() {
		_ = tx.Rollback(context.Background())
		<-done
	})

	deadline := time.Now().Add(20 * time.Second)
	for {
		var n int
		if err := setup.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE pid = $1 AND NOT granted", waiter).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 0 {
			return holder, waiter
		}
		if time.Now().After(deadline) {
			t.Fatal("the waiting session never showed an ungranted lock")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// duration is the HH:MM:SS the ::interval(0)::text cast produces, with the day prefix PostgreSQL adds past 24 h.
var duration = regexp.MustCompile(`^(\d+ days? )?\d{2}:\d{2}:\d{2}$`)

func TestLocksRowConflictNamesNoRelationAndFormatsDurations(t *testing.T) {
	holder, waiter := rowConflict(t)
	text, isErr := callLocks(t, testpg.RO(t))
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	fields := strings.Split(findRow(t, text, fmt.Sprintf("%d,", waiter)), ",")
	if len(fields) != 8 {
		t.Fatalf("row has %d fields: %v", len(fields), fields)
	}
	// A row-level conflict is a wait on the holder's transaction id, which names no table.
	if fields[2] != "transactionid ShareLock" {
		t.Errorf("lock = %q, want %q", fields[2], "transactionid ShareLock")
	}
	if fields[3] != fmt.Sprint(holder) {
		t.Errorf("blocking_pid = %q, want %d", fields[3], holder)
	}
	for _, f := range []struct{ name, got string }{{"waiting", fields[1]}, {"tx_age", fields[6]}} {
		if !duration.MatchString(f.got) {
			t.Errorf("%s = %q, not an HH:MM:SS duration", f.name, f.got)
		}
	}
}

// TestLocksOrdersTheLongestWaitFirst covers the ordering rule, which lives in locks.sql and so is invisible
// to the renderer's tests. Two independent contentions, the first started a good second before the second.
func TestLocksOrdersTheLongestWaitFirst(t *testing.T) {
	_, _, first := contend(t)
	time.Sleep(1500 * time.Millisecond)
	_, second := rowConflict(t)

	text, isErr := callLocks(t, testpg.RO(t))
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	var order []int32
	for _, l := range strings.Split(text, "\n") {
		for _, p := range []int32{first, second} {
			if strings.HasPrefix(l, fmt.Sprintf("%d,", p)) {
				order = append(order, p)
			}
		}
	}
	if len(order) != 2 || order[0] != first || order[1] != second {
		t.Errorf("rows ordered %v, want the older wait (%d) before %d:\n%s", order, first, second, text)
	}
}

// TestLocksNoInheritMemberMatchesTheData pins the privilege test on the case that splits membership from
// privilege. PostgreSQL 14 masks pg_stat_activity on mere membership, so a NOINHERIT member reads the
// details there; 15 and up mask on has_privs_of_role, so the same role reads nothing. The tool must be right
// on both, and the property that holds either way is that the note goes out exactly when a cell is hidden.
func TestLocksNoInheritMemberMatchesTheData(t *testing.T) {
	_, _, waiter := contend(t)
	dsn, role := noStatsRole(t)
	c := conn(t, testpg.Admin(t))
	q := pgx.Identifier{role}.Sanitize()
	for _, stmt := range []string{"ALTER ROLE " + q + " NOINHERIT", "GRANT pg_read_all_stats TO " + q} {
		if _, err := c.Exec(context.Background(), stmt); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		cc, err := pgx.Connect(context.Background(), testpg.Admin(t))
		if err != nil {
			t.Errorf("revoke from %s: connect: %v", role, err)
			return
		}
		defer func() { _ = cc.Close(context.Background()) }()
		if _, err := cc.Exec(context.Background(), "REVOKE pg_read_all_stats FROM "+q); err != nil {
			t.Errorf("revoke pg_read_all_stats from %s: %v", role, err)
		}
	})

	text, isErr := callLocks(t, dsn)
	if isErr {
		t.Fatalf("tool error: %s", text)
	}
	fields := strings.Split(findRow(t, text, fmt.Sprintf("%d,", waiter)), ",")
	hidden := fields[1] == `\N` && fields[5] == `\N`
	noted := strings.Contains(text, "-- session details hidden: grant pg_read_all_stats to "+role)
	if hidden != noted {
		t.Errorf("hidden cells = %v but note = %v, the report contradicts itself:\n%s", hidden, noted, text)
	}
}

func TestLocksWithNoContention(t *testing.T) {
	for _, mode := range []string{"restricted", "unrestricted"} {
		t.Run(mode, func(t *testing.T) {
			text, isErr := callLocks(t, testpg.RO(t), "--access-mode="+mode)
			if isErr {
				t.Fatalf("tool error: %s", text)
			}
			if text != "-- no lock wait in progress\n" {
				t.Errorf("got %q", text)
			}
		})
	}
}

func TestLocksUnrestrictedReportsTheSameChain(t *testing.T) {
	_, holder, waiter := contend(t)
	restricted, _ := callLocks(t, testpg.RO(t))
	unrestricted, _ := callLocks(t, testpg.RO(t), "--access-mode=unrestricted")
	for _, text := range []string{restricted, unrestricted} {
		row := findRow(t, text, fmt.Sprintf("%d,", waiter))
		fields := strings.Split(row, ",")
		if fields[3] != fmt.Sprint(holder) || fields[4] != fmt.Sprint(holder) {
			t.Errorf("row = %q", row)
		}
		if strings.Contains(text, "pg_terminate_backend") || strings.Contains(text, "pg_cancel_backend") {
			t.Errorf("the report suggests an action:\n%s", text)
		}
	}
}
