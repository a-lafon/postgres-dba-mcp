package stats

import (
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// queryCap is the length at which a query text is shortened, whatever --max-cell says.
const queryCap = 500

// maskedText is the query text pg_stat_statements returns, instead of the text, to a role lacking pg_read_all_stats.
const maskedText = "<insufficient privilege>"

func renderTopQueries(res topQueries, cfg *config.Config) (string, error) {
	caps := make([]int, len(res.Columns))
	caps[len(caps)-1] = queryCap // the query text is the last column
	// Names go through CSVLine so that a newline in one cannot forge a line of the prefix.
	prefix := "-- by " + res.SortBy + ", database " + line(res.Database) + ", statistics since " + res.Since
	hidden := 0
	for _, r := range res.Rows {
		if string(r[len(r)-1]) == maskedText {
			hidden++
		}
	}
	// A role holding pg_read_all_stats never sees a masked text: a single one already means the privilege is missing.
	if hidden > 0 {
		count := "query texts"
		if hidden < len(res.Rows) {
			count = strconv.Itoa(hidden) + " of " + strconv.Itoa(len(res.Rows)) + " query texts"
		}
		prefix += "\n-- " + count + " hidden: grant pg_read_all_stats to " + line(res.User) + " to read them"
	}
	t := format.Table{
		Columns: res.Columns,
		Rows:    res.Rows,
		Caps:    caps,
		Limit:   len(res.Rows),
		More:    res.More,
		Prefix:  prefix,
	}
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, res.Elapsed)
}

// line escapes a name like a CSV field, without the trailing newline.
func line(name string) string { return strings.TrimSuffix(format.CSVLine(name), "\n") }
