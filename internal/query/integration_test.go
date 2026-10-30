//go:build integration

package query

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
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

// client is an in-memory MCP client of a server that registers execute_sql on the fixture.
type client struct{ cs *mcp.ClientSession }

func newClient(t *testing.T, dsn string, args ...string) *client {
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
	return &client{cs}
}

// exec calls execute_sql and returns the text and the isError flag.
func (c *client) exec(t *testing.T, sql string, maxRows ...int) (string, bool) {
	t.Helper()
	args := map[string]any{"sql": sql}
	if len(maxRows) > 0 {
		args["max_rows"] = maxRows[0]
	}
	res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "execute_sql", Arguments: args})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// ok calls exec and fails on an error result.
func (c *client) ok(t *testing.T, sql string, maxRows ...int) string {
	t.Helper()
	text, isErr := c.exec(t, sql, maxRows...)
	if isErr {
		t.Fatalf("%q failed: %s", sql, text)
	}
	return text
}

func adminConn(t *testing.T) *pgx.Conn {
	t.Helper()
	conn, err := pgx.Connect(context.Background(), testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func scalar(t *testing.T, conn *pgx.Conn, sql string) string {
	t.Helper()
	var s string
	if err := conn.QueryRow(context.Background(), sql).Scan(&s); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return s
}

var durationRe = regexp.MustCompile(`, \d+(\.\d+)? m?s\n`)

// withoutDuration replaces the duration that ends the footer by "<d>".
func withoutDuration(s string) string { return durationRe.ReplaceAllString(s, ", <d>\n") }

func TestRestrictedExecution(t *testing.T) {
	c := newClient(t, testpg.RO(t), "--max-cell=200")
	admin := adminConn(t)

	t.Run("write refused", func(t *testing.T) {
		text, isErr := c.exec(t, "DELETE FROM exec.canary")
		want := "ERROR: restricted mode: DeleteStmt statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW); start the server with --access-mode=unrestricted to run it"
		if !isErr || text != want {
			t.Errorf("got %v %q", isErr, text)
		}
		if n := scalar(t, admin, "SELECT count(*)::text FROM exec.canary"); n != "1" {
			t.Errorf("canary rows = %s", n)
		}
	})
	t.Run("denied function", func(t *testing.T) {
		text, isErr := c.exec(t, "SELECT * FROM pg_read_file('/etc/passwd')")
		if !isErr || text != "ERROR: restricted mode: function pg_read_file() is not allowed" {
			t.Errorf("got %v %q", isErr, text)
		}
	})
	t.Run("allowed function by configuration", func(t *testing.T) {
		c := newClient(t, testpg.RO(t), "--allow-function=pg_terminate_backend")
		c.ok(t, "SELECT pg_terminate_backend(0)")
	})
	t.Run("trailing semicolon", func(t *testing.T) {
		if got := withoutDuration(c.ok(t, "SELECT 1 AS x;")); got != "x\n1\n-- 1 row, <d>\n" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("explain", func(t *testing.T) {
		text := c.ok(t, "EXPLAIN SELECT * FROM public.events WHERE account_id = 1")
		lines := strings.Split(text, "\n")
		if lines[0] != "QUERY PLAN" || !strings.HasPrefix(lines[1], "Seq Scan on events") {
			t.Errorf("got %q", text)
		}
	})
	t.Run("value types", func(t *testing.T) {
		want := "id,txt,empty,num,arr,ts,doc,bin\n" +
			"1,hello,\"\",532.10,\"{1,2}\",2026-03-02 10:14:00+00,\"{\"\"a\"\": 1}\"," + `\x` + strings.Repeat("ab", 99) + "…[+4512 chars]\n" +
			"2,\\N,\\N,\\N,\\N,\\N,\\N,\\N\n" +
			"-- 2 rows, <d>\n" +
			"-- 1 cell shortened (bin); pass max_cell (up to 10000) to read more\n"
		if got := withoutDuration(c.ok(t, "SELECT * FROM exec.types ORDER BY id")); got != want {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})
	t.Run("long cells shortened", func(t *testing.T) {
		text := c.ok(t, "SELECT id, body FROM exec.docs ORDER BY id LIMIT 3")
		if n := strings.Count(text, "…[+3450 chars]"); n != 3 || !strings.Contains(text, "-- 3 cells shortened (body); ") {
			t.Errorf("got %q", text)
		}
	})
	t.Run("max_cell", func(t *testing.T) {
		call := func(maxCell int) (string, bool) {
			res, err := c.cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "execute_sql",
				Arguments: map[string]any{"sql": "SELECT repeat('x', 5000) AS txt", "max_cell": maxCell}})
			if err != nil {
				t.Fatalf("CallTool: %v", err)
			}
			return res.Content[0].(*mcp.TextContent).Text, res.IsError
		}
		text := c.ok(t, "SELECT repeat('x', 5000) AS txt")
		if !strings.Contains(text, "…[+4800 chars]") || !strings.Contains(text, "-- 1 cell shortened (txt); pass max_cell (up to 10000) to read more\n") {
			t.Errorf("default: got %q", text)
		}
		if text, isErr := call(6000); isErr || !strings.Contains(text, strings.Repeat("x", 5000)+"\n") || strings.Contains(text, "shortened") {
			t.Errorf("max_cell=6000: got %v %q", isErr, text[:100])
		}
		if text, isErr := call(10001); !isErr || text != "ERROR: invalid arguments: max_cell 10001 exceeds the server limit 10000" {
			t.Errorf("max_cell=10001: got %v %q", isErr, text)
		}
	})
	t.Run("unknown column", func(t *testing.T) {
		text, isErr := c.exec(t, "SELECT nme FROM exec.types")
		want := "ERROR: column \"nme\" does not exist (SQLSTATE 42703)\nLINE 1: SELECT nme FROM exec.types\n               ^\nPOSITION: 8"
		if !isErr || !strings.HasPrefix(text, want) {
			t.Errorf("got %v %q", isErr, text)
		}
	})
	t.Run("unknown column on the second line", func(t *testing.T) {
		text, isErr := c.exec(t, "SELECT id,\n       nme\nFROM exec.types")
		want := "ERROR: column \"nme\" does not exist (SQLSTATE 42703)\nLINE 2:        nme\n               ^\nPOSITION: 19"
		if !isErr || !strings.HasPrefix(text, want) {
			t.Errorf("got %v %q", isErr, text)
		}
	})
	t.Run("short bytea in full", func(t *testing.T) {
		if got := withoutDuration(c.ok(t, `SELECT '\xdeadbeef'::bytea AS b`)); got != "b\n\\xdeadbeef\n-- 1 row, <d>\n" {
			t.Errorf("got %q", got)
		}
	})
	t.Run("tsvector summarized by type, not by name", func(t *testing.T) {
		sql := `SELECT 1 AS id, to_tsvector('simple', 'amazing boat boy boat') AS fulltext, 'a:1 b:2'::tsvector::text AS as_text,
			to_tsvector('simple', 'x') AS note, ''::tsvector AS empty, NULL::tsvector AS nothing, 'txt' AS fulltext2`
		want := "id,fulltext,as_text,note,empty,nothing,fulltext2\n" +
			"1,[tsvector: 3 lexemes],'a':1 'b':2,[tsvector: 1 lexeme],[tsvector: 0 lexemes],\\N,txt\n" +
			"-- 1 row, <d>\n"
		if got := withoutDuration(c.ok(t, sql)); got != want {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})
	t.Run("backslashes and control characters are lossless", func(t *testing.T) {
		sql := `SELECT * FROM (VALUES (1, E'l1\nl2'), (2, 'a\nb'), (3, 'a\\b'), (4, 'a\'), (5, '\x41'),
			(6, 'C:\temp\new'), (7, '\N'), (8, NULL), (9, '\xdeadbeef'::bytea::text)) AS t(id, v) ORDER BY id`
		want := "id,v\n" +
			`1,l1\nl2` + "\n" +
			`2,a\\nb` + "\n" +
			`3,a\\\b` + "\n" +
			`4,a\` + "\n" +
			`5,\x41` + "\n" +
			`6,C:\\temp\\new` + "\n" +
			`7,"\N"` + "\n" +
			`8,\N` + "\n" +
			`9,\xdeadbeef` + "\n" +
			"-- 9 rows, <d>\n"
		if got := withoutDuration(c.ok(t, sql)); got != want {
			t.Errorf("got %q\nwant %q", got, want)
		}
	})
	t.Run("truncation follows the row limit", func(t *testing.T) {
		text := c.ok(t, "SELECT generate_series(1, 5000)", 5)
		if !strings.Contains(text, "-- 5 rows, truncated (more than 5), ") {
			t.Errorf("got %q", text)
		}
	})
}

// TestTransactionMode checks the runner each mode uses with a role that can write, so that restricted mode does not
// rely on the role alone.
func TestTransactionMode(t *testing.T) {
	for _, tt := range []struct{ mode, want string }{
		{config.Restricted, "on"},
		{config.Unrestricted, "off"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			c := newClient(t, testpg.RW(t), "--access-mode="+tt.mode)
			want := "transaction_read_only\n" + tt.want + "\n-- 1 row, <d>\n"
			if got := withoutDuration(c.ok(t, "SHOW transaction_read_only")); got != want {
				t.Errorf("got %q, want %q", got, want)
			}
		})
	}
}

func TestBoundedRead(t *testing.T) {
	c := newClient(t, testpg.RO(t))
	c.ok(t, "SELECT 1") // warms the parser and the pool: they are not part of the bound

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	start := time.Now()
	text := c.ok(t, "SELECT * FROM public.events")
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 1+100+2 || !strings.HasPrefix(lines[101], "-- 100 rows, truncated (more than 100), ") {
		t.Fatalf("lines = %d, footer %q", len(lines), lines[len(lines)-2])
	}
	if elapsed > 2*time.Second {
		t.Errorf("call took %v", elapsed)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
		t.Errorf("Go heap allocated %d bytes during the call", grown)
	}
}

// TestBoundedReadOfLargeValues reads rows far larger than the result can hold. The cells are cut on the way in,
// so the call keeps no more than it renders: holding them whole took a gigabyte for a 22 kB answer.
func TestBoundedReadOfLargeValues(t *testing.T) {
	admin := adminConn(t)
	if _, err := admin.Exec(context.Background(), `CREATE TABLE exec.big (id int, doc text);
		INSERT INTO exec.big SELECT g, repeat('x', 5*1024*1024) FROM generate_series(1, 20) g;
		GRANT SELECT ON exec.big TO pgdba_ro`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := admin.Exec(context.Background(), "DROP TABLE IF EXISTS exec.big"); err != nil {
			t.Error(err)
		}
	})
	c := newClient(t, testpg.RO(t))
	const sql = "SELECT id, doc FROM exec.big ORDER BY id"
	c.ok(t, sql, 20) // warms the parser, the pool and the driver's read buffer: they are not part of the bound

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	text := c.ok(t, sql, 20)
	runtime.ReadMemStats(&after)

	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if len(lines) != 1+20+2 || !strings.HasPrefix(lines[1], "1,xxx") || !strings.HasSuffix(lines[1], "…[+5242680 chars]") {
		t.Fatalf("lines = %d, first row %q", len(lines), lines[1])
	}
	if want := "-- 20 cells shortened (doc); pass max_cell (up to 10000) to read more"; lines[len(lines)-1] != want {
		t.Errorf("footer = %q, want %q", lines[len(lines)-1], want)
	}
	if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
		t.Errorf("Go heap allocated %d bytes for a %d-byte result", grown, len(text))
	}
}

func TestUnrestrictedExecution(t *testing.T) {
	admin := adminConn(t)
	drop := func() {
		if _, err := admin.Exec(context.Background(), "DROP TABLE IF EXISTS app_rw.t1, app_rw.t2"); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	t.Cleanup(drop)
	count := func(table string) string {
		return scalar(t, admin, "SELECT count(*)::text FROM "+table)
	}
	c := newClient(t, testpg.RW(t), "--access-mode=unrestricted")

	t.Run("truncation with a committing transaction", func(t *testing.T) {
		text := c.ok(t, "SELECT generate_series(1, 5000)", 5)
		if !strings.Contains(text, "-- 5 rows, truncated (more than 5), ") {
			t.Errorf("got %q", text)
		}
	})
	t.Run("comment only", func(t *testing.T) {
		if text, isErr := c.exec(t, "-- nothing here"); !isErr || text != "ERROR: empty query" {
			t.Errorf("got %v %q", isErr, text)
		}
	})
	t.Run("select into", func(t *testing.T) {
		if got := withoutDuration(c.ok(t, "SELECT generate_series(1, 3) AS x INTO app_rw.t2")); got != "-- SELECT 3, <d>\n" {
			t.Errorf("got %q", got)
		}
		if n := count("app_rw.t2"); n != "3" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("data-modifying cte", func(t *testing.T) {
		got := withoutDuration(c.ok(t, "WITH d AS (DELETE FROM app_rw.t2 RETURNING x) SELECT count(*) AS n FROM d"))
		if got != "n\n3\n-- 1 row, <d>\n" {
			t.Errorf("got %q", got)
		}
		if n := count("app_rw.t2"); n != "0" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("write committed", func(t *testing.T) {
		if got := withoutDuration(c.ok(t, "CREATE TABLE app_rw.t1 (x int)")); got != "-- CREATE TABLE, <d>\n" {
			t.Errorf("got %q", got)
		}
		if got := withoutDuration(c.ok(t, "INSERT INTO app_rw.t1 SELECT generate_series(1, 3)")); got != "-- INSERT 0 3, <d>\n" {
			t.Errorf("got %q", got)
		}
		if n := count("app_rw.t1"); n != "3" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("multiple statements refused", func(t *testing.T) {
		text, isErr := c.exec(t, "INSERT INTO app_rw.t1 VALUES (4); DELETE FROM app_rw.t1")
		if !isErr || text != "ERROR: only one statement per call is allowed; send each statement in its own call" {
			t.Errorf("got %v %q", isErr, text)
		}
		if n := count("app_rw.t1"); n != "3" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("returning rows bounded", func(t *testing.T) {
		text := c.ok(t, "INSERT INTO app_rw.t1 SELECT generate_series(1, 500) RETURNING x", 10)
		lines := strings.Split(text, "\n")
		if lines[0] != "x" || lines[10] != "10" || !strings.HasPrefix(lines[11], "-- 10 rows, truncated (more than 10), ") {
			t.Errorf("got %q", text)
		}
		if n := count("app_rw.t1"); n != "503" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("error rolls back", func(t *testing.T) {
		text, isErr := c.exec(t, "INSERT INTO app_rw.t1 VALUES ('x')")
		if !isErr || !strings.Contains(text, "(SQLSTATE 22P02)") {
			t.Errorf("got %v %q", isErr, text)
		}
		if n := count("app_rw.t1"); n != "503" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("select through the cursor", func(t *testing.T) {
		if got := withoutDuration(c.ok(t, "SELECT x FROM app_rw.t1 ORDER BY x LIMIT 2")); got != "x\n1\n1\n-- 2 rows, <d>\n" {
			t.Errorf("got %q", got)
		}
	})
}

// In unrestricted mode the transaction commits, so a SELECT whose rows have side effects must run to its end
// even when its output is truncated: the model sees fewer rows, the database must see the whole statement.
func TestUnrestrictedSelectRunsToCompletion(t *testing.T) {
	admin := adminConn(t)
	drop := func() {
		if _, err := admin.Exec(context.Background(),
			"DROP FUNCTION IF EXISTS app_rw.bump(int); DROP TABLE IF EXISTS app_rw.t4"); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(context.Background(), `CREATE TABLE app_rw.t4 (x int);
		GRANT ALL ON app_rw.t4 TO pgdba_rw;
		CREATE FUNCTION app_rw.bump(v int) RETURNS int LANGUAGE sql
		  AS 'INSERT INTO app_rw.t4 VALUES (v) RETURNING v'`); err != nil {
		t.Fatal(err)
	}
	const sql = "SELECT app_rw.bump(x) FROM generate_series(1, 500) x"
	count := func() string { return scalar(t, admin, "SELECT count(*)::text FROM app_rw.t4") }
	c := newClient(t, testpg.RW(t), "--access-mode=unrestricted")

	t.Run("unrestricted", func(t *testing.T) {
		text := c.ok(t, sql, 10)
		if !strings.Contains(text, "-- 10 rows, truncated (more than 10), ") {
			t.Errorf("got %q", text)
		}
		if n := count(); n != "500" {
			t.Errorf("rows written = %s, want 500: the statement was committed half done", n)
		}
	})
	t.Run("restricted writes nothing", func(t *testing.T) {
		if text, isErr := newClient(t, testpg.RO(t)).exec(t, sql, 10); !isErr {
			t.Errorf("got %q, want an error", text)
		}
		if n := count(); n != "500" {
			t.Errorf("rows = %s, want 500: the restricted call touched the table", n)
		}
	})
}

// In unrestricted mode a statement read without a cursor (DML with RETURNING, EXPLAIN) is drained to its end:
// the Go heap must stay bounded by max_rows, and statement_timeout must bound the time.
func TestUnrestrictedWithoutCursorIsBounded(t *testing.T) {
	admin := adminConn(t)
	drop := func() {
		if _, err := admin.Exec(context.Background(), "DROP TABLE IF EXISTS app_rw.t3"); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(context.Background(), "CREATE TABLE app_rw.t3 (x int); GRANT ALL ON app_rw.t3 TO pgdba_rw"); err != nil {
		t.Fatal(err)
	}
	c := newClient(t, testpg.RW(t), "--access-mode=unrestricted", "--statement-timeout=2s")
	c.ok(t, "SELECT 1") // warms the parser and the pool: they are not part of the bound

	t.Run("memory", func(t *testing.T) {
		var before, after runtime.MemStats
		runtime.GC()
		runtime.ReadMemStats(&before)
		text := c.ok(t, "INSERT INTO app_rw.t3 SELECT generate_series(1, 500000) RETURNING x", 10)
		runtime.ReadMemStats(&after)
		if !strings.Contains(text, "-- 10 rows, truncated (more than 10), ") {
			t.Fatalf("got %q", text)
		}
		if grown := after.TotalAlloc - before.TotalAlloc; grown > 16<<20 {
			t.Errorf("Go heap allocated %d bytes while draining 500000 rows", grown)
		}
		if n := scalar(t, admin, "SELECT count(*)::text FROM app_rw.t3"); n != "500000" {
			t.Errorf("rows = %s", n)
		}
	})
	t.Run("time", func(t *testing.T) {
		start := time.Now()
		text, isErr := c.exec(t, "EXPLAIN ANALYZE SELECT pg_sleep(30)")
		if !isErr || !strings.Contains(text, "(SQLSTATE 57014)") {
			t.Errorf("got %v %q, want a statement timeout (SQLSTATE 57014)", isErr, text)
		}
		if d := time.Since(start); d > 6*time.Second {
			t.Errorf("call took %v with --statement-timeout=2s", d)
		}
	})
}

func TestSettingsDoNotLeak(t *testing.T) {
	c := newClient(t, testpg.RW(t), "--access-mode=unrestricted", "--statement-timeout=7s")
	c.ok(t, "SET statement_timeout = 0")
	for i := range 6 { // more calls than pool connections, so every connection is reused
		if got := withoutDuration(c.ok(t, "SHOW statement_timeout")); got != "statement_timeout\n7s\n-- 1 row, <d>\n" {
			t.Fatalf("call %d: got %q", i, got)
		}
	}
}

type corpusCase struct{ ID, Family, SQL string }

// loadCorpus reads a "-- case: <id> <family>" file of testdata/safesql.
func loadCorpus(t *testing.T, name string) []corpusCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "safesql", name))
	if err != nil {
		t.Fatal(err)
	}
	var cases []corpusCase
	for _, line := range strings.Split(string(b), "\n") {
		if rest, ok := strings.CutPrefix(line, "-- case:"); ok {
			f := strings.Fields(rest)
			if len(f) != 2 {
				t.Fatalf("%s: bad case header %q", name, line)
			}
			cases = append(cases, corpusCase{ID: f[0], Family: f[1]})
		} else if len(cases) > 0 {
			cases[len(cases)-1].SQL += line + "\n"
		}
	}
	for i := range cases {
		cases[i].SQL = strings.TrimSpace(cases[i].SQL)
	}
	if len(cases) == 0 {
		t.Fatalf("%s: no case", name)
	}
	return cases
}

// snapshot reads, as the superuser, everything an attack could have changed.
func snapshot(t *testing.T, admin *pgx.Conn) string {
	t.Helper()
	return scalar(t, admin, `SELECT concat_ws(' | ',
  (SELECT count(*) FROM pg_class),
  (SELECT count(*) FROM pg_largeobject_metadata),
  (SELECT stats_reset FROM pg_stat_statements_info),
  (SELECT coalesce(sum(hashtext(t::text)), 0) FROM exec.canary t),
  (SELECT count(*) || '/' || coalesce(sum(hashtext(t::text)), 0) FROM public.accounts t),
  (SELECT count(*) || '/' || coalesce(sum(hashtext(t::text)), 0) FROM public.events t),
  (SELECT count(*) || '/' || coalesce(sum(hashtext(t::text)), 0) FROM public.orders t),
  (SELECT count(*) FROM pg_locks l JOIN pg_stat_activity a USING (pid)
     WHERE l.locktype = 'advisory' AND a.application_name = 'postgres-dba-mcp'))`)
}

func TestAttackCorpusLeavesNoSideEffect(t *testing.T) {
	cases := loadCorpus(t, "attacks.sql")
	admin := adminConn(t)
	for role, dsn := range map[string]string{"pgdba_ro": testpg.RO(t), "postgres": testpg.Admin(t)} {
		t.Run(role, func(t *testing.T) {
			c := newClient(t, dsn, "--statement-timeout=7s")
			want := snapshot(t, admin)
			for _, tc := range cases {
				text, isErr := c.exec(t, tc.SQL)
				if !isErr {
					t.Errorf("case %s: not refused: %q", tc.ID, text)
				}
				if got := snapshot(t, admin); got != want {
					t.Fatalf("case %s: side effect\nbefore %s\nafter  %s", tc.ID, want, got)
				}
				if got := withoutDuration(c.ok(t, "SHOW statement_timeout")); got != "statement_timeout\n7s\n-- 1 row, <d>\n" {
					t.Fatalf("case %s: settings leaked: %q", tc.ID, got)
				}
			}
		})
	}
}

func TestLegitCorpusIsNotRefused(t *testing.T) {
	c := newClient(t, testpg.RO(t))
	for _, tc := range loadCorpus(t, "legit.sql") {
		t.Run(tc.ID, func(t *testing.T) {
			// Postgres errors (an undefined PostGIS function, say) are fine: only the safety check may not refuse.
			if text, _ := c.exec(t, tc.SQL); strings.Contains(text, "restricted mode:") {
				t.Errorf("refused: %s", text)
			}
		})
	}
}
