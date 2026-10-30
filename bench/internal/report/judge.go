package report

import (
	"fmt"
	"slices"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// renderJudge writes the answer-quality section: per task, the pairs each server won or tied, the mean scores per
// server and the factual errors the judge found. It is informational and never enters the verdict.
func renderJudge(sb *strings.Builder, in Inputs) {
	sb.WriteString("## N3 — answer quality (judge)\n\n")
	if in.Judge == nil || len(in.Judge.Pairs) == 0 {
		sb.WriteString("not run\n\n")
		return
	}
	j := in.Judge
	model := j.Model
	if j.Effort != "" {
		model += " (effort " + j.Effort + ")"
	}
	fmt.Fprintf(sb, "Informational: not part of the verdict. Judge: %s through `claude -p` (no tool, no MCP), the same family of model as the agents, so it may favour its own style. "+
		"Each pair (same task, same run index) is judged twice with the A/B order swapped; a server wins only when both orders agree, otherwise the pair is a tie. "+
		"Scores are 1 to 5, the mean of both orders and all runs of the task. Judge cost: %.2f USD.\n\n", model, j.CostUSD)

	type tally struct {
		dba, crystal, ties int
		sum                map[string][3]float64 // per server: correctness, completeness, actionability
		n                  map[string]int
	}
	newTally := func() *tally { return &tally{sum: map[string][3]float64{}, n: map[string]int{}} }
	byTask, total := map[string]*tally{}, newTally()
	var tasks []string
	// Errors are kept once per task, run and server, even though both orders list them.
	var found []string
	seen := map[string]bool{}
	for _, p := range j.Pairs {
		t := byTask[p.Task]
		if t == nil {
			t = newTally()
			byTask[p.Task] = t
			tasks = append(tasks, p.Task)
		}
		for _, x := range []*tally{t, total} {
			switch p.Outcome {
			case results.DBA:
				x.dba++
			case results.Crystal:
				x.crystal++
			default:
				x.ties++
			}
			for _, o := range p.Orders {
				for server, sc := range o.Scores {
					s := x.sum[server]
					s[0] += float64(sc.Correctness)
					s[1] += float64(sc.Completeness)
					s[2] += float64(sc.Actionability)
					x.sum[server] = s
					x.n[server]++
				}
			}
		}
		for _, o := range p.Orders {
			for _, server := range []string{results.DBA, results.Crystal} {
				for _, e := range o.Scores[server].Errors {
					key := fmt.Sprintf("%s %s run %d: %s", server, p.Task, p.Run, e)
					if !seen[key] {
						seen[key] = true
						found = append(found, "- "+key)
					}
				}
			}
		}
	}
	mean := func(x *tally, server string) string {
		if x.n[server] == 0 {
			return "n/a"
		}
		s, n := x.sum[server], float64(x.n[server])
		return fmt.Sprintf("%.1f / %.1f / %.1f", s[0]/n, s[1]/n, s[2]/n)
	}
	var rows [][]string
	for _, t := range tasks {
		x := byTask[t]
		rows = append(rows, []string{t, fmt.Sprint(x.dba), fmt.Sprint(x.ties), fmt.Sprint(x.crystal), mean(x, results.DBA), mean(x, results.Crystal)})
	}
	rows = append(rows, []string{"all", fmt.Sprint(total.dba), fmt.Sprint(total.ties), fmt.Sprint(total.crystal), mean(total, results.DBA), mean(total, results.Crystal)})
	tbl(sb, []string{"Task", "dba wins", "Ties", "crystal wins", "dba mean (correct. / compl. / action.)", "crystal mean (correct. / compl. / action.)"}, rows)
	if len(found) == 0 {
		sb.WriteString("Factual errors and hallucinations listed by the judge: none.\n\n")
	} else {
		sb.WriteString("Factual errors and hallucinations listed by the judge:\n\n" + strings.Join(found, "\n") + "\n\n")
	}
	renderStability(sb, j.Pairs, tasks)
	renderAttribution(sb, j.Attributions)
}

// renderStability shows, per task, the share of pairs whose outcome is the same in every repeat of the judge.
func renderStability(sb *strings.Builder, pairs []results.JudgePair, tasks []string) {
	same, n := map[string]int{}, map[string]int{}
	for _, p := range pairs {
		if len(p.Repeats) < 2 {
			continue
		}
		n[p.Task]++
		if !slices.ContainsFunc(p.Repeats, func(o string) bool { return o != p.Repeats[0] }) {
			same[p.Task]++
		}
	}
	if len(n) == 0 {
		return
	}
	var rows [][]string
	var totalSame, total int
	for _, t := range tasks {
		if n[t] > 0 {
			rows = append(rows, []string{t, fmt.Sprint(n[t]), fmt.Sprintf("%d (%.0f%%)", same[t], 100*float64(same[t])/float64(n[t]))})
			totalSame, total = totalSame+same[t], total+n[t]
		}
	}
	rows = append(rows, []string{"all", fmt.Sprint(total), fmt.Sprintf("%d (%.0f%%)", totalSame, 100*float64(totalSame)/float64(total))})
	sb.WriteString("Stability: the same pairs judged again (each time in both orders); agreement is the share of pairs whose outcome is identical in every repeat (the table above uses the first pass).\n\n")
	tbl(sb, []string{"Task", "Pairs repeated", "Identical outcome"}, rows)
}

// renderAttribution shows where the errors of the answers come from, per server, and the errors that come from a
// tool, which are the product's to fix.
func renderAttribution(sb *strings.Builder, attrs []results.JudgeAttribution) {
	if len(attrs) == 0 {
		return
	}
	sb.WriteString("Error origin: a second pass, **not blind** (it reads the tool calls, whose names reveal the server), gives the judge the question, the facts, the tool calls with their results (each result cut to its head and tail) and the final answer. " +
		"It classifies every error it finds, and every error of the blind pass for that answer, as `tool` (the tool output was wrong, ambiguous or lossy and the answer relayed it), `misread` (the tool output said otherwise), `unsupported` (nothing supports it) or `none` (not an error after all). The counts are of errors, not of answers, and an error that both passes found is counted twice.\n\n")
	origins := []string{results.OriginTool, results.OriginMisread, results.OriginUnsupported, results.OriginNone}
	count := map[string]map[string]int{}
	var tool []string
	for _, a := range attrs {
		if count[a.Server] == nil {
			count[a.Server] = map[string]int{}
		}
		for _, e := range a.Errors {
			count[a.Server][e.Origin]++
			if e.Origin == results.OriginTool {
				tool = append(tool, fmt.Sprintf("- %s %s run %d, `%s` (`%s`): %s (%s)", a.Server, a.Task, a.Run, e.Tool, a.File, e.Claim, e.Why))
			}
		}
	}
	var rows [][]string
	for _, server := range []string{results.DBA, results.Crystal} {
		row := []string{server}
		total := 0
		for _, o := range origins {
			row = append(row, fmt.Sprint(count[server][o]))
			total += count[server][o]
		}
		rows = append(rows, append(row, fmt.Sprint(total)))
	}
	tbl(sb, []string{"Server", "tool", "misread", "unsupported", "none", "Errors"}, rows)
	if len(tool) == 0 {
		sb.WriteString("Errors that come from a tool: none.\n\n")
		return
	}
	sb.WriteString("Errors that come from a tool (tool, transcript file):\n\n" + strings.Join(tool, "\n") + "\n\n")
}
