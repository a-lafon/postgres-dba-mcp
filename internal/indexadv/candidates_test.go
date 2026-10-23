package indexadv

import (
	"slices"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

func testTable(oid int64, schema, name string, cols ...string) *table {
	t := &table{OID: oid, Schema: schema, Name: name, Quoted: schema + "." + name, Kind: "r", Cols: map[string]*column{}}
	for _, c := range cols {
		t.Cols[c] = &column{Name: c, Quoted: c}
	}
	return t
}

func describeAll(cs []candidate) []string {
	var out []string
	for _, c := range cs {
		var cols []string
		for _, col := range c.Cols {
			cols = append(cols, col.Name)
		}
		out = append(out, c.Table.Name+"("+strings.Join(cols, ",")+")")
	}
	return out
}

func TestCandidatesOrder(t *testing.T) {
	events := testTable(1, "public", "events", "account_id", "created_at", "kind")
	use := columnUse{events: {
		"created_at": {use: safesql.UseSort, qi: 0, pos: 60},
		"account_id": {use: safesql.UseEq, qi: 0, pos: 40},
	}}
	got := describeAll(candidates(use, nil))
	want := []string{"events(account_id)", "events(created_at)", "events(account_id,created_at)"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
	if slices.Contains(got, "events(created_at,account_id)") {
		t.Error("one column order per set")
	}
}

func TestCandidatesOrderRules(t *testing.T) {
	tb := testTable(1, "s", "t", "a", "b", "c")
	tests := []struct {
		name string
		use  map[string]*usage
		want string // the 3-column candidate
	}{
		{"use beats position", map[string]*usage{
			"a": {safesql.UseSort, 0, 1, nil}, "b": {safesql.UseEq, 0, 99, nil}, "c": {safesql.UseRange, 0, 50, nil}}, "t(b,c,a)"},
		{"position breaks a tie", map[string]*usage{
			"a": {safesql.UseEq, 1, 5, nil}, "b": {safesql.UseEq, 0, 90, nil}, "c": {safesql.UseEq, 0, 10, nil}}, "t(c,b,a)"},
		{"name breaks the last tie", map[string]*usage{
			"c": {safesql.UseEq, 0, 5, nil}, "a": {safesql.UseEq, 0, 5, nil}, "b": {safesql.UseEq, 0, 5, nil}}, "t(a,b,c)"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := describeAll(candidates(columnUse{tb: tc.use}, nil))
			if i := slices.IndexFunc(got, func(s string) bool { return strings.Count(s, ",") == 2 }); i < 0 || got[i] != tc.want {
				t.Errorf("got %v, want %s", got, tc.want)
			}
		})
	}
}

func TestCandidatesCounts(t *testing.T) {
	for n, want := range map[int]int{1: 1, 2: 3, 3: 7, 4: 14, 5: 25} {
		cols := []string{"c1", "c2", "c3", "c4", "c5"}[:n]
		tb := testTable(1, "public", "t", cols...)
		u := map[string]*usage{}
		for i, c := range cols {
			u[c] = &usage{safesql.UseEq, 0, i, nil}
		}
		if got := len(candidates(columnUse{tb: u}, nil)); got != want {
			t.Errorf("%d columns: %d candidates, want %d", n, got, want)
		}
	}
}

func TestCandidatesExistingExcluded(t *testing.T) {
	tb := testTable(7, "public", "t", "a", "b")
	use := columnUse{tb: {"a": {safesql.UseEq, 0, 1, nil}, "b": {safesql.UseEq, 0, 2, nil}}}
	got := describeAll(candidates(use, map[int64][][]string{7: {{"a"}, {"b", "a"}}}))
	want := []string{"t(b)", "t(a,b)"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestCandidatesTableOrder(t *testing.T) {
	a, b := testTable(1, "public", "b", "x"), testTable(2, "audit", "z", "x")
	u := func() map[string]*usage { return map[string]*usage{"x": {safesql.UseEq, 0, 1, nil}} }
	for range 5 { // map iteration order must not leak
		got := describeAll(candidates(columnUse{a: u(), b: u()}, nil))
		if !slices.Equal(got, []string{"z(x)", "b(x)"}) {
			t.Fatalf("got %v", got)
		}
	}
}

func TestOwner(t *testing.T) {
	a := testTable(1, "public", "a", "id", "x")
	b := testTable(2, "public", "b", "id")
	view := testTable(3, "public", "v", "x")
	view.Kind = "v"
	cat := testTable(4, "pg_catalog", "pg_class", "relname")
	tables := map[relKey]*table{
		{"", "a"}: a, {"", "b"}: b, {"", "v"}: view, {"pg_catalog", "pg_class"}: cat, {"", "pg_class"}: cat,
	}
	rel := func(n string) safesql.RelRef { return safesql.RelRef{Name: n} }
	tests := []struct {
		name string
		ref  safesql.ColumnRef
		want *table
	}{
		{"qualified", safesql.ColumnRef{Column: "id", Rels: []safesql.RelRef{rel("b")}}, b},
		{"unqualified, one owner", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{rel("a"), rel("b")}}, a},
		{"unqualified, two owners", safesql.ColumnRef{Column: "id", Rels: []safesql.RelRef{rel("a"), rel("b")}}, nil},
		{"unqualified, no owner", safesql.ColumnRef{Column: "zz", Rels: []safesql.RelRef{rel("a")}}, nil},
		{"self join counts once", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{rel("a"), rel("a")}}, a},
		{"view owner is not indexable", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{rel("v")}}, nil},
		{"view makes it ambiguous", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{rel("a"), rel("v")}}, nil},
		{"subquery beside the one table that has it", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{rel("a"), {}}}, a},
		{"only a subquery", safesql.ColumnRef{Column: "x", Rels: []safesql.RelRef{{}}}, nil},
		{"subquery does not hide an ambiguity", safesql.ColumnRef{Column: "id", Rels: []safesql.RelRef{rel("a"), rel("b"), {}}}, nil},
		{"catalog table ignored", safesql.ColumnRef{Column: "relname", Rels: []safesql.RelRef{rel("pg_class")}}, nil},
		{"unresolved qualifier", safesql.ColumnRef{Column: "x"}, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := owner(tc.ref, tables); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCandidateDDL(t *testing.T) {
	tb := testTable(1, "public", "events", "a")
	tb.Schema, tb.Quoted = "My Schema", `"My Schema".events`
	c := candidate{Table: tb, Cols: []*column{{Name: "a", Quoted: "a"}, {Name: "Order", Quoted: `"Order"`}, {Name: "order date", Quoted: `"order date"`}}}
	if got, want := c.ddl(), `CREATE INDEX ON "My Schema".events (a, "Order", "order date");`; got != want {
		t.Errorf("ddl %q, want %q", got, want)
	}
	if got, want := c.index().Table, `"My Schema"."events"`; got != want {
		t.Errorf("hypo table %q, want %q", got, want)
	}
	if got, want := c.index().Columns, []string{"a", `"Order"`, `"order date"`}; !slices.Equal(got, want) {
		t.Errorf("hypo columns %q, want %q", got, want)
	}
}
