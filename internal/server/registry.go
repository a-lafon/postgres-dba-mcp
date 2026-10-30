// Package server owns the MCP plumbing shared by every tool: the registry, argument decoding, result and
// error conversion, annotations, the per-call log line and the warnings logged once.
package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// logWriter receives the log lines of this package; tests replace it.
var (
	logMu     sync.Mutex
	logWriter io.Writer = os.Stderr
)

func logLine(line string) {
	logMu.Lock()
	defer logMu.Unlock()
	_, _ = fmt.Fprintln(logWriter, line)
}

// Tool is one MCP tool, registered with Registry.Add.
type Tool struct {
	Group   string    // schema|query|explain|stats|health|index|locks
	Def     *mcp.Tool // Name, Description, InputSchema; Annotations are set by Add
	Title   string    // required; the annotation title
	Writes  bool      // true only for execute_sql in unrestricted mode
	Handler func(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds the MCP server and the tools that the enabled groups let through.
type Registry struct {
	srv *mcp.Server
	cfg *config.Config
	d   *db.DB

	mu     sync.Mutex
	names  map[string]bool
	warned map[string]bool
}

// New builds the MCP server (name, version, instructions) and an empty registry.
func New(cfg *config.Config, d *db.DB, version string) *Registry {
	mode := "restricted (read-only)"
	if cfg.AccessMode == config.Unrestricted {
		mode = "unrestricted (read-write)"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: "postgres-dba-mcp", Version: version},
		&mcp.ServerOptions{Instructions: instructions(mode)})
	return &Registry{srv: srv, cfg: cfg, d: d, names: map[string]bool{}, warned: map[string]bool{}}
}

func instructions(mode string) string {
	return "PostgreSQL tools for one database. Access mode: " + mode + ".\n" +
		"Tabular results are CSV with a header row; NULL is \\N. In values, \\n \\r \\t are control characters and \\\\ is a backslash.\n" +
		"A tsvector value is shown as [tsvector: <n> lexemes]; select col::text to read it.\n" +
		"The last line starts with \"--\" and reports row count, truncation and timing.\n" +
		"If a result is truncated, refine the query (WHERE, LIMIT, aggregates) rather than asking for more rows.\n" +
		"A long cell is cut and ends with …[+<n> chars]; execute_sql takes max_cell to read more of it.\n" +
		"When a column holds jsonb or long text, pass max_cell on the first call: reading the same row twice costs more than one wider call."
}

// MCP returns the MCP server that transports serve.
func (r *Registry) MCP() *mcp.Server { return r.srv }

// DB returns the database handle shared by the tools.
func (r *Registry) DB() *db.DB { return r.d }

// Config returns the process configuration.
func (r *Registry) Config() *config.Config { return r.cfg }

// Warn writes line to stderr, once per distinct text for the process lifetime.
func (r *Registry) Warn(line string) {
	r.mu.Lock()
	seen := r.warned[line]
	r.warned[line] = true
	r.mu.Unlock()
	if !seen {
		logLine(line)
	}
}

// Add registers t unless its group is disabled by --tools. It panics on a programming error: empty title,
// duplicate name, or an array in the input schema without items.
func (r *Registry) Add(t Tool) {
	if t.Def == nil || t.Def.Name == "" {
		panic("server: tool without a definition or a name")
	}
	name := t.Def.Name
	if t.Title == "" {
		panic(fmt.Sprintf("server: tool %q has no Title", name))
	}
	if t.Handler == nil {
		panic(fmt.Sprintf("server: tool %q has no Handler", name))
	}
	if err := checkArrayItems(t.Def.InputSchema); err != nil {
		panic(fmt.Sprintf("server: tool %q: %v", name, err))
	}
	r.mu.Lock()
	dup := r.names[name]
	r.names[name] = true
	r.mu.Unlock()
	if dup {
		panic(fmt.Sprintf("server: tool %q registered twice", name))
	}
	if !r.cfg.ToolEnabled(t.Group) {
		return
	}

	f := false
	writes := t.Writes
	t.Def.Annotations = &mcp.ToolAnnotations{
		Title:           t.Title,
		ReadOnlyHint:    !writes,
		DestructiveHint: &writes,
		OpenWorldHint:   &f,
	}
	r.srv.AddTool(t.Def, r.handler(name, t.Handler))
}

// handler converts the text-or-error handler into an MCP handler and logs the call.
func (r *Registry) handler(name string, h func(context.Context, json.RawMessage) (string, error)) mcp.ToolHandler {
	return func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		text, err := h(ctx, req.Params.Arguments)
		ms := time.Since(start).Milliseconds()
		if err != nil {
			line := fmt.Sprintf("call tool=%s ms=%d status=error", name, ms)
			var pe *pgconn.PgError
			if errors.As(err, &pe) {
				line += " sqlstate=" + pe.Code
			}
			logLine(line)
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: format.Error(err)}}}, nil
		}
		logLine(fmt.Sprintf("call tool=%s ms=%d status=ok bytes=%d", name, ms, len(text)))
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}, nil
	}
}

// Decode reads tool arguments into v, refusing unknown fields. Missing or null arguments read as {}.
func Decode(args json.RawMessage, v any) error {
	if t := bytes.TrimSpace(args); len(t) == 0 || string(t) == "null" {
		args = json.RawMessage("{}")
	}
	dec := json.NewDecoder(bytes.NewReader(args))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("invalid arguments: %s", strings.TrimPrefix(err.Error(), "json: "))
	}
	return nil
}

// checkArrayItems returns an error naming the property path of the first array without items.
func checkArrayItems(schema any) error {
	raw, err := json.Marshal(schema)
	if err != nil {
		return fmt.Errorf("marshal input schema: %w", err)
	}
	var node any
	if err := json.Unmarshal(raw, &node); err != nil {
		return fmt.Errorf("unmarshal input schema: %w", err)
	}
	return walkSchema(node, "")
}

func walkSchema(node any, path string) error {
	switch n := node.(type) {
	case []any:
		for _, e := range n {
			if err := walkSchema(e, path); err != nil {
				return err
			}
		}
	case map[string]any:
		if isArrayType(n["type"]) && n["items"] == nil {
			return fmt.Errorf("array property %q has no items", strings.TrimPrefix(path, "."))
		}
		for k, v := range n {
			var err error
			switch k {
			case "properties":
				props, _ := v.(map[string]any)
				for pk, pv := range props {
					if err = walkSchema(pv, path+"."+pk); err != nil {
						return err
					}
				}
			case "items":
				err = walkSchema(v, path+"[]")
			default:
				err = walkSchema(v, path)
			}
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func isArrayType(t any) bool {
	switch v := t.(type) {
	case string:
		return v == "array"
	case []any:
		for _, e := range v {
			if e == "array" {
				return true
			}
		}
	}
	return false
}
