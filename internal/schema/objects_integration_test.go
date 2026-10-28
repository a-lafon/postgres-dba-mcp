//go:build integration

package schema

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// callTool runs tool through an in-memory MCP client against the database of dsn and returns its text and isError.
func callTool(t *testing.T, dsn, tool, args string, cfgArgs ...string) (string, bool) {
	t.Helper()
	cfg, err := config.Parse(append(cfgArgs, dsn), func(string) string { return "" })
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
	var raw json.RawMessage
	if args != "" {
		raw = json.RawMessage(args)
	}
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: raw})
	if err != nil {
		t.Fatal(err)
	}
	return res.Content[0].(*mcp.TextContent).Text, res.IsError
}

// expect compares got with want, where <size> stands for a size and <ms> for a duration.
func expect(t *testing.T, got, want string) {
	t.Helper()
	re := regexp.QuoteMeta(want)
	re = strings.ReplaceAll(re, "<size>", `\d+(\.\d)? (bytes|kB|MB)`)
	re = strings.ReplaceAll(re, "<ms>", `\d+(\.\d)? ms`)
	if !regexp.MustCompile("^" + re + "$").MatchString(got) {
		t.Errorf("got\n%s\nwant\n%s", got, want)
	}
}

func callOK(t *testing.T, tool, args string, cfgArgs ...string) string {
	t.Helper()
	text, isErr := callTool(t, testpg.RO(t), tool, args, cfgArgs...)
	if isErr {
		t.Fatalf("%s %s failed: %s", tool, args, text)
	}
	return text
}

func callErr(t *testing.T, dsn, tool, args string) string {
	t.Helper()
	text, isErr := callTool(t, dsn, tool, args)
	if !isErr {
		t.Fatalf("%s %s succeeded: %s", tool, args, text)
	}
	return text
}

func TestListObjectsDefaultAndTables(t *testing.T) {
	want := "name,kind,rows,size\n" +
		"customers,table,~1.0k,<size>\nevents_p,partitioned (2 partitions),~1.0k,<size>\n" +
		"orders,table,~3.0k,<size>\nwide,table,~10,<size>\n-- 4 rows, <ms>\n"
	expect(t, callOK(t, "list_objects", `{"schema_name":"schema_tools"}`), want)
	expect(t, callOK(t, "list_objects", `{"schema_name":"schema_tools","object_type":"table"}`), want)
}

func TestListObjectsErrors(t *testing.T) {
	ro := testpg.RO(t)
	for _, c := range []struct{ args, want string }{
		{`{"schema_name":"schema_tools","object_type":"function"}`, `ERROR: unsupported object_type "function" (valid: table, view, sequence, extension)`},
		{`{"schema_name":"nope","object_type":"table"}`, `ERROR: schema "nope" not found`},
	} {
		if got := callErr(t, ro, "list_objects", c.args); got != c.want {
			t.Errorf("%s: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestListObjectsViewsSequencesExtensions(t *testing.T) {
	expect(t, callOK(t, "list_objects", `{"schema_name":"schema_tools","object_type":"view"}`),
		"name,kind\nactive_customers,view\n-- 1 row, <ms>\n")
	expect(t, callOK(t, "list_objects", `{"schema_name":"schema_tools","object_type":"sequence"}`),
		"name,data_type,last_value,max_value,used\ncustomers_id_seq,integer,1000,2147483647,0.0%\n"+
			"invoice_seq,bigint,1490,9223372036854775807,0.0%\n-- 2 rows, <ms>\n")

	text := callOK(t, "list_objects", `{"schema_name":"whatever","object_type":"extension"}`)
	if !strings.HasPrefix(text, "name,version,schema,relocatable\n") {
		t.Fatalf("header: %q", text)
	}
	conn, err := pgx.Connect(context.Background(), testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	for _, name := range []string{"hypopg", "pg_stat_statements", "plpgsql"} {
		var version, schema string
		var reloc bool
		if err := conn.QueryRow(context.Background(), `SELECT e.extversion, n.nspname, e.extrelocatable FROM pg_extension e JOIN pg_namespace n ON n.oid = e.extnamespace WHERE extname = $1`, name).Scan(&version, &schema, &reloc); err != nil {
			t.Fatal(err)
		}
		line := name + "," + version + "," + schema + "," + yesNo(reloc) + "\n"
		if !strings.Contains(text, line) {
			t.Errorf("missing line %q in\n%s", line, text)
		}
	}
}

func TestDetailsTables(t *testing.T) {
	expect(t, callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"orders"}`), strings.Join([]string{
		"table schema_tools.orders: ~3.0k rows, <size> + <size> indexes -- customer orders",
		"  id bigint PK",
		"  customer_id integer NOT NULL → customers(id) ON DELETE CASCADE",
		"  status text NOT NULL DEFAULT 'new'::text",
		"  total numeric(10,2)",
		"  parent_id bigint → orders(id)",
		"indexes:",
		"  orders_pkey btree (id) PK",
		"  orders_customer_id_idx btree (customer_id)",
		"referenced by: orders.parent_id",
		"triggers:",
		"  orders_touch BEFORE UPDATE FOR EACH ROW",
	}, "\n"))
	expect(t, callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"customers"}`), strings.Join([]string{
		"table schema_tools.customers: ~1.0k rows, <size> + <size> indexes -- registered customers",
		"  id integer PK GENERATED BY DEFAULT AS IDENTITY",
		"  email text NOT NULL -- login email",
		"  name text",
		"  created_at timestamp with time zone NOT NULL DEFAULT now()",
		"indexes:",
		"  customers_pkey btree (id) PK",
		"  customers_email_key btree (email) UNIQUE",
		"checks:",
		"  customers_email_check CHECK ((email ~~ '%@%'::text))",
		"referenced by: orders.customer_id",
	}, "\n"))
}

func TestDetailsPartitionedTable(t *testing.T) {
	text := callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"events_p"}`)
	if !strings.HasPrefix(text, "table schema_tools.events_p (partitioned by RANGE (created_at)): ~1.0k rows") {
		t.Errorf("header: %q", text)
	}
	for _, p := range []string{"  events_p_2025 FOR VALUES FROM (", "  events_p_2026 FOR VALUES FROM ("} {
		if !strings.Contains(text, "partitions:\n") || !strings.Contains(text, p) {
			t.Errorf("partitions section lacks %q:\n%s", p, text)
		}
	}
	part := callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"events_p_2025"}`)
	if !strings.HasPrefix(part, "table schema_tools.events_p_2025 (partition of events_p FOR VALUES FROM (") {
		t.Errorf("partition header: %q", part)
	}
}

func TestDetailsErrors(t *testing.T) {
	ro := testpg.RO(t)
	for _, c := range []struct{ args, want string }{
		{`{"schema_name":"schema_tools","object_name":"nope"}`, `ERROR: table "schema_tools.nope" not found`},
		{`{"schema_name":"schema_tools","object_name":"active_customers"}`, `ERROR: "schema_tools.active_customers" is a view, not a table`},
		{`{"schema_name":"schema_tools","object_name":"events_p","object_type":"view"}`, `ERROR: "schema_tools.events_p" is a partitioned table, not a view`},
		{`{"schema_name":"schema_tools","object_name":"orders_pkey","object_type":"sequence"}`, `ERROR: "schema_tools.orders_pkey" is an index, not a sequence`},
		{`{"schema_name":"nope","object_name":"x"}`, `ERROR: schema "nope" not found`},
		{`{"schema_name":"x","object_name":"nope","object_type":"extension"}`, `ERROR: extension "nope" not installed`},
		{`{"schema_name":"x","object_name":"y","object_type":"function"}`, `ERROR: unsupported object_type "function" (valid: table, view, sequence, extension)`},
	} {
		if got := callErr(t, ro, "get_object_details", c.args); got != c.want {
			t.Errorf("%s: got %q, want %q", c.args, got, c.want)
		}
	}
}

func TestDetailsViewSequenceExtension(t *testing.T) {
	view := callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"active_customers","object_type":"view"}`)
	if !strings.HasPrefix(view, "view schema_tools.active_customers\n  id integer\n  email text\ndefinition:\n  ") || !strings.Contains(view, "customers") {
		t.Errorf("view details: %q", view)
	}
	if got := callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"invoice_seq","object_type":"sequence"}`); got !=
		"sequence schema_tools.invoice_seq: bigint, start 1000, increment 10, min 1, max 9223372036854775807, no cycle, last value 1490 (0.0% used)" {
		t.Errorf("sequence details: %q", got)
	}
	if got := callOK(t, "get_object_details", `{"schema_name":"schema_tools","object_name":"customers_id_seq","object_type":"sequence"}`); !strings.HasSuffix(got, ", owned by customers.id") {
		t.Errorf("identity sequence details: %q", got)
	}

	var version string
	conn, err := pgx.Connect(context.Background(), testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(context.Background()) }()
	if err := conn.QueryRow(context.Background(), `SELECT extversion FROM pg_extension WHERE extname = 'hypopg'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if got := callOK(t, "get_object_details", `{"schema_name":"ignored","object_name":"hypopg","object_type":"extension"}`); !strings.HasPrefix(got, "extension hypopg: version "+version+", schema ") {
		t.Errorf("extension details: %q", got)
	}
}

func TestDetailsByteBudgetOnWideTable(t *testing.T) {
	args := `{"schema_name":"schema_tools","object_name":"wide"}`
	small := callOK(t, "get_object_details", args, "--max-bytes=1024")
	lines := strings.Split(small, "\n")
	if len(small) > 1024 || lines[len(lines)-1] != "-- truncated (1024-byte limit)" {
		t.Errorf("small budget: %d bytes, last line %q", len(small), lines[len(lines)-1])
	}
	full := callOK(t, "get_object_details", args)
	if strings.Contains(full, "-- truncated") || strings.Count(full, "\n  wide_column_") != 60 {
		t.Errorf("default budget: truncated or missing columns:\n%s", full)
	}
}

// tempSchema creates sql as postgres in a fresh database and returns the DSN of pgdba_ro on it.
func tempSchema(t *testing.T, sql string) string {
	t.Helper()
	name := testpg.TempDB(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testpg.DSN(testpg.Admin(t), name))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, sql); err != nil {
		t.Fatal(err)
	}
	return testpg.DSN(testpg.RO(t), name)
}

func TestDetailsNoConstraintLeak(t *testing.T) {
	dsn := tempSchema(t, `
		CREATE SCHEMA leak;
		CREATE TABLE leak.a (id int, CONSTRAINT t_check CHECK (id > 0));
		CREATE TABLE leak.b (n int, CONSTRAINT t_check CHECK (n > 5));
		GRANT USAGE ON SCHEMA leak TO pgdba_ro;
		GRANT SELECT ON ALL TABLES IN SCHEMA leak TO pgdba_ro;`)
	text, isErr := callTool(t, dsn, "get_object_details", `{"schema_name":"leak","object_name":"a"}`)
	if isErr || strings.Count(text, "t_check") != 1 || !strings.Contains(text, "t_check CHECK ((id > 0))") || strings.Contains(text, "n > 5") {
		t.Errorf("details of a: %q", text)
	}
}

func TestMixedCaseNames(t *testing.T) {
	dsn := tempSchema(t, `
		CREATE SCHEMA "Sales";
		CREATE TABLE "Sales"."OrderItems" ("ItemId" integer GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, "Qty" integer);
		GRANT USAGE ON SCHEMA "Sales" TO pgdba_ro;
		GRANT SELECT ON ALL TABLES IN SCHEMA "Sales" TO pgdba_ro;
		GRANT SELECT ON ALL SEQUENCES IN SCHEMA "Sales" TO pgdba_ro;`)
	list, isErr := callTool(t, dsn, "list_objects", `{"schema_name":"Sales"}`)
	if isErr || !strings.Contains(list, "\nOrderItems,table,") {
		t.Errorf("list: %q", list)
	}
	text, isErr := callTool(t, dsn, "get_object_details", `{"schema_name":"Sales","object_name":"OrderItems"}`)
	if isErr || !strings.HasPrefix(text, "table Sales.OrderItems:") || !strings.Contains(text, "\n  ItemId integer PK GENERATED BY DEFAULT AS IDENTITY\n") {
		t.Errorf("table details: %q", text)
	}
	seq, isErr := callTool(t, dsn, "get_object_details", `{"schema_name":"Sales","object_name":"OrderItems_ItemId_seq","object_type":"sequence"}`)
	if isErr || !strings.HasSuffix(seq, ", owned by OrderItems.ItemId") {
		t.Errorf("sequence details: %q", seq)
	}
	if got := callErr(t, dsn, "list_objects", `{"schema_name":"sales"}`); got != `ERROR: schema "sales" not found` {
		t.Errorf("lower-case schema: %q", got)
	}
}

func TestAuditToolsObjects(t *testing.T) {
	// The definition text depends on the Postgres version; the header and columns do not.
	mv := callOK(t, "get_object_details", `{"schema_name":"audit_tools","object_name":"parent_names","object_type":"view"}`)
	head, def, _ := strings.Cut(mv, "\ndefinition:\n")
	expect(t, head, "materialized view audit_tools.parent_names: ~20 rows, <size> -- cached parent names\n  id integer\n  name text")
	if !strings.Contains(def, "audit_tools.parent") {
		t.Errorf("definition: %q", def)
	}
	expect(t, callOK(t, "list_objects", `{"schema_name":"audit_tools","object_type":"view"}`),
		"name,kind\nparent_names,materialized\n-- 1 row, <ms>\n")

	// A foreign key to another schema is qualified, and so is the table that references it.
	expect(t, callOK(t, "get_object_details", `{"schema_name":"audit_tools_b","object_name":"child"}`), strings.Join([]string{
		"table audit_tools_b.child: ~40 rows, <size> + <size> indexes",
		"  id integer PK",
		"  parent_id integer NOT NULL → audit_tools.parent(id) ON DELETE CASCADE",
		"indexes:",
		"  child_pkey btree (id) PK",
	}, "\n"))
	parent := callOK(t, "get_object_details", `{"schema_name":"audit_tools","object_name":"parent"}`)
	if !strings.HasSuffix(parent, "\nreferenced by: audit_tools_b.child.parent_id") {
		t.Errorf("parent details: %q", parent)
	}
}

// A descending sequence counts from max toward min: 75 of 100..1 is 25.3 %, not 74.7 %. It lives in a temporary
// database because analyze_db_health reads every sequence of the fixture.
func TestDescendingSequence(t *testing.T) {
	dsn := tempSchema(t, `
		CREATE SCHEMA audit_tools;
		CREATE SEQUENCE audit_tools.countdown AS integer INCREMENT -1 MINVALUE 1 MAXVALUE 100 START 100;
		SELECT setval('audit_tools.countdown', 75);
		GRANT USAGE ON SCHEMA audit_tools TO pgdba_ro;
		GRANT SELECT ON ALL SEQUENCES IN SCHEMA audit_tools TO pgdba_ro;`)
	if got, isErr := callTool(t, dsn, "get_object_details", `{"schema_name":"audit_tools","object_name":"countdown","object_type":"sequence"}`); isErr || got !=
		"sequence audit_tools.countdown: integer, start 100, increment -1, min 1, max 100, no cycle, last value 75 (25.3% used)" {
		t.Errorf("descending sequence details: %q", got)
	}
	list, isErr := callTool(t, dsn, "list_objects", `{"schema_name":"audit_tools","object_type":"sequence"}`)
	if isErr {
		t.Fatal(list)
	}
	expect(t, list, "name,data_type,last_value,max_value,used\ncountdown,integer,75,100,25.3%\n-- 1 row, <ms>\n")
}
