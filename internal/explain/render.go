package explain

import (
	"fmt"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
)

const maxExpr = 120

// renderSummary writes the summary, cut on whole lines to maxBytes.
func renderSummary(s summary, maxBytes int) string {
	head := fmt.Sprintf("Total cost %.2f, estimated rows %s", s.TotalCost, approx(s.Rows))
	if s.Analyze {
		head += ", actual rows " + approx(deref(s.ActualRows)) + ", execution " +
			format.Duration(time.Duration(deref(s.ExecutionMs)*float64(time.Millisecond)))
	}
	if s.Generic {
		head += ", generic plan ($n parameters)"
	}
	lines := []string{head}
	lines = append(lines, hypoLines(s.Hypo)...)
	for _, n := range s.Nodes {
		lines = append(lines, nodeLine(n, s.Analyze))
		for _, w := range n.Warnings {
			lines = append(lines, "   WARNING: "+w)
		}
	}
	return fit(lines, maxBytes)
}

// renderVerbose writes the TEXT plan, after the hypothetical comparison lines when there are indexes.
func renderVerbose(plan string, h *hypoSummary, maxBytes int) string {
	lines := append(hypoLines(h), strings.Split(plan, "\n")...)
	return fit(lines, maxBytes)
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

func hypoLines(h *hypoSummary) []string {
	if h == nil {
		return nil
	}
	change := 0.0
	if h.Before > 0 {
		change = (h.After - h.Before) / h.Before
	}
	used := "none (the planner did not choose them)"
	if len(h.Used) > 0 {
		used = strings.Join(h.Used, ", ")
	}
	return []string{
		fmt.Sprintf("With hypothetical indexes: total cost %.2f -> %.2f (%s)", h.Before, h.After, format.PercentChange(change)),
		"Hypothetical indexes used: " + used,
	}
}

// nodeName is the node type as Postgres's TEXT format names it.
func nodeName(n *hypo.Node) string {
	name := n.NodeType
	if n.ParallelAware {
		name = "Parallel " + name
	}
	if n.ScanDirection == "Backward" {
		name += " Backward"
	}
	return name
}

func nodeLine(s shown, analyze bool) string {
	n := s.Node
	var b strings.Builder
	b.WriteString("-> " + nodeName(n))
	if n.Index != "" {
		b.WriteString(" using " + n.Index)
	}
	if n.Relation != "" {
		b.WriteString(" on " + qualified(n))
	}
	if n.JoinType != "" {
		b.WriteString(" (" + n.JoinType + ")")
	}
	unit := "cost"
	if analyze {
		unit = "time"
	}
	fmt.Fprintf(&b, ": %s of %s, rows %s", format.Percent(s.Share), unit, approx(s.Rows))
	if s.Actual != nil {
		fmt.Fprintf(&b, " (actual %s)", approx(*s.Actual))
	}
	if n.RowsRemovedByFilter != nil {
		b.WriteString(", removed by filter " + approx(*n.RowsRemovedByFilter))
	}
	for _, e := range []struct{ label, expr string }{
		{"filter", n.Filter}, {"index cond", n.IndexCond}, {"hash cond", n.HashCond},
		{"merge cond", n.MergeCond}, {"join filter", n.JoinFilter},
	} {
		if e.expr != "" {
			b.WriteString(", " + e.label + ": " + format.Shorten(e.expr, maxExpr))
		}
	}
	return b.String()
}

// fit joins lines, or cuts them after the last whole line that fits with the truncation line.
func fit(lines []string, maxBytes int) string {
	out := strings.Join(lines, "\n")
	if len(out) <= maxBytes {
		return out
	}
	marker := fmt.Sprintf("-- truncated (%d-byte limit)", maxBytes)
	size := len(marker)
	kept := 0
	for _, l := range lines {
		if size+len(l)+1 > maxBytes {
			break
		}
		size += len(l) + 1
		kept++
	}
	return strings.Join(append(lines[:kept:kept], marker), "\n")
}
