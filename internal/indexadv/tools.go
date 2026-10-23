// Package indexadv implements the index advisor tools, analyze_query_indexes and analyze_workload_indexes: a
// greedy search (crystaldba's DTA) over candidate btree indexes, priced with hypothetical indexes and the planner's
// cost estimates, in one read-only transaction.
package indexadv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

const (
	defaultMaxMB = 10000
	maxQueries   = 10
	mebibyte     = 1 << 20

	queryToolDescription    = "Analyze a list of (up to 10) SQL queries and recommend optimal indexes"
	workloadToolDescription = "Analyze frequently executed queries in the database and recommend optimal indexes"
)

var (
	sizeProperty   = map[string]any{"type": "integer", "default": defaultMaxMB, "description": "Max index size in MB"}
	methodProperty = map[string]any{"type": "string", "enum": []string{"dta", "llm"}, "default": "dta", "description": "Method to use for analysis"}

	querySchema = mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"queries": map[string]any{"type": "array", "items": map[string]any{"type": "string"},
				"description": "List of Query strings to analyze"},
			"max_index_size_mb": sizeProperty,
			"method":            methodProperty,
		},
		"required": []string{"queries"},
	})
	workloadSchema = mustJSON(map[string]any{
		"type": "object",
		"properties": map[string]any{
			"max_index_size_mb": sizeProperty,
			"method":            methodProperty,
		},
	})
)

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("indexadv: marshal input schema: %v", err))
	}
	return b
}

// args is the decoded input of both tools; Queries is absent from the workload tool's schema.
type args struct {
	Queries        []string `json:"queries"`
	MaxIndexSizeMB *int     `json:"max_index_size_mb"`
	Method         *string  `json:"method"`
}

// workloadArgs is the input of analyze_workload_indexes: it refuses `queries` as an unknown argument.
type workloadArgs struct {
	MaxIndexSizeMB *int    `json:"max_index_size_mb"`
	Method         *string `json:"method"`
}

// Register adds analyze_query_indexes and analyze_workload_indexes to r. It panics when --allow-function or
// --deny-function holds an invalid pattern.
func Register(r *server.Registry) {
	cfg := r.Config()
	policy, err := safesql.NewPolicy(cfg.AllowFunctions, cfg.DenyFunctions)
	if err != nil {
		panic(fmt.Sprintf("indexadv: %v", err))
	}
	if cfg.AccessMode == config.Restricted {
		for _, w := range policy.Warnings() {
			r.Warn(w)
		}
	}
	safesql.Warm()
	a := &advisor{cfg: cfg, policy: policy, db: r.DB()}
	r.Add(server.Tool{
		Group: "index",
		Title: "Index advice for queries",
		Def: &mcp.Tool{Name: "analyze_query_indexes", Description: queryToolDescription,
			InputSchema: querySchema},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := server.Decode(raw, &in); err != nil {
				return "", err
			}
			if err := checkQueries(in.Queries); err != nil {
				return "", err
			}
			maxMB, err := validate(in.MaxIndexSizeMB, in.Method)
			if err != nil {
				return "", err
			}
			return a.run(ctx, request{queries: in.Queries, maxBytes: maxMB * mebibyte})
		},
	})
	r.Add(server.Tool{
		Group: "index",
		Title: "Index advice for workload",
		Def: &mcp.Tool{Name: "analyze_workload_indexes", Description: workloadToolDescription,
			InputSchema: workloadSchema},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in workloadArgs
			if err := server.Decode(raw, &in); err != nil {
				return "", err
			}
			maxMB, err := validate(in.MaxIndexSizeMB, in.Method)
			if err != nil {
				return "", err
			}
			return a.run(ctx, request{workload: true, maxBytes: maxMB * mebibyte})
		},
	})
}

// checkQueries applies the size rules of the queries list.
func checkQueries(qs []string) error {
	switch {
	case len(qs) == 0:
		return errors.New("Please provide a non-empty list of queries to analyze.") //nolint:staticcheck // text fixed by the spec (crystaldba's)
	case len(qs) > maxQueries:
		return errors.New("Please provide a list of up to 10 queries to analyze.") //nolint:staticcheck // text fixed by the spec (crystaldba's)
	}
	return nil
}

// validate applies the parameter rules shared by both tools and returns max_index_size_mb.
func validate(maxMB *int, method *string) (int64, error) {
	if method != nil {
		switch *method {
		case "dta":
		case "llm":
			return 0, errors.New(`method "llm" is not supported by postgres-dba-mcp; use "dta"`)
		default:
			return 0, fmt.Errorf("invalid method %q: use dta", *method)
		}
	}
	mb := defaultMaxMB
	if maxMB != nil {
		mb = *maxMB
	}
	if mb < 1 {
		return 0, errors.New("max_index_size_mb must be at least 1")
	}
	return int64(mb), nil
}
