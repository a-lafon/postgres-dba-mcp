package health

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

const (
	nameWidth   = 13
	maxPerKind  = 10
	maxUnusedIx = 10
)

// item is one problem; kind groups the items that share the per-kind cap.
type item struct{ kind, text string }

// section is the report part of one check: problem items, OK with optional facts, or skipped.
type section struct {
	name  string
	facts string
	items []item
	skip  string // why the check could not run here; replaces the OK and problem lines
}

// text renders the section, applying the per-kind cap.
func (s section) text() string {
	if s.skip != "" {
		return fmt.Sprintf("%-*s%s\n", nameWidth, s.name, "skipped: "+s.skip)
	}
	pad := strings.Repeat(" ", nameWidth)
	items := capItems(s.items)
	if len(items) == 0 {
		line := "OK"
		if s.facts != "" {
			line += " (" + s.facts + ")"
		}
		return fmt.Sprintf("%-*s%s\n", nameWidth, s.name, line)
	}
	var b strings.Builder
	for i, it := range items {
		if i == 0 {
			fmt.Fprintf(&b, "%-*s", nameWidth, s.name)
		} else {
			b.WriteString(pad)
		}
		b.WriteString("! " + it.text + "\n")
	}
	return b.String()
}

// capItems keeps the first maxPerKind items of each kind and adds "… and <n> more <kind>" after them.
// Kinds keep the order of their first item.
func capItems(items []item) []item {
	var kinds []string
	byKind := map[string][]item{}
	for _, it := range items {
		if _, ok := byKind[it.kind]; !ok {
			kinds = append(kinds, it.kind)
		}
		byKind[it.kind] = append(byKind[it.kind], it)
	}
	var out []item
	for _, k := range kinds {
		list := byKind[k]
		if len(list) > maxPerKind {
			more := item{k, fmt.Sprintf("… and %d more %s", len(list)-maxPerKind, k)}
			list = append(list[:maxPerKind:maxPerKind], more)
		}
		out = append(out, list...)
	}
	return out
}

// renderReport joins the sections. When maxBytes would be exceeded it stops at a section boundary and ends
// with a truncation line.
func renderReport(sections []section, maxBytes int) string {
	texts := make([]string, len(sections))
	total := 0
	for i, s := range sections {
		texts[i] = s.text()
		total += len(texts[i])
	}
	if total <= maxBytes {
		return strings.Join(texts, "")
	}
	trunc := fmt.Sprintf("-- truncated (%d-byte limit)\n", maxBytes)
	var b strings.Builder
	for _, t := range texts {
		if b.Len()+len(t)+len(trunc) > maxBytes {
			break
		}
		b.WriteString(t)
	}
	return b.String() + trunc
}

func (r indexResult) render() section {
	var s section
	for _, ix := range r.Invalid {
		s.items = append(s.items, item{"invalid", fmt.Sprintf("invalid: %s.%s on %s.%s", ix.Schema, ix.Name, ix.Schema, ix.Table)})
	}
	s.items = append(s.items, duplicateItems(r.Duplicates)...)
	for _, b := range r.Bloated {
		s.items = append(s.items, item{"bloated", fmt.Sprintf("bloated: %s.%s on %s.%s, ~%s wasted of %s",
			b.Schema, b.Name, b.Schema, b.Table, format.Size(b.Wasted), format.Size(b.Size))})
	}
	if len(r.Unused) > 0 {
		var total int64
		for _, u := range r.Unused {
			total += u.Size
		}
		list := unusedEntries(r.Unused)
		if len(list) > maxUnusedIx {
			list = append(list[:maxUnusedIx:maxUnusedIx], "…")
		}
		days := fmt.Sprintf("%d days", r.StatsDays)
		if r.StatsDays == 1 {
			days = "1 day"
		}
		s.items = append(s.items, item{"rarely used", fmt.Sprintf("rarely used (<= %d scans, over up to %s since %s): %d indexes, %s: %s",
			unusedMaxScans, days, r.StatsSince.UTC().Format("2006-01-02 15:04 UTC"), len(r.Unused), format.Size(total), strings.Join(list, ", "))})
	}
	return s
}

func (r connectionResult) render() section {
	var s section
	if r.Total > maxConnections {
		s.items = append(s.items, item{"connections", fmt.Sprintf("%s connections (threshold %s)", format.Approx(r.Total), format.Approx(maxConnections))})
	}
	if r.IdleInTx > maxIdleInTx {
		s.items = append(s.items, item{"idle in transaction", fmt.Sprintf("%s idle in transaction (threshold %s)", format.Approx(r.IdleInTx), format.Approx(maxIdleInTx))})
	}
	s.facts = fmt.Sprintf("cluster: %s total, %s idle in transaction", format.Approx(r.Total), format.Approx(r.IdleInTx))
	return s
}

func (r vacuumResult) render() section {
	var s section
	for _, w := range r.Wraparound {
		s.items = append(s.items, item{"wraparound", fmt.Sprintf("wraparound: %s.%s has %s transactions left (threshold %s)",
			w.Schema, w.Table, format.Approx(w.Left), format.Approx(xidThreshold))})
	}
	for _, d := range r.Dead {
		last := "never"
		if d.LastVacuum != nil {
			last = d.LastVacuum.UTC().Format("2006-01-02 15:04 UTC")
		}
		s.items = append(s.items, item{"dead rows", fmt.Sprintf("dead rows: %s.%s %s (%s dead, %s live), last vacuum %s",
			d.Schema, d.Table, format.Percent(float64(d.Dead)/float64(d.Dead+d.Live)),
			format.Approx(d.Dead), format.Approx(d.Live), last)})
	}
	return s
}

func (r sequenceResult) render() section {
	var s section
	for _, q := range r.Over {
		what := q.DataType
		if q.OwnerCol != "" {
			what = fmt.Sprintf("%s.%s.%s, %s", q.OwnerSchema, q.OwnerTable, q.OwnerCol, q.DataType)
		}
		s.items = append(s.items, item{"sequence", fmt.Sprintf("%s.%s (%s): %s used, %s left",
			q.Schema, q.Name, what, format.Percent(q.Used), format.Approx(q.Left))})
	}
	if r.Readable == 0 {
		s.facts = "no sequences"
	}
	return s
}

func (r replicationResult) render() section {
	var s section
	if r.Replica {
		if !r.Streaming {
			s.items = append(s.items, item{"replica", "replica is not streaming from its primary"})
		} else {
			s.facts = "replica, streaming"
			switch {
			case r.Lag == nil:
			case *r.Lag == 0:
				s.facts += ", lag 0"
			default:
				s.facts += ", lag " + format.Duration(time.Duration(*r.Lag*float64(time.Second)))
			}
		}
	} else {
		s.facts = fmt.Sprintf("primary, %d replicas, %d slots", r.Replicas, r.Slots)
	}
	for _, sl := range r.Inactive {
		text := fmt.Sprintf("inactive slot %s (%s)", sl.Name, sl.Type)
		if sl.Type == "logical" {
			text += ", database " + sl.Database
		}
		s.items = append(s.items, item{"inactive slot", text})
	}
	return s
}

// culpritText is ": <table> <pct> of disk reads, <n> seq scans; ..." or "" when no table is named.
func (r bufferResult) culpritText() string {
	parts := make([]string, len(r.Culprits))
	for i, c := range r.Culprits {
		scans := "scans"
		if c.SeqScans == 1 {
			scans = "scan"
		}
		parts[i] = fmt.Sprintf("%s %d%% of disk reads, %s seq %s", c.Table, int(c.Share*100+1e-9), format.Approx(c.SeqScans), scans)
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, "; ")
}

func (r bufferResult) render() section {
	var s section
	limit := strconv.FormatFloat(cacheHitThreshold*100, 'f', -1, 64) + "%"
	if r.Tables != nil && *r.Tables < cacheHitThreshold {
		s.items = append(s.items, item{"table cache", fmt.Sprintf("table cache hit %s (below %s)%s", format.Percent(*r.Tables), limit, r.culpritText())})
	}
	if r.Indexes != nil && *r.Indexes < cacheHitThreshold {
		s.items = append(s.items, item{"index cache", fmt.Sprintf("index cache hit %s (below %s)", format.Percent(*r.Indexes), limit)})
	}
	var parts []string
	if r.Tables != nil {
		parts = append(parts, format.Percent(*r.Tables)+" tables")
	}
	if r.Indexes != nil {
		parts = append(parts, format.Percent(*r.Indexes)+" indexes")
	}
	if len(parts) == 0 {
		s.facts = "no cache statistics yet"
	} else {
		s.facts = "cache hit " + strings.Join(parts, ", ")
	}
	return s
}

func (r constraintResult) render() section {
	var s section
	for _, c := range r.Invalid {
		text := fmt.Sprintf("%s.%s.%s", c.Schema, c.Table, c.Name)
		if c.RefTable != "" {
			text += fmt.Sprintf(" referencing %s.%s", c.RefSchema, c.RefTable)
		}
		s.items = append(s.items, item{"constraint", text + " is NOT VALID"})
	}
	return s
}
