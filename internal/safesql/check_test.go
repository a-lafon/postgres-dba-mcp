package safesql

import (
	"reflect"
	"strings"
	"testing"
)

func defaultPolicy(t testing.TB) *Policy {
	t.Helper()
	p, err := NewPolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheck(t *testing.T) {
	const notAllowed = "restricted mode: %s statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW)"
	na := func(n string) string { return strings.Replace(notAllowed, "%s", n, 1) }
	tests := []struct {
		name, sql string
		want      Statement
		wantErr   string // exact message; empty means accepted
	}{
		{"trailing semicolon", "SELECT 1;", Statement{Kind: Select}, ""},
		{"two statements", "SELECT 1; DROP TABLE t", Statement{}, "restricted mode: only one statement per call is allowed"},
		{"empty", "", Statement{}, "restricted mode: empty query"},
		{"whitespace and comments", "  -- x\n /* y */ ", Statement{}, "restricted mode: empty query"},
		{"values", "VALUES (1), (2)", Statement{Kind: Select}, ""},
		{"table", "TABLE accounts", Statement{Kind: Select}, ""},
		{"union", "SELECT 1 UNION SELECT 2", Statement{Kind: Select}, ""},
		{"with select", "WITH c AS (SELECT 1) SELECT * FROM c", Statement{Kind: Select}, ""},
		{"show", "SHOW server_version", Statement{Kind: Show}, ""},
		{"insert", "INSERT INTO t VALUES (1)", Statement{}, na("InsertStmt")},
		{"commit", "COMMIT", Statement{}, na("TransactionStmt")},
		{"set transaction", "SET TRANSACTION READ WRITE", Statement{}, na("VariableSetStmt")},
		{"set role", "SET ROLE postgres", Statement{}, na("VariableSetStmt")},
		{"reset all", "RESET ALL", Statement{}, na("VariableSetStmt")},
		{"explain delete", "EXPLAIN ANALYZE DELETE FROM t", Statement{}, "restricted mode: EXPLAIN is only allowed on SELECT, VALUES, TABLE or WITH … SELECT"},
		{"explain analyze select", "EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM events WHERE account_id = 1", Statement{Kind: Explain, Analyze: true}, ""},
		{"explain plain", "EXPLAIN SELECT 1", Statement{Kind: Explain}, ""},
		{"explain legacy analyze", "EXPLAIN ANALYZE SELECT 1", Statement{Kind: Explain, Analyze: true}, ""},
		{"explain analyze false", "EXPLAIN (ANALYZE false) SELECT 1", Statement{Kind: Explain}, ""},
		{"explain analyze off", "EXPLAIN (ANALYZE off) SELECT 1", Statement{Kind: Explain}, ""},
		{"explain analyze 0", "EXPLAIN (ANALYZE 0) SELECT 1", Statement{Kind: Explain}, ""},
		{"explain analyze true", "EXPLAIN (ANALYZE true) SELECT 1", Statement{Kind: Explain, Analyze: true}, ""},
		{"explain analyze on", "EXPLAIN (ANALYZE on) SELECT 1", Statement{Kind: Explain, Analyze: true}, ""},
		{"explain analyse spelling", "EXPLAIN (ANALYSE) SELECT 1", Statement{Kind: Explain, Analyze: true}, ""},
		{"explain into", "EXPLAIN SELECT * INTO t2 FROM t", Statement{}, "restricted mode: SELECT … INTO is not allowed"},
		{"data-modifying cte", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", Statement{}, "restricted mode: data-modifying WITH (INSERT/UPDATE/DELETE/MERGE) is not allowed"},
		{"cte in subquery", "SELECT * FROM (WITH d AS (UPDATE t SET x = 1 RETURNING *) SELECT * FROM d) s", Statement{}, "restricted mode: data-modifying WITH (INSERT/UPDATE/DELETE/MERGE) is not allowed"},
		{"explain data-modifying cte", "EXPLAIN WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", Statement{}, "restricted mode: data-modifying WITH (INSERT/UPDATE/DELETE/MERGE) is not allowed"},
		{"lock in subquery", "SELECT * FROM (SELECT * FROM t FOR UPDATE) s", Statement{}, "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"},
		{"for share", "SELECT * FROM t FOR SHARE", Statement{}, "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"},
		{"for no key update", "SELECT * FROM t FOR NO KEY UPDATE", Statement{}, "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"},
		{"for key share", "SELECT * FROM t FOR KEY SHARE", Statement{}, "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"},
		{"select into", "SELECT * INTO newtable FROM t", Statement{}, "restricted mode: SELECT … INTO is not allowed"},
		{"select into in cte", "WITH c AS (SELECT * INTO x FROM t) SELECT 1", Statement{}, "restricted mode: SELECT … INTO is not allowed"},
		{"order: multi before type", "DROP TABLE t; SELECT 1 INTO x", Statement{}, "restricted mode: only one statement per call is allowed"},
		{"order: type before function", "EXPLAIN DELETE FROM t WHERE pg_notify('a', 'b') IS NULL", Statement{}, "restricted mode: EXPLAIN is only allowed on SELECT, VALUES, TABLE or WITH … SELECT"},
		{"order: into before locking", "SELECT * INTO x FROM t FOR UPDATE", Statement{}, "restricted mode: SELECT … INTO is not allowed"},
		{"order: locking before data-modifying with", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d FOR UPDATE", Statement{}, "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"},
		{"order: data-modifying with before function", "WITH d AS (DELETE FROM t RETURNING *) SELECT pg_notify('a', 'b') FROM d", Statement{}, "restricted mode: data-modifying WITH (INSERT/UPDATE/DELETE/MERGE) is not allowed"},
		{"order: into before function", "SELECT pg_notify('a', 'b') INTO x", Statement{}, "restricted mode: SELECT … INTO is not allowed"},
		{"function in from", "SELECT * FROM pg_read_file('/etc/passwd')", Statement{}, "restricted mode: function pg_read_file() is not allowed"},
		{"function nested", "SELECT (SELECT length(lo_get(x)) FROM (VALUES (1)) v(x))", Statement{}, "restricted mode: function lo_get() is not allowed"},
		{"function in cte", "WITH c AS (SELECT pg_terminate_backend(123)) SELECT * FROM c", Statement{}, "restricted mode: function pg_terminate_backend() is not allowed"},
		{"schema and case", "SELECT pg_catalog.SET_CONFIG('statement_timeout', '0', false)", Statement{}, "restricted mode: function pg_catalog.set_config() is not allowed"},
		{"public lo_import", "SELECT public.lo_import('/etc/passwd')", Statement{}, "restricted mode: function public.lo_import() is not allowed"},
		{"quoted upper", `SELECT "LO_IMPORT"('x')`, Statement{}, "restricted mode: function LO_IMPORT() is not allowed"},
		{"first denied in source order", "SELECT pg_notify('a', 'b'), lo_get(1)", Statement{}, "restricted mode: function pg_notify() is not allowed"},
		{"ordinary functions", "SELECT now(), count(*), ST_Distance(a, b), my_schema.score(x) FROM t GROUP BY 1", Statement{Kind: Select, Unresolved: []FuncRef{
			{Name: "now", Full: "now"}, {Name: "count", Full: "count"},
			{Name: "st_distance", Full: "st_distance"}, {Schema: "my_schema", Name: "score", Full: "my_schema.score"},
		}}, ""},
		{"http_get", "SELECT http_get('http://x')", Statement{}, "restricted mode: function http_get() is not allowed"},
		{"net.http_post", "SELECT net.http_post('http://x')", Statement{}, "restricted mode: function net.http_post() is not allowed"},
		{"pg_file_write", "SELECT pg_file_write('f', 'x', false)", Statement{}, "restricted mode: function pg_file_write() is not allowed"},
		{"slash in quoted name", `SELECT "lo_/x"(1)`, Statement{}, "restricted mode: function lo_/x() is not allowed"},
	}
	p := defaultPolicy(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Check(tt.sql, p)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				if !reflect.DeepEqual(got, tt.want) {
					t.Fatalf("got %+v, want %+v", got, tt.want)
				}
				return
			}
			if err == nil {
				t.Fatalf("accepted, want error %q", tt.wantErr)
			}
			if err.Error() != tt.wantErr {
				t.Fatalf("error %q, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestCheckNilPolicy(t *testing.T) {
	if _, err := Check("SELECT 1", nil); err == nil {
		t.Fatal("nil policy accepted")
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name, sql string
		count     int
		kind      Kind
	}{
		{"update", "UPDATE t SET x = 1", 1, Other},
		{"select into", "SELECT * INTO t2 FROM t", 1, Other},
		{"data-modifying cte", "WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d", 1, Other},
		{"nested data-modifying cte", "SELECT * FROM (WITH d AS (DELETE FROM t RETURNING *) SELECT * FROM d) s", 1, Other},
		{"comment only", "-- nothing", 0, 0},
		{"empty", "", 0, 0},
		{"denied function still select", "SELECT pg_terminate_backend(1)", 1, Select},
		{"locking select is cursor-able", "SELECT * FROM t FOR UPDATE", 1, Select},
		{"explain", "EXPLAIN ANALYZE DELETE FROM t", 1, Explain},
		{"show", "SHOW ALL", 1, Show},
		{"values", "VALUES (1)", 1, Select},
		{"two statements", "SELECT 1; SELECT 2", 2, 0},
		{"transaction", "COMMIT", 1, Other},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			count, kind, err := Classify(tt.sql)
			if err != nil {
				t.Fatal(err)
			}
			if count != tt.count || kind != tt.kind {
				t.Fatalf("got (%d, %v), want (%d, %v)", count, kind, tt.count, tt.kind)
			}
		})
	}
}

func TestSyntaxErrors(t *testing.T) {
	tests := []struct {
		name, sql string
		contains  string
	}{
		{"typo", "SELEC 1", "(position 1)"},
		{"end of input", "SELECT 1 +", "(position 11)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Check(tt.sql, defaultPolicy(t))
			if err == nil || !strings.HasPrefix(err.Error(), "syntax error:") || !strings.Contains(err.Error(), tt.contains) {
				t.Fatalf("error = %v", err)
			}
			if _, _, err2 := Classify(tt.sql); err2 == nil || err2.Error() != err.Error() {
				t.Fatalf("Classify error = %v, want %v", err2, err)
			}
		})
	}
}

func TestParseErrorFormat(t *testing.T) {
	tests := []struct {
		e    parseError
		want string
	}{
		{parseError{Msg: "boom", Pos: 3}, "syntax error: boom (position 3)"},
		{parseError{Msg: "boom"}, "syntax error: boom"},
	}
	for _, tt := range tests {
		if got := tt.e.Error(); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func TestParseStatements(t *testing.T) {
	tree, err := parse("SELECT 1; SELECT 2;  SELECT 3")
	if err != nil {
		t.Fatal(err)
	}
	stmts := tree.(map[string]any)["stmts"].([]any)
	if len(stmts) != 3 {
		t.Fatalf("got %d statements, want 3", len(stmts))
	}
	for i, s := range stmts[1:] {
		if loc, _ := s.(map[string]any)["stmt_location"].(float64); loc == 0 {
			t.Errorf("statement %d has no stmt_location", i+1)
		}
	}
}
