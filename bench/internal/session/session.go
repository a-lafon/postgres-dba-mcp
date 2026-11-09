// Package session measures what a DBA investigation costs in context. It is deterministic and calls no model: a
// fixed sequence of intentions, each answered by the server's best tool, or, when it has none, by the same
// fallback SQL through execute_sql for every server that falls back. That is what an agent facing a tool-poor
// server does, and it is what makes the comparison honest: a missing tool is not a missing answer, it is a bigger
// answer to read.
package session

import (
	"context"
	"fmt"
	"io"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// Intent is one turn: what the DBA wants, the tool that answers it best, and the SQL a server without that tool
// falls back to. SQL is the same for every server that falls back, and it is written to the results.
type Intent struct {
	ID   string
	Want string
	Tool string // canonical bench tool; empty when no server has one and everybody runs SQL
	Args map[string]any
	SQL  string
}

const slowQuery = "SELECT * FROM events WHERE account_id = 42 ORDER BY created_at DESC LIMIT 20"

// Intents is the investigation, in order: find your way around, look at the suspect table, read a plan, see what
// the workload costs, check the indexes, then three targeted reads. Ten turns, fixed, the same for every server.
func Intents() []Intent {
	return []Intent{
		{ID: "schemas", Want: "which schemas are there", Tool: "list_schemas", Args: map[string]any{},
			SQL: "SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg_%' AND nspname <> 'information_schema' ORDER BY 1"},
		{ID: "tables", Want: "which tables are in public", Tool: "list_objects",
			Args: map[string]any{"schema_name": "public", "object_type": "table"},
			SQL:  "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public' AND table_type = 'BASE TABLE' ORDER BY 1"},
		{ID: "detail", Want: "what public.rental looks like", Tool: "get_object_details",
			Args: map[string]any{"schema_name": "public", "object_name": "rental", "object_type": "table"},
			SQL:  "SELECT column_name, data_type, is_nullable FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'rental' ORDER BY ordinal_position"},
		{ID: "sizes", Want: "which tables are the biggest",
			SQL: "SELECT c.relname, pg_total_relation_size(c.oid) AS bytes FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind = 'r' ORDER BY 2 DESC LIMIT 10"},
		{ID: "plan", Want: "why a slow query is slow", Tool: "explain_query",
			Args: map[string]any{"sql": slowQuery}, SQL: "EXPLAIN " + slowQuery},
		{ID: "top", Want: "the costliest queries of the workload", Tool: "get_top_queries",
			Args: map[string]any{"sort_by": "total_time", "limit": 10},
			SQL:  "SELECT query, calls, total_exec_time, mean_exec_time FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 10"},
		{ID: "health", Want: "which indexes are unused or invalid", Tool: "analyze_db_health",
			Args: map[string]any{"health_type": "index"},
			SQL:  "SELECT schemaname, relname, indexrelname, idx_scan, pg_relation_size(indexrelid) AS bytes FROM pg_stat_user_indexes ORDER BY idx_scan, bytes DESC LIMIT 20"},
		{ID: "count", Want: "how many rows the suspect account has",
			SQL: "SELECT count(*) FROM events WHERE account_id = 42"},
		{ID: "sample", Want: "its last events", SQL: slowQuery},
		{ID: "status", Want: "how the orders break down by status",
			SQL: "SELECT status, count(*) FROM orders GROUP BY 1 ORDER BY 2 DESC"},
	}
}

// call says how a server answers the intent: with the tool it has for it, or, when it has none, with the same
// fallback SQL every other tool-poor server runs. ok is false when it cannot answer at all.
func (it Intent) call(spec servers.Spec) (name string, args map[string]any, fallback bool, ok bool) {
	if it.Tool != "" {
		if name, ok = spec.ToolName(it.Tool); ok {
			return name, it.Args, false, true
		}
	}
	if name, ok = spec.ToolName("execute_sql"); !ok {
		return "", nil, true, false
	}
	return name, map[string]any{"sql": it.SQL}, true, true
}

// Runner replays the investigation on every server.
type Runner struct {
	Counter *tokens.Counter
	Specs   []servers.Spec
	Log     io.Writer
}

// Run returns one row per server, with its tool-definition cost and its ten turns.
func (r *Runner) Run(ctx context.Context) ([]results.SessionServer, error) {
	intents := Intents()
	var out []results.SessionServer
	for _, spec := range r.Specs {
		row, err := r.runServer(ctx, spec, intents)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, nil
}

func (r *Runner) runServer(ctx context.Context, spec servers.Spec, intents []Intent) (results.SessionServer, error) {
	row := results.SessionServer{Server: spec.Name}
	srv, err := spec.Start(ctx)
	if err != nil {
		return row, err
	}
	defer srv.Close()
	tools, err := srv.Tools(ctx)
	if err != nil {
		return row, err
	}
	if row.ToolTokens, err = r.Counter.Tools(ctx, tools); err != nil {
		return row, err
	}
	cum := row.ToolTokens
	for _, it := range intents {
		_, _ = fmt.Fprintf(r.Log, "session %s %s\n", spec.Name, it.ID)
		turn := results.SessionTurn{Intent: it.ID, Want: it.Want, Status: results.StatusOK}
		name, args, fallback, ok := it.call(spec)
		if !ok {
			turn.Status, turn.Note = results.StatusNoTool, "no execute_sql to fall back to"
			row.Turns = append(row.Turns, turn)
			continue
		}
		turn.Tool = name
		if fallback {
			turn.Fallback, turn.SQL = true, it.SQL
		}
		res, err := srv.Call(ctx, name, args)
		if err != nil {
			turn.Status, turn.Note = results.StatusError, err.Error()
			row.Turns = append(row.Turns, turn)
			// The session is a session: a dead call ends it, and the turns after it are not measured.
			return row, nil
		}
		n, _, err := r.Counter.TextEst(ctx, res.Text)
		if err != nil {
			return row, err
		}
		turn.Tokens, turn.Bytes = n, len(res.Text)
		if res.IsError {
			turn.Note = firstLine(res.Text)
		}
		cum += n
		turn.Cumulative = cum
		row.Turns = append(row.Turns, turn)
	}
	return row, nil
}

func firstLine(s string) string {
	for i, c := range s {
		if c == '\n' {
			s = s[:i]
			break
		}
	}
	if len(s) > 160 {
		s = s[:160]
	}
	return s
}
