package indexadv

import (
	"strings"
	"testing"
	"time"
)

func idx(cols ...string) candidate {
	t := &table{Schema: "public", Name: "events", Quoted: "public.events"}
	c := candidate{Table: t}
	for _, n := range cols {
		c.Cols = append(c.Cols, &column{Name: n, Quoted: n})
	}
	return c
}

func queries(labels ...string) []*query {
	var qs []*query
	for _, l := range labels {
		qs = append(qs, &query{Label: l})
	}
	return qs
}

func TestRender(t *testing.T) {
	tests := []struct {
		name string
		res  result
		want string
	}{
		{"recommendation", result{
			Kind: recommended, Queries: queries("Q1"), BaseQ: []float64{4812}, FinalQ: []float64{8.4},
			Base: 4812, Final: 8.4, Elapsed: 1200 * time.Millisecond,
			Picks: []pick{{idx("account_id", "created_at"), 4613735, 8.4}},
		}, `Workload cost 4812.0 → 8.4 (-99.8%), 1 index, +4.4 MB, 1.2 s
#,cost_change,size,ddl
1,-99.8%,4.4 MB,"CREATE INDEX ON public.events (account_id, created_at);"
Per query: Q1 -99.8%
`},
		{"two indexes, three queries", result{
			Kind: recommended, Queries: queries("Q1", "Q2", "Q3"),
			BaseQ: []float64{40000, 12310, 0.5}, FinalQ: []float64{1000, 203.5, 0.5},
			Base: 52310, Final: 1204, Elapsed: 2 * time.Second,
			Picks: []pick{{idx("account_id"), 1048576, 4598}, {idx("kind", "created_at"), 2097152, 1204}},
		}, `Workload cost 52310.0 → 1204.0 (-97.7%), 2 indexes, +3.0 MB, 2.0 s
#,cost_change,size,ddl
1,-91.2%,1.0 MB,CREATE INDEX ON public.events (account_id);
2,-6.5%,2.0 MB,"CREATE INDEX ON public.events (kind, created_at);"
Per query: Q1 -97.5%, Q2 -98.3%, Q3 0.0% (unchanged)
`},
		{"large workload cost and dropped small indexes", result{
			Kind: recommended, Queries: queries("Q1"), BaseQ: []float64{38146387435.1}, FinalQ: []float64{604447401.6},
			Base: 38146387435.1, Final: 604447401.6, Elapsed: 4 * time.Second, BelowShare: true,
			Picks: []pick{{idx("account_id"), 1048576, 604447401.6}},
		}, `Workload cost 38B → 604M (-98.4%), 1 index, +1.0 MB, 4.0 s
#,cost_change,size,ddl
1,-98.4%,1.0 MB,CREATE INDEX ON public.events (account_id);
Per query: Q1 -98.4%
Not recommended: other indexes would each lower the workload cost by less than 1%.
`},
		{"no recommendation", result{
			Kind: noGain, Queries: queries("Q1"), Base: 8.4, Candidates: 4, Elapsed: 200 * time.Millisecond,
		}, `No index recommendation: no candidate lowers the workload cost by at least 10%.
Workload cost 8.4 (1 query, 4 candidates evaluated), 200 ms
`},
		{"no candidate column", result{Kind: noColumns}, `No index recommendation: the queries have no column in WHERE, JOIN, HAVING or ORDER BY that an index could serve.
`},
		{"skipped and budget", result{
			Kind: noQueries, Partial: true, Skipped: []skip{{"Q2", "not a single SELECT"}, {"Q3", "boom"}},
		}, `No index recommendation: no query could be analyzed.
Skipped: Q2 (not a single SELECT); Q3 (boom)
Stopped at the 30 s time budget: recommendations may be incomplete.
`},
		{"workload query lines", result{
			Kind: noGain, Workload: true, Base: 1, Candidates: 1, Elapsed: time.Second,
			Queries: []*query{{Label: "Q1", Calls: 60, MeanMs: 12.5, SQL: "SELECT *\n  FROM events\n WHERE a = $1"}},
		}, `No index recommendation: no candidate lowers the workload cost by at least 10%.
Workload cost 1.0 (1 query, 1 candidate evaluated), 1.0 s
Q1 (calls 60, mean 12 ms): SELECT * FROM events WHERE a = $1
`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.res, 10000); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestRenderMaxBytes(t *testing.T) {
	res := result{
		Kind: recommended, Workload: true, Base: 100, Final: 10,
		Queries: []*query{{Label: "Q1", SQL: strings.Repeat("x", 100)}}, BaseQ: []float64{100}, FinalQ: []float64{10},
		Picks: []pick{{idx("a"), 1024, 55}, {idx("b"), 1024, 10}},
	}
	for _, max := range []int{10000, 200, 120, 60, 20} {
		if got := render(res, max); len(got) > max {
			t.Errorf("max %d: %d bytes", max, len(got))
		}
	}
	if got := render(res, 200); strings.Contains(got, "Q1 (calls") {
		t.Errorf("query lines should go first when space is short:\n%s", got)
	}
}

// unservedResult is the bench workload: Q4 stays unserved, an index on orders (created_at) would serve it.
func unservedResult(us ...unserved) result {
	return result{
		Kind: recommended, Queries: queries("Q1", "Q4", "Q5"), BaseQ: []float64{3.8e10, 14658, 90000},
		FinalQ: []float64{1e6, 14658, 90000}, Base: 3.8e10, Final: 8e8, Elapsed: 748 * time.Millisecond, BelowShare: true,
		Picks:    []pick{{idx("account_id", "created_at"), 166 << 20, 8e8}},
		Unserved: us,
	}
}

func orders(cols ...string) candidate {
	c := idx(cols...)
	c.Table = &table{Schema: "public", Name: "orders", Quoted: "public.orders"}
	return c
}

func TestRenderUnserved(t *testing.T) {
	head := `Workload cost 38B → 800M (-97.9%), 1 index, +166 MB, 748 ms
#,cost_change,size,ddl
1,-97.9%,166 MB,"CREATE INDEX ON public.events (account_id, created_at);"
Per query: Q1 -100.0%, Q4 0.0% (unchanged), Q5 0.0% (unchanged)
`
	a := unserved{cand: orders("created_at"), size: 33 << 20, qs: []int{1}, qcost: []float64{8}, change: -0.004}
	b := unserved{cand: orders("status"), size: 5 << 20, qs: []int{1, 2}, qcost: []float64{100, 9000}, change: -0.003}
	tests := []struct {
		name string
		res  result
		want string
	}{
		{"one line replaces the share line", unservedResult(a),
			head + "Not recommended, small workload share: CREATE INDEX ON public.orders (created_at); Q4 -99.9%, workload -0.4%, 33 MB\n"},
		{"several queries on one line", unservedResult(b),
			head + "Not recommended, small workload share: CREATE INDEX ON public.orders (status); Q4 -99.3%, Q5 -90.0%, workload -0.3%, 5.0 MB\n"},
		{"bench example", result{
			Kind: recommended, Queries: queries("Q1", "Q2", "Q3", "Q4", "Q5", "Q6", "Q7"),
			BaseQ:  []float64{1e10, 1e10, 1e10, 14658, 5000, 4e9, 4e9},
			FinalQ: []float64{1, 1, 1, 14658, 5000, 4e6, 4e6},
			Base:   38.146e9, Final: 800e6, Elapsed: 748 * time.Millisecond,
			Picks:    []pick{{idx("account_id", "created_at"), 166 << 20, 1.449e9}, {orders("account_id", "created_at"), 33 << 20, 800e6}},
			Unserved: []unserved{{cand: orders("created_at"), size: 33 << 20, qs: []int{3}, qcost: []float64{8}, change: -0.004}},
		}, `Workload cost 38B → 800M (-97.9%), 2 indexes, +199 MB, 748 ms
#,cost_change,size,ddl
1,-96.2%,166 MB,"CREATE INDEX ON public.events (account_id, created_at);"
2,-1.7%,33 MB,"CREATE INDEX ON public.orders (account_id, created_at);"
Per query: Q1 -100.0%, Q2 -100.0%, Q3 -100.0%, Q4 0.0% (unchanged), Q5 0.0% (unchanged), Q6 -99.9%, Q7 -99.9%
Not recommended, small workload share: CREATE INDEX ON public.orders (created_at); Q4 -99.9%, workload -0.4%, 33 MB
`},
		{"fallback to the share line", unservedResult(),
			head + "Not recommended: other indexes would each lower the workload cost by less than 1%.\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := render(tc.res, 10000); got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s", got, tc.want)
			}
		})
	}
}

func TestRenderUnservedCappedAndDropped(t *testing.T) {
	var us []unserved
	for _, c := range []string{"a", "b", "c"} {
		us = append(us, unserved{cand: orders(c), size: 1 << 20, qs: []int{1}, qcost: []float64{8}})
	}
	res := unservedResult(us...)
	full := render(res, 10000)
	if n := strings.Count(full, "Not recommended, small workload share:"); n != 3 {
		t.Errorf("want 3 lines, got %d:\n%s", n, full)
	}
	// One byte short of the text with the lines: the lines go, the table row stays.
	short := render(res, len(full)-1)
	if strings.Contains(short, "Not recommended") || !strings.Contains(short, "\n1,-97.9%") {
		t.Errorf("hint lines should go before table rows:\n%s", short)
	}
}
