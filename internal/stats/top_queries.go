// Package stats holds the tools that read server statistics, such as get_top_queries.
package stats

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

//go:embed top_queries_time.sql
var timeSQL string

//go:embed top_queries_resources.sql
var resourcesSQL string

//go:embed top_queries_context.sql
var contextSQL string

//go:embed top_queries_context_legacy.sql
var contextLegacySQL string

const (
	defaultLimit = 10
	maxLimit     = 100
)

// Errors returned when pg_stat_statements cannot be read.
var (
	errNotInstalled = errors.New("pg_stat_statements is not installed in this database: run CREATE EXTENSION pg_stat_statements; it also requires shared_preload_libraries = 'pg_stat_statements'")
	errNotLoaded    = errors.New("pg_stat_statements is installed but not loaded: add it to shared_preload_libraries and restart PostgreSQL")
)

const inputSchema = `{"type":"object","properties":{` +
	`"sort_by":{"type":"string","enum":["resources","total_time","mean_time"],"default":"resources",` +
	`"description":"Ranking criteria: 'total_time' for total execution time or 'mean_time' for mean execution time per call, or 'resources' for resource-intensive queries"},` +
	`"limit":{"type":"integer","minimum":1,"maximum":100,"default":10,"description":"Number of queries to return"}}}`

// statsDB is what the tool needs from the database: version and extensions, and read-only transactions.
type statsDB interface {
	Info(ctx context.Context) (db.Info, error)
	ReadOnly(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}

// args are the validated tool arguments.
type args struct {
	SortBy string
	Limit  int
}

// topQueries is the computed result of get_top_queries.
type topQueries struct {
	SortBy, Database, Since string
	Columns                 []string
	Rows                    [][]format.Cell
	More                    bool
	Elapsed                 time.Duration
}

// parseArgs decodes and validates the raw tool arguments, applying the defaults.
func parseArgs(raw json.RawMessage) (args, error) {
	var in struct {
		SortBy *string `json:"sort_by"`
		Limit  *int    `json:"limit"`
	}
	if err := server.Decode(raw, &in); err != nil {
		return args{}, err
	}
	a := args{SortBy: "resources", Limit: defaultLimit}
	if in.SortBy != nil {
		a.SortBy = *in.SortBy
	}
	if in.Limit != nil {
		a.Limit = *in.Limit
	}
	switch a.SortBy {
	case "resources", "total_time", "mean_time":
	default:
		return args{}, fmt.Errorf("invalid sort_by %q: use resources, total_time or mean_time", a.SortBy)
	}
	if a.Limit < 1 || a.Limit > maxLimit {
		return args{}, fmt.Errorf("invalid limit %d: must be between 1 and %d", a.Limit, maxLimit)
	}
	return a, nil
}

// querySQL returns the statement for a validated sort_by. The ORDER BY column comes from this switch only.
func querySQL(sortBy string) string {
	switch sortBy {
	case "total_time":
		return strings.Replace(timeSQL, "{{order}}", "total_exec_time", 1)
	case "mean_time":
		return strings.Replace(timeSQL, "{{order}}", "mean_exec_time", 1)
	default:
		return resourcesSQL
	}
}

func topQueriesTool(cfg *config.Config, d statsDB) server.Tool {
	return server.Tool{
		Group: "stats",
		Title: "Top queries",
		Def: &mcp.Tool{
			Name:        "get_top_queries",
			Description: "Reports the slowest or most resource-intensive queries using data from the 'pg_stat_statements' extension. Only statements of the current database are reported.",
			InputSchema: json.RawMessage(inputSchema),
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			a, err := parseArgs(raw)
			if err != nil {
				return "", err
			}
			res, err := getTopQueries(ctx, d, a)
			if err != nil {
				return "", err
			}
			return renderTopQueries(res, cfg)
		},
	}
}

// getTopQueries reads limit+1 rows to learn whether more exist.
func getTopQueries(ctx context.Context, d statsDB, a args) (topQueries, error) {
	info, err := d.Info(ctx)
	if err != nil {
		return topQueries{}, err
	}
	if _, ok := info.Extensions["pg_stat_statements"]; !ok {
		return topQueries{}, errNotInstalled
	}
	res := topQueries{SortBy: a.SortBy}
	start := time.Now()
	err = d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, contextFor(info.Extensions["pg_stat_statements"])).Scan(&res.Database, &res.Since); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, querySQL(a.SortBy), a.Limit+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for _, f := range rows.FieldDescriptions() {
			res.Columns = append(res.Columns, f.Name)
		}
		for rows.Next() {
			raw := rows.RawValues()
			cells := make([]format.Cell, len(raw))
			for i, v := range raw {
				cells[i] = bytes.Clone(v) // nil stays NULL
			}
			res.Rows = append(res.Rows, cells)
		}
		return rows.Err()
	})
	if err != nil {
		return topQueries{}, mapReadError(err)
	}
	if len(res.Rows) > a.Limit {
		res.Rows, res.More = res.Rows[:a.Limit], true
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// mapReadError turns the "not preloaded" refusal of pg_stat_statements (SQLSTATE 55000) into its own error.
func mapReadError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "55000" && strings.Contains(pe.Message, "shared_preload_libraries") {
		return errNotLoaded
	}
	return fmt.Errorf("get top queries: %w", err)
}

// contextFor returns the context statement for an extension version. pg_stat_statements_info exists from
// extension 1.9; before, or for a version that cannot be read, the reset time is reported as unknown.
func contextFor(extVersion string) string {
	major, minor, ok := strings.Cut(extVersion, ".")
	maj, err1 := strconv.Atoi(major)
	minor2, err2 := strconv.Atoi(minor)
	if !ok || err1 != nil || err2 != nil || maj < 1 || (maj == 1 && minor2 < 9) {
		return contextLegacySQL
	}
	return contextSQL
}
