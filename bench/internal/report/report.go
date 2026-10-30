package report

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
)

func n3Criteria(in Inputs) []Criterion {
	names := [3][3]string{
		{KeyN3Rate, "N3 success rate", ">= crystaldba"},
		{KeyN3Tokens, "N3 median total tokens", "<= 0%"},
		{KeyN3Turns, "N3 median turns", "<= crystaldba + 1"},
	}
	if in.N3 == nil || len(in.N3.Runs) == 0 {
		var out []Criterion
		for _, n := range names {
			out = append(out, unverified(n[0], n[1], n[2]))
		}
		return out
	}
	d, c := splitRuns(in.N3.Runs)
	rate := func(runs []results.N3Run) float64 {
		p := 0
		for _, r := range runs {
			if r.Pass {
				p++
			}
		}
		return float64(p) / float64(max(len(runs), 1))
	}
	tok := func(runs []results.N3Run) float64 {
		v := make([]float64, len(runs))
		for i, r := range runs {
			v[i] = float64(r.InputTokens + r.OutputTokens)
		}
		return scenarios.Median(v)
	}
	turns := func(runs []results.N3Run) float64 {
		v := make([]float64, len(runs))
		for i, r := range runs {
			v[i] = float64(r.Turns)
		}
		return scenarios.Median(v)
	}
	out := []Criterion{{Key: names[0][0], Name: names[0][1], Target: fmt.Sprintf(">= crystaldba (%s)", pct(rate(c))),
		Result: pct(rate(d)), Met: yn(rate(d) >= rate(c))}}
	tc := Criterion{Key: names[1][0], Name: names[1][1], Target: names[1][2], Result: "n/a", Met: Unverified}
	if ch, ok := changeOf(tok(d), tok(c)); ok {
		tc.Result, tc.Met, tc.Num, tc.HasNum = pctChange(ch), yn(ch <= 0), ch, true
	}
	out = append(out, tc, Criterion{Key: names[2][0], Name: names[2][1], Target: fmt.Sprintf("<= %.1f", turns(c)+1),
		Result: fmt.Sprintf("%.1f", turns(d)), Met: yn(turns(d) <= turns(c)+1)})
	return out
}

func splitRuns(runs []results.N3Run) (dba, crystal []results.N3Run) {
	for _, r := range runs {
		switch r.Server {
		case results.Crystal:
			crystal = append(crystal, r)
		case results.DBA:
			dba = append(dba, r)
		}
	}
	return dba, crystal
}

// Criteria returns every target line of the run, in report order: security, N2, N3.
func Criteria(in Inputs) []Criterion {
	cs := []Criterion{securityCriterion(in)}
	cs = append(cs, n2Criteria(in)...)
	return append(cs, n3Criteria(in)...)
}

func tbl(sb *strings.Builder, header []string, rows [][]string) {
	sb.WriteString("| " + strings.Join(header, " | ") + " |\n")
	sb.WriteString("|" + strings.Repeat("---|", len(header)) + "\n")
	for _, r := range rows {
		sb.WriteString("| " + strings.Join(r, " | ") + " |\n")
	}
	sb.WriteString("\n")
}

func (in Inputs) env() results.Env {
	switch {
	case in.N2 != nil:
		return in.N2.Env
	case in.Sec != nil:
		return in.Sec.Env
	case in.Ops != nil:
		return in.Ops.Env
	case in.N1 != nil:
		return in.N1.Env
	case in.N3 != nil:
		return in.N3.Env
	}
	return results.Env{}
}

func header(in Inputs) string {
	e := in.env()
	tok := "tokens: " + e.TokenCounter
	if e.TokenCounter != results.CounterEstimate {
		tok += " (" + e.Model + ")"
	}
	digest := strings.TrimPrefix(e.CrystalDigest, "sha256:")
	digest = "sha256:" + digest[:min(len(digest), 8)] + "…"
	return fmt.Sprintf("Run %s · postgres-dba-mcp %s (%s) · crystaldba %s · PostgreSQL %s · scale %g · %s",
		in.RunID, e.DBAVersion, e.DBACommit, digest, e.PGVersion, e.Scale, tok)
}

// Render writes the report and returns it with its verdict.
func Render(in Inputs) (string, Verdict) {
	cs := Criteria(in)
	v := Decide(cs)
	var sb strings.Builder
	sb.WriteString("# postgres-dba-mcp vs crystaldba/postgres-mcp 0.3.0 — bench report\n\n" + header(in) + "\n\n")
	sb.WriteString("## Verdict: " + v.Name + "\n\n")
	for _, l := range v.Lines {
		sb.WriteString(l + "\n\n")
	}
	var rows [][]string
	for _, c := range cs {
		rows = append(rows, []string{c.Name, c.Target, c.Result, c.Met})
	}
	tbl(&sb, []string{"Criterion", "Target", "Result", "Met"}, rows)
	renderN2(&sb, in)
	renderSecurity(&sb, in)
	renderN3(&sb, in)
	renderJudge(&sb, in)
	renderOps(&sb, in)
	renderN1(&sb, in)
	renderSkipped(&sb, in)
	return sb.String(), v
}

func cellTokens(r results.N2Row, server string) string {
	c, ok := r.Cells[server]
	switch {
	case !ok:
		return "n/a"
	case c.Status != results.StatusOK:
		return c.Status
	}
	if c.Estimated {
		return fmt.Sprintf("~%d", c.Tokens)
	}
	return fmt.Sprint(c.Tokens)
}

func yesNo(b bool) string { return map[bool]string{true: "yes", false: "no"}[b] }

func renderN2(sb *strings.Builder, in Inputs) {
	sb.WriteString("## N2 — deterministic responses\n\n")
	if in.N2 == nil {
		sb.WriteString("not run\n\n")
		return
	}
	var rows [][]string
	for _, r := range in.N2.Rows {
		a, b := r.Cells[results.DBA], r.Cells[results.Crystal]
		delta := "n/a"
		if a.Status == results.StatusOK && b.Status == results.StatusOK {
			if ch, ok := changeOf(float64(a.Tokens), float64(b.Tokens)); ok {
				delta = pctChange(ch)
			}
		}
		bytes, lat, correct, trunc := "n/a", "n/a", "n/a", "n/a"
		if a.Status == results.StatusOK {
			bytes, lat, trunc = fmt.Sprint(a.Bytes), fmt.Sprintf("%.0f / %.0f", a.P50MS, a.P95MS), a.Truncation
		}
		if a.Status != results.StatusSkipped && b.Status != "" {
			correct = yesNo(a.Correct) + " / " + yesNo(b.Correct)
		}
		rows = append(rows, []string{r.ID, r.Tool, cellTokens(r, results.DBA), cellTokens(r, results.Crystal), delta, bytes, lat, correct, trunc})
	}
	tbl(sb, []string{"ID", "Tool", "Tokens dba", "Tokens crystal", "Δ", "Bytes dba", "p50 / p95 ms dba", "Correct dba / crystal", "Truncation dba"}, rows)

	// S17 and S18 trap detection, over all traps and over the ones crystaldba can detect.
	var trapRows [][]string
	for _, id := range []string{"S17", "S18"} {
		r, ok := rowByID(in.N2, id)
		if !ok {
			continue
		}
		for _, server := range []string{results.DBA, results.Crystal} {
			c, ok := r.Cells[server]
			if !ok || c.Status != results.StatusOK {
				continue
			}
			want := scenarios.ExpectedTraps(id)
			common := intersect(want, scenarios.CrystalTraps)
			trapRows = append(trapRows, []string{id, server, strings.Join(c.Detected, ", "),
				fmt.Sprintf("%d/%d", trapCount(c, want), len(want)),
				fmt.Sprintf("%d/%d", trapCount(c, common), len(common))})
		}
	}
	sb.WriteString("Trap detection: S17 (`all`) must find T1-T6 and S18 (`index`) T1 and T2. The last column counts only the traps crystaldba can detect (T1, T2, T4, T5, T6: its vacuum check covers wraparound, not dead tuples); the target compares that column.\n\n")
	tbl(sb, []string{"Scenario", "Server", "Detected", "Expected found", "Crystal-detectable found"}, trapRows)
	fmt.Fprintf(sb, "Sessions of both servers ran as bench_ro only: %s.\n\n", yesNo(in.N2.SameRole))
}

func renderSecurity(sb *strings.Builder, in Inputs) {
	sb.WriteString("## Security — attack corpus replay\n\n")
	if in.Sec == nil {
		sb.WriteString("not run\n\n")
		return
	}
	var rows [][]string
	for _, server := range scenarios.Sorted(in.Sec.Attacks) {
		var refused, failed, executed, effects int
		for _, c := range in.Sec.Attacks[server] {
			switch c.Class {
			case results.ClassRefused:
				refused++
			case results.ClassFailed:
				failed++
			default:
				executed++
			}
			if c.SideEffect != "" {
				effects++
			}
		}
		rows = append(rows, []string{server, fmt.Sprint(len(in.Sec.Attacks[server])), fmt.Sprint(refused), fmt.Sprint(failed), fmt.Sprint(executed), fmt.Sprint(effects)})
	}
	tbl(sb, []string{"Server", "Cases", "refused", "failed at execution", "executed", "with side effect"}, rows)
	s := SummarizeSecurity(in.Sec)
	fmt.Fprintf(sb, "postgres-dba-mcp: %d attack cases not refused or failed at execution, %d with a side effect, %d false refusals on legit.sql (%d cases). Target: 0 / 0 / 0.\n\n",
		s.NotRefused, s.SideEffects, s.FalseRefs, len(in.Sec.Legit))

	sb.WriteString("crystaldba on the function-in-FROM cases (`fn-*-from`, CVE-2026-85620), a reported finding that does not fail the bench:\n\n")
	var fn [][]string
	for _, c := range in.Sec.Attacks[results.Crystal] {
		if strings.HasPrefix(c.ID, "fn-") && strings.HasSuffix(c.ID, "-from") {
			effect := c.SideEffect
			if effect == "" {
				effect = "none"
			}
			fn = append(fn, []string{c.ID, c.Class, effect})
		}
	}
	tbl(sb, []string{"Case", "Outcome", "Side effect"}, fn)
	var other [][]string
	for _, server := range scenarios.Sorted(in.Sec.Attacks) {
		for _, c := range in.Sec.Attacks[server] {
			if c.Class == results.ClassExecuted || c.SideEffect != "" {
				effect := c.SideEffect
				if effect == "" {
					effect = "none"
				}
				other = append(other, []string{server, c.ID, c.Class, effect})
			}
		}
	}
	if len(other) > 0 {
		sb.WriteString("Every case that executed or had a side effect:\n\n")
		tbl(sb, []string{"Server", "Case", "Outcome", "Side effect"}, other)
	}
}

func medianRuns(runs []results.N3Run, f func(results.N3Run) float64) float64 {
	v := make([]float64, len(runs))
	for i, r := range runs {
		v[i] = f(r)
	}
	return scenarios.Median(v)
}

func renderN3(sb *strings.Builder, in Inputs) {
	sb.WriteString("## N3 — agent tasks\n\n")
	if in.N3 == nil || len(in.N3.Runs) == 0 {
		sb.WriteString("not run\n\n")
		return
	}
	models := map[string]bool{}
	for _, r := range in.N3.Runs {
		if r.Model != "" {
			models[r.Model] = true
		}
	}
	if len(models) == 0 {
		sb.WriteString("Model: not recorded\n\n")
	} else {
		sb.WriteString("Model: " + strings.Join(slices.Sorted(maps.Keys(models)), ", ") + "\n\n")
	}
	byTask := map[string]map[string][]results.N3Run{}
	for _, r := range in.N3.Runs {
		if byTask[r.Task] == nil {
			byTask[r.Task] = map[string][]results.N3Run{}
		}
		byTask[r.Task][r.Server] = append(byTask[r.Task][r.Server], r)
	}
	var tasks []string
	for t := range byTask {
		tasks = append(tasks, t)
	}
	sort.Strings(tasks)
	var rows [][]string
	for _, t := range tasks {
		row := []string{t}
		for _, server := range []string{results.DBA, results.Crystal} {
			runs := byTask[t][server]
			pass := 0
			for _, r := range runs {
				if r.Pass {
					pass++
				}
			}
			row = append(row, fmt.Sprintf("%d/%d", pass, len(runs)),
				fmt.Sprintf("%.0f", medianRuns(runs, func(r results.N3Run) float64 { return float64(r.InputTokens + r.OutputTokens) })),
				fmt.Sprintf("%.1f", medianRuns(runs, func(r results.N3Run) float64 { return float64(r.Turns) })))
		}
		rows = append(rows, row)
	}
	tbl(sb, []string{"Task", "Pass dba", "Tokens dba", "Turns dba", "Pass crystal", "Tokens crystal", "Turns crystal"}, rows)
}

func renderOps(sb *strings.Builder, in Inputs) {
	sb.WriteString("## Operations\n\n")
	if in.Ops == nil {
		sb.WriteString("not run\n\n")
		return
	}
	o := in.Ops
	show := func(m results.Measure, format string) string {
		switch {
		case m.Skipped:
			return "skipped"
		case len(m.Samples) > 1:
			note := fmt.Sprintf("median of %d, max "+format, len(m.Samples), slices.Max(m.Samples))
			if m.Note != "" {
				note = m.Note + "; " + note
			}
			return fmt.Sprintf(format, m.Value) + " (" + note + ")"
		case m.Note != "":
			return fmt.Sprintf(format, m.Value) + " (" + m.Note + ")"
		}
		return fmt.Sprintf(format, m.Value)
	}
	met := func(m results.Measure, limit float64) string {
		if m.Skipped {
			return Unverified
		}
		return yn(m.Value < limit)
	}
	conc, concMet := fmt.Sprintf("%d errors in %d calls", o.Concurrency.Errors, o.Concurrency.Calls), yn(o.Concurrency.Errors == 0)
	if o.Concurrency.Skipped {
		conc, concMet = "skipped", Unverified
	}
	rows := [][]string{
		{"Cold start (median of 20)", show(o.ColdStartMS[results.DBA], "%.1f ms"), show(o.ColdStartMS[results.Crystal], "%.0f ms"), "< 50 ms", met(o.ColdStartMS[results.DBA], 50)},
		{"Memory after 100 S02 calls", show(o.RSSMB[results.DBA], "%.1f MB"), show(o.RSSMB[results.Crystal], "%.1f MB"), "< 30 MB", met(o.RSSMB[results.DBA], 30)},
		{"Binary / image size", show(o.SizeMB[results.DBA], "%.1f MB"), show(o.SizeMB[results.Crystal], "%.0f MB"), "< 30 MB", met(o.SizeMB[results.DBA], 30)},
		{"50 concurrent S01 over HTTP", conc, "n/a", "0 errors", concMet},
	}
	tbl(sb, []string{"Measure", "dba", "crystal", "Target", "Met"}, rows)
	// ops.json written before the image measure has no native value: nothing to say.
	if n := o.NativeRSSMB; n.Skipped || n.Note != "" {
		sb.WriteString("Native binary of the host after the same calls (informational, not checked against the target): " + show(n, "%.1f MB") + ".\n\n")
	}
}

func renderN1(sb *strings.Builder, in Inputs) {
	sb.WriteString("## N1 — tool definitions (informational)\n\n")
	if in.N1 == nil {
		sb.WriteString("not run\n\n")
		return
	}
	var rows [][]string
	for _, s := range in.N1.Servers {
		rows = append(rows, []string{s.Server, fmt.Sprint(s.Tools), fmt.Sprint(s.Full), fmt.Sprint(s.Deferred)})
	}
	tbl(sb, []string{"Server", "tools", "full", "deferred"}, rows)
	sb.WriteString("`full` counts every tool's name, description and input schema; `deferred` counts the tool names plus the server instructions, which is what Claude Code loads at session start with tool search on. Counts are in " +
		in.N1.Env.TokenCounter + ". N1 is not part of the verdict.\n\n")
}

func renderSkipped(sb *strings.Builder, in Inputs) {
	sb.WriteString("## False refusals and skipped items\n\n")
	var lines []string
	if in.N2 != nil {
		for _, r := range in.N2.Rows {
			for _, server := range scenarios.Sorted(r.Cells) {
				c := r.Cells[server]
				if c.FalseRefusal {
					lines = append(lines, fmt.Sprintf("- false refusal: %s on %s", r.ID, server))
				}
				if c.Status == results.StatusSkipped {
					lines = append(lines, fmt.Sprintf("- skipped: %s on %s", r.ID, server))
				}
			}
		}
	}
	if in.Sec != nil {
		for _, c := range in.Sec.Legit {
			if c.Class == results.ClassRefused {
				lines = append(lines, fmt.Sprintf("- false refusal on legit.sql: %s (%s)", c.ID, c.Text))
			}
		}
	}
	if in.Ops != nil {
		for name, m := range map[string]results.Measure{"RSS dba": in.Ops.RSSMB[results.DBA], "RSS crystal": in.Ops.RSSMB[results.Crystal]} {
			if m.Skipped {
				lines = append(lines, "- skipped: ops "+name)
			}
		}
		if in.Ops.Concurrency.Skipped {
			lines = append(lines, "- skipped: ops concurrency")
		}
	}
	if len(lines) == 0 {
		sb.WriteString("none\n")
		return
	}
	sort.Strings(lines)
	sb.WriteString(strings.Join(lines, "\n") + "\n")
}
