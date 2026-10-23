// Package hypo creates hypothetical indexes with hypopg, explains queries with or without them and estimates
// their size. It always resets hypopg state before and after use, so nothing leaks to the next call.
package hypo

import "github.com/a-lafon/postgres-dba-mcp/internal/db"

// Index is a hypothetical index. Table may be schema-qualified; Using "" means btree.
type Index struct {
	Table   string
	Columns []string
	Using   string
}

// Node is one node of an EXPLAIN (FORMAT JSON) plan.
type Node struct {
	NodeType            string   `json:"Node Type"`
	ParallelAware       bool     `json:"Parallel Aware"`
	ScanDirection       string   `json:"Scan Direction"`
	WorkersPlanned      int      `json:"Workers Planned"`
	Schema              string   `json:"Schema"`
	Relation            string   `json:"Relation Name"`
	Index               string   `json:"Index Name"`
	Filter              string   `json:"Filter"`
	IndexCond           string   `json:"Index Cond"`
	JoinType            string   `json:"Join Type"`
	HashCond            string   `json:"Hash Cond"`
	MergeCond           string   `json:"Merge Cond"`
	JoinFilter          string   `json:"Join Filter"`
	SortSpaceType       string   `json:"Sort Space Type"`
	StartupCost         float64  `json:"Startup Cost"`
	TotalCost           float64  `json:"Total Cost"`
	PlanRows            float64  `json:"Plan Rows"`
	ActualRows          *float64 `json:"Actual Rows"`
	ActualTotalMs       *float64 `json:"Actual Total Time"`
	Loops               *float64 `json:"Actual Loops"`
	RowsRemovedByFilter *float64 `json:"Rows Removed by Filter"`
	SortSpaceUsedKB     *float64 `json:"Sort Space Used"`
	Plans               []*Node  `json:"Plans"`
}

// Totals holds what EXPLAIN reports outside the plan tree.
type Totals struct {
	PlanningMs, ExecutionMs *float64
	// HypoIndexNames are the names hypopg gave to the created indexes, in input order.
	HypoIndexNames []string
}

// Available reports whether the hypopg extension is installed.
func Available(info db.Info) bool {
	_, ok := info.Extensions["hypopg"]
	return ok
}
