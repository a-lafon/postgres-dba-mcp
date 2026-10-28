package query

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

func testConfig(t *testing.T, args ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(append(args, "postgres://u@localhost/db"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// testTool builds execute_sql without a database: a call that reaches it panics on the nil *db.DB.
func testTool(t *testing.T, args ...string) server.Tool {
	t.Helper()
	cfg := testConfig(t, args...)
	policy, err := safesql.NewPolicy(cfg.AllowFunctions, cfg.DenyFunctions)
	if err != nil {
		t.Fatal(err)
	}
	return executeSQLTool(cfg, nil, policy)
}

func TestToolDefinition(t *testing.T) {
	tests := []struct {
		name                          string
		args                          []string
		desc, maxRows                 string
		readOnlyHint, destructiveHint bool
	}{
		{"restricted", []string{"--max-rows=100", "--max-rows-limit=1000"}, "Execute a read-only SQL query. Long cells are shortened; pass max_cell to read more. The result is also capped at 32768 bytes, whatever max_rows says; the footer says so when it is cut.",
			"Maximum rows to return (default 100, at most 1000)", true, false},
		{"unrestricted", []string{"--access-mode=unrestricted", "--max-rows=10", "--max-rows-limit=50", "--max-bytes=2048"}, "Execute any SQL query. Long cells are shortened; pass max_cell to read more. The result is also capped at 2048 bytes, whatever max_rows says; the footer says so when it is cut.",
			"Maximum rows to return (default 10, at most 50)", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := testConfig(t, tt.args...)
			reg := server.New(cfg, nil, "test")
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
			list, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			if len(list.Tools) != 1 {
				t.Fatalf("tools = %d, want 1", len(list.Tools))
			}
			tool := list.Tools[0]
			if tool.Name != "execute_sql" || tool.Description != tt.desc {
				t.Errorf("name/description = %q / %q", tool.Name, tool.Description)
			}
			if tool.Annotations.Title != "Execute SQL" || tool.Annotations.ReadOnlyHint != tt.readOnlyHint ||
				tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint != tt.destructiveHint {
				t.Errorf("annotations = %+v", tool.Annotations)
			}
			raw, _ := json.Marshal(tool.InputSchema)
			var schema struct {
				Required   []string
				Properties map[string]struct {
					Type        string
					Description string
					Minimum     *float64
				}
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if len(schema.Required) != 1 || schema.Required[0] != "sql" {
				t.Errorf("required = %v", schema.Required)
			}
			if p := schema.Properties["sql"]; p.Type != "string" || p.Description != "SQL to run" {
				t.Errorf("sql property = %+v", p)
			}
			if p := schema.Properties["max_rows"]; p.Type != "integer" || p.Description != tt.maxRows || p.Minimum == nil || *p.Minimum != 1 {
				t.Errorf("max_rows property = %+v", p)
			}
			if p := schema.Properties["max_cell"]; p.Type != "integer" || p.Description != "Maximum characters per cell before it is shortened (default 200, at most 10000)" || p.Minimum == nil || *p.Minimum != 1 {
				t.Errorf("max_cell property = %+v", p)
			}
		})
	}
}

func TestArgumentValidation(t *testing.T) {
	tests := []struct {
		name, args, want string
	}{
		{"missing sql", `{}`, "invalid arguments: sql is required"},
		{"null args", `null`, "invalid arguments: sql is required"},
		{"blank sql", `{"sql":" \n\t"}`, "invalid arguments: sql is required"},
		{"max_rows zero", `{"sql":"SELECT 1","max_rows":0}`, "invalid arguments: max_rows must be at least 1"},
		{"max_rows negative", `{"sql":"SELECT 1","max_rows":-3}`, "invalid arguments: max_rows must be at least 1"},
		{"max_rows above limit", `{"sql":"SELECT 1","max_rows":5000}`, "invalid arguments: max_rows 5000 exceeds the server limit 1000"},
		{"max_cell zero", `{"sql":"SELECT 1","max_cell":0}`, "invalid arguments: max_cell must be at least 1"},
		{"max_cell above limit", `{"sql":"SELECT 1","max_cell":20000}`, "invalid arguments: max_cell 20000 exceeds the server limit 10000"},
		{"unknown field", `{"sql":"SELECT 1","limit":3}`, `invalid arguments: unknown field "limit"`},
	}
	for _, mode := range []string{config.Restricted, config.Unrestricted} {
		for _, tt := range tests {
			t.Run(mode+"/"+tt.name, func(t *testing.T) {
				tool := testTool(t, "--access-mode="+mode)
				_, err := tool.Handler(context.Background(), json.RawMessage(tt.args))
				if err == nil || err.Error() != tt.want {
					t.Fatalf("err = %v, want %q", err, tt.want)
				}
			})
		}
	}
}

// TestUnrestrictedRefusals covers the statement count check of unrestricted mode, done before any database call;
// restricted refusals and the runner each mode uses are covered by the integration tests.
func TestUnrestrictedRefusals(t *testing.T) {
	tests := []struct{ name, sql, wantErr string }{
		{"comment only", "-- nothing here", "empty query"},
		{"multi", "INSERT INTO t VALUES (4); DELETE FROM t", "only one statement per call is allowed; send each statement in its own call"},
		{"syntax error", "SELEC 1", "syntax error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := testTool(t, "--access-mode="+config.Unrestricted)
			args, _ := json.Marshal(map[string]string{"sql": tt.sql})
			_, err := tool.Handler(context.Background(), args)
			if err == nil || !strings.HasPrefix(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want prefix %q", err, tt.wantErr)
			}
		})
	}
}

// TestRestrictedRefusalHint checks that a refusal unrestricted mode would have run names the flag,
// and that one it would refuse all the same does not.
func TestRestrictedRefusalHint(t *testing.T) {
	const hint = "; start the server with --access-mode=unrestricted to run it"
	tests := []struct {
		name, sql string
		wantHint  bool
	}{
		{"write", "DELETE FROM t", true},
		{"denied function", "SELECT pg_read_file('/etc/passwd')", true},
		{"multi", "SELECT 1; DELETE FROM t", false},
		{"syntax error", "SELEC 1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool := testTool(t, "--access-mode="+config.Restricted)
			args, _ := json.Marshal(map[string]string{"sql": tt.sql})
			_, err := tool.Handler(context.Background(), args)
			if err == nil {
				t.Fatalf("err = nil, want a refusal")
			}
			if got := strings.HasSuffix(err.Error(), hint); got != tt.wantHint {
				t.Fatalf("err = %q, hint = %v, want %v", err, got, tt.wantHint)
			}
		})
	}
}

// TestRegisterWarnHelper is the body of the subprocess started by TestRegisterLogsPolicyWarnings.
func TestRegisterWarnHelper(t *testing.T) {
	mode := os.Getenv("PGDBA_QUERY_WARN_MODE")
	if mode == "" {
		t.Skip("helper process only")
	}
	Register(server.New(testConfig(t, "--access-mode="+mode, "--allow-function=pg_terminate_backend"), nil, "test"))
}

func TestRegisterLogsPolicyWarnings(t *testing.T) {
	const warning = "WARNING: restricted mode: --allow-function=pg_terminate_backend re-enables functions denied by default"
	for mode, want := range map[string]bool{config.Restricted: true, config.Unrestricted: false} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(os.Args[0], "-test.run=^TestRegisterWarnHelper$")
			cmd.Env = append(os.Environ(), "PGDBA_QUERY_WARN_MODE="+mode)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("helper failed: %v\n%s", err, out)
			}
			if got := strings.Contains(string(out), warning); got != want {
				t.Errorf("warning logged = %v, want %v; output:\n%s", got, want, out)
			}
		})
	}
}
