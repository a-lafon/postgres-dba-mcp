package schema

import (
	"context"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
)

func parse(t *testing.T, args ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(append(args, "postgres://u@h/db"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The tools get a nil runner: a test that reaches the database panics.

func TestUnknownArgument(t *testing.T) {
	tool := listSchemasTool(parse(t), nil)
	_, err := tool.Handler(context.Background(), []byte(`{"foo":1}`))
	if err == nil || err.Error() != `invalid arguments: unknown field "foo"` {
		t.Errorf("error = %v", err)
	}
}

func TestToolDefinition(t *testing.T) {
	tool := listSchemasTool(parse(t), nil)
	if tool.Group != "schema" || tool.Title != "List schemas" || tool.Def.Name != "list_schemas" ||
		tool.Def.Description != "List all schemas in the database" {
		t.Errorf("tool = %+v", tool)
	}
}

func TestRenderSchemas(t *testing.T) {
	n := int64(3)
	res := schemaList{Rows: []schemaRow{
		{"public", "pg_database_owner", "user", &n},
		{"pg_catalog", "postgres", "system", nil},
	}, More: true}
	got, err := renderSchemas(res, parse(t, "--max-rows=2"))
	if err != nil {
		t.Fatal(err)
	}
	want := "schema_name,owner,type,tables\npublic,pg_database_owner,user,3\npg_catalog,postgres,system,\\N\n" +
		"-- 2 rows, truncated (more than 2), 0.0 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n"
	if got != want {
		t.Errorf("render =\n%q\nwant\n%q", got, want)
	}
}
