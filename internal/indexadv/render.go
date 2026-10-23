package indexadv

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

const (
	maxQueryLines = 10  // workload query lines shown
	queryCap      = 120 // code points of a query text
	reasonCap     = 200 // code points of a skip reason
)

const budgetLine = "Stopped at the 30 s time budget: recommendations may be incomplete."

// render writes the answer as text of at most maxBytes bytes. When it does not fit, it drops the unserved-query
// lines, then the workload query lines, then the last table rows, and as a last resort cuts the text.
func render(res result, maxBytes int) string {
	rows := len(res.Picks)
	for n := rows; n >= 0; n-- {
		for _, drop := range []struct{ hints, queries bool }{{true, true}, {false, true}, {false, false}} {
			if s := compose(res, drop.hints, drop.queries, n); len(s) <= maxBytes {
				return s
			}
		}
	}
	s := compose(res, false, false, 0)
	s = s[:maxBytes]
	for !utf8.ValidString(s) {
		s = s[:len(s)-1]
	}
	return s
}

// compose builds the text with at most rows table rows, the unserved-query lines when withHints is set and the
// workload query lines when withQueries is set.
func compose(res result, withHints, withQueries bool, rows int) string {
	var b strings.Builder
	line := func(s string) { b.WriteString(s + "\n") }

	switch res.Kind {
	case noQueries:
		line("No index recommendation: no query could be analyzed.")
	case noColumns:
		line("No index recommendation: the queries have no column in WHERE, JOIN, HAVING or ORDER BY that an index could serve.")
	case noGain:
		if res.Partial { // the budget cut the first round: "no gain" is unproven
			line("No index recommendation: the time budget ended the search before every candidate was evaluated.")
		} else {
			line("No index recommendation: no candidate lowers the workload cost by at least 10%.")
		}
		line(fmt.Sprintf("Workload cost %s (%s, %s evaluated), %s", cost(res.Base),
			plural(len(res.Queries), "query", "queries"), plural(res.Candidates, "candidate", "candidates"), format.Duration(res.Elapsed)))
	default:
		var size int64
		for _, p := range res.Picks {
			size += p.size
		}
		line(fmt.Sprintf("Workload cost %s → %s (%s), %s, +%s, %s", cost(res.Base), cost(res.Final),
			format.PercentChange(change(res.Base, res.Final)), plural(len(res.Picks), "index", "indexes"),
			format.Size(size), format.Duration(res.Elapsed)))
		b.WriteString(indexTable(res, rows))
		parts := make([]string, len(res.Queries))
		for i, q := range res.Queries {
			parts[i] = q.Label + " " + queryChange(res.BaseQ[i], res.FinalQ[i])
		}
		line("Per query: " + strings.Join(parts, ", "))
		if len(res.Unserved) > 0 {
			if withHints {
				for _, u := range res.Unserved {
					line(unservedLine(res, u))
				}
			}
		} else if res.BelowShare {
			line("Not recommended: other indexes would each lower the workload cost by less than 1%.")
		}
	}
	if withQueries && res.Workload {
		for i, q := range res.Queries {
			if i == maxQueryLines {
				break
			}
			line(fmt.Sprintf("%s (calls %d, mean %s): %s", q.Label, q.Calls,
				format.Duration(time.Duration(q.MeanMs*float64(time.Millisecond))), format.Shorten(strings.Join(strings.Fields(q.SQL), " "), queryCap)))
		}
	}
	if len(res.Skipped) > 0 {
		parts := make([]string, len(res.Skipped))
		for i, s := range res.Skipped {
			parts[i] = s.Label + " (" + format.Shorten(s.Reason, reasonCap) + ")"
		}
		line("Skipped: " + strings.Join(parts, "; "))
	}
	if res.Partial {
		line(budgetLine)
	}
	return b.String()
}

// unservedLine renders an index left out of the selection that serves queries the picks leave unserved: their own
// cost change, then the workload cost change and the size.
func unservedLine(res result, u unserved) string {
	parts := make([]string, 0, len(u.qs)+2)
	for j, i := range u.qs {
		parts = append(parts, res.Queries[i].Label+" "+format.PercentChange(change(res.BaseQ[i], u.qcost[j])))
	}
	parts = append(parts, "workload "+format.PercentChange(u.change), format.Size(u.size))
	return "Not recommended, small workload share: " + u.cand.ddl() + " " + strings.Join(parts, ", ")
}

// indexTable renders the index table: each cost change is relative to the base workload cost.
func indexTable(res result, rows int) string {
	var b strings.Builder
	b.WriteString(format.CSVLine("#", "cost_change", "size", "ddl"))
	prev := res.Base
	for i, p := range res.Picks {
		if i < rows {
			b.WriteString(format.CSVLine(strconv.Itoa(i+1), format.PercentChange(change(res.Base, res.Base+p.cost-prev)),
				format.Size(p.size), p.cand.ddl()))
		}
		prev = p.cost
	}
	return b.String()
}

// cost renders a workload cost with one decimal, and as a rounded count (38B, 604M) from a million up, where
// the weighted costs of a pg_stat_statements workload would otherwise run to a dozen digits.
func cost(v float64) string {
	if v < 1e6 {
		return fmt.Sprintf("%.1f", v)
	}
	return format.Approx(int64(math.Round(v)))
}

// change is the relative variation from base to now, 0 when there was no cost.
func change(base, now float64) float64 {
	if base == 0 {
		return 0
	}
	return now/base - 1
}

// queryChange renders the cost change of one query; an unmoved cost reads "0.0% (unchanged)".
func queryChange(base, final float64) string {
	if c := change(base, final); format.PercentChange(c) != "+0.0%" {
		return format.PercentChange(c)
	}
	return format.Percent(0) + " (unchanged)"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
