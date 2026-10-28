package stats

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

func parse(t *testing.T, flags ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(append(flags, "postgres://u@h/db"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestParseArgs(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    args
		wantErr string
	}{
		{"defaults", ``, args{"resources", 10}, ""},
		{"empty object", `{}`, args{"resources", 10}, ""},
		{"explicit", `{"sort_by":"mean_time","limit":100}`, args{"mean_time", 100}, ""},
		{"invalid sort_by", `{"sort_by":"cpu"}`, args{}, `invalid sort_by "cpu": use resources, total_time or mean_time`},
		{"limit zero", `{"limit":0}`, args{}, "invalid limit 0: must be between 1 and 100"},
		{"limit too big", `{"limit":101}`, args{}, "invalid limit 101: must be between 1 and 100"},
		{"unknown field", `{"foo":1}`, args{}, `invalid arguments: unknown field "foo"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseArgs([]byte(tt.in))
			if tt.wantErr != "" {
				if err == nil || err.Error() != tt.wantErr {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestQuerySQLOrderColumn(t *testing.T) {
	tests := []struct{ sortBy, want string }{
		{"total_time", "ORDER BY total_exec_time DESC"},
		{"mean_time", "ORDER BY mean_exec_time DESC"},
		{"resources", "ORDER BY total_exec_time DESC"},
	}
	for _, tt := range tests {
		t.Run(tt.sortBy, func(t *testing.T) {
			sql := querySQL(tt.sortBy)
			if !strings.Contains(sql, tt.want) || strings.Contains(sql, "{{") {
				t.Errorf("sql = %s", sql)
			}
		})
	}
	if strings.Contains(querySQL("x; DROP TABLE t"), "DROP") {
		t.Error("unvalidated sort_by reached the SQL")
	}
}

func TestToolDefinition(t *testing.T) {
	tool := topQueriesTool(parse(t), nil)
	if tool.Group != "stats" || tool.Title != "Top queries" || tool.Def.Name != "get_top_queries" || tool.Writes {
		t.Errorf("tool = %+v", tool)
	}
}

func cells(vals ...string) []format.Cell {
	out := make([]format.Cell, len(vals))
	for i, v := range vals {
		if v != `\N` {
			out[i] = format.Cell(v)
		}
	}
	return out
}

func TestRenderLiteralOutput(t *testing.T) {
	res := topQueries{
		SortBy: "mean_time", Database: "pgdba", Since: "2026-10-20 08:00:00+00",
		Columns: []string{"calls", "total_ms", "mean_ms", "rows", "query"},
		Rows: [][]format.Cell{
			cells("5", "251.3", "50.3", "5", "SELECT pg_sleep($1)"),
			cells("200", "40.1", "0.2", "200", "SELECT count(*) FROM generate_series($1, $2)"),
		},
		Elapsed: 4 * time.Millisecond,
	}
	got, err := renderTopQueries(res, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "-- by mean_time, database pgdba, statistics since 2026-10-20 08:00:00+00\n" +
		"calls,total_ms,mean_ms,rows,query\n" +
		"5,251.3,50.3,5,SELECT pg_sleep($1)\n" +
		"200,40.1,0.2,200,\"SELECT count(*) FROM generate_series($1, $2)\"\n" +
		"-- 2 rows, 4 ms\n"
	if got != want {
		t.Errorf("render =\n%s\nwant\n%s", got, want)
	}
}

func TestRenderNullPercentagesAndTruncation(t *testing.T) {
	res := topQueries{
		SortBy: "resources", Database: "d", Since: "unknown",
		Columns: []string{"calls", "total_ms", "mean_ms", "time_pct", "blks_accessed_pct", "blks_read_pct", "blks_dirtied_pct", "wal_pct", "query"},
		Rows:    [][]format.Cell{cells("1", "9.0", "9.0", "60.0", "50.0", "0.0", `\N`, `\N`, "SELECT 1")},
		More:    true,
	}
	got, err := renderTopQueries(res, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "\n1,9.0,9.0,60.0,50.0,0.0,\\N,\\N,SELECT 1\n") {
		t.Errorf("NULL percentages not rendered as \\N: %q", got)
	}
	if !strings.Contains(got, "-- 1 row, truncated (more than 1), ") {
		t.Errorf("truncation not reported: %q", got)
	}
}

func TestRenderShortensQueryAt500IgnoringMaxCell(t *testing.T) {
	long := strings.Repeat("a", 600)
	res := topQueries{
		SortBy: "total_time", Database: "d", Since: "unknown",
		Columns: []string{"calls", "query"},
		Rows:    [][]format.Cell{cells("1", long)},
	}
	got, err := renderTopQueries(res, parse(t, "--max-cell=64"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\n1," + strings.Repeat("a", 500) + "…[+100 chars]\n"
	if !strings.Contains(got, want) {
		t.Errorf("query cell not cut at 500: %q", got)
	}
}

func TestRenderMaxCellAppliesToOtherCells(t *testing.T) {
	long := strings.Repeat("a", 600)
	res := topQueries{
		SortBy: "total_time", Database: "d", Since: "unknown",
		Columns: []string{"calls", "query"},
		Rows:    [][]format.Cell{cells(strings.Repeat("9", 50), long)},
	}
	got, err := renderTopQueries(res, parse(t, "--max-cell=16"))
	if err != nil {
		t.Fatal(err)
	}
	want := "\n" + strings.Repeat("9", 16) + "…[+34 chars]," + strings.Repeat("a", 500) + "…[+100 chars]\n"
	if !strings.Contains(got, want) || !strings.Contains(got, "2 cells shortened") {
		t.Errorf("other cell not cut at --max-cell: %q", got)
	}
}

func TestContextFor(t *testing.T) {
	tests := []struct {
		version string
		legacy  bool
	}{
		{"1.8", true}, {"1.9", false}, {"1.10", false}, {"1.11", false}, {"2.0", false},
		{"1.7", true}, {"", true}, {"abc", true}, {"1", true},
	}
	for _, tt := range tests {
		t.Run(tt.version, func(t *testing.T) {
			got := contextFor(tt.version)
			if want := map[bool]string{true: contextLegacySQL, false: contextSQL}[tt.legacy]; got != want {
				t.Errorf("contextFor(%q) is the wrong statement", tt.version)
			}
		})
	}
	if strings.Contains(contextLegacySQL, "pg_stat_statements_info") && strings.Contains(strings.ReplaceAll(contextLegacySQL, "-- ", ""), "FROM pg_stat_statements_info") {
		t.Error("the legacy statement reads pg_stat_statements_info")
	}
}

func TestMapReadError(t *testing.T) {
	notLoaded := &pgconn.PgError{Code: "55000", Message: "pg_stat_statements must be loaded via \"shared_preload_libraries\""}
	tests := []struct {
		name string
		err  error
		is   error
	}{
		{"not preloaded", notLoaded, errNotLoaded},
		{"not preloaded, wrapped", fmt.Errorf("query: %w", notLoaded), errNotLoaded},
		{"55000 for another reason", &pgconn.PgError{Code: "55000", Message: "object not in prerequisite state"}, nil},
		{"another code", &pgconn.PgError{Code: "42501", Message: "permission denied for shared_preload_libraries"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapReadError(tt.err)
			if tt.is != nil && !errors.Is(got, tt.is) {
				t.Errorf("got %v, want %v", got, tt.is)
			}
			if tt.is == nil && (errors.Is(got, errNotLoaded) || !errors.Is(got, tt.err)) {
				t.Errorf("got %v, want the original error wrapped", got)
			}
		})
	}
}

func TestRenderHiddenQueryTexts(t *testing.T) {
	mk := func(sort string, texts ...string) topQueries {
		res := topQueries{SortBy: sort, Database: "d", User: "app", Since: "unknown", Columns: []string{"calls", "query"}}
		for _, q := range texts {
			res.Rows = append(res.Rows, cells("1", q))
		}
		return res
	}
	const hint = "grant pg_read_all_stats to app to read them"
	for _, c := range []struct {
		name string
		res  topQueries
		want string // substring, "" for no hint at all
	}{
		{"all masked", mk("total_time", "<insufficient privilege>", "<insufficient privilege>"), "\n-- query texts hidden: " + hint + "\n"},
		{"single row masked", mk("total_time", "<insufficient privilege>"), "\n-- query texts hidden: " + hint + "\n"},
		{"some masked", mk("total_time", "<insufficient privilege>", "SELECT 1", "<insufficient privilege>"), "\n-- 2 of 3 query texts hidden: " + hint + "\n"},
		{"one of several masked", mk("total_time", "<insufficient privilege>", "SELECT 1"), "\n-- 1 of 2 query texts hidden: " + hint + "\n"},
		{"none masked", mk("total_time", "SELECT 1"), ""},
	} {
		got, err := renderTopQueries(c.res, parse(t))
		if err != nil {
			t.Fatal(err)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s: want %q in %q", c.name, c.want, got)
		}
		if c.want == "" && strings.Contains(got, "hidden") {
			t.Errorf("%s: unexpected hint in %q", c.name, got)
		}
	}
}

func TestRenderPrefixNamesCannotForgeLine(t *testing.T) {
	res := topQueries{SortBy: "total_time", Database: "d\n-- forged", User: "a\n-- forged", Since: "unknown",
		Columns: []string{"calls", "query"}, Rows: [][]format.Cell{cells("1", "<insufficient privilege>")}}
	got, err := renderTopQueries(res, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(got, "\n-- forged"); n != 0 {
		t.Errorf("%d forged line(s) in %q", n, got)
	}
	if lines := strings.Split(got, "\n"); !strings.HasPrefix(lines[0], "-- by total_time") || !strings.HasPrefix(lines[1], "-- query texts hidden") || lines[2] != "calls,query" {
		t.Errorf("prefix is not exactly two lines: %q", got)
	}
}
