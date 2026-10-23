package indexadv

import (
	"errors"
	"math"
	"slices"
	"testing"
)

// fake prices sets from a table: each candidate key has a cost factor, factors multiply.
type fake struct {
	base    float64
	factor  map[string]float64
	size    map[string]int64
	calls   int
	budgetN int // return errBudget from this cost call on (0 = never)
}

func (f *fake) cost(set []candidate) (float64, error) {
	f.calls++
	if f.budgetN > 0 && f.calls >= f.budgetN {
		return 0, errBudget
	}
	c := f.base
	for _, s := range set {
		c *= f.factor[s.key]
	}
	return c, nil
}

func (f *fake) sizeOf(c candidate) (int64, error) {
	if f.size[c.key] < 0 {
		return 0, errUnusable
	}
	return f.size[c.key], nil
}

func cand(key string) candidate { return candidate{key: key} }

func keys(ps []pick) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.cand.key)
	}
	return out
}

func TestSearch(t *testing.T) {
	const mb = 1 << 20
	tests := []struct {
		name    string
		f       fake
		cands   []string
		baseSz  int64
		max     int64
		want    []string
		partial bool
	}{
		{"gain below 10% is ignored", fake{base: 100, factor: map[string]float64{"a": 0.95}, size: map[string]int64{"a": mb}},
			[]string{"a"}, 100 * mb, 100 * mb, nil, false},
		{"gain of exactly 10% qualifies", fake{base: 100, factor: map[string]float64{"a": 0.9}, size: map[string]int64{"a": mb}},
			[]string{"a"}, 100 * mb, 100 * mb, []string{"a"}, false},
		{"larger gain goes first", fake{base: 100, factor: map[string]float64{"a": 0.5, "b": 0.1}, size: map[string]int64{"a": mb, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"b", "a"}, false},
		{"tie keeps the first candidate first", fake{base: 100, factor: map[string]float64{"a": 0.5, "b": 0.5}, size: map[string]int64{"a": mb, "b": mb}},
			[]string{"b", "a"}, 100 * mb, 100 * mb, []string{"b", "a"}, false},
		{"second index must still gain 10%", fake{base: 100, factor: map[string]float64{"a": 0.5, "b": 0.95}, size: map[string]int64{"a": mb, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"a"}, false},
		{"two indexes, selection order kept", fake{base: 1000, factor: map[string]float64{"a": 0.5, "b": 0.2}, size: map[string]int64{"a": mb, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"b", "a"}, false},
		{"a gain that does not pay for the space is refused", fake{base: 100, factor: map[string]float64{"a": 0.85}, size: map[string]int64{"a": 1000 * mb}},
			[]string{"a"}, mb, 2000 * mb, nil, false},
		{"size budget", fake{base: 100, factor: map[string]float64{"a": 0.1, "b": 0.5}, size: map[string]int64{"a": 10 * mb, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 5 * mb, []string{"b"}, false},
		{"unusable candidate skipped", fake{base: 100, factor: map[string]float64{"a": 0.1, "b": 0.5}, size: map[string]int64{"a": -1, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"b"}, false},
		{"failed configuration is no improvement", fake{base: 100, factor: map[string]float64{"a": math.Inf(1), "b": 0.5}, size: map[string]int64{"a": mb, "b": mb}},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"b"}, false},
		{"deadline keeps what was chosen", fake{base: 1000, factor: map[string]float64{"a": 0.5, "b": 0.5}, size: map[string]int64{"a": mb, "b": mb}, budgetN: 4},
			[]string{"a", "b"}, 100 * mb, 100 * mb, []string{"a"}, true},
		{"zero cost stops", fake{base: 0, factor: map[string]float64{"a": 0.5}, size: map[string]int64{"a": mb}},
			[]string{"a"}, 100 * mb, 100 * mb, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var cands []candidate
			for _, k := range tc.cands {
				cands = append(cands, cand(k))
			}
			s := search{cost: tc.f.cost, size: tc.f.sizeOf, baseSize: tc.baseSz, maxBytes: tc.max}
			picks, partial, _, err := s.run(cands)
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(picks); !slices.Equal(got, tc.want) {
				t.Errorf("picks %v, want %v", got, tc.want)
			}
			if partial != tc.partial {
				t.Errorf("partial %v, want %v", partial, tc.partial)
			}
		})
	}
}

func TestSearchError(t *testing.T) {
	boom := errors.New("boom")
	s := search{
		cost: func([]candidate) (float64, error) { return 0, boom },
		size: func(candidate) (int64, error) { return 0, nil },
	}
	if _, _, _, err := s.run([]candidate{cand("a")}); !errors.Is(err, boom) {
		t.Errorf("got %v, want boom", err)
	}
}

// wide builds a candidate widening base with extra columns that one query uses together.
func wide(key string, cols []*column, together bool) candidate {
	return candidate{key: key, Table: wideTable, Cols: cols, together: together}
}

var (
	wideTable = &table{}
	colA      = &column{Name: "a"}
	colB      = &column{Name: "b"}
	colC      = &column{Name: "c"}
)

func TestSearchWidens(t *testing.T) {
	const mb = 1 << 20
	a := wide("a", []*column{colA}, true)
	ab := wide("a,b", []*column{colA, colB}, true)
	abc := wide("a,b,c", []*column{colA, colB, colC}, true)
	tests := []struct {
		name  string
		f     fake
		cands []candidate
		max   int64
		want  []string
	}{
		{"within tolerance, the wider index replaces the narrower", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.101}, size: map[string]int64{"a": mb, "a,b": 2 * mb}},
			[]candidate{a, ab}, 100 * mb, []string{"a,b"}},
		{"cheaper wide index wins even when larger", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.05}, size: map[string]int64{"a": mb, "a,b": 40 * mb}},
			[]candidate{a, ab}, 100 * mb, []string{"a,b"}},
		{"beyond tolerance, the narrower index stays", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.13}, size: map[string]int64{"a": mb, "a,b": 2 * mb}},
			[]candidate{a, ab}, 100 * mb, []string{"a"}},
		{"columns no query uses together do not widen", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.1}, size: map[string]int64{"a": mb, "a,b": 2 * mb}},
			[]candidate{a, wide("a,b", []*column{colA, colB}, false)}, 100 * mb, []string{"a"}},
		{"size budget stops the widening", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.1}, size: map[string]int64{"a": mb, "a,b": 10 * mb}},
			[]candidate{a, ab}, 5 * mb, []string{"a"}},
		{"widening repeats", fake{base: 100, factor: map[string]float64{"a": 0.1, "a,b": 0.1, "a,b,c": 0.1}, size: map[string]int64{"a": mb, "a,b": 2 * mb, "a,b,c": 3 * mb}},
			[]candidate{a, ab, abc}, 100 * mb, []string{"a,b,c"}},
		{"leading columns must match", fake{base: 100, factor: map[string]float64{"a": 0.1, "b,c": 0.1}, size: map[string]int64{"a": mb, "b,c": 2 * mb}},
			[]candidate{a, wide("b,c", []*column{colB, colC}, true)}, 100 * mb, []string{"a"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Overlapping indexes: the best of the set serves the workload, they do not multiply.
			best := func(set []candidate) (float64, error) {
				c := tc.f.base
				for _, x := range set {
					c = min(c, tc.f.base*tc.f.factor[x.key])
				}
				return c, nil
			}
			s := search{cost: best, size: tc.f.sizeOf, baseSize: 100 * mb, maxBytes: tc.max}
			picks, _, _, err := s.run(tc.cands)
			if err != nil {
				t.Fatal(err)
			}
			if got := keys(picks); !slices.Equal(got, tc.want) {
				t.Errorf("picks %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSearchMinShare(t *testing.T) {
	const mb = 1 << 20
	// b brings 10% of the cost left (500 -> 450) but 0.5% of the base cost (10000).
	f := fake{base: 10000, factor: map[string]float64{"a": 0.05, "b": 0.9}, size: map[string]int64{"a": mb, "b": mb}}
	s := search{cost: f.cost, size: f.sizeOf, baseSize: 100 * mb, maxBytes: 100 * mb}
	picks, _, small, err := s.run([]candidate{cand("a"), cand("b")})
	if err != nil {
		t.Fatal(err)
	}
	if got := keys(picks); !slices.Equal(got, []string{"a"}) || !small {
		t.Errorf("picks %v, small %v; want [a], true", got, small)
	}
	// 0.9 of 10000 is 10% of the base cost: b stays.
	f = fake{base: 10000, factor: map[string]float64{"a": 0.5, "b": 0.9}, size: map[string]int64{"a": mb, "b": mb}}
	s = search{cost: f.cost, size: f.sizeOf, baseSize: 100 * mb, maxBytes: 100 * mb}
	if picks, _, small, _ = s.run([]candidate{cand("a"), cand("b")}); len(picks) != 2 || small {
		t.Errorf("picks %v, small %v; want 2 picks, false", keys(picks), small)
	}
}
