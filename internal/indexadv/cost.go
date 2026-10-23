package indexadv

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
)

var (
	// errBudget reports that the time budget ran out.
	errBudget = errors.New("time budget reached")
	// errUnusable reports a candidate that hypopg cannot create, such as a column type without btree support.
	errUnusable = errors.New("candidate cannot be created")
)

// costModel prices the workload under sets of hypothetical indexes with hypo.Explain. The cost of a query only
// depends on the indexes of its own tables, so costs are memoized on that restriction: adding an index to
// table T re-explains only the queries that reference T.
type costModel struct {
	tx       pgx.Tx
	queries  []*query
	deadline time.Time
	guarded  bool // the deadline is enforced once the base costs are known

	memo  map[string]float64 // NaN marks a failed EXPLAIN
	sizes map[string]int64   // -1 marks an unusable candidate
}

func newCostModel(tx pgx.Tx, queries []*query, deadline time.Time) *costModel {
	return &costModel{tx: tx, queries: queries, deadline: deadline,
		memo: map[string]float64{}, sizes: map[string]int64{}}
}

// expired reports whether the search must stop before running another statement.
func (m *costModel) expired() bool { return m.guarded && time.Now().After(m.deadline) }

// relevant returns the candidates of set that can change the plan of q, in key order.
func relevant(q *query, set []candidate) []candidate {
	var out []candidate
	for _, c := range set {
		if !q.exact || q.hasTable(c.Table) {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b candidate) int { return strings.Compare(a.key, b.key) })
	return out
}

// queryCost returns the planner's total cost of q under set. A failed EXPLAIN is an error only for the empty set
// (see total).
func (m *costModel) queryCost(ctx context.Context, q *query, set []candidate) (float64, error) {
	rel := relevant(q, set)
	keys := make([]string, len(rel))
	idxs := make([]hypo.Index, len(rel))
	for i, c := range rel {
		keys[i], idxs[i] = c.key, c.index()
	}
	key := fmt.Sprintf("%d|%s", q.idx, strings.Join(keys, "|"))
	if v, ok := m.memo[key]; ok {
		if math.IsNaN(v) {
			return 0, errExplain
		}
		return v, nil
	}
	if m.expired() {
		return 0, errBudget
	}
	plan, err := hypo.Explain(ctx, m.tx, q.SQL, idxs, false)
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		m.memo[key] = math.NaN()
		q.failure = err
		return 0, errExplain
	}
	m.memo[key] = plan.TotalCost
	return plan.TotalCost, nil
}

// errExplain marks a statement that failed in Postgres; the cause is kept on the query.
var errExplain = errors.New("explain failed")

// total returns Σ weight × cost of the workload under set. A configuration whose EXPLAIN fails counts as
// infinitely expensive, that is as no improvement.
func (m *costModel) total(ctx context.Context, set []candidate) (float64, error) {
	var sum float64
	for _, q := range m.queries {
		c, err := m.queryCost(ctx, q, set)
		switch {
		case errors.Is(err, errExplain):
			return math.Inf(1), nil
		case err != nil:
			return 0, err
		}
		sum += q.Weight * c
	}
	return sum, nil
}

// size returns the hypopg size estimate of c, cached.
func (m *costModel) size(ctx context.Context, c candidate) (int64, error) {
	if v, ok := m.sizes[c.key]; ok {
		if v < 0 {
			return 0, errUnusable
		}
		return v, nil
	}
	if m.expired() {
		return 0, errBudget
	}
	v, err := hypo.Size(ctx, m.tx, c.index())
	if err != nil {
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		m.sizes[c.key] = -1
		return 0, errUnusable
	}
	m.sizes[c.key] = v
	return v, nil
}

// baseCosts prices every query without hypothetical indexes. A query that cannot be explained is removed and
// reported as skipped.
func (m *costModel) baseCosts(ctx context.Context) (kept []*query, skipped []skip, err error) {
	for _, q := range m.queries {
		_, err := m.queryCost(ctx, q, nil)
		switch {
		case errors.Is(err, errExplain):
			skipped = append(skipped, skip{q.Label, reason(q.failure)})
		case err != nil:
			return nil, nil, err
		default:
			kept = append(kept, q)
		}
	}
	m.queries = kept
	return kept, skipped, nil
}
