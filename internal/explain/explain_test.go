package explain

import (
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

func f(v float64) *float64 { return &v }

func TestWeighCapAndShares(t *testing.T) {
	// Seven children of equal cost under a root that adds nothing: only five are listed.
	root := &hypo.Node{NodeType: "Append", TotalCost: 70}
	for i := 0; i < 7; i++ {
		root.Plans = append(root.Plans, &hypo.Node{NodeType: "Seq Scan", TotalCost: 10, PlanRows: 1})
	}
	got := weigh(root, false)
	if len(got) != 5 {
		t.Fatalf("len = %d, want 5", len(got))
	}
	if got[0].Share < 0.14 || got[0].Share > 0.15 {
		t.Fatalf("share = %v, want 1/7", got[0].Share)
	}
}

func TestWeighFallbackSingle(t *testing.T) {
	root := &hypo.Node{NodeType: "Append", TotalCost: 1000}
	for i := 0; i < 20; i++ {
		root.Plans = append(root.Plans, &hypo.Node{NodeType: "Seq Scan", TotalCost: 50})
	}
	got := weigh(root, false)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (no node reaches 10%%)", len(got))
	}
}

func TestWeighLimitAndParallel(t *testing.T) {
	// Limit (cost 5) above Gather (cost 100) above a parallel scan (cost 90): self costs 0 (floored), 10, 90.
	scan := &hypo.Node{NodeType: "Seq Scan", ParallelAware: true, TotalCost: 90, PlanRows: 100}
	g := &hypo.Node{NodeType: "Gather", WorkersPlanned: 2, TotalCost: 100, PlanRows: 300, Plans: []*hypo.Node{scan}}
	root := &hypo.Node{NodeType: "Limit", TotalCost: 5, Plans: []*hypo.Node{g}}
	got := weigh(root, false)
	if got[0].Node != scan || got[0].Share != 0.9 || got[0].Rows != 300 {
		t.Fatalf("got %+v", got[0])
	}
}

func TestWeighAnalyze(t *testing.T) {
	child := &hypo.Node{NodeType: "Index Scan", ActualTotalMs: f(2), Loops: f(10), ActualRows: f(3)}
	root := &hypo.Node{NodeType: "Nested Loop", ActualTotalMs: f(30), Loops: f(1), Plans: []*hypo.Node{child}}
	got := weigh(root, true)
	// self(root) = 30-20 = 10, self(child) = 20.
	if got[0].Node != child || got[0].Actual == nil || *got[0].Actual != 30 {
		t.Fatalf("got %+v", got[0])
	}
	if s := got[0].Share; s < 0.66 || s > 0.67 {
		t.Fatalf("share = %v", s)
	}
}

func TestWarnings(t *testing.T) {
	seq := &hypo.Node{NodeType: "Seq Scan", Schema: "public", Relation: "events", Filter: "(a = 1)"}
	idx := &hypo.Node{NodeType: "Seq Scan", Schema: "public", Relation: "small", Filter: "(a = 1)"}
	seqActual := &hypo.Node{NodeType: "Seq Scan", Schema: "public", Relation: "events", Filter: "(a = 1)", PlanRows: 17, ActualRows: f(90000)}
	sort := &hypo.Node{NodeType: "Sort", SortSpaceType: "Disk", SortSpaceUsedKB: f(2048)}
	tests := []struct {
		name    string
		s       shown
		analyze bool
		want    string // substring of the only warning, "" for none
	}{
		{"seq few rows", shown{Node: seq, Rows: 17}, false, "sequential scan of ~200k rows returns ~17; an index on the filtered columns may help"},
		{"seq many rows", shown{Node: seq, Rows: 50000}, false, ""},
		{"seq small table", shown{Node: idx, Rows: 1}, false, ""},
		{"seq analyze uses actual", shown{Node: seqActual, Rows: 17, Actual: f(90000)}, true, "estimate off"},
		{"bad estimate", shown{Node: &hypo.Node{NodeType: "Hash Join", PlanRows: 10, ActualRows: f(5000)}, Rows: 10, Actual: f(5000)}, true,
			"row estimate off by 500x (estimated 10, actual 5.0k); statistics may be stale"},
		{"bad estimate relation", shown{Node: &hypo.Node{NodeType: "Index Scan", Schema: "public", Relation: "t", PlanRows: 1000, ActualRows: f(10)}, Rows: 1000, Actual: f(10)}, true,
			"off by 100x (estimated 1.0k, actual 10); statistics may be stale (ANALYZE public.t)"},
		{"exact estimate over loops", shown{Node: &hypo.Node{NodeType: "Index Scan", PlanRows: 1, ActualRows: f(1), Loops: f(5000)}, Rows: 1, Actual: f(5000)}, true, ""},
		{"bad estimate per loop", shown{Node: &hypo.Node{NodeType: "Index Scan", PlanRows: 1, ActualRows: f(1000), Loops: f(5000)}, Rows: 1, Actual: f(5e6)}, true,
			"row estimate off by 1000x (estimated 1, actual 1.0k per loop); statistics may be stale"},
		{"estimate only", shown{Node: &hypo.Node{NodeType: "Hash Join"}, Rows: 10, Actual: f(5000)}, false, ""},
		{"small numbers", shown{Node: &hypo.Node{NodeType: "Hash Join", PlanRows: 1, ActualRows: f(50)}, Rows: 1, Actual: f(50)}, true, ""},
		{"sort disk", shown{Node: sort, Rows: 5, Actual: f(5)}, true, "sort spilled to disk (2.0 MB); raise work_mem or add an index that provides the order"},
		{"sort disk without analyze", shown{Node: sort, Rows: 5}, false, ""},
	}
	tuples := map[string]float64{"public.events": 200000, "public.small": 500}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			nodes := []shown{tt.s}
			addWarnings(nodes, tt.analyze, tuples)
			w := nodes[0].Warnings
			if tt.want == "" {
				if len(w) != 0 {
					t.Fatalf("warnings = %v", w)
				}
				return
			}
			if len(w) != 1 || !strings.Contains(w[0], tt.want) {
				t.Fatalf("warnings = %v, want %q", w, tt.want)
			}
		})
	}
}

func TestRenderSummary(t *testing.T) {
	scan := &hypo.Node{NodeType: "Seq Scan", ParallelAware: true, Schema: "public", Relation: "events",
		Filter: "(events.account_id = 42)", RowsRemovedByFilter: f(12000)}
	join := &hypo.Node{NodeType: "Hash Join", JoinType: "Inner", HashCond: "(a.id = b.id)", MergeCond: "m", JoinFilter: "jf"}
	back := &hypo.Node{NodeType: "Index Scan", ScanDirection: "Backward", Index: "<1>btree_events_a", Schema: "public", Relation: "events",
		IndexCond: strings.Repeat("x", 150)}
	s := summary{TotalCost: 5512.344, Rows: 20, Nodes: []shown{
		{Node: scan, Share: 0.961, Rows: 17, Warnings: []string{"sequential scan of ~200k rows returns ~17; an index on the filtered columns may help"}},
		{Node: join, Share: 0.2, Rows: 1200},
		{Node: back, Share: 0.1, Rows: 20},
	}}
	got := renderSummary(s, 10000)
	want := `Total cost 5512.34, estimated rows 20
-> Parallel Seq Scan on public.events: 96.1% of cost, rows 17, removed by filter 12k, filter: (events.account_id = 42)
   WARNING: sequential scan of ~200k rows returns ~17; an index on the filtered columns may help
-> Hash Join (Inner): 20.0% of cost, rows 1.2k, hash cond: (a.id = b.id), merge cond: m, join filter: jf
-> Index Scan Backward using <1>btree_events_a on public.events: 10.0% of cost, rows 20, index cond: ` + strings.Repeat("x", 120) + "…[+30 chars]"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}

	s = summary{TotalCost: 82.1, Rows: 20, Analyze: true, Generic: true, ExecutionMs: f(38.2), ActualRows: f(20),
		Hypo:  &hypoSummary{Before: 5512.34, After: 82.1, Used: []string{"<1>a", "<2>b"}},
		Nodes: []shown{{Node: back, Share: 1, Rows: 20, Actual: f(20)}}}
	got = renderSummary(s, 10000)
	for _, sub := range []string{
		"Total cost 82.10, estimated rows 20, actual rows 20, execution 38 ms, generic plan ($n parameters)\n",
		"With hypothetical indexes: total cost 5512.34 -> 82.10 (-98.5%)\n",
		"Hypothetical indexes used: <1>a, <2>b\n",
		": 100.0% of time, rows 20 (actual 20), index cond:",
	} {
		if !strings.Contains(got, sub) {
			t.Fatalf("missing %q in:\n%s", sub, got)
		}
	}
	s.Hypo.Used = nil
	if got := renderSummary(s, 10000); !strings.Contains(got, "Hypothetical indexes used: none (the planner did not choose them)") {
		t.Fatalf("got:\n%s", got)
	}
}

func TestFit(t *testing.T) {
	lines := []string{"aaaaaaaaaa", "bbbbbbbbbb", "cccccccccc", "dddddddddd"}
	if got := fit(lines, 140); got != strings.Join(lines, "\n") {
		t.Fatalf("got %q", got)
	}
	marker := "-- truncated (80-byte limit)" // the limit shown is the configured one, not the budget
	if got := fit(lines, 80); got != "aaaaaaaaaa\n"+marker || len(got) > 80-footerReserve {
		t.Fatalf("got %q", got)
	}
	if got := fit([]string{strings.Repeat("z", 500)}, 80); got != marker {
		t.Fatalf("got %q", got)
	}
}

func TestValidate(t *testing.T) {
	cfg := func(mode string) *config.Config { return &config.Config{AccessMode: mode} }
	policy, err := safesql.NewPolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	withHypopg := db.Info{VersionNum: 160004, Extensions: map[string]string{"hypopg": "1.4"}}
	noHypopg := db.Info{VersionNum: 160004}
	idx := []struct {
		Table   string   `json:"table"`
		Columns []string `json:"columns"`
		Using   string   `json:"using"`
	}{{Table: "events", Columns: []string{"a"}}}
	badIdx := []struct {
		Table   string   `json:"table"`
		Columns []string `json:"columns"`
		Using   string   `json:"using"`
	}{{Table: "events", Columns: []string{"lower(a)"}}}

	tests := []struct {
		name string
		mode string
		info db.Info
		in   args
		want string // error text, "" for success
	}{
		{"ok", config.Restricted, noHypopg, args{SQL: "SELECT 1"}, ""},
		{"analyze with hypo first", config.Restricted, withHypopg, args{SQL: "SELEC", Analyze: true, Hypo: idx}, "analyze cannot be combined with hypothetical indexes"},
		{"invalid index second", config.Restricted, noHypopg, args{SQL: "SELEC", Hypo: badIdx},
			`invalid hypothetical index: column "lower(a)" is not a valid identifier`},
		{"hypopg missing", config.Restricted, noHypopg, args{SQL: "SELEC", Hypo: idx},
			"hypothetical indexes need the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-16-hypopg package)"},
		{"syntax", config.Restricted, withHypopg, args{SQL: "SELEC 1"}, "syntax error:"},
		{"two statements", config.Unrestricted, withHypopg, args{SQL: "SELECT 1; SELECT 2"}, "explain_query takes exactly one statement"},
		{"nested explain", config.Unrestricted, withHypopg, args{SQL: "EXPLAIN SELECT 1"},
			"pass the query without EXPLAIN; use the analyze and verbose parameters instead"},
		{"analyze write unrestricted", config.Unrestricted, withHypopg, args{SQL: "UPDATE orders SET status = 'x'", Analyze: true},
			"analyze is only allowed on SELECT, VALUES, TABLE or WITH … SELECT queries"},
		{"write unrestricted ok", config.Unrestricted, withHypopg, args{SQL: "DELETE FROM orders"}, ""},
		{"write restricted", config.Restricted, withHypopg, args{SQL: "DELETE FROM orders"}, "restricted mode:"},
		{"show restricted", config.Restricted, withHypopg, args{SQL: "SHOW work_mem"},
			"restricted mode: only SELECT, VALUES, TABLE or WITH … SELECT queries can be explained"},
		{"denied function", config.Restricted, withHypopg, args{SQL: "SELECT * FROM pg_read_file('/etc/passwd')"},
			"restricted mode: function pg_read_file() is not allowed"},
		{"analyze params", config.Restricted, withHypopg, args{SQL: "SELECT $1", Analyze: true},
			"analyze is not possible with $n parameters; replace them with values"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validate(cfg(tt.mode), policy, func() (db.Info, error) { return tt.info, nil }, tt.in)
			switch {
			case tt.want == "" && err != nil:
				t.Fatalf("err = %v", err)
			case tt.want != "" && (err == nil || !strings.HasPrefix(err.Error(), tt.want)):
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}

// A bad argument is refused without asking the server for its version and extensions.
func TestValidateSkipsInfoWithoutHypotheticalIndexes(t *testing.T) {
	policy, err := safesql.NewPolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	info := func() (db.Info, error) {
		t.Error("db.Info was fetched")
		return db.Info{}, nil
	}
	for _, in := range []args{{SQL: "SELEC 1"}, {SQL: "SELECT 1; SELECT 2"}, {SQL: "SELECT 1", Analyze: true, Hypo: []struct {
		Table   string   `json:"table"`
		Columns []string `json:"columns"`
		Using   string   `json:"using"`
	}{{Table: "t", Columns: []string{"a"}}}}} {
		if _, err := validate(&config.Config{AccessMode: config.Unrestricted}, policy, info, in); err == nil {
			t.Errorf("%+v accepted", in)
		}
	}
}
