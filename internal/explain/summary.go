package explain

import (
	"context"
	"fmt"
	"math"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
)

const (
	maxNodes       = 5
	minShare       = 0.10
	seqScanRows    = 10000 // minimum reltuples for the sequential scan warning
	seqScanFrac    = 0.01
	badEstimateMin = 100
	badEstimateX   = 10
)

// shown is a plan node selected for the summary.
type shown struct {
	Node     *hypo.Node
	Share    float64  // fraction of the summed self cost (or self time)
	Rows     float64  // estimated rows, all processes
	Actual   *float64 // actual rows times loops, analyze only
	Warnings []string
}

// summary is the computed result of explain_query.
type summary struct {
	TotalCost, Rows float64
	ActualRows      *float64
	ExecutionMs     *float64
	Analyze         bool
	Generic         bool
	Hypo            *hypoSummary
	Nodes           []shown
}

// hypoSummary compares the plan without and with the hypothetical indexes.
type hypoSummary struct {
	Before, After float64
	Used          []string
}

type flat struct {
	node *hypo.Node
	rows float64
	self float64
}

// measure is the node's total cost, or its total time over all loops with analyze.
func measure(n *hypo.Node, analyze bool) float64 {
	if !analyze {
		return n.TotalCost
	}
	if n.ActualTotalMs == nil {
		return 0
	}
	return *n.ActualTotalMs * loops(n)
}

func loops(n *hypo.Node) float64 {
	if n.Loops == nil {
		return 1
	}
	return *n.Loops
}

// flatten lists the nodes in plan order. mult is the process count (workers plus the leader) of the nearest
// Gather ancestor, 0 when there is none.
func flatten(n *hypo.Node, mult float64, analyze bool, out *[]flat) {
	rows := n.PlanRows
	if n.ParallelAware && mult > 0 {
		rows *= mult
	}
	self := measure(n, analyze)
	for _, c := range n.Plans {
		self -= measure(c, analyze)
	}
	*out = append(*out, flat{n, rows, math.Max(self, 0)})
	if n.NodeType == "Gather" || n.NodeType == "Gather Merge" {
		mult = float64(n.WorkersPlanned + 1)
	}
	for _, c := range n.Plans {
		flatten(c, mult, analyze, out)
	}
}

// weigh selects the nodes that carry at least 10 % of the cost (or time), at most 5, largest first; the largest
// alone when none reaches 10 %.
func weigh(root *hypo.Node, analyze bool) []shown {
	var nodes []flat
	flatten(root, 0, analyze, &nodes)
	var sum float64
	for _, f := range nodes {
		sum += f.self
	}
	all := make([]shown, len(nodes))
	for i, f := range nodes {
		all[i] = shown{Node: f.node, Rows: f.rows}
		if sum > 0 {
			all[i].Share = f.self / sum
		}
		if analyze && f.node.ActualRows != nil {
			a := *f.node.ActualRows * loops(f.node)
			all[i].Actual = &a
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Share > all[j].Share })
	n := 0
	for n < len(all) && n < maxNodes && all[n].Share >= minShare {
		n++
	}
	return all[:max(n, 1)]
}

// relKey identifies a relation in the reltuples map.
func relKey(schema, name string) string { return schema + "." + name }

// seqScanned lists the relations of the shown sequential scans that have a filter.
func seqScanned(nodes []shown) (schemas, names []string) {
	for _, s := range nodes {
		if isFilteredSeqScan(s.Node) {
			schemas, names = append(schemas, s.Node.Schema), append(names, s.Node.Relation)
		}
	}
	return schemas, names
}

func isFilteredSeqScan(n *hypo.Node) bool {
	return n.NodeType == "Seq Scan" && n.Filter != "" && n.Relation != ""
}

// reltuples reads pg_class.reltuples of the given relations with one query.
func reltuples(ctx context.Context, tx pgx.Tx, schemas, names []string) (map[string]float64, error) {
	out := map[string]float64{}
	if len(names) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `SELECT n.nspname, c.relname, c.reltuples
FROM unnest($1::text[], $2::text[]) AS r(s, t)
JOIN pg_namespace n ON n.nspname = r.s
JOIN pg_class c ON c.relnamespace = n.oid AND c.relname = r.t`, schemas, names)
	if err != nil {
		return nil, fmt.Errorf("read relation sizes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var s, n string
		var t float32
		if err := rows.Scan(&s, &n, &t); err != nil {
			return nil, fmt.Errorf("read relation sizes: %w", err)
		}
		out[relKey(s, n)] = float64(t)
	}
	return out, rows.Err()
}

// addWarnings attaches the three warnings of the spec to the shown nodes.
func addWarnings(nodes []shown, analyze bool, tuples map[string]float64) {
	for i := range nodes {
		s := &nodes[i]
		n := s.Node
		rows := s.Rows
		if analyze && s.Actual != nil {
			rows = *s.Actual
		}
		if t := tuples[relKey(n.Schema, n.Relation)]; isFilteredSeqScan(n) && t >= seqScanRows && rows <= t*seqScanFrac {
			s.Warnings = append(s.Warnings, fmt.Sprintf("sequential scan of ~%s rows returns ~%s; an index on the filtered columns may help",
				approx(t), approx(rows)))
		}
		// Plan Rows and Actual Rows are both per loop: compare them as is, not the loop total of Actual.
		if analyze && n.ActualRows != nil {
			est, act := n.PlanRows, *n.ActualRows
			hi, lo := math.Max(est, act), math.Min(est, act)
			if hi >= badEstimateMin && hi >= badEstimateX*lo {
				per := ""
				if loops(n) > 1 {
					per = " per loop"
				}
				w := fmt.Sprintf("row estimate off by %.0fx (estimated %s, actual %s%s); statistics may be stale",
					hi/math.Max(lo, 1), approx(est), approx(act), per)
				if n.Relation != "" {
					w += fmt.Sprintf(" (ANALYZE %s)", qualified(n))
				}
				s.Warnings = append(s.Warnings, w)
			}
		}
		if analyze && n.SortSpaceType == "Disk" && n.SortSpaceUsedKB != nil {
			s.Warnings = append(s.Warnings, fmt.Sprintf("sort spilled to disk (%s); raise work_mem or add an index that provides the order",
				format.Size(int64(*n.SortSpaceUsedKB*1024))))
		}
	}
}

func approx(f float64) string { return format.Approx(int64(math.Round(f))) }

// qualified is schema.relation, or the bare relation when the plan has no schema.
func qualified(n *hypo.Node) string {
	if n.Schema == "" {
		return n.Relation
	}
	return n.Schema + "." + n.Relation
}

// usedIndexes returns the hypothetical index names that the plan scans, in creation order.
func usedIndexes(root *hypo.Node, names []string) []string {
	in := map[string]bool{}
	var walk func(*hypo.Node)
	walk = func(n *hypo.Node) {
		in[n.Index] = true
		for _, c := range n.Plans {
			walk(c)
		}
	}
	walk(root)
	var used []string
	for _, n := range names {
		if in[n] {
			used = append(used, n)
		}
	}
	return used
}
