package indexadv

import (
	"slices"
	"testing"
)

func keyed(key string) candidate {
	c := idx(key)
	c.key = key
	return c
}

func TestGroupHits(t *testing.T) {
	tests := []struct {
		name string
		hits []hit
		want []string // candidate keys
		qs   [][]int  // queries of each group
	}{
		{"none", nil, nil, nil},
		{"one candidate serves two queries", []hit{{1, keyed("a"), 10, 5}, {3, keyed("a"), 10, 7}}, []string{"a"}, [][]int{{1, 3}}},
		{"first appearance order", []hit{{0, keyed("b"), 1, 1}, {1, keyed("a"), 1, 1}, {2, keyed("b"), 1, 1}}, []string{"b", "a"}, [][]int{{0, 2}, {1}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := groupHits(tc.hits)
			if len(got) != len(tc.want) {
				t.Fatalf("got %d groups, want %d", len(got), len(tc.want))
			}
			for i, u := range got {
				if u.cand.key != tc.want[i] || !slices.Equal(u.qs, tc.qs[i]) || len(u.qcost) != len(u.qs) {
					t.Errorf("group %d: %+v", i, u)
				}
			}
		})
	}
}

func TestRankUnserved(t *testing.T) {
	mk := func(changes ...float64) []unserved {
		var us []unserved
		for i, c := range changes {
			us = append(us, unserved{cand: keyed(string(rune('a' + i))), change: c})
		}
		return us
	}
	keys := func(us []unserved) string {
		var s string
		for _, u := range us {
			s += u.cand.key
		}
		return s
	}
	tests := []struct {
		name string
		in   []unserved
		n    int
		want string
	}{
		{"empty", nil, 3, ""},
		{"largest gain first", mk(-0.002, -0.009, -0.004), 3, "bca"},
		{"capped", mk(-0.001, -0.004, -0.003, -0.002), 3, "bcd"},
		{"ties keep order", mk(-0.002, -0.002), 3, "ab"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := keys(rankUnserved(tc.in, tc.n)); got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
