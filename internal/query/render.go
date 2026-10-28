package query

import (
	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// render turns res into the tool text: a CSV table with its footer, or the command-tag footer.
func render(res result, cfg *config.Config, maxRows, maxCell int) (string, error) {
	if len(res.Columns) == 0 {
		return format.RenderTag(res.Tag, res.Elapsed), nil
	}
	t := format.Table{Columns: res.Columns, Rows: res.Rows, More: res.More, Limit: maxRows, CellLimit: cfg.MaxCellLimit}
	return format.Render(t, cfg.MaxBytes, maxCell, res.Elapsed)
}
