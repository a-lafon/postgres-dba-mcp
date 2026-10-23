package schema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// definitionCells is how many --max-cell widths a view definition may take before it is shortened.
const definitionCells = 10

// fkActions names the pg_constraint action codes; "a" (no action) is not shown.
var fkActions = map[string]string{"r": "RESTRICT", "c": "CASCADE", "n": "SET NULL", "d": "SET DEFAULT"}

func renderDetails(d objectDetails, cfg *config.Config) string {
	var lines []string
	switch d.Type {
	case "table":
		lines = tableLines(d)
	case "view":
		lines = viewLines(d, cfg.MaxCell*definitionCells)
	case "sequence":
		lines = []string{sequenceLine(d)}
	default:
		lines = []string{extensionLine(d)}
	}
	return fitLines(lines, cfg.MaxBytes)
}

// fitLines joins lines with newlines. Over the budget, it keeps the whole lines that fit and ends with the truncation line.
func fitLines(lines []string, maxBytes int) string {
	if full := strings.Join(lines, "\n"); len(full) <= maxBytes {
		return full
	}
	trailer := fmt.Sprintf("-- truncated (%d-byte limit)", maxBytes)
	size, kept := len(trailer), 0
	for _, l := range lines {
		if size+len(l)+1 > maxBytes {
			break
		}
		size += len(l) + 1
		kept++
	}
	return strings.Join(append(lines[:kept:kept], trailer), "\n")
}

// oneLine keeps a comment on its line.
var oneLine = strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ")

func commentSuffix(c *string) string {
	if c == nil {
		return ""
	}
	return " -- " + oneLine.Replace(*c)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// qualify writes name with its schema only when the schema differs from home.
func qualify(home, schema, name string) string {
	if schema == home {
		return name
	}
	return schema + "." + name
}

func rowsText(rows *int64) string {
	if rows == nil {
		return "rows unknown"
	}
	return "~" + format.Approx(*rows) + " rows"
}

func tableLines(d objectDetails) []string {
	r := d.Relation
	var h strings.Builder
	fmt.Fprintf(&h, "table %s.%s", d.Schema, d.Name)
	if r.ParentName != nil {
		fmt.Fprintf(&h, " (partition of %s %s)", qualify(d.Schema, deref(r.ParentSchema), *r.ParentName), deref(r.Bound))
	}
	if r.PartitionKey != nil {
		fmt.Fprintf(&h, " (partitioned by %s)", *r.PartitionKey)
	}
	fmt.Fprintf(&h, ": %s, %s", rowsText(r.Rows), format.Size(r.TableSize))
	if r.IndexSize > 0 {
		fmt.Fprintf(&h, " + %s indexes", format.Size(r.IndexSize))
	}
	h.WriteString(commentSuffix(r.Comment))
	lines := []string{h.String()}

	pk := map[string]bool{}
	fk := map[string]constraint{}
	var multiFK, checks []string
	for _, c := range d.Constraints {
		switch {
		case c.Type == "p" && len(c.Columns) == 1:
			pk[c.Columns[0]] = true
		case c.Type == "f" && len(c.Columns) == 1:
			fk[c.Columns[0]] = c
		case c.Type == "f":
			multiFK = append(multiFK, c.Name+" "+c.Def)
		case c.Type == "c":
			checks = append(checks, c.Name+" "+c.Def)
		}
	}
	for _, c := range d.Columns {
		lines = append(lines, "  "+columnLine(d, c, pk[c.Name], fk[c.Name]))
	}

	lines = appendSection(lines, "foreign keys:", multiFK)
	var idx []string
	for _, i := range d.Indexes {
		_, def, _ := strings.Cut(i.Def, " USING ")
		if i.Primary {
			def += " PK"
		}
		if i.Unique && !i.Primary {
			def += " UNIQUE"
		}
		if !i.Valid {
			def += " INVALID"
		}
		idx = append(idx, i.Name+" "+def)
	}
	lines = appendSection(lines, "indexes:", idx)
	lines = appendSection(lines, "checks:", checks)
	var parts []string
	for _, p := range d.Partitions {
		parts = append(parts, qualify(d.Schema, p.Schema, p.Name)+" "+p.Bound)
	}
	lines = appendSection(lines, "partitions:", parts)
	if len(d.ReferencedBy) > 0 {
		refs := make([]string, len(d.ReferencedBy))
		for i, ref := range d.ReferencedBy {
			t := qualify(d.Schema, ref.Schema, ref.Table)
			if len(ref.Columns) == 1 {
				refs[i] = t + "." + ref.Columns[0]
			} else {
				refs[i] = t + "(" + strings.Join(ref.Columns, ", ") + ")"
			}
		}
		lines = append(lines, "referenced by: "+strings.Join(refs, ", "))
	}
	var trg []string
	for _, t := range d.Triggers {
		trg = append(trg, t.Name+" "+triggerText(t.Type))
	}
	return appendSection(lines, "triggers:", trg)
}

// appendSection adds a section title and its indented items, nothing when there are no items.
func appendSection(lines []string, title string, items []string) []string {
	if len(items) == 0 {
		return lines
	}
	lines = append(lines, title)
	for _, it := range items {
		lines = append(lines, "  "+it)
	}
	return lines
}

// columnLine writes name, type, PK, NOT NULL, default or generation, single-column foreign key and comment.
func columnLine(d objectDetails, c column, pk bool, fk constraint) string {
	parts := []string{c.Name, c.Type}
	if pk {
		parts = append(parts, "PK")
	} else if c.NotNull {
		parts = append(parts, "NOT NULL")
	}
	switch {
	case c.Identity == "a":
		parts = append(parts, "GENERATED ALWAYS AS IDENTITY")
	case c.Identity == "d":
		parts = append(parts, "GENERATED BY DEFAULT AS IDENTITY")
	case c.Generated == "s" && c.Default != nil:
		parts = append(parts, "GENERATED ALWAYS AS ("+*c.Default+") STORED")
	case c.Generated == "v" && c.Default != nil:
		parts = append(parts, "GENERATED ALWAYS AS ("+*c.Default+") VIRTUAL")
	case c.Default != nil:
		parts = append(parts, "DEFAULT "+*c.Default)
	}
	if fk.Name != "" && fk.RefTable != nil && len(fk.RefColumns) == 1 {
		s := "→ " + qualify(d.Schema, deref(fk.RefSchema), *fk.RefTable) + "(" + fk.RefColumns[0] + ")"
		if a, ok := fkActions[fk.OnDelete]; ok {
			s += " ON DELETE " + a
		}
		if a, ok := fkActions[fk.OnUpdate]; ok {
			s += " ON UPDATE " + a
		}
		parts = append(parts, s)
	}
	return strings.Join(parts, " ") + commentSuffix(c.Comment)
}

// triggerText decodes pg_trigger.tgtype: timing, events and level.
func triggerText(typ int32) string {
	timing := "AFTER"
	switch {
	case typ&2 != 0:
		timing = "BEFORE"
	case typ&64 != 0:
		timing = "INSTEAD OF"
	}
	var events []string
	for _, e := range []struct {
		bit  int32
		name string
	}{{4, "INSERT"}, {8, "DELETE"}, {16, "UPDATE"}, {32, "TRUNCATE"}} {
		if typ&e.bit != 0 {
			events = append(events, e.name)
		}
	}
	level := "STATEMENT"
	if typ&1 != 0 {
		level = "ROW"
	}
	return timing + " " + strings.Join(events, " OR ") + " FOR EACH " + level
}

func viewLines(d objectDetails, maxDefinition int) []string {
	r := d.Relation
	h := fmt.Sprintf("view %s.%s", d.Schema, d.Name)
	if r.Kind == "m" {
		h = fmt.Sprintf("materialized view %s.%s: %s, %s", d.Schema, d.Name, rowsText(r.Rows), format.Size(r.TableSize))
	}
	lines := []string{h + commentSuffix(r.Comment)}
	for _, c := range d.Columns {
		lines = append(lines, "  "+c.Name+" "+c.Type)
	}
	lines = append(lines, "definition:")
	for _, l := range strings.Split(format.Shorten(deref(r.Definition), maxDefinition), "\n") {
		lines = append(lines, "  "+l)
	}
	return lines
}

func sequenceLine(d objectDetails) string {
	s := d.Sequence
	cycle := "no cycle"
	if s.Cycle {
		cycle = "cycle"
	}
	last := "last value unknown"
	if s.LastValue != nil {
		last = "last value " + strconv.FormatInt(*s.LastValue, 10)
		if used, ok := sequenceUsed(s.LastValue, s.Min, s.Max, s.Increment); ok {
			last += " (" + used + " used)"
		}
	}
	line := fmt.Sprintf("sequence %s.%s: %s, start %d, increment %d, min %d, max %d, %s, %s",
		d.Schema, d.Name, s.DataType, s.Start, s.Increment, s.Min, s.Max, cycle, last)
	if s.OwnerTable != nil && s.OwnerColumn != nil {
		line += ", owned by " + qualify(d.Schema, deref(s.OwnerSchema), *s.OwnerTable) + "." + *s.OwnerColumn
	}
	return line
}

func extensionLine(d objectDetails) string {
	e := d.Extension
	return fmt.Sprintf("extension %s: version %s, schema %s, relocatable %s", d.Name, e.Version, e.Schema, yesNo(e.Relocatable)) +
		commentSuffix(e.Comment)
}
