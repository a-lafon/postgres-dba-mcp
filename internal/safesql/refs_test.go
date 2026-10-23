package safesql

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// describe renders a reference as "column:use[rel,rel]", a rel being schema.name or name, with "/alias" when aliased.
func describe(r ColumnRef) string {
	uses := map[Use]string{UseEq: "eq", UseRange: "range", UseSort: "sort", UseOther: "other"}
	rels := make([]string, len(r.Rels))
	for i, rel := range r.Rels {
		s := rel.Name
		if rel.Schema != "" {
			s = rel.Schema + "." + s
		}
		if rel.Alias != "" {
			s += "/" + rel.Alias
		}
		rels[i] = s
	}
	return fmt.Sprintf("%s:%s[%s]", r.Column, uses[r.Use], strings.Join(rels, ","))
}

func TestRefs(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		want []string
	}{
		{"qualified and unqualified", "SELECT * FROM events e, orders WHERE e.account_id = 1 AND status = 'x'",
			[]string{"account_id:eq[events/e]", "status:eq[events/e,orders]"}},
		{"schema qualifier", "SELECT * FROM public.events WHERE public.events.kind = 'a'",
			[]string{"kind:eq[public.events]"}},
		{"alias hides the table name", "SELECT * FROM events e WHERE events.kind = 'a'", []string{"kind:eq[]"}},
		{"nested join", "SELECT * FROM a JOIN b ON a.id = b.a_id JOIN c ON c.b_id = b.id WHERE c.kind = 'x'",
			[]string{"id:eq[a]", "a_id:eq[b]", "b_id:eq[c]", "id:eq[b]", "kind:eq[c]"}},
		{"uses", "SELECT * FROM t WHERE a < 1 AND b BETWEEN 1 AND 2 AND c LIKE 'x%' AND d IN (1, 2) AND e IS NULL AND f IS NOT NULL AND g <> 1 AND lower(h) = 'x' AND i = ANY(ARRAY[1])",
			[]string{"a:range[t]", "b:range[t]", "c:range[t]", "d:eq[t]", "e:eq[t]", "f:other[t]", "g:other[t]", "h:other[t]", "i:eq[t]"}},
		{"order by and having", "SELECT x FROM t GROUP BY x HAVING count(y) > 1 ORDER BY z DESC",
			[]string{"y:other[t]", "z:sort[t]"}},
		{"select list and group by ignored", "SELECT kind, count(*) FROM events WHERE id = 1 GROUP BY kind",
			[]string{"id:eq[events]"}},
		{"subquery scope", "SELECT * FROM a WHERE x IN (SELECT y FROM b WHERE z = 1)",
			[]string{"x:eq[a]", "z:eq[b]"}},
		{"correlated subquery", "SELECT * FROM a WHERE EXISTS (SELECT 1 FROM b WHERE b.k = a.k)",
			[]string{"k:eq[b]", "k:eq[a]"}},
		{"scalar subquery in select list", "SELECT (SELECT max(v) FROM b WHERE b.k = 1) FROM a",
			[]string{"k:eq[b]"}},
		{"cte is opaque", "WITH c AS (SELECT * FROM t WHERE p = 1) SELECT * FROM c JOIN u ON c.id = u.id WHERE n = 1",
			[]string{"p:eq[t]", "id:eq[/c]", "id:eq[u]", "n:eq[/c,u]"}},
		{"subselect alias is opaque", "SELECT * FROM a JOIN (SELECT id FROM b) s ON s.id = a.id",
			[]string{"id:eq[/s]", "id:eq[a]"}},
		{"union branches", "SELECT * FROM a WHERE x = 1 UNION SELECT * FROM b WHERE y = 1",
			[]string{"x:eq[a]", "y:eq[b]"}},
		{"parameters", "SELECT * FROM t WHERE a = $1 AND b = $12", []string{"a:eq[t]", "b:eq[t]"}},
		{"unknown qualifier", "SELECT * FROM t WHERE zz.a = 1", []string{"a:eq[]"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := Refs(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			got := []string{}
			for _, r := range refs {
				got = append(got, describe(r))
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("Refs(%q)\n got %v\nwant %v", tc.sql, got, tc.want)
			}
		})
	}
}

func TestRefsPositions(t *testing.T) {
	sql := "SELECT * FROM events WHERE account_id = 1 ORDER BY created_at"
	for range 3 {
		refs, err := Refs(sql)
		if err != nil {
			t.Fatal(err)
		}
		if len(refs) != 2 {
			t.Fatalf("got %d refs", len(refs))
		}
		for _, r := range refs {
			if got := sql[r.Pos : r.Pos+len(r.Column)]; got != r.Column {
				t.Errorf("Pos %d points at %q, want %q", r.Pos, got, r.Column)
			}
		}
		if refs[0].Pos >= refs[1].Pos {
			t.Errorf("not in source order: %d, %d", refs[0].Pos, refs[1].Pos)
		}
	}
}

func TestRefsSyntaxError(t *testing.T) {
	if _, err := Refs("SELEC 1"); err == nil {
		t.Error("expected a syntax error")
	}
}

func TestRelations(t *testing.T) {
	tests := []struct {
		sql  string
		want []RelRef
	}{
		{"SELECT 1", nil},
		{"SELECT * FROM pg_catalog.pg_class c JOIN b ON true", []RelRef{{"pg_catalog", "pg_class", "c"}, {"", "b", ""}}},
		{"WITH c AS (SELECT * FROM t) SELECT * FROM c", []RelRef{{"", "t", ""}, {"", "", "c"}}},
	}
	for _, tc := range tests {
		t.Run(tc.sql, func(t *testing.T) {
			got, err := Relations(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("got %v want %v", got, tc.want)
			}
		})
	}
}
