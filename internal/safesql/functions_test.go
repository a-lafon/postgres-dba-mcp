package safesql

import "testing"

// TestFunctionLocations checks that a denied call is found in every clause
// the spec lists. A generic walk must not forget any.
func TestFunctionLocations(t *testing.T) {
	tests := []struct{ name, sql string }{
		{"target list", "SELECT lo_get(1)"},
		{"range function", "SELECT * FROM lo_get(1)"},
		{"range function with ordinality", "SELECT * FROM lo_get(1) WITH ORDINALITY"},
		{"rows from", "SELECT * FROM ROWS FROM (lo_get(1))"},
		{"lateral", "SELECT * FROM accounts a, LATERAL lo_get(a.id)"},
		{"where", "SELECT 1 FROM t WHERE lo_get(1) IS NULL"},
		{"join", "SELECT 1 FROM t JOIN u ON lo_get(u.id) IS NULL"},
		{"group by", "SELECT 1 FROM t GROUP BY lo_get(1)"},
		{"having", "SELECT 1 FROM t GROUP BY a HAVING lo_get(1) IS NULL"},
		{"order by", "SELECT 1 FROM t ORDER BY lo_get(1)"},
		{"window partition", "SELECT sum(a) OVER (PARTITION BY lo_get(1)) FROM t"},
		{"window order", "SELECT sum(a) OVER w FROM t WINDOW w AS (ORDER BY lo_get(1))"},
		{"values", "SELECT * FROM (VALUES (lo_get(1))) v(x)"},
		{"values top level", "VALUES (lo_get(1))"},
		{"subquery", "SELECT (SELECT lo_get(1))"},
		{"exists subquery", "SELECT 1 WHERE EXISTS (SELECT lo_get(1))"},
		{"in subquery", "SELECT 1 WHERE 1 IN (SELECT lo_get(1))"},
		{"cte", "WITH c AS (SELECT lo_get(1)) SELECT * FROM c"},
		{"nested argument", "SELECT upper(lower(lo_get(1)))"},
		{"cast argument", "SELECT lo_get(1)::text"},
		{"case expression", "SELECT CASE WHEN true THEN lo_get(1) END"},
		{"coalesce", "SELECT coalesce(NULL, lo_get(1))"},
		{"filter clause", "SELECT count(*) FILTER (WHERE lo_get(1) IS NULL) FROM t"},
		{"aggregate argument", "SELECT sum(lo_get(1)) FROM t"},
		{"set operation", "SELECT 1 UNION SELECT lo_get(1)"},
		{"limit", "SELECT 1 LIMIT lo_get(1)"},
		{"explain inner", "EXPLAIN SELECT lo_get(1)"},
		{"explain analyze inner", "EXPLAIN (ANALYZE) SELECT * FROM lo_get(1)"},
	}
	p := defaultPolicy(t)
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Check(tt.sql, p)
			if err == nil || err.Error() != "restricted mode: function lo_get() is not allowed" {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestFuncCallsOrderAndNames(t *testing.T) {
	tree, err := parseStatements("SELECT a.F(1), b(2), pg_catalog.c(3) FROM t WHERE d(4)")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, c := range funcCalls(tree[0]) {
		got = append(got, c.full)
	}
	want := []string{"a.f", "b", "pg_catalog.c", "d"}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestHasFuncCall(t *testing.T) {
	tests := []struct {
		sql  string
		want bool
	}{
		{"SELECT a + b FROM t WHERE a > 1 ORDER BY b LIMIT 10", false},
		{"SELECT a::text FROM t", false}, // a cast is not a FuncCall
		{"SELECT * FROM t", false},
		{"", false},
		{"SELECT f(x) FROM t", true},
		{"SELECT count(*) FROM t", true},
		{"SELECT * FROM generate_series(1, 5)", true},
		{"SELECT (SELECT now())", true},
		{"SELECT f(", true}, // unreadable: the cautious side
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			if got := HasFuncCall(tt.sql); got != tt.want {
				t.Errorf("HasFuncCall(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}
