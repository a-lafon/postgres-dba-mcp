package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
)

// captureLog replaces the log writer for the test and returns what was written.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	logMu.Lock()
	old := logWriter
	logWriter = writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func testConfig(t *testing.T, args ...string) *config.Config {
	t.Helper()
	cfg, err := config.Parse(append(args, "postgres://u@127.0.0.1:1/x"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func newRegistry(t *testing.T, args ...string) *Registry {
	t.Helper()
	cfg := testConfig(t, args...)
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return New(cfg, d, "test")
}

func objectSchema(props string) json.RawMessage {
	return json.RawMessage(`{"type":"object","properties":{` + props + `},"additionalProperties":false}`)
}

func textTool(group, name string, h func(context.Context, json.RawMessage) (string, error)) Tool {
	return Tool{Group: group, Title: "Title " + name, Handler: h,
		Def: &mcp.Tool{Name: name, Description: "d", InputSchema: objectSchema("")}}
}

func connect(t *testing.T, r *Registry) *mcp.ClientSession {
	t.Helper()
	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := r.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return cs
}

func TestInitialize(t *testing.T) {
	for _, tt := range []struct {
		args []string
		mode string
	}{
		{nil, "restricted (read-only)"},
		{[]string{"--access-mode=unrestricted"}, "unrestricted (read-write)"},
	} {
		t.Run(tt.mode, func(t *testing.T) {
			cs := connect(t, newRegistry(t, tt.args...))
			res := cs.InitializeResult()
			if res.ServerInfo.Name != "postgres-dba-mcp" || res.ServerInfo.Version != "test" {
				t.Errorf("serverInfo = %+v", res.ServerInfo)
			}
			want := "PostgreSQL tools for one database. Access mode: " + tt.mode + ".\n" +
				"Tabular results are CSV with a header row; NULL is \\N. In values, \\n \\r \\t are control characters and \\\\ is a backslash.\n" +
				"A tsvector value is shown as [tsvector: <n> lexemes]; select col::text to read it.\n" +
				"The last line starts with \"--\" and reports row count, truncation and timing.\n" +
				"If a result is truncated, refine the query (WHERE, LIMIT, aggregates) rather than asking for more rows."
			if res.Instructions != want {
				t.Errorf("instructions = %q, want %q", res.Instructions, want)
			}
		})
	}
}

func TestListToolsAndGroups(t *testing.T) {
	r := newRegistry(t, "--tools=schema")
	ok := func(context.Context, json.RawMessage) (string, error) { return "x", nil }
	r.Add(textTool("schema", "a_schema", ok))
	r.Add(textTool("query", "b_query", ok))
	cs := connect(t, r)

	res, err := cs.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Tools) != 1 || res.Tools[0].Name != "a_schema" {
		t.Fatalf("tools = %+v", res.Tools)
	}
	raw, _ := json.Marshal(res.Tools[0])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if _, has := m["outputSchema"]; has {
		t.Errorf("tool has an outputSchema: %s", raw)
	}
	a := m["annotations"].(map[string]any)
	if a["title"] != "Title a_schema" || a["readOnlyHint"] != true || a["destructiveHint"] != false || a["openWorldHint"] != false {
		t.Errorf("annotations = %v", a)
	}

	if _, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: "b_query"}); err == nil {
		t.Error("calling a disabled tool succeeded, want a protocol error")
	}
}

func TestWritesAnnotations(t *testing.T) {
	r := newRegistry(t)
	tool := textTool("query", "w", func(context.Context, json.RawMessage) (string, error) { return "", nil })
	tool.Writes = true
	r.Add(tool)
	a := tool.Def.Annotations
	if a.ReadOnlyHint || a.DestructiveHint == nil || !*a.DestructiveHint || a.OpenWorldHint == nil || *a.OpenWorldHint {
		t.Errorf("annotations = %+v", a)
	}
}

func TestCallShapes(t *testing.T) {
	logs := captureLog(t)
	r := newRegistry(t)
	r.Add(textTool("schema", "t", func(_ context.Context, args json.RawMessage) (string, error) {
		var in struct{}
		if err := Decode(args, &in); err != nil {
			return "", err
		}
		return strings.Repeat("a", 512), nil
	}))
	r.Add(Tool{Group: "schema", Title: "Q", Def: &mcp.Tool{Name: "q", InputSchema: objectSchema(`"q":{"type":"string"}`)},
		Handler: func(_ context.Context, args json.RawMessage) (string, error) {
			var in struct{ Q string }
			if err := Decode(args, &in); err != nil {
				return "", err
			}
			return "", fmt.Errorf("run: %w", &pgconn.PgError{Code: "22P02", Message: `invalid input syntax for type integer: "` + in.Q + `"`})
		}})
	cs := connect(t, r)
	ctx := context.Background()

	t.Run("success shape", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "t"})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		if res.IsError || len(res.Content) != 1 || res.StructuredContent != nil || strings.Contains(string(raw), "structuredContent") {
			t.Fatalf("result = %s", raw)
		}
		if tc, ok := res.Content[0].(*mcp.TextContent); !ok || len(tc.Text) != 512 {
			t.Errorf("content = %+v", res.Content[0])
		}
		if !regexp.MustCompile(`call tool=t ms=[0-9]+ status=ok bytes=512\n`).MatchString(logs()) {
			t.Errorf("log = %q", logs())
		}
	})

	t.Run("unknown argument", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "t", Arguments: map[string]any{"foo": 1}})
		if err != nil {
			t.Fatal(err)
		}
		text := res.Content[0].(*mcp.TextContent).Text
		if !res.IsError || text != `ERROR: invalid arguments: unknown field "foo"` {
			t.Errorf("result = %v %q", res.IsError, text)
		}
		if !regexp.MustCompile(`call tool=t ms=[0-9]+ status=error\n`).MatchString(logs()) {
			t.Errorf("log = %q", logs())
		}
		if strings.Contains(logs(), "status=error sqlstate=") && !strings.Contains(logs(), "sqlstate=22P02") {
			t.Errorf("unexpected sqlstate in %q", logs())
		}
	})

	t.Run("missing arguments", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "t", Arguments: nil})
		if err != nil || res.IsError {
			t.Fatalf("result = %+v, err = %v", res, err)
		}
	})

	t.Run("postgres error logs the sqlstate only", func(t *testing.T) {
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "q", Arguments: map[string]any{"Q": "secret-literal"}})
		if err != nil {
			t.Fatal(err)
		}
		if !res.IsError || !strings.HasPrefix(res.Content[0].(*mcp.TextContent).Text, "ERROR: invalid input syntax") {
			t.Errorf("result = %+v", res)
		}
		if !regexp.MustCompile(`call tool=q ms=[0-9]+ status=error sqlstate=22P02\n`).MatchString(logs()) {
			t.Errorf("log = %q", logs())
		}
		if strings.Contains(logs(), "secret-literal") {
			t.Errorf("log leaks the value: %q", logs())
		}
	})
}

func TestWarnOnce(t *testing.T) {
	logs := captureLog(t)
	r := newRegistry(t)
	r.Warn("WARNING: shared")
	r.Warn("WARNING: shared")
	r.Warn("WARNING: other")
	if got := strings.Count(logs(), "WARNING: shared\n"); got != 1 {
		t.Errorf("shared warning logged %d times, want 1; log = %q", got, logs())
	}
	if !strings.Contains(logs(), "WARNING: other\n") {
		t.Errorf("log = %q", logs())
	}
}

func TestAddPanics(t *testing.T) {
	ok := func(context.Context, json.RawMessage) (string, error) { return "", nil }
	withSchema := func(name, schema string) Tool {
		tl := textTool("schema", name, ok)
		tl.Def.InputSchema = json.RawMessage(schema)
		return tl
	}
	noTitle := textTool("schema", "t", ok)
	noTitle.Title = ""
	tests := []struct {
		name string
		tool Tool
		want []string
	}{
		{"array without items", withSchema("t", `{"type":"object","properties":{"cols":{"type":"array"}}}`), []string{`"t"`, "cols"}},
		{"nested array without items",
			withSchema("n", `{"type":"object","properties":{"rows":{"type":"array","items":{"type":"object","properties":{"tags":{"type":"array"}}}}}}`),
			[]string{`"n"`, "rows[].tags"}},
		{"missing title", noTitle, []string{"Title"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := newRegistry(t)
			defer func() {
				v := recover()
				if v == nil {
					t.Fatal("Add did not panic")
				}
				for _, w := range tt.want {
					if !strings.Contains(fmt.Sprint(v), w) {
						t.Errorf("panic %q does not contain %q", v, w)
					}
				}
			}()
			r.Add(tt.tool)
		})
	}

	t.Run("array with items is accepted", func(t *testing.T) {
		r := newRegistry(t)
		r.Add(withSchema("t", `{"type":"object","properties":{"cols":{"type":"array","items":{"type":"string"}}}}`))
	})
	t.Run("duplicate name", func(t *testing.T) {
		r := newRegistry(t)
		r.Add(textTool("schema", "t", ok))
		defer func() {
			if recover() == nil {
				t.Error("duplicate Add did not panic")
			}
		}()
		r.Add(textTool("schema", "t", ok))
	})
}

func TestDecode(t *testing.T) {
	var in struct{ A int }
	for _, args := range []string{"", "null", "{}", " "} {
		if err := Decode(json.RawMessage(args), &in); err != nil {
			t.Errorf("Decode(%q) = %v", args, err)
		}
	}
	if err := Decode(json.RawMessage(`{"A":1}`), &in); err != nil || in.A != 1 {
		t.Errorf("Decode = %v, A = %d", err, in.A)
	}
	if err := Decode(json.RawMessage(`{"B":1}`), &in); err == nil || err.Error() != `invalid arguments: unknown field "B"` {
		t.Errorf("Decode unknown field = %v", err)
	}
}
