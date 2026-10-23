//go:build integration

package explain

import (
	"context"
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

// call runs explain_query through an in-memory MCP client against the fixture and returns the text and isError.
func call(t *testing.T, mode string, args map[string]any, extra ...string) (string, bool) {
	t.Helper()
	dsn := testpg.RO(t)
	if mode == config.Unrestricted {
		dsn = testpg.Admin(t) // pgdba_rw has no DELETE privilege on the fixture tables
	}
	flags := append([]string{"--access-mode", mode}, extra...)
	cfg, err := config.Parse(append(flags, dsn), func(string) string { return "" })
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "explain_query", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

func ok(t *testing.T, mode string, args map[string]any) string {
	t.Helper()
	text, isErr := call(t, mode, args)
	if isErr {
		t.Fatalf("unexpected error: %s", text)
	}
	return text
}

const slow = "SELECT * FROM events WHERE account_id = 42 ORDER BY created_at DESC LIMIT 20"

// count runs a scalar query on the admin DSN (the fixture's committed state).
func count(t *testing.T, sql string) int {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close(ctx) }()
	var n int
	if err := c.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// S12
func TestSlowQuery(t *testing.T) {
	out := ok(t, config.Restricted, map[string]any{"sql": slow})
	lines := strings.Split(out, "\n")
	if !strings.HasPrefix(lines[0], "Total cost ") {
		t.Fatalf("header: %s", out)
	}
	if !regexp.MustCompile(`(?m)^-> (Parallel )?Seq Scan on public\.events: .*filter: `).MatchString(out) {
		t.Fatalf("no seq scan line:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^   WARNING: sequential scan of ~200k rows returns ~\d+`).MatchString(out) {
		t.Fatalf("no seq scan warning:\n%s", out)
	}
}

// S13
func TestHypotheticalIndex(t *testing.T) {
	out := ok(t, config.Restricted, map[string]any{"sql": slow,
		"hypothetical_indexes": []any{map[string]any{"table": "events", "columns": []any{"account_id", "created_at"}}}})
	m := regexp.MustCompile(`With hypothetical indexes: total cost [\d.]+ -> [\d.]+ \(-(\d+)\.\d%\)`).FindStringSubmatch(out)
	if m == nil || m[1] < "90" {
		t.Fatalf("no comparison line >= 90%%:\n%s", out)
	}
	if !regexp.MustCompile(`(?m)^Hypothetical indexes used: <\d+>btree_events_account_id_created_at$`).MatchString(out) {
		t.Fatalf("index not used:\n%s", out)
	}
	if n := count(t, "SELECT count(*) FROM pg_indexes WHERE tablename = 'events' AND indexname LIKE '%account_id%'"); n != 0 {
		t.Fatal("a real index exists")
	}
	// The verbose form keeps the comparison lines.
	v := ok(t, config.Restricted, map[string]any{"sql": slow, "verbose": true,
		"hypothetical_indexes": []any{map[string]any{"table": "events", "columns": []any{"account_id", "created_at"}}}})
	if !strings.HasPrefix(v, "With hypothetical indexes:") || !strings.Contains(v, "Index Scan") {
		t.Fatalf("verbose with indexes:\n%s", v)
	}
}

// S14
func TestAnalyze(t *testing.T) {
	out := ok(t, config.Restricted, map[string]any{"sql": "SELECT account_id, count(*) FROM events GROUP BY account_id", "analyze": true})
	if !strings.Contains(strings.SplitN(out, "\n", 2)[0], ", actual rows ") || !strings.Contains(out, ", execution ") ||
		!strings.Contains(out, " of time, ") || !strings.Contains(out, "(actual ") {
		t.Fatalf("analyze output:\n%s", out)
	}
}

func TestNoWarningOnIndexScan(t *testing.T) {
	out := ok(t, config.Restricted, map[string]any{"sql": "SELECT * FROM events WHERE id = 42"})
	if strings.Contains(out, "WARNING:") {
		t.Fatalf("unexpected warning:\n%s", out)
	}
}

func TestVerbose(t *testing.T) {
	out := ok(t, config.Restricted, map[string]any{"sql": "SELECT * FROM events WHERE account_id = 42", "verbose": true})
	if !strings.Contains(out, "Seq Scan on public.events") {
		t.Fatalf("verbose:\n%s", out)
	}
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "{") || strings.HasPrefix(l, "[") {
			t.Fatalf("JSON line %q", l)
		}
	}
	short, _ := call(t, config.Restricted, map[string]any{"sql": "SELECT * FROM events WHERE account_id = 42", "verbose": true}, "--max-bytes", "1024")
	if len(short) > 1024 {
		t.Fatalf("len %d", len(short))
	}
}

// A verbose ANALYZE executes the query once: two runs of a 0.5 s query would take at least 1 s.
func TestVerboseAnalyzeRunsOnce(t *testing.T) {
	// A first call takes the connection and parser start-up out of the measure.
	ok(t, config.Unrestricted, map[string]any{"sql": "SELECT 1", "analyze": true, "verbose": true})
	start := time.Now()
	out, isErr := call(t, config.Unrestricted, map[string]any{"sql": "SELECT pg_sleep(1)", "analyze": true, "verbose": true})
	if isErr || !strings.Contains(out, "Execution Time") {
		t.Fatalf("verbose analyze: %q", out)
	}
	if d := time.Since(start); d >= 1900*time.Millisecond {
		t.Fatalf("the query ran more than once: %v", d)
	}
}

func TestUnrestrictedWrites(t *testing.T) {
	before := count(t, "SELECT count(*) FROM orders WHERE status = 'failed'")
	text, isErr := call(t, config.Unrestricted, map[string]any{"sql": "UPDATE orders SET status = 'done'", "analyze": true})
	if !isErr || text != "ERROR: analyze is only allowed on SELECT, VALUES, TABLE or WITH … SELECT queries" {
		t.Fatalf("analyze write: %q", text)
	}
	out := ok(t, config.Unrestricted, map[string]any{"sql": "DELETE FROM orders WHERE status = 'failed'"})
	if !strings.HasPrefix(out, "Total cost ") {
		t.Fatalf("plain explain of a delete:\n%s", out)
	}
	if n := count(t, "SELECT count(*) FROM orders WHERE status = 'failed'"); n != before || before == 0 {
		t.Fatalf("failed orders %d -> %d", before, n)
	}
	if n := count(t, "SELECT count(*) FROM orders WHERE status = 'done'"); n == 0 {
		t.Fatal("orders were updated")
	}
}

func TestRefusals(t *testing.T) {
	tests := []struct {
		name string
		mode string
		args map[string]any
		want string
	}{
		{"delete restricted", config.Restricted, map[string]any{"sql": "DELETE FROM orders"}, "ERROR: restricted mode:"},
		{"denied function", config.Restricted, map[string]any{"sql": "SELECT * FROM pg_read_file('/etc/passwd')"},
			"ERROR: restricted mode: function pg_read_file() is not allowed"},
		{"nested explain", config.Restricted, map[string]any{"sql": "EXPLAIN SELECT 1"},
			"ERROR: pass the query without EXPLAIN; use the analyze and verbose parameters instead"},
		{"unknown column", config.Restricted, map[string]any{"sql": "SELECT nme FROM events"},
			`ERROR: column "nme" does not exist (SQLSTATE 42703)`},
		{"unknown field", config.Restricted, map[string]any{"sql": "SELECT 1", "nope": 1}, "ERROR: invalid arguments"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := call(t, tt.mode, tt.args)
			if !isErr || !strings.HasPrefix(text, tt.want) {
				t.Fatalf("isError=%v text=%q, want prefix %q", isErr, text, tt.want)
			}
		})
	}
}

// $n parameters get a generic plan; a `$5` inside a string literal is not a parameter. The path differs below PG 16.
func TestParameterized(t *testing.T) {
	for q, generic := range map[string]bool{
		"SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2": true,
		"SELECT * FROM events WHERE kind = 'costs $5' LIMIT 3":                         false,
	} {
		out := ok(t, config.Restricted, map[string]any{"sql": q})
		if got := strings.HasSuffix(strings.SplitN(out, "\n", 2)[0], ", generic plan ($n parameters)"); got != generic {
			t.Fatalf("%s: generic plan = %v, want %v:\n%s", q, got, generic, out)
		}
	}
}

// pg_stat_statements writes interval '1 year' as `interval $1`: the text copied from get_top_queries must be
// explained with a generic plan, in both modes.
func TestTypedLiteralParameter(t *testing.T) {
	for _, mode := range []string{config.Restricted, config.Unrestricted} {
		for _, q := range []string{
			"SELECT count(*) FROM events WHERE created_at < now() - interval $1",
			"SELECT count(*) FROM events WHERE created_at < now() - interval $1 day to second",
			"SELECT count(*) FROM events WHERE created_at < timestamptz $1",
		} {
			out := ok(t, mode, map[string]any{"sql": q})
			if !strings.HasSuffix(strings.SplitN(out, "\n", 2)[0], ", generic plan ($n parameters)") {
				t.Fatalf("%s %s:\n%s", mode, q, out)
			}
		}
	}
}

func TestToolList(t *testing.T) {
	// The schema is part of the contract: required sql, no outputSchema.
	cfg, _ := config.Parse([]string{testpg.RO(t)}, func(string) string { return "" })
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
	list, err := cs.ListTools(ctx, nil)
	if err != nil || len(list.Tools) != 1 {
		t.Fatalf("tools: %v %v", list, err)
	}
	tool := list.Tools[0]
	schema := tool.InputSchema.(map[string]any)
	props := schema["properties"].(map[string]any)
	for _, p := range []string{"sql", "analyze", "hypothetical_indexes", "verbose"} {
		if props[p] == nil {
			t.Errorf("missing property %s", p)
		}
	}
	if tool.Name != "explain_query" || tool.OutputSchema != nil || len(schema["required"].([]any)) != 1 {
		t.Fatalf("tool = %+v", tool)
	}
}
