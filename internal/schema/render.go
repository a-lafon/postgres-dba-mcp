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
	for _, r := range res.Rows {
		var tables format.Cell // NULL for system schemas
		if r.Tables != nil {
			tables = format.Cell(strconv.FormatInt(*r.Tables, 10))
		}
		t.Rows = append(t.Rows, []format.Cell{format.Cell(r.Name), format.Cell(r.Owner), format.Cell(r.Type), tables})
	}
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}
