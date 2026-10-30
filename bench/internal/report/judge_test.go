package report

import (
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestRenderJudge(t *testing.T) {
	order := func(a, winner string, dba, crystal results.JudgeScores) results.JudgeOrder {
		return results.JudgeOrder{A: a, Winner: winner, Scores: map[string]results.JudgeScores{results.DBA: dba, results.Crystal: crystal}}
	}
	good := results.JudgeScores{Correctness: 5, Completeness: 4, Actionability: 3, Errors: []string{}}
	bad := results.JudgeScores{Correctness: 1, Completeness: 2, Actionability: 3, Errors: []string{"invented index"}}
	pairs := []results.JudgePair{
		{Task: "T04", Run: 1, Outcome: results.DBA, Orders: [2]results.JudgeOrder{
			order(results.DBA, results.DBA, good, bad), order(results.Crystal, results.DBA, good, bad)}},
		{Task: "T04", Run: 2, Outcome: judgeTie, Orders: [2]results.JudgeOrder{
			order(results.DBA, results.DBA, good, good), order(results.Crystal, results.Crystal, good, good)}},
	}
	pairs[0].Repeats = []string{results.DBA, results.DBA, results.DBA}
	pairs[1].Repeats = []string{judgeTie, results.DBA, judgeTie}
	attrs := []results.JudgeAttribution{{Task: "T04", Run: 1, Server: results.Crystal, File: "T04-crystal-1.jsonl", Errors: []results.AttributedError{
		{Claim: "rarely used", Origin: results.OriginTool, Call: 2, Tool: "analyze_db_health", Why: "count hidden"},
		{Claim: "x", Origin: results.OriginNone, Why: "y"}}}}
	var sb strings.Builder
	renderJudge(&sb, Inputs{Judge: &results.Judge{Model: "claude-x", Effort: "low", CostUSD: 1.5, Pairs: pairs, Attributions: attrs}})
	md := sb.String()
	for _, want := range []string{
		"## N3 — answer quality (judge)",
		"Judge: claude-x (effort low) through",
		"| T04 | 1 | 1 | 0 | 5.0 / 4.0 / 3.0 | 3.0 / 3.0 / 3.0 |",
		"| all | 1 | 1 | 0 |",
		"- crystal T04 run 1: invented index\n",
		"| T04 | 2 | 1 (50%) |",
		"| all | 2 | 1 (50%) |",
		"**not blind**",
		"| crystal | 1 | 0 | 0 | 1 | 2 |",
		"- crystal T04 run 1, `analyze_db_health` (`T04-crystal-1.jsonl`): rarely used (count hidden)",
	} {
		if !strings.Contains(md, want) {
			t.Errorf("section lacks %q:\n%s", want, md)
		}
	}
	if n := strings.Count(md, "invented index"); n != 1 {
		t.Errorf("error listed %d times, want once per run and server", n)
	}
	sb.Reset()
	renderJudge(&sb, Inputs{})
	if !strings.Contains(sb.String(), "not run") {
		t.Errorf("no judge: %q", sb.String())
	}
}

const judgeTie = "tie"
