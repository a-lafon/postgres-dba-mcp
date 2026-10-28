package schema

import (
	"strconv"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

func renderSchemas(res schemaList, cfg *config.Config) (string, error) {
	t := format.Table{
		Columns: []string{"schema_name", "owner", "type", "tables"},
		Limit:   cfg.MaxRows,
		More:    res.More,
	}
	var hidden []string
	var role string
	for _, r := range res.Rows {
		if !r.Readable {
			hidden, role = append(hidden, r.Name), r.Role
		}
		var tables format.Cell // NULL for system schemas
		if r.Tables != nil {
			tables = format.Cell(strconv.FormatInt(*r.Tables, 10))
		}
		t.Rows = append(t.Rows, []format.Cell{format.Cell(r.Name), format.Cell(r.Owner), format.Cell(r.Type), tables})
	}
	t.Prefix = unreadableNote("schema", role, hidden)
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}
