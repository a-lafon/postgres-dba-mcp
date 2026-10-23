package health

import (
	"fmt"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// template replaces the partition's table name in an index name by "<partition>", so that the same index of every
// partition of a tree has the same text. A name that does not contain the table name is returned unchanged.
func template(name, table string) string { return strings.ReplaceAll(name, table, "<partition>") }

func duplicateText(d dupPair) string {
	return fmt.Sprintf("duplicate: %s.%s (%s) covered by %s.%s (%s)",
		d.Index.Schema, d.Index.Name, strings.Join(d.Index.Columns, ", "),
		d.Covering.Schema, d.Covering.Name, strings.Join(d.Covering.Columns, ", "))
}

// duplicateItems makes one item per duplicate, except that duplicates of one partition tree that differ only by the
// partition name become one item when they span at least two partitions. The group sits at the place of its first member.
func duplicateItems(dups []dupPair) []item {
	type group struct {
		first  dupPair
		tables int
	}
	type key struct{ root, index, cols, cover, coverCols string }
	var slots []*group // one per output item, in order
	byKey := map[key]*group{}
	for _, d := range dups {
		if d.Index.Root == "" {
			slots = append(slots, &group{first: d, tables: 1})
			continue
		}
		k := key{d.Index.Root, template(d.Index.Name, d.Index.Table), strings.Join(d.Index.Columns, ", "),
			template(d.Covering.Name, d.Covering.Table), strings.Join(d.Covering.Columns, ", ")}
		g := byKey[k]
		if g == nil {
			g = &group{first: d}
			byKey[k] = g
			slots = append(slots, g)
		}
		g.tables++
	}
	out := make([]item, len(slots))
	for i, g := range slots {
		d := g.first
		text := duplicateText(d)
		if g.tables > 1 {
			text = fmt.Sprintf("duplicate on %d partitions of %s: %s (%s) covered by %s (%s); e.g. %s",
				g.tables, d.Index.Root, template(d.Index.Name, d.Index.Table), strings.Join(d.Index.Columns, ", "),
				template(d.Covering.Name, d.Covering.Table), strings.Join(d.Covering.Columns, ", "), d.Index.Table)
		}
		out[i] = item{"duplicate", text}
	}
	return out
}

// unusedEntries lists the rarely used indexes in the given order (largest first). The same index of several
// partitions of one tree is one entry with the summed size, at the place of the first.
func unusedEntries(unused []unusedIndex) []string {
	type entry struct {
		u     unusedIndex
		count int
		size  int64
		scans int64
	}
	var entries []*entry
	byKey := map[[3]string]*entry{}
	for _, u := range unused {
		if u.Root != "" {
			k := [3]string{u.Root, u.Schema, template(u.Name, u.Table)}
			if e := byKey[k]; e != nil {
				e.count++
				e.size += u.Size
				e.scans += u.Scans
				continue
			}
			e := &entry{u, 1, u.Size, u.Scans}
			byKey[k] = e
			entries = append(entries, e)
			continue
		}
		entries = append(entries, &entry{u, 1, u.Size, u.Scans})
	}
	list := make([]string, len(entries))
	for i, e := range entries {
		scans := fmt.Sprintf("%d scans", e.scans)
		if e.scans == 1 {
			scans = "1 scan"
		}
		list[i] = fmt.Sprintf("%s.%s %s %s", e.u.Schema, e.u.Name, format.Size(e.size), scans)
		if e.count > 1 {
			list[i] = fmt.Sprintf("%s.%s (%d partitions of %s) %s %s", e.u.Schema, template(e.u.Name, e.u.Table), e.count, e.u.Root, format.Size(e.size), scans)
		}
	}
	return list
}
