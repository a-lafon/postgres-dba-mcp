package schema

import (
	"context"
	"strings"
	"testing"
)

func TestObjectToolDefinitions(t *testing.T) {
	cfg := parse(t)
	list, det := listObjectsTool(cfg, nil), objectDetailsTool(cfg, nil)
	for _, c := range []struct {
		name, tool, title, desc string
		got                     [3]string
	}{
		{"list", "list_objects", "List objects", "List objects in a schema", [3]string{list.Def.Name, list.Title, list.Def.Description}},
		{"details", "get_object_details", "Object details", "Show detailed information about a database object", [3]string{det.Def.Name, det.Title, det.Def.Description}},
	} {
		t.Run(c.name, func(t *testing.T) {
			if c.got != [3]string{c.tool, c.title, c.desc} {
				t.Errorf("got %v", c.got)
			}
		})
	}
	if list.Group != "schema" || det.Group != "schema" {
		t.Errorf("groups = %q, %q", list.Group, det.Group)
	}
}

func TestObjectArgumentErrors(t *testing.T) {
	cfg := parse(t)
	list, det := listObjectsTool(cfg, nil), objectDetailsTool(cfg, nil)
	for _, c := range []struct {
		name string
		tool func() (string, error)
		want string
	}{
		{"list unsupported type", func() (string, error) {
			return list.Handler(context.Background(), []byte(`{"schema_name":"s","object_type":"function"}`))
		}, `unsupported object_type "function" (valid: table, view, sequence, extension)`},
		{"details unsupported type", func() (string, error) {
			return det.Handler(context.Background(), []byte(`{"schema_name":"s","object_name":"o","object_type":"index"}`))
		}, `unsupported object_type "index" (valid: table, view, sequence, extension)`},
		{"list missing schema_name", func() (string, error) { return list.Handler(context.Background(), nil) }, "schema_name is required"},
		{"details missing object_name", func() (string, error) {
			return det.Handler(context.Background(), []byte(`{"schema_name":"s"}`))
		}, "schema_name and object_name are required"},
		{"unknown field", func() (string, error) {
			return list.Handler(context.Background(), []byte(`{"schema_name":"s","x":1}`))
		}, `invalid arguments: unknown field "x"`},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := c.tool()
			if err == nil || err.Error() != c.want {
				t.Errorf("error = %v, want %q", err, c.want)
			}
		})
	}
}

func TestRenderObjects(t *testing.T) {
	n, last, down := int64(1500), int64(1490), int64(75)
	cfg := parse(t)
	for _, c := range []struct {
		name string
		res  objectList
		want string
	}{
		{"tables", objectList{Type: "table", Tables: []tableRow{
			{"a", "table", &n, 2048, true, "ro"}, {"b", "partitioned", nil, 0, true, "ro"},
		}}, "name,kind,rows,size\na,table,~1.5k,2.0 kB\nb,partitioned,\\N,0 bytes\n-- 2 rows, 0.0 ms\n"},
		{"views", objectList{Type: "view", Views: []viewRow{{"v", "view", true, "ro"}, {"m", "materialized", true, "ro"}}},
			"name,kind\nv,view\nm,materialized\n-- 2 rows, 0.0 ms\n"},
		{"sequences", objectList{Type: "sequence", Sequences: []sequenceRow{
			{"s", "bigint", &last, 1, 9223372036854775807, 10}, {"u", "integer", nil, 1, 100, 1}, {"d", "integer", &down, 1, 100, -1},
		}}, "name,data_type,last_value,max_value,used\ns,bigint,1490,9223372036854775807,0.0%\nu,integer,\\N,100,\\N\nd,integer,75,100,25.3%\n-- 3 rows, 0.0 ms\n"},
		{"extensions", objectList{Type: "extension", Extensions: []extensionRow{{"hypopg", "1.4.1", "public", true}}},
			"name,version,schema,relocatable\nhypopg,1.4.1,public,yes\n-- 1 row, 0.0 ms\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, err := renderObjects(c.res, cfg)
			if err != nil || got != c.want {
				t.Errorf("render = %q, %v\nwant %q", got, err, c.want)
			}
		})
	}
}

func str(s string) *string { return &s }

func TestTableLines(t *testing.T) {
	rows := int64(3000)
	d := objectDetails{
		Type: "table", Schema: "s", Name: "orders",
		Relation: relation{Kind: "r", Rows: &rows, TableSize: 8192, IndexSize: 16384, Comment: str("customer\norders")},
		Columns: []column{
			{Name: "id", Type: "bigint", NotNull: true},
			{Name: "cid", Type: "integer", NotNull: true, Comment: str("buyer")},
			{Name: "ref", Type: "integer"},
			{Name: "status", Type: "text", NotNull: true, Default: str("'new'::text")},
			{Name: "gen", Type: "integer", Generated: "s", Default: str("(a + 1)")},
			{Name: "ident", Type: "integer", NotNull: true, Identity: "a"},
		},
		Constraints: []constraint{
			{Name: "pk", Type: "p", Columns: []string{"id"}},
			{Name: "fk1", Type: "f", Columns: []string{"cid"}, RefSchema: str("other"), RefTable: str("customers"), RefColumns: []string{"id"}, OnDelete: "c", OnUpdate: "a"},
			{Name: "fk2", Type: "f", Columns: []string{"ref"}, RefSchema: str("s"), RefTable: str("orders"), RefColumns: []string{"id"}, OnDelete: "a", OnUpdate: "n"},
			{Name: "fk3", Type: "f", Columns: []string{"a", "b"}, Def: "FOREIGN KEY (a, b) REFERENCES t(x, y)"},
			{Name: "ck", Type: "c", Def: "CHECK ((a > 0))"},
		},
		Indexes: []index{
			{Name: "pk", Def: "CREATE UNIQUE INDEX pk ON s.orders USING btree (id)", Primary: true, Unique: true, Valid: true},
			{Name: "u", Def: "CREATE UNIQUE INDEX u ON s.orders USING btree (cid)", Unique: true, Valid: true},
			{Name: "bad", Def: "CREATE INDEX bad ON s.orders USING hash (ref)"},
		},
		Partitions:   []partition{{"s", "p1", "FOR VALUES IN (1)"}, {"x", "p2", "FOR VALUES IN (2)"}},
		ReferencedBy: []reference{{"s", "child", []string{"oid"}}, {"y", "multi", []string{"a", "b"}}},
		Triggers:     []trigger{{"t1", 1 | 2 | 4 | 16}, {"t2", 4 | 8}, {"t3", 1 | 64 | 4}},
	}
	want := strings.Join([]string{
		"table s.orders: ~3.0k rows, 8.0 kB + 16 kB indexes -- customer orders",
		"  id bigint PK",
		"  cid integer NOT NULL → other.customers(id) ON DELETE CASCADE -- buyer",
		"  ref integer → orders(id) ON UPDATE SET NULL",
		"  status text NOT NULL DEFAULT 'new'::text",
		"  gen integer GENERATED ALWAYS AS ((a + 1)) STORED",
		"  ident integer NOT NULL GENERATED ALWAYS AS IDENTITY",
		"foreign keys:",
		"  fk3 FOREIGN KEY (a, b) REFERENCES t(x, y)",
		"indexes:",
		"  pk btree (id) PK",
		"  u btree (cid) UNIQUE",
		"  bad hash (ref) INVALID",
		"checks:",
		"  ck CHECK ((a > 0))",
		"partitions:",
		"  p1 FOR VALUES IN (1)",
		"  x.p2 FOR VALUES IN (2)",
		"referenced by: child.oid, y.multi(a, b)",
		"triggers:",
		"  t1 BEFORE INSERT OR UPDATE FOR EACH ROW",
		"  t2 AFTER INSERT OR DELETE FOR EACH STATEMENT",
		"  t3 INSTEAD OF INSERT FOR EACH ROW",
	}, "\n")
	if got := renderDetails(d, parse(t)); got != want {
		t.Errorf("details =\n%s\nwant\n%s", got, want)
	}
}

func TestTableHeaders(t *testing.T) {
	rows := int64(5)
	for _, c := range []struct {
		name string
		r    relation
		want string
	}{
		{"unknown rows, no index", relation{Kind: "r", TableSize: 0}, "table s.t: rows unknown, 0 bytes"},
		{"partitioned", relation{Kind: "p", Rows: &rows, PartitionKey: str("RANGE (a)")}, "table s.t (partitioned by RANGE (a)): ~5 rows, 0 bytes"},
		{"partition", relation{Kind: "r", Rows: &rows, ParentSchema: str("s"), ParentName: str("par"), Bound: str("FOR VALUES IN (1)")},
			"table s.t (partition of par FOR VALUES IN (1)): ~5 rows, 0 bytes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			got := renderDetails(objectDetails{Type: "table", Schema: "s", Name: "t", Relation: c.r}, parse(t))
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}

func TestViewSequenceExtensionLines(t *testing.T) {
	cfg := parse(t, "--max-cell=16")
	rows, last, down := int64(7), int64(1490), int64(75)
	view := objectDetails{Type: "view", Schema: "s", Name: "v", Relation: relation{Kind: "v", Comment: str("c"), Definition: str(" SELECT a,\n    b\n   FROM t;")},
		Columns: []column{{Name: "a", Type: "integer"}}}
	mat := objectDetails{Type: "view", Schema: "s", Name: "m", Relation: relation{Kind: "m", Rows: &rows, TableSize: 8192, Definition: str("SELECT 1")}}
	// 16 * 10 = 160 code points plus 20: a longer definition is shortened.
	long := objectDetails{Type: "view", Schema: "s", Name: "l", Relation: relation{Kind: "v", Definition: str(strings.Repeat("x", 190))}}
	seq := objectDetails{Type: "sequence", Schema: "s", Name: "q", Sequence: sequenceInfo{DataType: "bigint", Start: 1000, Increment: 10, Min: 1, Max: 9223372036854775807, LastValue: &last,
		OwnerSchema: str("s"), OwnerTable: str("t"), OwnerColumn: str("id")}}
	unused := objectDetails{Type: "sequence", Schema: "s", Name: "q", Sequence: sequenceInfo{DataType: "integer", Start: 1, Increment: 1, Min: 1, Max: 10, Cycle: true}}
	desc := objectDetails{Type: "sequence", Schema: "s", Name: "d", Sequence: sequenceInfo{DataType: "integer", Start: 100, Increment: -1, Min: 1, Max: 100, LastValue: &down}}
	ext := objectDetails{Type: "extension", Name: "hypopg", Extension: extensionInfo{Version: "1.4", Schema: "public", Relocatable: true, Comment: str("hypothetical indexes")}}
	for _, c := range []struct {
		name string
		d    objectDetails
		want string
	}{
		{"view", view, "view s.v -- c\n  a integer\ndefinition:\n   SELECT a,\n      b\n     FROM t;"},
		{"materialized", mat, "materialized view s.m: ~7 rows, 8.0 kB\ndefinition:\n  SELECT 1"},
		{"long definition", long, "view s.l\ndefinition:\n  " + strings.Repeat("x", 160) + "…[+30 chars]"},
		{"sequence", seq, "sequence s.q: bigint, start 1000, increment 10, min 1, max 9223372036854775807, no cycle, last value 1490 (0.0% used), owned by t.id"},
		{"unused sequence", unused, "sequence s.q: integer, start 1, increment 1, min 1, max 10, cycle, last value unknown"},
		{"descending sequence", desc, "sequence s.d: integer, start 100, increment -1, min 1, max 100, no cycle, last value 75 (25.3% used)"},
		{"extension", ext, "extension hypopg: version 1.4, schema public, relocatable yes -- hypothetical indexes"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := renderDetails(c.d, cfg); got != c.want {
				t.Errorf("got\n%q\nwant\n%q", got, c.want)
			}
		})
	}
}

func TestTableLongTextKeepsStructure(t *testing.T) {
	long := strings.Repeat("x", 300)
	d := objectDetails{
		Type: "table", Schema: "s", Name: "t",
		Relation: relation{Kind: "r", Comment: str(long)},
		Columns: []column{
			{Name: "a", Type: "integer", Default: str(long)},
			{Name: "b", Type: "integer"},
		},
		Indexes: []index{{Name: "i", Def: "CREATE INDEX i ON s.t USING btree (b)", Valid: true}},
	}
	got := renderDetails(d, parse(t, "--max-bytes=1024"))
	for _, want := range []string{"…[+", "  b integer", "indexes:", "  i btree (b)"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in\n%s", want, got)
		}
	}
	if strings.Contains(got, "truncated") || len(got) > 1024 {
		t.Errorf("unexpected truncation (len %d):\n%s", len(got), got)
	}
}

func TestTwoForeignKeysOnOneColumn(t *testing.T) {
	d := objectDetails{
		Type: "table", Schema: "s", Name: "t",
		Columns: []column{{Name: "a", Type: "integer"}},
		Constraints: []constraint{
			{Name: "fk_a_t1", Type: "f", Columns: []string{"a"}, Def: "FOREIGN KEY (a) REFERENCES t1(id)", RefTable: str("t1"), RefSchema: str("s"), RefColumns: []string{"id"}},
			{Name: "fk_a_t2", Type: "f", Columns: []string{"a"}, Def: "FOREIGN KEY (a) REFERENCES t2(id)", RefTable: str("t2"), RefSchema: str("s"), RefColumns: []string{"id"}},
		},
	}
	want := "table s.t: rows unknown, 0 bytes\n  a integer → t1(id)\nforeign keys:\n  fk_a_t2 FOREIGN KEY (a) REFERENCES t2(id)"
	if got := renderDetails(d, parse(t)); got != want {
		t.Errorf("details =\n%s\nwant\n%s", got, want)
	}
}

func TestDetailsByteBudget(t *testing.T) {
	d := objectDetails{Type: "table", Schema: "s", Name: "wide"}
	for i := 0; i < 100; i++ {
		d.Columns = append(d.Columns, column{Name: "column_" + strings.Repeat("x", 20), Type: "integer"})
	}
	cfg := parse(t, "--max-bytes=1024")
	got := renderDetails(d, cfg)
	if len(got) > 1024 {
		t.Errorf("len = %d, want <= 1024", len(got))
	}
	lines := strings.Split(got, "\n")
	if last := lines[len(lines)-1]; last != "-- truncated (1024-byte limit)" {
		t.Errorf("last line = %q", last)
	}
	if lines[1] != "  column_xxxxxxxxxxxxxxxxxxxx integer" {
		t.Errorf("column lines were cut mid-line: %q", lines[1])
	}
	if full := renderDetails(d, parse(t)); strings.Contains(full, "truncated") || strings.Count(full, "\n") != 100 {
		t.Errorf("default budget truncated the text")
	}
}

func TestArticle(t *testing.T) {
	for in, want := range map[string]string{"index": "an index", "view": "a view", "materialized view": "a materialized view", "partitioned table": "a partitioned table"} {
		if got := article(in); got != want {
			t.Errorf("article(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestUnreadableNote(t *testing.T) {
	for _, c := range []struct {
		noun  string
		names []string
		want  string
	}{
		{"table", nil, ""},
		{"table", []string{"a"}, "-- 1 table not readable by ro: a"},
		{"view", []string{"a", "b"}, "-- 2 views not readable by ro: a, b"},
		{"table", []string{"a", "b", "c", "d", "e", "f", "g"}, "-- 7 tables not readable by ro: a, b, c, d, e, …"},
	} {
		if got := unreadableNote(c.noun, "ro", c.names); got != c.want {
			t.Errorf("unreadableNote(%q, %v) = %q, want %q", c.noun, c.names, got, c.want)
		}
	}
}

func TestUnreadableNoteForgery(t *testing.T) {
	got := unreadableNote("table", "r\no", []string{"x\n-- 4 rows, 0.0 ms", "y\r\tz"})
	if strings.ContainsAny(got, "\r\n\t") {
		t.Errorf("note has a control character: %q", got)
	}
	res := objectList{Type: "table", Tables: []tableRow{{"x\n-- 4 rows, 0.0 ms", "table", nil, 0, false, "ro"}}}
	out, err := renderObjects(res, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(out, "\n"); n != 4 { // note, header, row, footer
		t.Errorf("render has %d lines, want 4:\n%q", n, out)
	}
}

func TestRenderObjectsUnreadable(t *testing.T) {
	res := objectList{Type: "view", Views: []viewRow{{"v", "view", false, "ro"}}}
	got, err := renderObjects(res, parse(t))
	if err != nil {
		t.Fatal(err)
	}
	if want := "-- 1 view not readable by ro: v\nname,kind\nv,view\n"; !strings.HasPrefix(got, want) {
		t.Errorf("render =\n%q\nwant prefix %q", got, want)
	}
}
