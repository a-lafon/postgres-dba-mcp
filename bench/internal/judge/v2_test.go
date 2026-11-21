package judge

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

var v2Facts = []Fact{
	{ID: "F1", Statement: "Customer 526, KARL SEAL, spent 221.55.", Recognize: "the id or the name, and the amount to the cent"},
	{ID: "F2", Statement: "Customer 148, ELEANOR HUNT, is second with 216.54."},
}

const okGrade = `{"facts": [{"id": "F1", "label": "present", "evidence": "KARL SEAL 221.55"}, {"id": "F2", "label": "absent", "evidence": ""}], "unsupported": ["customer 999 spent 300"], "doubt": false, "doubt_quote": ""}`

func TestParseGrade(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", okGrade, true},
		{"one code fence", "```json\n" + okGrade + "\n```", true},
		{"prose around", "Here: " + okGrade, false},
		{"text after", okGrade + " done", false},
		{"unknown field", strings.Replace(okGrade, `"doubt": false`, `"doubt": false, "score": 3`, 1), false},
		{"missing doubt", strings.Replace(okGrade, `"doubt": false, `, ``, 1), false},
		{"missing evidence", strings.Replace(okGrade, `, "evidence": ""`, ``, 1), false},
		{"bad label", strings.Replace(okGrade, `"absent"`, `"partial"`, 1), false},
		{"disputed is not a judge label", strings.Replace(okGrade, `"absent"`, `"disputed"`, 1), false},
		{"fact missing", strings.Replace(okGrade, `, {"id": "F2", "label": "absent", "evidence": ""}`, ``, 1), false},
		{"facts out of order", strings.NewReplacer(`"F1"`, `"F2"`, `"F2"`, `"F1"`).Replace(okGrade), false},
		{"doubt as string", strings.Replace(okGrade, `"doubt": false`, `"doubt": "no"`, 1), false},
		{"not json", "present, absent", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g, err := ParseGrade(tt.in, v2Facts)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok = %v", err, tt.ok)
			}
			if tt.name == "plain" && (g.Facts[0].Label != results.FactPresent || g.Facts[1].Label != results.FactAbsent ||
				g.Unsupported[0] != "customer 999 spent 300" || g.Doubt) {
				t.Errorf("grade = %+v", g)
			}
		})
	}
}

func grade(doubt bool, labels ...string) results.JudgeGrade {
	g := results.JudgeGrade{Doubt: doubt}
	for i, l := range labels {
		g.Facts = append(g.Facts, results.FactGrade{ID: fmt.Sprintf("F%d", i+1), Label: l})
	}
	return g
}

func TestConsensus(t *testing.T) {
	P, A, C := results.FactPresent, results.FactAbsent, results.FactContradicted
	tests := []struct {
		name      string
		solved    bool
		grades    []results.JudgeGrade
		facts     map[string]string
		doubt     string
		confWrong bool
	}{
		{"agree, failed, no doubt", false, []results.JudgeGrade{grade(false, P, C, A), grade(false, P, C, A)},
			map[string]string{"F1": P, "F2": C, "F3": A}, results.DoubtNo, true},
		{"disagree on facts and doubt", false, []results.JudgeGrade{grade(true, P, C, A), grade(false, A, C, P)},
			map[string]string{"F1": results.FactDisputed, "F2": C, "F3": results.FactDisputed}, results.DoubtDisputed, false},
		{"failed but signals a limit", false, []results.JudgeGrade{grade(true, A), grade(true, A)},
			map[string]string{"F1": A}, results.DoubtYes, false},
		{"solved, no doubt", true, []results.JudgeGrade{grade(false, P), grade(false, P)},
			map[string]string{"F1": P}, results.DoubtNo, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := results.GradedAnswer{Solved: tt.solved, Grades: tt.grades}
			Consensus(&a)
			if fmt.Sprint(a.Facts) != fmt.Sprint(tt.facts) || a.Doubt != tt.doubt || a.ConfidentlyWrong != tt.confWrong {
				t.Errorf("got facts %v doubt %s confidently wrong %v", a.Facts, a.Doubt, a.ConfidentlyWrong)
			}
		})
	}
}

func TestPromptV2(t *testing.T) {
	it := Item{Task: "A01", Server: "dba", Run: 1, Question: "Which 5 customers spent the most?", Facts: v2Facts,
		Wrong: []string{"MARY SMITH is first"}, Answer: "postgres-dba-mcp's `execute_sql` says KARL SEAL spent 221.55.\n-- 5 rows, 3 ms"}
	p, err := PromptV2(it)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Which 5 customers spent the most?", "- F1: Customer 526, KARL SEAL, spent 221.55. How to recognize it: the id or the name",
		"- F2: Customer 148, ELEANOR HUNT, is second with 216.54.\n", "- MARY SMITH is first", "[server]'s [tool] says KARL SEAL spent 221.55."} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q:\n%s", want, p)
		}
	}
	for _, leak := range []string{"postgres-dba-mcp", "execute_sql", "-- 5 rows", "dba\n", "Run"} {
		if strings.Contains(p, leak) {
			t.Errorf("prompt leaks %q", leak)
		}
	}
	it.Wrong = nil
	if p, _ = PromptV2(it); !strings.Contains(p, "None listed.") {
		t.Errorf("prompt without wrong claims:\n%s", p)
	}
}

func TestGrade(t *testing.T) {
	var calls atomic.Int32
	fake := func(model string, bad bool) CallFunc {
		var first atomic.Bool
		return func(_ context.Context, prompt string) (tokens.Result, error) {
			calls.Add(1)
			if strings.Contains(prompt, "crystaldba") {
				t.Errorf("%s got an answer that names its server", model)
			}
			if bad && !first.Swap(true) {
				return tokens.Result{Text: "Sure! Here is the grade.", Model: model, CostUSD: 0.01}, nil
			}
			if bad && !strings.Contains(prompt, "could not be read") {
				t.Errorf("retry prompt lacks the parse error")
			}
			return tokens.Result{Text: okGrade, Model: model, CostUSD: 0.02}, nil
		}
	}
	j := &Judge{Dir: t.TempDir(), Parallel: 2, Models: map[string]CallFunc{
		"claude-sonnet-5-5": fake("claude-sonnet-5-5", true), "claude-opus-5-5": fake("claude-opus-5-5", false)}}
	items := []Item{{Task: "A01", Server: "crystal", Run: 1, Question: "q", Answer: "crystaldba says KARL SEAL 221.55", Facts: v2Facts}}
	out, err := j.Grade(context.Background(), items)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3 (one retry)", calls.Load())
	}
	a := out.Answers[0]
	if fmt.Sprint(out.Judges) != "[claude-opus-5-5 claude-sonnet-5-5]" || a.Grades[0].Model != "claude-opus-5-5" ||
		fmt.Sprint(a.Grades[1].CallsUSD) != "[0.01 0.02]" || a.Facts["F1"] != results.FactPresent || !a.ConfidentlyWrong {
		t.Errorf("out = %+v", out)
	}
	if out.CostUSD < 0.0499 || out.CostUSD > 0.0501 {
		t.Errorf("cost = %v, want 0.05", out.CostUSD)
	}
	// A second pass reads the kept replies back and calls nothing.
	j2 := &Judge{Dir: j.Dir, Models: j.Models}
	if _, err := j2.Grade(context.Background(), items); err != nil || calls.Load() != 3 {
		t.Errorf("second pass: err %v, calls %d", err, calls.Load())
	}
	// A reply that never parses fails after one retry.
	j3 := &Judge{Dir: t.TempDir(), Models: map[string]CallFunc{"m": func(context.Context, string) (tokens.Result, error) {
		return tokens.Result{Text: "{}", Model: "m"}, nil
	}}}
	if _, err := j3.Grade(context.Background(), items); err == nil {
		t.Error("an unparsable reply did not fail")
	}
}

func TestReadItems(t *testing.T) {
	ok := `[{"task":"A01","server":"dba","run":1,"solved":true,"question":"q","answer":"a","facts":[{"id":"F1","statement":"s","recognize":""}],"wrong":[]}]`
	if items, err := ReadItems([]byte(ok)); err != nil || len(items) != 1 || !items[0].Solved {
		t.Fatalf("items %+v, err %v", items, err)
	}
	for _, bad := range []string{
		`[{"task":"A01","server":"dba","run":0,"facts":[{"id":"F1","statement":"s"}]}]`,
		`[{"task":"A01","server":"dba","run":1,"facts":[]}]`,
		`[{"task":"A01","server":"dba","run":1,"facts":[{"id":"F1","statement":"s"},{"id":"F1","statement":"t"}]}]`,
		`[{"task":"A01","server":"dba","run":1,"facts":[{"id":"F1","statement":"s"}]},{"task":"A01","server":"dba","run":1,"facts":[{"id":"F1","statement":"s"}]}]`,
	} {
		if _, err := ReadItems([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}

func TestItemsFromTaskRuns(t *testing.T) {
	facts := []results.Fact{{Text: "966 have active = 1."}, {Text: "There is a single count.", Wrong: true}, {Text: "987 have activebool."}}
	runs := []results.TaskRun{
		{Task: "A04", Server: "dba", Arm: results.ArmRO, Rep: 1, Solved: true, Final: "966 and 987", Facts: facts},
		{Task: "A04", Server: "dba", Arm: results.ArmW, Rep: 1, Final: "987", Facts: facts},
	}
	items := ItemsFromTaskRuns(runs, map[string]string{"A04": "How many active customers?"})
	b, err := json.Marshal(items)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ReadItems(b); err != nil {
		t.Fatalf("the items do not pass ReadItems: %v", err)
	}
	it := items[0]
	if it.Question != "How many active customers?" || !it.Solved || it.Answer != "966 and 987" || items[1].Task != "A04-w" {
		t.Errorf("items = %+v", items)
	}
	if len(it.Facts) != 2 || it.Facts[1].ID != "F2" || it.Facts[1].Statement != "987 have activebool." || len(it.Wrong) != 1 {
		t.Errorf("facts = %+v, wrong = %v", it.Facts, it.Wrong)
	}
}
