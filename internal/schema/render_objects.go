package schema

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// cell makes a text cell; an absent value stays NULL.
func cell(s string) format.Cell { return format.Cell(s) }

func int64Cell(n *int64) format.Cell {
	if n == nil {
		return nil
	}
	return cell(strconv.FormatInt(*n, 10))
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

// maxUnreadableNames caps the names listed in the unreadable note.
const maxUnreadableNames = 5

// unreadableNote is the Prefix line naming the listed objects the role cannot read, or "" when all are readable.
func unreadableNote(noun, role string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	label := noun + "s"
	if len(names) == 1 {
		label = noun
	}
	// Names belong to other roles: escape them like a column name so none can add a line (a forged footer).
	esc := func(s string) string { return strings.TrimSuffix(format.CSVLine(s), "\n") }
	shown := names
	if len(names) > maxUnreadableNames {
		shown = names[:maxUnreadableNames]
	}
	list := make([]string, len(shown))
	for i, n := range shown {
		list[i] = esc(n)
	}
	out := strings.Join(list, ", ")
	if len(names) > maxUnreadableNames {
		out += ", …"
	}
	return fmt.Sprintf("-- %d %s not readable by %s: %s", len(names), label, esc(role), out)
}

func renderObjects(res objectList, cfg *config.Config) (string, error) {
	t := format.Table{Limit: cfg.MaxRows, More: res.More}
	var hidden []string
	var role string
	switch res.Type {
	case "table":
		t.Columns = []string{"name", "kind", "rows", "size"}
		for _, r := range res.Tables {
			if !r.Readable {
				hidden, role = append(hidden, r.Name), r.Role
			}
			var rows format.Cell
			if r.Rows != nil {
				rows = cell("~" + format.Approx(*r.Rows))
			}
			t.Rows = append(t.Rows, []format.Cell{cell(r.Name), cell(r.Kind), rows, cell(format.Size(r.Size))})
		}
	case "view":
		t.Columns = []string{"name", "kind"}
		for _, r := range res.Views {
			if !r.Readable {
				hidden, role = append(hidden, r.Name), r.Role
			}
			t.Rows = append(t.Rows, []format.Cell{cell(r.Name), cell(r.Kind)})
		}
	case "sequence":
		t.Columns = []string{"name", "data_type", "last_value", "max_value", "used"}
		for _, r := range res.Sequences {
			var used format.Cell
			if u, ok := sequenceUsed(r.LastValue, r.MinValue, r.MaxValue, r.Increment); ok {
				used = cell(u)
			}
			max := r.MaxValue
			t.Rows = append(t.Rows, []format.Cell{cell(r.Name), cell(r.DataType), int64Cell(r.LastValue), int64Cell(&max), used})
		}
	case "extension":
		t.Columns = []string{"name", "version", "schema", "relocatable"}
		for _, r := range res.Extensions {
			t.Rows = append(t.Rows, []format.Cell{cell(r.Name), cell(r.Version), cell(r.Schema), cell(yesNo(r.Relocatable))})
		}
	}
	t.Prefix = unreadableNote(res.Type, role, hidden)
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}
