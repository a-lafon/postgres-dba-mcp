//go:build integration

package stats

import (
	"context"
	"encoding/csv"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

const (
	countQuery = "SELECT count(*) FROM generate_series($1, $2)"
	sleepQuery = "SELECT pg_sleep($1)"
	dbidFilter = "dbid = (SELECT oid FROM pg_database WHERE datname = current_database())"
)

// connect opens a connection for the test and closes it at cleanup.
func connect(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	c, err := pgx.Connect(context.Background(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(context.Background()) })
	return c
}

// exec runs each statement on dsn count times.
func exec(t *testing.T, dsn string, count int, stmts ...string) {
	t.Helper()
	c := connect(t, dsn)
	for _, s := range stmts {
		for i := 0; i < count; i++ {
			if _, err := c.Exec(context.Background(), s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}
}

// resetStats clears pg_stat_statements as admin. Safe because integration packages run one at a time.
func resetStats(t *testing.T) {
	t.Helper()
	if _, err := connect(t, testpg.Admin(t)).Exec(context.Background(), "SELECT pg_stat_statements_reset()"); err != nil {
		t.Fatal(err)
	}
}

// workload resets the statistics, then runs the two statement shapes that only this package uses.
func workload(t *testing.T) {
	t.Helper()
	resetStats(t)
	ro := testpg.RO(t)
	exec(t, ro, 200, "SELECT count(*) FROM generate_series(1, 1000)")
	exec(t, ro, 5, "SELECT pg_sleep(0.05)")
}

// callTool runs get_top_queries through an in-memory MCP client on the database of dsn.
func callTool(t *testing.T, dsn string, args map[string]any) (text string, isError bool) {
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "get_top_queries", Arguments: args})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// table is a parsed tool output.
type table struct {
	Context string
	Footer  string
	Header  []string
	Rows    []map[string]string
}

func parseOutput(t *testing.T, text string) table {
	t.Helper()
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	var tb table
	var csvLines []string
	for i, l := range lines {
		switch {
		case i == 0:
			tb.Context = l
		case strings.HasPrefix(l, "-- "):
			tb.Footer += l + "\n"
		default:
			csvLines = append(csvLines, l)
		}
	}
	recs, err := csv.NewReader(strings.NewReader(strings.Join(csvLines, "\n"))).ReadAll()
	if err != nil || len(recs) == 0 {
		t.Fatalf("parse %q: %v", text, err)
	}
	tb.Header = recs[0]
	for _, r := range recs[1:] {
		row := map[string]string{}
		for i, h := range tb.Header {
			row[h] = r[i]
		}
		tb.Rows = append(tb.Rows, row)
	}
	return tb
}

func (tb table) index(query string) int {
	return slices.IndexFunc(tb.Rows, func(r map[string]string) bool { return r["query"] == query })
}

// adminStat returns calls and rows of a normalized statement of the current database.
func adminStat(t *testing.T, query string) (calls, rows string) {
	t.Helper()
	err := connect(t, testpg.Admin(t)).QueryRow(context.Background(),
		"SELECT calls::text, rows::text FROM pg_stat_statements WHERE "+dbidFilter+" AND query = $1", query).Scan(&calls, &rows)
	if err != nil {
		t.Fatalf("admin stat of %q: %v", query, err)
	}
	return calls, rows
}

func TestTimeRankings(t *testing.T) {
	workload(t)
	for _, sortBy := range []string{"total_time", "mean_time"} {
		t.Run(sortBy, func(t *testing.T) {
			text, isErr := callTool(t, testpg.RO(t), map[string]any{"sort_by": sortBy})
			if isErr {
				t.Fatal(text)
			}
			tb := parseOutput(t, text)
			if !strings.HasPrefix(tb.Context, "-- by "+sortBy+", database pgdba, statistics since ") {
				t.Errorf("context line = %q", tb.Context)
			}
			if got := strings.Join(tb.Header, ","); got != "calls,total_ms,mean_ms,rows,query" {
				t.Errorf("header = %s", got)
			}
			sleep, count := tb.index(sleepQuery), tb.index(countQuery)
			if sleep < 0 || count < 0 || sleep > count {
				t.Fatalf("order: sleep at %d, count at %d in\n%s", sleep, count, text)
			}
			for _, q := range []string{sleepQuery, countQuery} {
				calls, rows := adminStat(t, q)
				r := tb.Rows[tb.index(q)]
				if r["calls"] != calls || r["rows"] != rows {
					t.Errorf("%s: tool calls/rows = %s/%s, pg_stat_statements = %s/%s", q, r["calls"], r["rows"], calls, rows)
				}
			}
		})
	}
}

func TestMultiLineQueryCollapsed(t *testing.T) {
	resetStats(t)
	exec(t, testpg.RO(t), 1, "SELECT\n\t1   AS\n    multi_line_marker")
	text, isErr := callTool(t, testpg.RO(t), map[string]any{"sort_by": "total_time"})
	if isErr {
		t.Fatal(text)
	}
	if got := parseOutput(t, text).index("SELECT $1 AS multi_line_marker"); got < 0 {
		t.Errorf("collapsed query not found in\n%s", text)
	}
}

func TestDefaultsAndNullShares(t *testing.T) {
	workload(t)
	text, isErr := callTool(t, testpg.RO(t), nil)
	if isErr {
		t.Fatal(text)
	}
	tb := parseOutput(t, text)
	want := "calls,total_ms,mean_ms,time_pct,blks_accessed_pct,blks_read_pct,blks_dirtied_pct,wal_pct,query"
	if !strings.HasPrefix(tb.Context, "-- by resources, ") || strings.Join(tb.Header, ",") != want || len(tb.Rows) == 0 || len(tb.Rows) > 10 {
		t.Fatalf("unexpected output:\n%s", text)
	}
	var dirtied, wal string
	err := connect(t, testpg.Admin(t)).QueryRow(context.Background(),
		"SELECT coalesce(sum(shared_blks_dirtied), 0)::text, coalesce(sum(wal_bytes), 0)::text FROM pg_stat_statements WHERE "+dbidFilter).Scan(&dirtied, &wal)
	if err != nil {
		t.Fatal(err)
	}
	if dirtied == "0" && wal == "0" {
		for _, r := range tb.Rows {
			if r["blks_dirtied_pct"] != `\N` || r["wal_pct"] != `\N` {
				t.Errorf("zero sums must give NULL shares: %v", r)
			}
		}
	}
}

// TestTop10MatchesPgStatStatements is the M2 exit criterion for get_top_queries.
func TestTop10MatchesPgStatStatements(t *testing.T) {
	workload(t)
	text, isErr := callTool(t, testpg.RO(t), map[string]any{"sort_by": "total_time", "limit": 10})
	if isErr {
		t.Fatal(text)
	}
	tb := parseOutput(t, text)
	prev := -1.0
	for _, r := range tb.Rows {
		v, err := strconv.ParseFloat(r["total_ms"], 64)
		if err != nil {
			t.Fatal(err)
		}
		if prev >= 0 && v > prev {
			t.Errorf("total_ms not in non-increasing order: %v after %v", v, prev)
		}
		prev = v
	}

	rows, err := connect(t, testpg.Admin(t)).Query(context.Background(),
		`SELECT regexp_replace(query, '\s+', ' ', 'g'), total_exec_time FROM pg_stat_statements WHERE `+dbidFilter+
			` ORDER BY total_exec_time DESC LIMIT 10`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	// The server's own lifecycle statements (set_config, begin, DISCARD ALL,
	// hypopg_reset, extension probes) run between the two reads and fill the
	// bottom of the top 10 with near-zero ties whose order is arbitrary. Only
	// a statement clearly above the tool's last row has a stable rank. The
	// margin covers the 0.1 ms rounding and timings that grow between reads.
	cutoff := prev + 1
	checked := 0
	for rows.Next() {
		var q string
		var total float64
		if err := rows.Scan(&q, &total); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(q, "pg_stat_statements") {
			continue // its own timings move between the two reads
		}
		if total <= cutoff {
			continue // tie zone at the cutoff
		}
		checked++
		if tb.index(format.Shorten(q, queryCap)) < 0 {
			t.Errorf("top 10 statement %q missing from tool output:\n%s", q, text)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if checked == 0 {
		t.Error("no statement compared")
	}
}

func TestOtherDatabasesExcluded(t *testing.T) {
	resetStats(t)
	name := testpg.TempDB(t)
	const marker = "SELECT $1 AS other_database_marker" // constants are normalized to $n
	exec(t, testpg.DSN(testpg.RO(t), name), 1, "SELECT 1 AS other_database_marker")

	var n int
	err := connect(t, testpg.Admin(t)).QueryRow(context.Background(),
		"SELECT count(*) FROM pg_stat_statements WHERE query = $1", marker).Scan(&n)
	if err != nil || n != 1 {
		t.Fatalf("marker not recorded in the other database: n=%d err=%v", n, err)
	}
	for _, sortBy := range []string{"total_time", "mean_time", "resources"} {
		text, isErr := callTool(t, testpg.RO(t), map[string]any{"sort_by": sortBy, "limit": 100})
		if isErr || strings.Contains(text, "other_database_marker") {
			t.Errorf("sort_by=%s: error=%v, marker visible:\n%s", sortBy, isErr, text)
		}
	}
}

func TestExtensionAbsent(t *testing.T) {
	name := testpg.TempDB(t)
	text, isErr := callTool(t, testpg.DSN(testpg.RO(t), name), nil)
	want := "ERROR: pg_stat_statements is not installed in this database: run CREATE EXTENSION pg_stat_statements; " +
		"it also requires shared_preload_libraries = 'pg_stat_statements'"
	if !isErr || text != want {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
}

func TestInvalidArguments(t *testing.T) {
	tests := []struct {
		name string
		args map[string]any
		want string
	}{
		{"sort_by", map[string]any{"sort_by": "cpu"}, `ERROR: invalid sort_by "cpu": use resources, total_time or mean_time`},
		{"limit 0", map[string]any{"limit": 0}, "ERROR: invalid limit 0: must be between 1 and 100"},
		{"limit 101", map[string]any{"limit": 101}, "ERROR: invalid limit 101: must be between 1 and 100"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			text, isErr := callTool(t, testpg.RO(t), tt.args)
			if !isErr || text != tt.want {
				t.Errorf("isError=%v text=%q", isErr, text)
			}
		})
	}
}

func TestResourcesLimitHonored(t *testing.T) {
	resetStats(t)
	aggs := []string{
		"count(g)", "sum(g)", "avg(g)", "min(g)", "max(g)", "bit_or(g)", "bool_and(g > 0)",
		"stddev(g)", "variance(g)", "string_agg(g::text, ',')", "array_length(array_agg(g), 1)", "count(DISTINCT g)",
	}
	stmts := make([]string, len(aggs))
	for i, a := range aggs {
		stmts[i] = fmt.Sprintf("SELECT %s FROM generate_series(1, 200000) AS g", a)
	}
	exec(t, testpg.RO(t), 1, stmts...)

	text, isErr := callTool(t, testpg.RO(t), map[string]any{"sort_by": "resources", "limit": 5})
	if isErr {
		t.Fatal(text)
	}
	tb := parseOutput(t, text)
	if len(tb.Rows) != 5 || !strings.Contains(tb.Footer, "-- 5 rows, truncated (more than 5), ") {
		t.Errorf("rows=%d footer=%q", len(tb.Rows), tb.Footer)
	}
}
