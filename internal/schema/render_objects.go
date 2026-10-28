package schema

import (
	"strconv"

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

func renderObjects(res objectList, cfg *config.Config) (string, error) {
	t := format.Table{Limit: cfg.MaxRows, More: res.More}
	switch res.Type {
	case "table":
		t.Columns = []string{"name", "kind", "rows", "size"}
		for _, r := range res.Tables {
			var rows format.Cell
			if r.Rows != nil {
				rows = cell("~" + format.Approx(*r.Rows))
			}
			t.Rows = append(t.Rows, []format.Cell{cell(r.Name), cell(r.Kind), rows, cell(format.Size(r.Size))})
		}
	case "view":
		t.Columns = []string{"name", "kind"}
		for _, r := range res.Views {
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
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}
