package indexadv

import (
	"errors"
	"math"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

func ptr[T any](v T) *T { return &v }

func TestValidate(t *testing.T) {
	tests := []struct {
		name   string
		maxMB  *int
		method *string
		want   int64
		err    string
	}{
		{"defaults", nil, nil, 10000, ""},
		{"explicit", ptr(5), ptr("dta"), 5, ""},
		{"llm refused", nil, ptr("llm"), 0, `method "llm" is not supported by postgres-dba-mcp; use "dta"`},
		{"unknown method", nil, ptr("x"), 0, `invalid method "x"`},
		{"size below 1", ptr(0), nil, 0, "max_index_size_mb must be at least 1"},
		{"negative size", ptr(-3), nil, 0, "max_index_size_mb must be at least 1"},
		{"size overflowing bytes", ptr(math.MaxInt), nil, 0, "max_index_size_mb must be at most"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := validate(tc.maxMB, tc.method)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Errorf("got %d, %v; want %d", got, err, tc.want)
			}
		})
	}
}

func TestCheckQueries(t *testing.T) {
	tests := []struct {
		n   int
		err string
	}{
		{0, "Please provide a non-empty list of queries to analyze."},
		{1, ""},
		{10, ""},
		{11, "Please provide a list of up to 10 queries to analyze."},
	}
	for _, tc := range tests {
		err := checkQueries(make([]string, tc.n))
		switch {
		case tc.err == "" && err != nil, tc.err != "" && (err == nil || err.Error() != tc.err):
			t.Errorf("%d queries: %v, want %q", tc.n, err, tc.err)
		}
	}
}

func TestPrecheck(t *testing.T) {
	info := func(ext ...string) db.Info {
		i := db.Info{VersionNum: 170002, Extensions: map[string]string{}}
		for _, e := range ext {
			i.Extensions[e] = "1.11"
		}
		return i
	}
	statements := func(version string) db.Info {
		i := info("hypopg")
		i.Extensions["pg_stat_statements"] = version
		return i
	}
	tests := []struct {
		name     string
		info     db.Info
		workload bool
		err      string
	}{
		{"hypopg missing", info(), false, "index analysis needs the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-17-hypopg package)"},
		{"hypopg missing is checked first", info(), true, "index analysis needs the hypopg extension"},
		{"pg_stat_statements missing", info("hypopg"), true, "pg_stat_statements is not installed in this database"},
		{"pg_stat_statements not needed for queries", info("hypopg"), false, ""},
		{"all there", info("hypopg", "pg_stat_statements"), true, ""},
		{"pg_stat_statements 1.8", statements("1.8"), true, "analyze_workload_indexes needs pg_stat_statements 1.9 or later (installed: 1.8): run ALTER EXTENSION pg_stat_statements UPDATE"},
		{"pg_stat_statements 1.8 not needed for queries", statements("1.8"), false, ""},
		{"pg_stat_statements 1.9", statements("1.9"), true, ""},
		{"pg_stat_statements 1.10 is above 1.9", statements("1.10"), true, ""},
		{"pg_stat_statements 2.0", statements("2.0"), true, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := precheck(tc.info, tc.workload)
			switch {
			case tc.err == "" && err != nil, tc.err != "" && (err == nil || !strings.HasPrefix(err.Error(), tc.err)):
				t.Errorf("got %v, want %q", err, tc.err)
			}
		})
	}
}

func TestStatsError(t *testing.T) {
	other := errors.New("connection reset")
	tests := []struct {
		name string
		err  error
		want string
	}{
		{"not preloaded", &pgconn.PgError{Code: "55000", Message: "pg_stat_statements must be loaded via shared_preload_libraries"},
			"pg_stat_statements is installed but not loaded: add it to shared_preload_libraries and restart PostgreSQL"},
		{"other SQLSTATE", &pgconn.PgError{Severity: "ERROR", Code: "42501", Message: "permission denied for view pg_stat_statements"},
			"read pg_stat_statements: ERROR: permission denied for view pg_stat_statements (SQLSTATE 42501)"},
		{"not a PgError", other, "read pg_stat_statements: connection reset"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := statsError(tt.err); got.Error() != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
	if !errors.Is(statsError(other), other) {
		t.Error("statsError does not wrap the cause")
	}
}

func TestEmptyWorkloadText(t *testing.T) {
	want := "No query in pg_stat_statements for this database has calls >= 50 and mean time >= 5 ms."
	if got := emptyWorkload(); got != want {
		t.Errorf("got %q", got)
	}
}

func TestIntake(t *testing.T) {
	policy, err := safesql.NewPolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := func(mode string) *config.Config { return &config.Config{AccessMode: mode} }
	in := []string{
		"SELECT * FROM events WHERE account_id = 1",
		"DELETE FROM events",
		"SELECT 1; SELECT 2",
		"SELEC",
		"WITH x AS (SELECT 1) SELECT * FROM x",
	}
	tests := []struct {
		mode        string
		kept        []string
		skippedWhy  map[string]string
		skippedKeys []string
	}{
		{config.Restricted, []string{"Q1", "Q5"}, map[string]string{
			"Q2": "restricted mode: DeleteStmt statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW)",
			"Q3": "restricted mode: only one statement per call is allowed",
		}, []string{"Q2", "Q3", "Q4"}},
		{config.Unrestricted, []string{"Q1", "Q5"}, map[string]string{
			"Q2": "not a single SELECT", "Q3": "not a single SELECT",
		}, []string{"Q2", "Q3", "Q4"}},
	}
	for _, tc := range tests {
		t.Run(tc.mode, func(t *testing.T) {
			var qs []*query
			for _, s := range in {
				qs = append(qs, &query{SQL: s, Weight: 1})
			}
			kept, skipped := intake(cfg(tc.mode), policy, qs)
			var labels, skipKeys []string
			for _, q := range kept {
				labels = append(labels, q.Label)
			}
			for _, s := range skipped {
				skipKeys = append(skipKeys, s.Label)
				if want, ok := tc.skippedWhy[s.Label]; ok && s.Reason != want {
					t.Errorf("%s: reason %q, want %q", s.Label, s.Reason, want)
				}
				if s.Label == "Q4" && !strings.HasPrefix(s.Reason, "syntax error") {
					t.Errorf("Q4: reason %q", s.Reason)
				}
			}
			if !slices.Equal(labels, tc.kept) || !slices.Equal(skipKeys, tc.skippedKeys) {
				t.Errorf("kept %v skipped %v", labels, skipKeys)
			}
			if kept[0].SQL != in[0] {
				t.Error("query text changed")
			}
		})
	}
}
