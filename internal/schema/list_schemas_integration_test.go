//go:build integration

package schema

import (
	"context"
	"fmt"
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

// callListSchemas runs list_schemas through an in-memory MCP client against the fixture.
func callListSchemas(t *testing.T, args ...string) string {
	t.Helper()
	cfg, err := config.Parse(append(args, testpg.RO(t)), func(string) string { return "" })
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
	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "list_schemas"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Content[0].(*mcp.TextContent).Text
	if res.IsError {
		t.Fatalf("list_schemas failed: %s", text)
	}
	return text
}

func TestListSchemasFixture(t *testing.T) {
	text := callListSchemas(t)
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	if lines[0] != "schema_name,owner,type,tables" {
		t.Fatalf("header = %q", lines[0])
	}
	footer := lines[len(lines)-1]
	rows := lines[1 : len(lines)-1]
	m := regexp.MustCompile(`^-- (\d+) rows, \d+ ms$`).FindStringSubmatch(footer)
	if m == nil || m[1] != fmt.Sprint(len(rows)) {
		t.Fatalf("footer %q does not match %d rows", footer, len(rows))
	}

	// Expected owners and table counts come from the catalogs, so later seed files may add schemas.
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	expect := func(schema, typ string) string {
		var owner string
		if err := admin.QueryRow(ctx, "SELECT pg_get_userbyid(nspowner)::text FROM pg_namespace WHERE nspname = $1", schema).Scan(&owner); err != nil {
			t.Fatal(err)
		}
		tables := `\N`
		if typ == "user" {
			var n int
			if err := admin.QueryRow(ctx, "SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = $1 AND c.relkind IN ('r', 'p')", schema).Scan(&n); err != nil {
				t.Fatal(err)
			}
			tables = fmt.Sprint(n)
		}
		return fmt.Sprintf("%s,%s,%s,%s", schema, owner, typ, tables)
	}
	want := []string{expect("app_rw", "user"), expect("public", "user"), expect("information_schema", "system"), expect("pg_catalog", "system")}
	if want[0] != "app_rw,pgdba_rw,user,0" || !strings.HasPrefix(want[1], "public,") || !strings.HasSuffix(want[1], ",user,3") {
		t.Errorf("fixture core rows = %v", want)
	}
	next := 0
	for _, r := range rows {
		if next < len(want) && r == want[next] {
			next++
		}
	}
	if next != len(want) {
		t.Errorf("rows %q do not contain %q in this relative order", rows, want)
	}
	for _, r := range rows {
		if strings.HasPrefix(r, "pg_toast,") || strings.HasPrefix(r, "pg_temp_") || strings.HasPrefix(r, "pg_toast_temp_") {
			t.Errorf("omitted schema listed: %s", r)
		}
	}
}

func TestListSchemasRowLimit(t *testing.T) {
	text := callListSchemas(t, "--max-rows=2")
	if !strings.Contains(text, "-- 2 rows, truncated (more than 2), ") {
		t.Errorf("text = %q", text)
	}
}
