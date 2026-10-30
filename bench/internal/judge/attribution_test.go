package judge

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

func TestRunRepeats(t *testing.T) {
	cases := []Case{{Task: "T01", Run: 1, Question: "q", Facts: "f", Answers: map[string]string{results.DBA: "answer-dba", results.Crystal: "answer-crystal"}}}
	var calls atomic.Int32
	j := &Judge{Call: fake("answer-dba", false, &calls), Dir: t.TempDir()}
	pairs, err := j.Run(context.Background(), cases, 3)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 6 || len(pairs[0].Repeats) != 3 || pairs[0].Repeats[2] != results.DBA {
		t.Errorf("calls = %d, repeats = %v", calls.Load(), pairs[0].Repeats)
	}
	// A first pass already paid for is read back and not counted twice.
	before := j.Spent()
	if _, err := j.Run(context.Background(), cases, 1); err != nil || j.Spent() != before || calls.Load() != 6 {
		t.Errorf("err = %v, spent = %v (was %v), calls = %d", err, j.Spent(), before, calls.Load())
	}
}

const okAttr = `{"errors": [{"claim": "rarely used", "origin": "tool", "call": 2, "why": "the output hides the scan count"}, {"claim": "x", "origin": "none", "call": 0, "why": "stated openly"}]}`

func TestParseAttribution(t *testing.T) {
	tests := []struct {
		name  string
		in    string
		calls int
		ok    bool
	}{
		{"plain", okAttr, 2, true},
		{"no error", `{"errors": []}`, 0, true},
		{"call out of range", okAttr, 1, false},
		{"tool without call", strings.Replace(okAttr, `"call": 2`, `"call": 0`, 1), 2, false},
		{"none with call", strings.Replace(okAttr, `"origin": "none", "call": 0`, `"origin": "none", "call": 1`, 1), 2, false},
		{"unknown origin", strings.Replace(okAttr, `"tool"`, `"server"`, 1), 2, false},
		{"missing why", strings.Replace(okAttr, `, "why": "stated openly"`, ``, 1), 2, false},
		{"text after", okAttr + " ok", 2, false},
		{"not json", "none", 2, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs, err := ParseAttribution(tt.in, tt.calls)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok = %v", err, tt.ok)
			}
			if tt.name == "plain" && (len(errs) != 2 || errs[0].Origin != results.OriginTool || errs[0].Call != 2) {
				t.Errorf("errors = %+v", errs)
			}
		})
	}
}

func TestShorten(t *testing.T) {
	if got := Shorten("short"); got != "short" {
		t.Errorf("short result changed: %q", got)
	}
	long := strings.Repeat("é", resultHead+resultTail+100)
	got := Shorten(long)
	if !strings.Contains(got, "[... 100 characters cut by the benchmark, not by the tool ...]") || len([]rune(got)) > resultHead+resultTail+80 || !strings.HasPrefix(got, "éé") {
		t.Errorf("long result: %d runes", len([]rune(got)))
	}
}

func TestAttribute(t *testing.T) {
	calls := []Call{{Name: "analyze_db_health", Input: `{}`, Result: "! rarely used (<= 50 scans)"}, {Name: "execute_sql", Result: "ok"}}
	cases := []Case{{Task: "T06", Run: 3, Question: "q", Facts: "f",
		Answers: map[string]string{results.DBA: "ans-dba", results.Crystal: "ans-crystal"},
		Trails:  map[string]Trail{results.DBA: {File: "T06-dba-3.jsonl", Calls: calls}, results.Crystal: {File: "T06-crystal-3.jsonl"}}}}
	pairs := []results.JudgePair{{Orders: [2]results.JudgeOrder{
		{Scores: map[string]results.JudgeScores{results.DBA: {Errors: []string{"never scanned"}}}},
		{Scores: map[string]results.JudgeScores{results.DBA: {Errors: []string{"never scanned"}}}}}}}
	var seen []string
	j := &Judge{Dir: t.TempDir(), Call: func(_ context.Context, p string) (tokens.Result, error) {
		seen = append(seen, p)
		if strings.Contains(p, "ans-dba") {
			return tokens.Result{Text: `{"errors": [{"claim": "never scanned", "origin": "tool", "call": 1, "why": "w"}]}`}, nil
		}
		return tokens.Result{Text: `{"errors": []}`}, nil
	}, Parallel: 1}
	got, err := j.Attribute(context.Background(), cases, pairs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Server != results.DBA || got[0].File != "T06-dba-3.jsonl" || got[0].Errors[0].Tool != "analyze_db_health" || len(got[1].Errors) != 0 {
		t.Errorf("attributions = %+v", got)
	}
	for _, p := range seen {
		if strings.Contains(p, "ans-dba") && (strings.Count(p, "never scanned") != 1 || !strings.Contains(p, "### Call 1: analyze_db_health") || !strings.Contains(p, "rarely used")) {
			t.Errorf("dba prompt lacks the claimed error once or the calls:\n%s", p)
		}
		if strings.Contains(p, "ans-crystal") && !strings.Contains(p, "(no tool call)") {
			t.Errorf("crystal prompt: %s", p)
		}
	}
}

func TestPairwisePromptRule(t *testing.T) {
	p, err := Prompt("q", "f", "a", "b")
	if err != nil || !strings.Contains(p, "states openly") || !strings.Contains(p, "against the reference facts, not against what the answer happens to display") {
		t.Errorf("prompt lacks the rule: %v", err)
	}
}

func TestCalibration(t *testing.T) {
	var cases []Case
	var pairs []results.JudgePair
	for _, task := range []string{"T01", "T04", "T06"} {
		for run := 1; run <= 3; run++ {
			cases = append(cases, Case{Task: task, Run: run, Question: "q " + task, Facts: "- fact\n", Answers: map[string]string{results.DBA: "DBA-ANSWER", results.Crystal: "CRYSTAL-ANSWER"}})
			pairs = append(pairs, results.JudgePair{Task: task, Run: run, Outcome: results.DBA})
		}
	}
	sheet, key := Sheet(cases, pairs, 5)
	if len(key.Pairs) != 5 {
		t.Fatalf("pairs = %d, want 5", len(key.Pairs))
	}
	tasks := map[string]bool{}
	for _, p := range key.Pairs[:3] {
		tasks[p.Task] = true
	}
	if len(tasks) != 3 {
		t.Errorf("the first three pairs cover %v, want one per task", tasks)
	}
	bare := strings.ReplaceAll(strings.ReplaceAll(sheet, "DBA-ANSWER", ""), "CRYSTAL-ANSWER", "")
	for _, leak := range []string{"dba", "crystal", "judge", "Judge", "winner"} {
		if strings.Contains(bare, leak) {
			t.Errorf("sheet mentions %q", leak)
		}
	}
	if again, k2 := Sheet(cases, pairs, 5); again != sheet || k2.Pairs[4] != key.Pairs[4] {
		t.Error("sheet is not reproducible")
	}
	// The judge preferred dba everywhere. The human agrees on pair 1, differs on pair 2, ties on pair 4, leaves 3 and 5.
	pick := func(p KeyPair, server string) string {
		if p.A == server {
			return "A"
		}
		return "B"
	}
	filled := sheet
	for n, v := range map[int]string{1: pick(key.Pairs[0], results.DBA), 2: pick(key.Pairs[1], results.Crystal), 4: "TIE"} {
		const line = "Verdict (A / B / tie): "
		i := strings.Index(filled, fmt.Sprintf("## Pair %d\n", n))
		j := i + strings.Index(filled[i:], line) + len(line)
		filled = filled[:j] + v + filled[j:]
	}
	ag, err := Compare(filled, key)
	if err != nil || ag.Filled != 3 || ag.Agree != 1 || len(ag.Lines) != 5 || !strings.Contains(ag.Lines[2], "not filled") {
		t.Errorf("agreement = %+v, err = %v", ag, err)
	}
	if _, err := Compare(strings.Replace(sheet, "Verdict (A / B / tie): ", "Verdict (A / B / tie): maybe", 1), key); err == nil {
		t.Error("an invalid verdict was accepted")
	}
}
