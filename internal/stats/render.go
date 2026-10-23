package stats

import (
	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// queryCap is the length at which a query text is shortened, whatever --max-cell says.
const queryCap = 500

func renderTopQueries(res topQueries, cfg *config.Config) (string, error) {
	caps := make([]int, len(res.Columns))
	caps[len(caps)-1] = queryCap // the query text is the last column
	t := format.Table{
		Columns: res.Columns,
		Rows:    res.Rows,
		Caps:    caps,
		Limit:   len(res.Rows),
		More:    res.More,
		Prefix:  "-- by " + res.SortBy + ", database " + res.Database + ", statistics since " + res.Since,
	}
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}
