// Package explain implements the explain_query tool: a compact summary of a query plan, optionally with
// hypothetical indexes, always run in a read-only transaction that is rolled back.
package explain

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

const (
	toolDescription = "Explains the execution plan for a SQL query, showing how the database will execute it and provides detailed cost estimates. " +
		"Queries with $n parameters get a generic plan."
	analyzeDescription = "When True, actually runs the query to show real execution statistics instead of estimates. " +
		"Takes longer but provides more accurate information."
	hypoDescription = `A list of hypothetical indexes to simulate. Each index must be a dictionary with these keys:
    - 'table': The table name to add the index to (e.g., 'users')
    - 'columns': List of column names to include in the index (e.g., ['email'] or ['last_name', 'first_name'])
    - 'using': Optional index method (default: 'btree', other options include 'hash', 'gist', etc.)

Examples: [
    {"table": "users", "columns": ["email"], "using": "btree"},
    {"table": "orders", "columns": ["user_id", "created_at"]}
]
If there is no hypothetical index, you can pass an empty list.`
	verboseDescription = "When true, returns the full PostgreSQL text plan instead of the summary."
)

// footerReserve leaves room in the byte budget for the "-- plan, <duration>" footer line.
const footerReserve = 40

// inputSchema is the explicit input schema of explain_query.
var inputSchema = mustJSON(map[string]any{
	"type": "object",
	"properties": map[string]any{
		"sql":     map[string]any{"type": "string", "description": "SQL query to explain"},
		"analyze": map[string]any{"type": "boolean", "default": false, "description": analyzeDescription},
		"hypothetical_indexes": map[string]any{
			"type": "array", "default": []any{}, "description": hypoDescription,
			"items": map[string]any{
				"type":                 "object",
				"additionalProperties": false,
				"required":             []string{"table", "columns"},
				"properties": map[string]any{
					"table":   map[string]any{"type": "string"},
					"columns": map[string]any{"type": "array", "minItems": 1, "items": map[string]any{"type": "string"}},
					"using":   map[string]any{"type": "string"},
				},
			},
		},
		"verbose": map[string]any{"type": "boolean", "default": false, "description": verboseDescription},
	},
	"required": []string{"sql"},
})

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("explain: marshal input schema: %v", err))
	}
	return b
}

// args is the decoded input of explain_query.
type args struct {
	SQL     string `json:"sql"`
	Analyze bool   `json:"analyze"`
	Verbose bool   `json:"verbose"`
	Hypo    []struct {
		Table   string   `json:"table"`
		Columns []string `json:"columns"`
		Using   string   `json:"using"`
	} `json:"hypothetical_indexes"`
}

// Register adds explain_query to r. It panics when --allow-function or --deny-function holds an invalid pattern.
func Register(r *server.Registry) {
	cfg := r.Config()
	policy, err := safesql.NewPolicy(cfg.AllowFunctions, cfg.DenyFunctions)
	if err != nil {
		panic(fmt.Sprintf("explain: %v", err))
	}
	if cfg.AccessMode == config.Restricted {
		for _, w := range policy.Warnings() {
			r.Warn(w)
		}
	}
	safesql.Warm()
	r.Add(server.Tool{
		Group: "explain",
		Title: "Explain query",
		Def: &mcp.Tool{
			Name:        "explain_query",
			Description: toolDescription,
			InputSchema: inputSchema,
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := server.Decode(raw, &in); err != nil {
				return "", err
			}
			return explain(ctx, cfg, policy, r.DB(), in)
		},
	})
}

// validate applies the checks of the spec in order and returns the hypothetical indexes.
// info is called only when a check needs the server (hypothetical indexes), so a bad argument costs no round trip.
func validate(cfg *config.Config, policy *safesql.Policy, info func() (db.Info, error), in args) ([]hypo.Index, error) {
	var idxs []hypo.Index
	if in.Analyze && len(in.Hypo) > 0 {
		return nil, errors.New("analyze cannot be combined with hypothetical indexes")
	}
	for _, h := range in.Hypo {
		idx := hypo.Index{Table: h.Table, Columns: h.Columns, Using: h.Using}
		if err := hypo.Validate(idx); err != nil {
			return nil, err
		}
		idxs = append(idxs, idx)
	}
	if len(idxs) > 0 {
		inf, err := info()
		if err != nil {
			return nil, err
		}
		if !hypo.Available(inf) {
			return nil, fmt.Errorf("hypothetical indexes need the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-%d-hypopg package)", inf.VersionNum/10000)
		}
	}
	count, kind, err := safesql.Classify(in.SQL)
	switch {
	case err != nil:
		return nil, err // already starts with "syntax error:"
	case count != 1:
		return nil, errors.New("explain_query takes exactly one statement")
	case kind == safesql.Explain:
		return nil, errors.New("pass the query without EXPLAIN; use the analyze and verbose parameters instead")
	case in.Analyze && kind != safesql.Select:
		return nil, errors.New("analyze is only allowed on SELECT, VALUES, TABLE or WITH … SELECT queries")
	}
	if cfg.AccessMode == config.Restricted {
		st, err := safesql.Check(in.SQL, policy)
		if err != nil {
			return nil, err
		}
		if st.Kind != safesql.Select {
			return nil, errors.New("restricted mode: only SELECT, VALUES, TABLE or WITH … SELECT queries can be explained")
		}
	}
	if in.Analyze && safesql.HasParams(in.SQL) {
		return nil, errors.New("analyze is not possible with $n parameters; replace them with values")
	}
	return idxs, nil
}

func explain(ctx context.Context, cfg *config.Config, policy *safesql.Policy, d *db.DB, in args) (string, error) {
	// A query copied from get_top_queries has `interval $1`: fix it before every check and the EXPLAIN.
	in.SQL = safesql.RewriteTypedParams(in.SQL)
	idxs, err := validate(cfg, policy, func() (db.Info, error) { return d.Info(ctx) }, in)
	if err != nil {
		return "", err
	}
	start := time.Now()
	var out string
	err = d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		out, err = explainTx(ctx, tx, cfg.MaxBytes, in, idxs)
		return err
	})
	if err != nil {
		return "", err
	}
	return out + "\n" + format.RenderTag("plan", time.Since(start)), nil
}

func explainTx(ctx context.Context, tx pgx.Tx, maxBytes int, in args, idxs []hypo.Index) (string, error) {
	s := summary{Analyze: in.Analyze, Generic: safesql.HasParams(in.SQL)}
	// Without hypothetical indexes the verbose form needs no JSON plan: skip it, or ANALYZE runs the query twice.
	if in.Verbose && len(idxs) == 0 {
		text, err := hypo.ExplainText(ctx, tx, in.SQL, nil, in.Analyze)
		if err != nil {
			return "", err
		}
		return renderVerbose(text, nil, maxBytes), nil
	}
	var before *hypo.Node
	if len(idxs) > 0 {
		var err error
		if before, _, err = hypo.ExplainWithTotals(ctx, tx, in.SQL, nil, false); err != nil {
			return "", err
		}
	}
	root, tot, err := hypo.ExplainWithTotals(ctx, tx, in.SQL, idxs, in.Analyze)
	if err != nil {
		return "", err
	}
	if before != nil {
		s.Hypo = &hypoSummary{Before: before.TotalCost, After: root.TotalCost, Used: usedIndexes(root, tot.HypoIndexNames)}
	}
	if in.Verbose {
		text, err := hypo.ExplainText(ctx, tx, in.SQL, idxs, in.Analyze)
		if err != nil {
			return "", err
		}
		return renderVerbose(text, s.Hypo, maxBytes), nil
	}
	s.TotalCost, s.Rows, s.ExecutionMs = root.TotalCost, root.PlanRows, tot.ExecutionMs
	if root.ActualRows != nil {
		a := *root.ActualRows * loops(root)
		s.ActualRows = &a
	}
	s.Nodes = weigh(root, in.Analyze)
	schemas, names := seqScanned(s.Nodes)
	tuples, err := reltuples(ctx, tx, schemas, names)
	if err != nil {
		return "", err
	}
	addWarnings(s.Nodes, in.Analyze, tuples)
	return renderSummary(s, maxBytes), nil
}
