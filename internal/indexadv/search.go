package indexadv

import (
	"errors"
	"math"
	"slices"
)

const (
	// minGain is the least relative cost reduction an added index must bring.
	minGain = 0.10
	// minShare is the least reduction an added index must bring, as a share of the cost of the workload without
	// any index. It only bites from the second index on: the first one already needs minGain of that same cost.
	minShare = 0.01
	// extendTol is how much more than the selected index a wider index with the same leading columns may cost
	// and still replace it, as a relative cost excess.
	extendTol = 0.02
	// spaceWeight is crystaldba's alpha: objective = ln(cost) + spaceWeight * ln(space).
	spaceWeight = 2.0
)

// search is the greedy index selection of crystaldba's DTA. Its cost and size functions return errBudget when the
// time budget is spent and errUnusable for a candidate that cannot be created.
type search struct {
	cost     func(set []candidate) (float64, error)
	size     func(c candidate) (int64, error)
	baseSize int64 // size of the candidate tables
	maxBytes int64 // budget for the sum of the index sizes
}

// pick is one selected index with the workload cost once it is added.
type pick struct {
	cand candidate
	size int64
	cost float64
}

func objective(cost float64, space int64) float64 {
	return math.Log(cost) + spaceWeight*math.Log(float64(space))
}

// run adds, one at a time, the candidate that keeps the size budget, lowers the cost by at least minGain, and
// gives a better objective with a larger gain than the best so far; an index after the first must also lower the
// cost by minShare of the base cost. The selected index is then widened (see extend). The search stops when
// none qualifies. partial is true when the time budget ended the search, small when it ended with a candidate
// that the minShare rule alone refused.
func (s search) run(cands []candidate) (picks []pick, partial, small bool, err error) {
	var cur []candidate
	curCost, err := s.cost(nil)
	if err != nil {
		return nil, false, false, err
	}
	baseCost := curCost
	var curSize int64
	chosen := make([]bool, len(cands))
	for curCost > 0 {
		best, bestGain := -1, 0.0
		small = false
		bestObj := objective(curCost, s.baseSize+curSize)
		var bestSize int64
		var bestCost float64
		for i, c := range cands {
			if chosen[i] {
				continue
			}
			size, err := s.size(c)
			if errors.Is(err, errUnusable) {
				continue
			}
			if errors.Is(err, errBudget) {
				return picks, true, small, nil
			}
			if err != nil {
				return nil, false, false, err
			}
			if curSize+size > s.maxBytes {
				continue
			}
			cost, err := s.cost(append(append([]candidate(nil), cur...), c))
			if errors.Is(err, errBudget) {
				return picks, true, small, nil
			}
			if err != nil {
				return nil, false, false, err
			}
			gain := (curCost - cost) / curCost
			if gain < minGain {
				continue
			}
			if (curCost-cost)/baseCost < minShare {
				small = small || objective(cost, s.baseSize+curSize+size) < objective(curCost, s.baseSize+curSize)
				continue
			}
			if obj := objective(cost, s.baseSize+curSize+size); obj < bestObj && gain > bestGain {
				best, bestGain, bestObj, bestSize, bestCost = i, gain, obj, size, cost
			}
		}
		if best < 0 {
			break
		}
		// Widen the selection while a wider index qualifies; the narrower ones are never offered again.
		chosen[best] = true
		for {
			w, wsize, wcost, ok, err := s.extend(cands, chosen, cur, cands[best], bestCost, curSize)
			if errors.Is(err, errBudget) {
				break
			}
			if err != nil {
				return nil, false, false, err
			}
			if !ok {
				break
			}
			best, bestSize, bestCost = w, wsize, wcost
			chosen[best] = true
		}
		b := cands[best]
		cur = append(cur, b)
		curSize += bestSize
		curCost = bestCost
		picks = append(picks, pick{b, bestSize, bestCost})
	}
	return picks, false, small, nil
}

// extend looks for the candidate that widens b: same table, b's columns as leading columns, more columns that one
// query of the workload uses together, within the size budget and costing at most extendTol more than b. Among
// them it returns the index of the cheapest, the first in evaluation order on a tie. Such an index serves the conditions and
// the ordering of the queries that b only partly serves, at a cost the planner does not tell apart.
func (s search) extend(cands []candidate, chosen []bool, cur []candidate, b candidate, cost float64, curSize int64) (w int, wsize int64, wcost float64, ok bool, err error) {
	for i, c := range cands {
		if chosen[i] || !c.together || c.Table != b.Table || len(c.Cols) <= len(b.Cols) ||
			!slices.Equal(c.Cols[:len(b.Cols)], b.Cols) {
			continue
		}
		csize, err := s.size(c)
		if errors.Is(err, errUnusable) {
			continue
		}
		if err != nil {
			return 0, 0, 0, false, err
		}
		if curSize+csize > s.maxBytes {
			continue
		}
		ccost, err := s.cost(append(append([]candidate(nil), cur...), c))
		if err != nil {
			return 0, 0, 0, false, err
		}
		if ccost <= cost*(1+extendTol) && (!ok || ccost < wcost) {
			w, wsize, wcost, ok = i, csize, ccost, true
		}
	}
	return w, wsize, wcost, ok, nil
}
