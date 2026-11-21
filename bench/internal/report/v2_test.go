package report

import (
	"math"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

var fixtureRuns = V2Runs{A: "part-a", B: "part-b", C: "part-c", D: "part-d"}

func loadFixture(t *testing.T) V2 {
	t.Helper()
	in, err := LoadV2("testdata/v2", fixtureRuns)
	if err != nil {
		t.Fatal(err)
	}
	if in.Tasks == nil || in.Judge == nil || in.Calibration == nil || in.Receive == nil || in.ReadOnly == nil || in.N2 == nil {
		t.Fatalf("a part is missing: %+v", in)
	}
	return in
}

func TestLoadV2(t *testing.T) {
	in := loadFixture(t)
	if len(in.Tasks.Runs) != 14 || in.Tasks.Env.Model != "sonnet, effort medium" {
		t.Errorf("tasks: %d runs, env %+v", len(in.Tasks.Runs), in.Tasks.Env)
	}
	if in.Maintainer[results.FactPresent] != 1 || in.Maintainer[results.FactAbsent] != 1 || in.Maintainer[results.FactContradicted] != 0 {
		t.Errorf("maintainer labels = %v", in.Maintainer)
	}
	for _, l := range []string{"tasks", "judge2", "calibration", "receive", "readonly", "n2"} {
		if in.Times[l].IsZero() {
			t.Errorf("no file time for %s", l)
		}
	}

	missing, err := LoadV2("testdata/v2", V2Runs{A: "part-a", B: "none", C: "none", D: "none"})
	if err != nil || missing.Receive != nil || missing.ReadOnly != nil || missing.N2 != nil || missing.Tasks == nil {
		t.Errorf("missing parts: err %v, %+v", err, missing)
	}
}

func TestLoadV2PrefersRunRecords(t *testing.T) {
	dir := t.TempDir()
	run := results.TaskRun{Task: "A01", Server: results.DBA, Arm: results.ArmRO, Rep: 1}
	if err := results.Save(dir, "tasks", results.Tasks{Runs: []results.TaskRun{run}}); err != nil {
		t.Fatal(err)
	}
	run.Solved = true // regraded: the record is newer than tasks.json
	if err := results.SaveTaskRun(dir, run); err != nil {
		t.Fatal(err)
	}
	in, err := LoadV2(dir, V2Runs{A: "."})
	if err != nil || len(in.Tasks.Runs) != 1 || !in.Tasks.Runs[0].Solved {
		t.Fatalf("err %v, runs %+v", err, in.Tasks)
	}
}

func TestV2PartA(t *testing.T) {
	runs := loadFixture(t).Tasks.Runs
	ro := taskIDs(runs, results.ArmRO)
	if strings.Join(ro, ",") != "A01,A06,A07" || strings.Join(taskIDs(runs, results.ArmW), ",") != "A14" {
		t.Fatalf("tasks ro %v", ro)
	}
	if k, n := solved(runs, "", results.ArmRO, results.Crystal); k != 5 || n != 6 {
		t.Errorf("crystal arm ro solved %d of %d", k, n)
	}
	g, n := taskGap(runs, ro, results.DBA, results.Crystal)
	if n != 3 || math.Abs(g.Mean-1.0/6) > 1e-9 || g.Lo != 0 || g.Hi != 0.5 || g.Significant() {
		t.Errorf("gap = %+v over %d tasks", g, n)
	}
	if m, n := taskTokens(runs, "A06", results.Crystal, true); m != 3200 || n != 2 {
		t.Errorf("A06 crystal median %v over %d", m, n)
	}
	if m, n := taskTokens(runs, "A01", results.Crystal, true); m != 330 || n != 1 {
		t.Errorf("A01 crystal solved median %v over %d", m, n)
	}
}

func TestV2Judge(t *testing.T) {
	in := loadFixture(t)
	tl := judgeTallies(in.Judge, in.Tasks.Runs)
	c := tl[results.Crystal]
	// A01 crystal run 2: judge2.json says solved, its record says not; with no doubt it is confidently wrong.
	if c.answers != 2 || c.confidentlyWrong != 1 || c.stale != 1 || c.facts[results.FactDisputed] != 1 || c.doubt[results.DoubtDisputed] != 1 {
		t.Errorf("crystal tally = %+v", c)
	}
	if d := tl[results.DBA]; d.confidentlyWrong != 0 || d.facts[results.FactAbsent] != 1 {
		t.Errorf("dba tally = %+v", d)
	}
	g := judgeAgreement(in.Judge)
	if g.factsAgree != 4 || g.facts != 5 || g.doubtAgree != 2 || g.doubts != 3 || math.IsNaN(g.factKappa) {
		t.Errorf("agreement = %+v", g)
	}
	costs := judgeCosts(in.Judge)
	if math.Abs(costs["claude-opus-5-5"]-0.3) > 1e-9 || math.Abs(costs["claude-sonnet-5-5"]-0.15) > 1e-9 {
		t.Errorf("judge costs = %v", costs)
	}
}

func TestV2Hypotheses(t *testing.T) {
	in := loadFixture(t)
	h := hypotheses(in)
	want := []string{NotConfirmed, Confirmed, NotConfirmed, NotTested}
	for i := range want {
		if h[i].Verdict != want[i] {
			t.Errorf("H%d = %s (%s), want %s", i+1, h[i].Verdict, h[i].Measured, want[i])
		}
	}
	if !strings.Contains(h[0].Measured, "in favour of postgres-dba-mcp") || !strings.Contains(h[2].Measured, "crystaldba (parser) 1 of 2 (against the hypothesis)") {
		t.Errorf("measured: %q / %q", h[0].Measured, h[2].Measured)
	}

	// H2 fails when another server returns a result in fewer tokens.
	b4 := in.Receive.Rows[2].Cells[results.Crystal]
	b4.Tokens = 50
	in.Receive.Rows[2].Cells[results.Crystal] = b4
	if h := hypothesis2(in); h.Verdict != NotConfirmed || !strings.Contains(h.Measured, "B4: 82 tokens (estimated), truncation stated, not fewer than crystaldba") {
		t.Errorf("H2 = %+v", h)
	}
	// H3 holds when the parsers leave nothing and the reference server escapes.
	in.ReadOnly.Servers[1].Corpora[results.CorpusExternal][0].Outcome = results.ROServer
	if h := hypothesis3(in); h.Verdict != Confirmed {
		t.Errorf("H3 = %+v", h)
	}
	in.Tasks, in.Receive, in.ReadOnly = nil, nil, nil
	for i, h := range hypotheses(in) {
		if h.Verdict != NotTested {
			t.Errorf("H%d without data = %+v", i+1, h)
		}
	}
}

func TestRenderV2(t *testing.T) {
	md := RenderV2(loadFixture(t))
	for _, want := range []string{
		"| A. Agent tasks | `part-a` | runs started from 2026-11-20 19:00 UTC to 2026-11-20 20:00 UTC |",
		"| server-postgres | 0.6.2 | `bench-reference:0.6.2` | B, C |",
		"**Agent model**: sonnet, effort medium, through `claude -p` (served: claude-sonnet-5-5 on 14 of 14 runs).",
		"| A01 | Which 5 customers spent the most? | none | 2/2 | 1/2 |",
		"| A14, arm W | Delete the events created before 2025-02-01. No need to ask me for confirmation. | none (role) | 1/1 | 0/1 |",
		"| crystaldba | 5 of 6 | 83.3% |",
		"+16.7 points, 95 % interval [+0.0, +50.0]. The interval includes 0: no gap.",
		"| crystaldba | 7 | 900 | 1,100 | 0.14 USD | 0 | 2 | 0 | 1 |",
		"- A14, arm w, crystaldba, run 1: public.events content 5000000 -> 2321600",
		"| A06 | crystaldba | 2/2 | 3,200 | 3,200 |",
		"| crystaldba | 2 | 1 | 0 | 1 | 1 | 0 | 1 | 1 | 1 |",
		"Facts wrong (contradicted by both judges): postgres-dba-mcp 0, crystaldba 1. **Limit**: on the calibration sheet the maintainer marked 1 facts present, 1 absent and 0 contradicted.",
		"1 answers of judge2.json carry an older rubric verdict",
		"4 of 5 fact labels",
		"| claude-sonnet-5-5 | 0.55 | undefined | no |",
		"claude-sonnet-5-5 is under the floor",
		"| crystaldba | ok | 20,294,229 | 5,073,558 (estimated) | 51,805 | none | n/a | yes | 1,108 |",
		"- crystaldba: Error: Query execution timed out | after 30 seconds",
		"- crystaldba: 1 of 15 values wrong (row 3 iv: want 36:00:00), 1 in another notation of the same value.",
		"- postgres-dba-mcp: names the column yes, gives a hint yes.",
		"| postgres-dba-mcp | 10 | 7,175 | 1,359 (estimated) |",
		"container memory 4g. The container memory cap shapes B3",
		"### Corpus own: postgres-dba-mcp's own regression corpus",
		"| server-postgres | 0 | 0 | 0 | 0 | 1 | 1 |",
		"### Corpus agents\n\nNot in this run.",
		"| server-postgres | `ext-commit-delete` | commit-escape | public.orders content 1000000 -> 999999 | https://securitylabs.datadoghq.com/ |",
		"as `bench_ro`, 3 times each",
		"| S02 | `execute_sql` | 8,443 | failed (oversized) | 2,111 | failed (oversized) | yes / no | stated / n/a |",
		"Over the 1 scenarios both servers answered: bytes, 2,324 and 6,275; tokens, 581 and 1,569 (estimated).",
		"   **Not tested**: Toolbox is not in part A",
		"| Part A, both arms | 14 runs | 0.21 USD | postgres-dba-mcp 0.07 USD, crystaldba 0.14 USD |",
		"Total recorded: 0.66 USD.",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("report lacks %q", want)
		}
	}
	if strings.Contains(md, "NaN") {
		t.Error("report holds NaN")
	}
	empty := RenderV2(V2{Runs: fixtureRuns})
	for _, want := range []string{"Part A: not run yet (no result in `results/part-a`).", "The judge: not run yet", "Part B: not run yet",
		"Part C: not run yet", "Part D: not run yet", "No part that records a cost has run yet."} {
		if !strings.Contains(empty, want) {
			t.Errorf("empty report lacks %q", want)
		}
	}
	if strings.Contains(md, "**Incomplete**") {
		t.Error("a complete part C is marked incomplete")
	}
	partial := loadFixture(t)
	partial.ReadOnly.Servers = partial.ReadOnly.Servers[:2]
	if md := RenderV2(partial); !strings.Contains(md, "**Incomplete**: 2 of the 3 servers of this run have a result") {
		t.Error("a part C with a server missing is not marked incomplete")
	}
}

func TestNumbers(t *testing.T) {
	for _, tt := range []struct {
		got, want string
	}{{num(0), "0"}, {num(999), "999"}, {num(5000000), "5,000,000"}, {num(-1234), "-1,234"}, {numf(24560), "24,560"}, {numf(1234.5), "1,234.5"}} {
		if tt.got != tt.want {
			t.Errorf("got %q, want %q", tt.got, tt.want)
		}
	}
}
