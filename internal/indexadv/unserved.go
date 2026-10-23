package indexadv

import (
	"context"
	"errors"
	"math"
	"slices"
)

const (
	// servedCut is the share of its base cost a query must be brought down to, at most, to count as served: a
	// query the picks leave above it is unserved.
	servedCut = 0.5
	// maxUnserved is the number of unserved-query lines rendered.
	maxUnserved = 3
)

// hit is one candidate that brings an unserved query down to servedCut of its base cost.
type hit struct {
	q    int // index of the query in result.Queries
	cand candidate
	size int64
	cost float64 // weighted cost of the query with the candidate added to the picks
}

// unserved is a candidate the search did not select that serves at least one query the picks leave unserved.
type unserved struct {
	cand   candidate
	size   int64
	qs     []int     // indexes of the queries it serves
	qcost  []float64 // their weighted cost with the candidate added to the picks
	change float64   // workload cost change when added to the picks, relative to the base cost
}

// groupHits merges the hits that name the same candidate, in order of first appearance.
func groupHits(hits []hit) []unserved {
	var out []unserved
	for _, h := range hits {
		i := slices.IndexFunc(out, func(u unserved) bool { return u.cand.key == h.cand.key })
		if i < 0 {
			out = append(out, unserved{cand: h.cand, size: h.size})
			i = len(out) - 1
		}
		out[i].qs = append(out[i].qs, h.q)
		out[i].qcost = append(out[i].qcost, h.cost)
	}
	return out
}

// rankUnserved sorts by workload gain, the largest first, and keeps at most n.
func rankUnserved(us []unserved, n int) []unserved {
	slices.SortStableFunc(us, func(a, b unserved) int {
		switch {
		case a.change < b.change:
			return -1
		case a.change > b.change:
			return 1
		}
		return 0
	})
	return us[:min(n, len(us))]
}

// findUnserved lists, for each query the picks leave above servedCut of its base cost, the candidate that lowers
// that query's cost most, provided it brings it to servedCut or less, is not a pick and fits in the size budget
// left. partial is true when the time budget ended the listing; what was found is returned.
func findUnserved(ctx context.Context, m *costModel, res result, cands []candidate, maxBytes int64) (out []unserved, partial bool, err error) {
	set := make([]candidate, len(res.Picks))
	var used int64
	for i, p := range res.Picks {
		set[i] = p.cand
		used += p.size
	}
	var hits []hit
scan:
	for i, q := range res.Queries {
		if res.FinalQ[i] <= servedCut*res.BaseQ[i] {
			continue
		}
		var best hit
		found := false
		for _, c := range cands {
			if slices.ContainsFunc(set, func(p candidate) bool { return p.key == c.key }) {
				continue
			}
			cost, err := m.queryCost(ctx, q, append(slices.Clone(set), c))
			if errors.Is(err, errExplain) {
				continue
			}
			if errors.Is(err, errBudget) {
				partial = true
				break scan
			}
			if err != nil {
				return nil, false, err
			}
			w := q.Weight * cost
			if w > servedCut*res.BaseQ[i] || (found && w >= best.cost) {
				continue
			}
			size, err := m.size(ctx, c)
			if errors.Is(err, errUnusable) {
				continue
			}
			if errors.Is(err, errBudget) {
				partial = true
				break scan
			}
			if err != nil {
				return nil, false, err
			}
			if used+size <= maxBytes {
				best, found = hit{i, c, size, w}, true
			}
		}
		if found {
			hits = append(hits, best)
		}
	}
	for _, u := range groupHits(hits) {
		total, err := m.total(ctx, append(slices.Clone(set), u.cand))
		if errors.Is(err, errBudget) {
			partial = true
			break
		}
		if err != nil {
			return nil, false, err
		}
		if math.IsInf(total, 1) || res.Base == 0 {
			continue
		}
		u.change = (total - res.Final) / res.Base
		out = append(out, u)
	}
	return rankUnserved(out, maxUnserved), partial, nil
}
