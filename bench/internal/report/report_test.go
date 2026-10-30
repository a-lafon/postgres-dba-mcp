package report

import (
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func cellOK(tokens int) results.N2Cell {
	return results.N2Cell{Status: results.StatusOK, Tokens: tokens, Bytes: 1000, P50MS: 4, P95MS: 6, Correct: true, Truncation: results.TruncNone}
}

// fixtureInputs returns a run where every target is met, with API token counts and an N3 run.
func fixtureInputs() Inputs {
	env := results.Env{RunID: "20261130T100000Z", Scale: 1, TokenCounter: results.CounterAPI, Model: "claude-sonnet-5-5",
		DBAVersion: "0.1.0", DBACommit: "abc1234", CrystalDigest: "sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b", PGVersion: "17.6"}
	row := func(id string, d, c results.N2Cell) results.N2Row {
		return results.N2Row{ID: id, Tool: "execute_sql", Cells: map[string]results.N2Cell{results.DBA: d, results.Crystal: c}}
	}
	s17d, s17c := cellOK(300), cellOK(500)
	s17d.Detected = []string{"T1", "T2", "T3", "T4", "T5", "T6"}
	s17c.Detected = []string{"T1", "T2", "T4", "T5", "T6"}
	s18d, s18c := cellOK(100), cellOK(120)
	s18d.Detected, s18c.Detected = []string{"T1", "T2"}, []string{"T1", "T2"}
	s19d, s19c := cellOK(100), cellOK(900)
	s19d.Advice = &results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: -0.998}
	s19c.Advice = &results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: -0.995}
	s20d := cellOK(30)
	s20d.Advice = &results.Advice{None: true}
	s02 := cellOK(900)
	s02.TruncExpected, s02.Truncation = true, results.TruncSignalled
	n2 := &results.N2{Env: env, MaxBytes: 32768, SameRole: true, Rows: []results.N2Row{
		row("S01", cellOK(400), cellOK(1000)), row("S02", s02, cellOK(60000)),
		row("S17", s17d, s17c), row("S18", s18d, s18c), row("S19", s19d, s19c),
		{ID: "S20", Tool: "analyze_query_indexes", Cells: map[string]results.N2Cell{results.DBA: s20d, results.Crystal: cellOK(80)}},
		row("S21", cellOK(50), cellOK(50)),
	}}
	run := func(server string, pass bool, tokens, turns int) results.N3Run {
		return results.N3Run{Task: "T01", Server: server, Pass: pass, InputTokens: tokens, OutputTokens: 0, Turns: turns, Model: "claude-opus-5-5"}
	}
	return Inputs{
		RunID: env.RunID, N2: n2,
		Sec: &results.Security{Env: env, Attacks: map[string][]results.SecurityCase{
			results.DBA:     {{ID: "write-insert", Class: results.ClassRefused}},
			results.Crystal: {{ID: "fn-lo-from", Class: results.ClassExecuted, SideEffect: "large objects +1"}},
		}, Legit: []results.SecurityCase{{ID: "simple-select", Class: results.ClassExecuted}}},
		N3: &results.N3{Env: env, Runs: []results.N3Run{run(results.DBA, true, 590, 3), run(results.Crystal, true, 1000, 3)}},
	}
}

func TestVerdicts(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Inputs)
		want   string
		line   string
	}{
		{"all met", func(*Inputs) {}, Go, ""},
		{"tokens at parity are met", func(in *Inputs) { in.N3.Runs[0].InputTokens = 1000 }, Go, ""},
		{"tokens above crystaldba are not met", func(in *Inputs) { in.N3.Runs[0].InputTokens = 1031 }, Inconclusive, "tokens did not decrease"},
		{"security side effect dominates", func(in *Inputs) {
			in.Sec.Attacks[results.DBA][0].SideEffect = "large objects +1"
		}, NoGo, ""},
		{"attack executed", func(in *Inputs) { in.Sec.Attacks[results.DBA][0].Class = results.ClassExecuted }, NoGo, ""},
		{"false refusal on legit", func(in *Inputs) { in.Sec.Legit[0].Class = results.ClassRefused }, NoGo, ""},
		{"estimated tokens block GO", func(in *Inputs) { in.N2.Env.TokenCounter = results.CounterEstimate }, Inconclusive, "median tokens S01-S06"},
		{"claude -p tokens are verified", func(in *Inputs) { in.N2.Env.TokenCounter = results.CounterClaude }, Go, ""},
		{"estimated cell blocks GO", func(in *Inputs) {
			in.N2.Env.TokenCounter = results.CounterClaude
			c := in.N2.Rows[0].Cells[results.DBA]
			c.Estimated = true
			in.N2.Rows[0].Cells[results.DBA] = c
		}, Inconclusive, "median tokens S01-S06"},
		{"N3 not run", func(in *Inputs) { in.N3 = nil }, Inconclusive, "N3 success rate"},
		{"success below crystaldba, tokens down", func(in *Inputs) {
			in.N3.Runs[0].Pass = false
		}, Iterate, IterateCause},
		{"turns too high, tokens down", func(in *Inputs) { in.N3.Runs[0].Turns = 9 }, Iterate, IterateCause},
		{"detection below crystaldba", func(in *Inputs) {
			in.N2.Rows[2].Cells[results.DBA] = cellWithTraps(cellOK(300), "T1", "T2")
		}, Iterate, IterateCause},
		{"tokens not down", func(in *Inputs) {
			in.N3.Runs[0].InputTokens = 2000
			in.N3.Runs[0].Pass = false
		}, Inconclusive, "tokens did not decrease"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := fixtureInputs()
			tt.mutate(&in)
			md, v := Render(in)
			if v.Name != tt.want {
				t.Fatalf("verdict = %s, want %s\n%s", v.Name, tt.want, md)
			}
			if !strings.Contains(md, "## Verdict: "+tt.want) || !strings.Contains(md, tt.line) {
				t.Errorf("report lacks verdict line or %q:\n%s", tt.line, md)
			}
		})
	}
}

func cellWithTraps(c results.N2Cell, ids ...string) results.N2Cell {
	c.Detected = ids
	return c
}

func TestRenderSections(t *testing.T) {
	md, _ := Render(fixtureInputs())
	prev := -1
	for _, h := range []string{"# postgres-dba-mcp vs crystaldba", "## Verdict:", "## N2 — ", "## Security — ", "## N3 — ", "## N3 — answer quality (judge)", "## Operations", "## N1 — ", "## False refusals and skipped items"} {
		i := strings.Index(md, h)
		if i < 0 || i < prev {
			t.Fatalf("section %q missing or out of order in:\n%s", h, md)
		}
		prev = i
	}
	for _, want := range []string{
		"Run 20261130T100000Z · postgres-dba-mcp 0.1.0 (abc1234) · crystaldba sha256:dbbd3468… · PostgreSQL 17.6 · scale 1 · tokens: count_tokens (claude-sonnet-5-5)",
		"| S01 | execute_sql | 400 | 1000 | -60.0% | 1000 | 4 / 6 | yes / yes | none |",
		"| N3 median total tokens | <= 0% | -41.0% | yes |",
		"Model: claude-opus-5-5\n",
		"| N2 trap detection (S17, S18) | >= crystaldba | S17 6/6 vs 5/6, S18 2/2 vs 2/2 | yes |",
		"fn-lo-from",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q:\n%s", want, md)
		}
	}
}

func TestOpsMemory(t *testing.T) {
	tests := []struct {
		name   string
		native results.Measure
		image  float64
		want   []string
		absent []string
	}{
		{"target reads the image", results.Measure{Value: 41.2, Note: "phys_footprint"}, 24.5,
			[]string{"| Memory after 100 S02 calls | 24.5 MB (linux image, docker stats) | 90.0 MB | < 30 MB | yes |",
				"informational, not checked against the target): 41.2 MB (phys_footprint)."}, nil},
		{"native above target does not fail it", results.Measure{Value: 41.2, Note: "phys_footprint"}, 31,
			[]string{"| < 30 MB | no |"}, nil},
		{"native skipped", results.Measure{Skipped: true, Note: "needs Pagila"}, 0,
			[]string{": skipped."}, nil},
		{"ops.json without native", results.Measure{}, 24.5, nil, []string{"Native binary"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := Inputs{Ops: &results.Ops{
				ColdStartMS: map[string]results.Measure{}, SizeMB: map[string]results.Measure{},
				RSSMB: map[string]results.Measure{
					results.DBA:     {Value: tt.image, Note: "linux image, docker stats"},
					results.Crystal: {Value: 90},
				},
				NativeRSSMB: tt.native,
			}}
			if tt.native.Skipped {
				in.Ops.RSSMB[results.DBA] = results.Measure{Skipped: true}
			}
			md, _ := Render(in)
			for _, w := range tt.want {
				if !strings.Contains(md, w) {
					t.Errorf("report lacks %q:\n%s", w, md)
				}
			}
			for _, a := range tt.absent {
				if strings.Contains(md, a) {
					t.Errorf("report has %q:\n%s", a, md)
				}
			}
		})
	}
}

func TestOpsMemorySamples(t *testing.T) {
	tests := []struct {
		name string
		dba  results.Measure
		want string
	}{
		{"median and max", results.Measure{Value: 15.8, Note: "linux image, docker stats", Samples: []float64{14.2, 30.3, 15.8}},
			"| Memory after 100 S02 calls | 15.8 MB (linux image, docker stats; median of 3, max 30.3 MB) | 90.0 MB | < 30 MB | yes |"},
		{"target reads the median, not the max", results.Measure{Value: 31.5, Note: "linux image, docker stats", Samples: []float64{15, 31.5, 40}},
			"max 40.0 MB) | 90.0 MB | < 30 MB | no |"},
		{"samples without note", results.Measure{Value: 20, Samples: []float64{19, 20, 22}},
			"| 20.0 MB (median of 3, max 22.0 MB) |"},
		{"ops.json without samples", results.Measure{Value: 24.5, Note: "linux image, docker stats"},
			"| 24.5 MB (linux image, docker stats) | 90.0 MB |"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, _ := Render(Inputs{Ops: &results.Ops{
				ColdStartMS: map[string]results.Measure{}, SizeMB: map[string]results.Measure{},
				RSSMB: map[string]results.Measure{results.DBA: tt.dba, results.Crystal: {Value: 90}},
			}})
			if !strings.Contains(md, tt.want) {
				t.Errorf("report lacks %q:\n%s", tt.want, md)
			}
		})
	}
}

func TestMissingLevels(t *testing.T) {
	in := Inputs{RunID: "r1"}
	md, v := Render(in)
	if v.Name != Inconclusive {
		t.Errorf("verdict = %s, want INCONCLUSIVE", v.Name)
	}
	if got := strings.Count(md, "not run"); got < 5 {
		t.Errorf("%d `not run` marks, want one per level:\n%s", got, md)
	}
	if strings.Contains(md, "| yes |") || strings.Contains(md, "| no |") {
		t.Errorf("a missing level produced a yes/no:\n%s", md)
	}
}

func TestTokenRatioTarget(t *testing.T) {
	in := fixtureInputs()
	in.N2.Rows = []results.N2Row{
		{ID: "S01", Cells: map[string]results.N2Cell{results.DBA: cellOK(1200), results.Crystal: cellOK(3000)}},
	}
	r, ok := TokenRatio(in.N2)
	if !ok || pct(r) != "40.0%" {
		t.Fatalf("ratio = %v %v, want 40.0%%", r, ok)
	}
	var got Criterion
	for _, c := range n2Criteria(in) {
		if c.Key == KeyN2Tokens {
			got = c
		}
	}
	if got.Result != "40.0%" || got.Met != Yes {
		t.Errorf("criterion = %+v", got)
	}
}

func TestAdviceMatches(t *testing.T) {
	ix := func(s ...string) *results.Advice { return &results.Advice{Indexes: s} }
	tests := []struct {
		name string
		a, b *results.Advice
		want bool
	}{
		{"same index", ix("events(a,b)"), ix("events(a,b)"), true},
		{"different index, gain within 10%", &results.Advice{Indexes: []string{"events(a)"}, Change: -0.95}, &results.Advice{Indexes: []string{"events(b)"}, Change: -0.99}, true},
		{"different index, gain apart", &results.Advice{Indexes: []string{"events(a)"}, Change: -0.5}, &results.Advice{Indexes: []string{"events(b)"}, Change: -0.99}, false},
		{"nothing recommended", ix(), ix("events(a)"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := AdviceMatches(tt.a, tt.b); got != tt.want {
				t.Errorf("AdviceMatches = %v, want %v", got, tt.want)
			}
		})
	}
}
