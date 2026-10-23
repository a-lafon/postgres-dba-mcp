package indexadv

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

// timeBudget is the time the search may take, from the start of the call. A variable, so that tests can lower it.
var timeBudget = 30 * time.Second

// outcome is the kind of answer an analysis gives.
type outcome int

const (
	// recommended means at least one index lowers the workload cost.
	recommended outcome = iota
	// noGain means candidates were evaluated and none lowers the cost enough.
	noGain
	// noColumns means the queries have no condition column an index could serve.
	noColumns
	// noQueries means no query could be analyzed.
	noQueries
)

// result is the computed answer of an analysis, rendered by render.go.
type result struct {
	Kind          outcome
	Workload      bool
	Queries       []*query  // the analyzed queries
	BaseQ, FinalQ []float64 // weighted cost of each analyzed query, before and after the picks
	Base, Final   float64   // weighted cost of the workload, before and after the picks
	Picks         []pick
	Candidates    int
	Skipped       []skip
	Partial       bool
	BelowShare    bool       // a further index was refused for gaining less than minShare of the base cost
	Unserved      []unserved // unselected indexes that would serve a query the picks leave unserved
	Elapsed       time.Duration
}

// request is a validated tool call.
type request struct {
	queries  []string // the given queries, for analyze_query_indexes
	workload bool
	maxBytes int64 // index size budget in bytes
}

// advisor holds what both tools share.
type advisor struct {
	cfg    *config.Config
	policy *safesql.Policy
	db     *db.DB
}

// run analyzes the request in one read-only transaction and renders the answer.
func (a *advisor) run(ctx context.Context, req request) (string, error) {
	start := time.Now()
	info, err := a.db.Info(ctx)
	if err != nil {
		return "", err
	}
	if err := precheck(info, req.workload); err != nil {
		return "", err
	}
	var res result
	var empty bool
	err = a.db.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		var qs []*query
		if req.workload {
			var err error
			if qs, err = loadWorkload(ctx, tx); err != nil {
				return err
			}
			if empty = len(qs) == 0; empty {
				return nil
			}
		} else {
			for _, s := range req.queries {
				qs = append(qs, &query{SQL: s, Weight: 1})
			}
		}
		var err error
		res, err = a.analyze(ctx, tx, start, req, qs)
		return err
	})
	if err != nil {
		return "", err
	}
	if empty {
		return emptyWorkload(), nil
	}
	res.Elapsed = time.Since(start)
	slices.SortStableFunc(res.Skipped, func(a, b skip) int { return labelNum(a.Label) - labelNum(b.Label) })
	return render(res, a.cfg.MaxBytes), nil
}

func (a *advisor) analyze(ctx context.Context, tx pgx.Tx, start time.Time, req request, qs []*query) (result, error) {
	res := result{Workload: req.workload}
	kept, skipped := intake(a.cfg, a.policy, qs)
	res.Skipped = skipped
	for _, q := range kept {
		rels, err := safesql.Relations(q.SQL)
		if err != nil {
			return result{}, fmt.Errorf("read relations of %s: %w", q.Label, err)
		}
		q.rels = rels
	}
	tables, err := resolve(ctx, tx, kept)
	if err != nil {
		return result{}, err
	}
	kept = bind(kept, tables)

	model := newCostModel(tx, kept, start.Add(timeBudget))
	kept, failed, err := model.baseCosts(ctx)
	if err != nil {
		return result{}, err
	}
	res.Skipped = append(res.Skipped, failed...)
	res.Queries = kept
	if len(kept) == 0 {
		res.Kind = noQueries
		return res, nil
	}

	use, err := collectUse(kept, tables)
	if err != nil {
		return result{}, err
	}
	if len(use) == 0 {
		res.Kind = noColumns
		return res, nil
	}
	existing, err := existingIndexes(ctx, tx, use)
	if err != nil {
		return result{}, err
	}
	cands := candidates(use, existing)
	res.Candidates = len(cands)
	var baseSize int64
	if err := checkAnalyzed(cands, &baseSize); err != nil {
		return result{}, err
	}

	if res.Base, err = model.total(ctx, nil); err != nil {
		return result{}, err
	}
	model.guarded = true
	s := search{
		cost:     func(set []candidate) (float64, error) { return model.total(ctx, set) },
		size:     func(c candidate) (int64, error) { return model.size(ctx, c) },
		baseSize: baseSize,
		maxBytes: req.maxBytes,
	}
	if res.Picks, res.Partial, res.BelowShare, err = s.run(cands); err != nil {
		return result{}, err
	}
	model.guarded = false
	res.Final = res.Base
	set := make([]candidate, len(res.Picks))
	for i, p := range res.Picks {
		set[i], res.Final = p.cand, p.cost
	}
	for _, q := range kept {
		b, err := model.queryCost(ctx, q, nil)
		if err != nil {
			return result{}, err
		}
		f, err := model.queryCost(ctx, q, set)
		if err != nil {
			return result{}, err
		}
		res.BaseQ = append(res.BaseQ, q.Weight*b)
		res.FinalQ = append(res.FinalQ, q.Weight*f)
	}
	res.Kind = noGain
	if len(res.Picks) > 0 {
		res.Kind = recommended
		model.guarded = true
		var partial bool
		if res.Unserved, partial, err = findUnserved(ctx, model, res, cands, req.maxBytes); err != nil {
			return result{}, err
		}
		res.Partial = res.Partial || partial
	}
	return res, nil
}

// labelNum returns the number of a label such as Q12.
func labelNum(label string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(label, "Q"))
	return n
}

// checkAnalyzed fails when a candidate table has never been analyzed, and sums the sizes of the candidate tables.
func checkAnalyzed(cands []candidate, baseSize *int64) error {
	seen := map[*table]bool{}
	var never []string
	for _, c := range cands {
		t := c.Table
		if seen[t] {
			continue
		}
		seen[t] = true
		*baseSize += t.Size
		if !t.Analyzed {
			never = append(never, t.Schema+"."+t.Name)
		}
	}
	if len(never) == 0 {
		return nil
	}
	slices.Sort(never)
	return fmt.Errorf("tables never analyzed: %s; run ANALYZE on them first", strings.Join(never, ", "))
}
