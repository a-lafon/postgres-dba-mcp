package report

import (
	"fmt"
	"sort"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// Compare renders the cross-server synthesis of a run from its raw result files. It is a separate document from
// report.md: report.md is the go/no-go of postgres-dba-mcp against crystaldba, two columns and a verdict, while
// this one has a column per measured server and no verdict at all. Folding one into the other would make both
// unreadable, so they are two commands over the same JSON.
func Compare(in Inputs) string {
	var sb strings.Builder
	e := in.env()
	price, priced := tokens.PriceOf(e.Model)
	sb.WriteString("# Bench synthesis — every measured server\n\n")
	sb.WriteString(header(in) + "\n\n")
	if priced {
		sb.WriteString("Dollars: " + price.Label() + ".\n\n")
	} else {
		sb.WriteString("Dollars: no price for model " + e.Model + "; the dollar columns read n/a.\n\n")
	}
	if e.TokenCounter == results.CounterEstimate {
		sb.WriteString("**Token counts are the harness estimate `ceil(bytes/4)`.** JSON tokenises more densely than CSV, " +
			"so the estimate understates the servers that answer in JSON (DBHub, Toolbox) more than it understates " +
			"postgres-dba-mcp's CSV. The bias runs against postgres-dba-mcp, which is the right way round for a " +
			"comparison it is a party to, but it is a bias and the figures below carry it.\n\n")
	}
	servers := comparedServers(in)
	compareN1(&sb, in, servers, price, priced)
	compareN2(&sb, in, servers)
	compareSecurity(&sb, in, servers)
	compareOps(&sb, in, servers)
	compareSession(&sb, in, price, priced)
	compareTotals(&sb, in, servers, price, priced)
	return sb.String()
}

// comparedServers returns every server named by any level of the run, postgres-dba-mcp and crystaldba first.
func comparedServers(in Inputs) []string {
	seen := map[string]bool{}
	add := func(name string) { seen[name] = true }
	if in.N1 != nil {
		for _, s := range in.N1.Servers {
			add(s.Server)
		}
	}
	if in.N2 != nil {
		for _, r := range in.N2.Rows {
			for name := range r.Cells {
				add(name)
			}
		}
	}
	if in.Sec != nil {
		for name := range in.Sec.Attacks {
			add(name)
		}
		for name := range in.Sec.Legit {
			add(name)
		}
	}
	if in.Ops != nil {
		for name := range in.Ops.ColdStartMS {
			add(name)
		}
	}
	if in.Session != nil {
		for _, s := range in.Session.Servers {
			add(s.Server)
		}
	}
	var rest []string
	for name := range seen {
		if name != results.DBA && name != results.Crystal {
			rest = append(rest, name)
		}
	}
	sort.Strings(rest)
	var out []string
	for _, name := range []string{results.DBA, results.Crystal} {
		if seen[name] {
			out = append(out, name)
		}
	}
	return append(out, rest...)
}

// usd formats what n tokens cost, or n/a when the model has no price.
func usd(price tokens.Price, priced bool, n int) string {
	if !priced {
		return "n/a"
	}
	return fmt.Sprintf("$%.4f", price.InputUSD(n))
}

func compareN1(sb *strings.Builder, in Inputs, servers []string, price tokens.Price, priced bool) {
	sb.WriteString("## Tool definitions\n\n")
	if in.N1 == nil {
		sb.WriteString("not run\n\n")
		return
	}
	byName := map[string]results.N1Server{}
	for _, s := range in.N1.Servers {
		byName[s.Server] = s
	}
	var rows [][]string
	for _, name := range servers {
		s, ok := byName[name]
		if !ok {
			continue
		}
		rows = append(rows, []string{name, fmt.Sprint(s.Tools), fmt.Sprint(s.Full), usd(price, priced, s.Full), fmt.Sprint(s.Deferred)})
	}
	tbl(sb, []string{"Server", "tools", "definition tokens", "$ per request", "deferred"}, rows)
	sb.WriteString("`deferred` is the tool names plus the server instructions, what a client with tool search loads at session start.\n\n")
}

// commonScenarios returns the scenario ids every server answered, which is the only set a median may compare.
func commonScenarios(n2 *results.N2, servers []string) []string {
	var ids []string
	for _, r := range n2.Rows {
		all := true
		for _, name := range servers {
			c, ok := r.Cells[name]
			if !ok || c.Status != results.StatusOK {
				all = false
				break
			}
		}
		if all {
			ids = append(ids, r.ID)
		}
	}
	return ids
}

func compareN2(sb *strings.Builder, in Inputs, all []string) {
	sb.WriteString("## Scenarios\n\n")
	if in.N2 == nil {
		sb.WriteString("not run\n\n")
		return
	}
	// Only the servers n2 measured: a server measured on n1 alone must not empty the common set.
	var servers []string
	for _, name := range all {
		for _, r := range in.N2.Rows {
			if _, ok := r.Cells[name]; ok {
				servers = append(servers, name)
				break
			}
		}
	}
	common := commonScenarios(in.N2, servers)
	fmt.Fprintf(sb, "Median over the %d scenarios every server answered (%s), as the goal requires. A tool the server "+
		"does not have is counted apart, never as a wrong answer.\n\n", len(common), strings.Join(common, ", "))
	inCommon := map[string]bool{}
	for _, id := range common {
		inCommon[id] = true
	}
	var rows [][]string
	for _, name := range servers {
		var med []float64
		var answered, correct, noTool, failed int
		for _, r := range in.N2.Rows {
			c, ok := r.Cells[name]
			if !ok {
				continue
			}
			switch c.Status {
			case results.StatusOK:
				answered++
				if c.Correct {
					correct++
				}
				if inCommon[r.ID] {
					med = append(med, float64(c.Tokens))
				}
			case results.StatusNoTool:
				noTool++
			case results.StatusSkipped:
			default:
				failed++
			}
		}
		rows = append(rows, []string{name, fmt.Sprintf("%.0f", scenarios.Median(med)), fmt.Sprint(answered),
			fmt.Sprintf("%d/%d", correct, answered), fmt.Sprint(noTool), fmt.Sprint(failed)})
	}
	tbl(sb, []string{"Server", "median tokens (common)", "answered", "correct", "no tool", "failed"}, rows)

	header := append([]string{"ID", "Tool"}, servers...)
	var per [][]string
	for _, r := range in.N2.Rows {
		row := []string{r.ID, r.Tool}
		for _, name := range servers {
			row = append(row, cellTokens(r, name))
		}
		per = append(per, row)
	}
	sb.WriteString("Response tokens, scenario by scenario:\n\n")
	tbl(sb, header, per)
}

func compareSecurity(sb *strings.Builder, in Inputs, servers []string) {
	sb.WriteString("## Attacks and false refusals\n\n")
	if in.Sec == nil {
		sb.WriteString("not run\n\n")
		return
	}
	var rows [][]string
	for _, name := range servers {
		cases, ok := in.Sec.Attacks[name]
		legit := in.Sec.Legit[name]
		if !ok && legit == nil {
			continue
		}
		var refused, failed, executed, effects int
		for _, c := range cases {
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
		falseRefs := 0
		for _, c := range legit {
			if c.Class == results.ClassRefused {
				falseRefs++
			}
		}
		rows = append(rows, []string{name, fmt.Sprint(len(cases)), fmt.Sprint(refused), fmt.Sprint(failed),
			fmt.Sprint(executed), fmt.Sprint(effects), fmt.Sprintf("%d/%d", falseRefs, len(legit))})
	}
	tbl(sb, []string{"Server", "attacks", "refused", "failed at execution", "executed", "with side effect", "false refusals on legit.sql"}, rows)
}

func compareOps(sb *strings.Builder, in Inputs, servers []string) {
	sb.WriteString("## Memory, start-up, size, concurrency\n\n")
	if in.Ops == nil {
		sb.WriteString("not run\n\n")
		return
	}
	o := in.Ops
	num := func(m results.Measure, ok bool, format string) string {
		switch {
		case !ok:
			return "n/a"
		case m.Skipped:
			return "skipped"
		}
		s := fmt.Sprintf(format, m.Value)
		if len(m.Samples) > 1 {
			s += fmt.Sprintf(" (median of %d)", len(m.Samples))
		}
		return s
	}
	var rows [][]string
	for _, name := range servers {
		cs, okC := o.ColdStartMS[name]
		rss, okR := o.RSSMB[name]
		size, okS := o.SizeMB[name]
		conc, okX := o.Concurrency[name]
		if !okC && !okR && !okS && !okX {
			continue
		}
		c := "n/a"
		if okX {
			c = concText(conc)
		}
		rows = append(rows, []string{name, num(cs, okC, "%.1f ms"), num(rss, okR, "%.1f MB"), num(size, okS, "%.0f MB"), c})
	}
	tbl(sb, []string{"Server", "cold start", "memory after 100 S02 calls", "binary / image", "concurrency"}, rows)
	sb.WriteString("Cold start of a container includes `docker run`; postgres-dba-mcp is the native binary there and the linux image for memory. " +
		fmt.Sprintf("Concurrency is %d calls at once on one stdio session of one process, the same method for every server.\n\n", opsConcurrent))
}

// opsConcurrent repeats ops.Concurrent, which this package cannot import without a cycle through scenarios.
const opsConcurrent = 50

// sessionTurns are the turns the session curve reports.
var sessionTurns = []int{1, 5, 10}

func compareSession(sb *strings.Builder, in Inputs, price tokens.Price, priced bool) {
	sb.WriteString("## Context accumulated over a ten-turn investigation\n\n")
	if in.Session == nil {
		sb.WriteString("not run\n\n")
		return
	}
	sb.WriteString("Context at turn N: the tool definitions, which every request carries, plus every answer from turn 1 to N. " +
		"A server without the right tool runs the same fallback SQL as every other server without it.\n\n")
	header := []string{"Server", "definitions"}
	for _, n := range sessionTurns {
		header = append(header, fmt.Sprintf("turn %d", n), fmt.Sprintf("$ turn %d", n))
	}
	header = append(header, "fallbacks")
	var rows [][]string
	for _, s := range in.Session.Servers {
		row := []string{s.Server, fmt.Sprint(s.ToolTokens)}
		for _, n := range sessionTurns {
			if n > len(s.Turns) {
				row = append(row, "n/a", "n/a")
				continue
			}
			cum := s.Turns[n-1].Cumulative
			row = append(row, fmt.Sprint(cum), usd(price, priced, cum))
		}
		fb := 0
		for _, t := range s.Turns {
			if t.Fallback {
				fb++
			}
		}
		row = append(row, fmt.Sprintf("%d/%d", fb, len(s.Turns)))
		rows = append(rows, row)
	}
	tbl(sb, header, rows)
}

func compareTotals(sb *strings.Builder, in Inputs, servers []string, price tokens.Price, priced bool) {
	sb.WriteString("## Total per task\n\n")
	if in.N1 == nil || in.N2 == nil {
		sb.WriteString("needs n1 and n2\n\n")
		return
	}
	sb.WriteString("One task is one request: the tool definitions the client sends plus the one answer it gets back, " +
		"the median over the scenarios every server answered. The session column is the whole ten-turn investigation.\n\n")
	defs := map[string]int{}
	for _, s := range in.N1.Servers {
		defs[s.Server] = s.Full
	}
	sess := map[string]int{}
	if in.Session != nil {
		for _, s := range in.Session.Servers {
			if len(s.Turns) > 0 {
				sess[s.Server] = s.Turns[len(s.Turns)-1].Cumulative
			}
		}
	}
	var n2Servers []string
	for _, name := range servers {
		for _, r := range in.N2.Rows {
			if _, ok := r.Cells[name]; ok {
				n2Servers = append(n2Servers, name)
				break
			}
		}
	}
	common := commonScenarios(in.N2, n2Servers)
	inCommon := map[string]bool{}
	for _, id := range common {
		inCommon[id] = true
	}
	var rows [][]string
	for _, name := range servers {
		d, hasDef := defs[name]
		var med []float64
		for _, r := range in.N2.Rows {
			if c, ok := r.Cells[name]; ok && c.Status == results.StatusOK && inCommon[r.ID] {
				med = append(med, float64(c.Tokens))
			}
		}
		if !hasDef && len(med) == 0 {
			continue
		}
		// A server measured on n1 alone has no answer to add: its task total is not comparable and reads n/a.
		row := []string{name, fmt.Sprint(d), "n/a", "n/a", "n/a"}
		if len(med) > 0 {
			total := d + int(scenarios.Median(med))
			row = []string{name, fmt.Sprint(d), fmt.Sprintf("%.0f", scenarios.Median(med)), fmt.Sprint(total), usd(price, priced, total)}
		}
		if s, ok := sess[name]; ok {
			row = append(row, fmt.Sprint(s), usd(price, priced, s))
		} else {
			row = append(row, "n/a", "n/a")
		}
		rows = append(rows, row)
	}
	tbl(sb, []string{"Server", "definitions", "median answer", "one task", "$ one task", "ten-turn session", "$ session"}, rows)
}
