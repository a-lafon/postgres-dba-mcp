package indexadv

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

//go:embed workload.sql
var workloadSQL string

// Workload thresholds of crystaldba 0.3.0. Variables, so that tests can lower them.
var (
	minCalls  int64   = 50
	minMeanMs float64 = 5
)

var errNoStatements = errors.New("pg_stat_statements is not installed in this database: run CREATE EXTENSION pg_stat_statements; it also requires shared_preload_libraries = 'pg_stat_statements'")

// query is one statement of the analyzed workload.
type query struct {
	Label  string  // Q1, Q2, ...
	SQL    string  // as given, never lowercased
	Weight float64 // 1, or total_exec_time for the workload tool
	Calls  int64   // workload tool only
	MeanMs float64 // workload tool only

	idx     int
	rels    []safesql.RelRef
	tables  []*table // distinct resolved tables
	exact   bool     // every relation is a resolved table, so its plan depends on the indexes of q.tables only
	failure error    // why EXPLAIN failed on the empty set
}

// hasTable reports whether q references t.
func (q *query) hasTable(t *table) bool {
	for _, x := range q.tables {
		if x == t {
			return true
		}
	}
	return false
}

// skip is a query left out of the analysis, with the reason.
type skip struct{ Label, Reason string }

// precheck verifies the extensions the analysis needs.
func precheck(info db.Info, workload bool) error {
	if !hypo.Available(info) {
		return fmt.Errorf("index analysis needs the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-%d-hypopg package)", info.VersionNum/10000)
	}
	if !workload {
		return nil
	}
	v, ok := info.Extensions["pg_stat_statements"]
	if !ok {
		return errNoStatements
	}
	// The workload query filters on toplevel, a column of extension 1.9 (PostgreSQL 14); a server upgraded with
	// pg_upgrade keeps the old extension version until ALTER EXTENSION ... UPDATE. A version that cannot be read is
	// refused like an old one.
	if !db.AtLeast(v, 1, 9) {
		return fmt.Errorf("analyze_workload_indexes needs pg_stat_statements 1.9 or later (installed: %s): run ALTER EXTENSION pg_stat_statements UPDATE", v)
	}
	return nil
}

// emptyWorkload is the text returned when no statement reaches the thresholds.
func emptyWorkload() string {
	return "No query in pg_stat_statements for this database has calls >= " + strconv.FormatInt(minCalls, 10) +
		" and mean time >= " + strconv.FormatFloat(minMeanMs, 'f', -1, 64) + " ms."
}

// loadWorkload reads the frequent, slow statements of the current database, heaviest first.
func loadWorkload(ctx context.Context, tx pgx.Tx) ([]*query, error) {
	rows, err := tx.Query(ctx, workloadSQL, minCalls, minMeanMs)
	if err != nil {
		return nil, statsError(err)
	}
	defer rows.Close()
	var qs []*query
	for rows.Next() {
		var id *int64
		q := &query{}
		var total float64
		if err := rows.Scan(&id, &q.SQL, &q.Calls, &total, &q.MeanMs); err != nil {
			return nil, fmt.Errorf("read pg_stat_statements: %w", err)
		}
		q.Weight = total
		qs = append(qs, q)
	}
	if err := rows.Err(); err != nil {
		return nil, statsError(err)
	}
	return qs, nil
}

// intake keeps the queries that are a single SELECT, labels them Q1, Q2, ... by input position and reports the
// others. In restricted mode the safesql policy applies; in unrestricted mode only the statement kind does.
func intake(cfg *config.Config, policy *safesql.Policy, qs []*query) (kept []*query, skipped []skip) {
	for i, q := range qs {
		q.Label = "Q" + strconv.Itoa(i+1)
		// pg_stat_statements writes a typed literal as `interval $1`: fix it before the check, so that the
		// checked text is the text that EXPLAIN runs.
		q.SQL = safesql.RewriteTypedParams(q.SQL)
		if err := accept(cfg, policy, q.SQL); err != nil {
			skipped = append(skipped, skip{q.Label, err.Error()})
			continue
		}
		q.idx = len(kept)
		kept = append(kept, q)
	}
	return kept, skipped
}

var errNotSelect = errors.New("not a single SELECT")

func accept(cfg *config.Config, policy *safesql.Policy, sql string) error {
	if cfg.AccessMode == config.Restricted {
		st, err := safesql.Check(sql, policy)
		if err != nil {
			return err
		}
		if st.Kind != safesql.Select {
			return errNotSelect
		}
		return nil
	}
	count, kind, err := safesql.Classify(sql)
	if err != nil {
		return err
	}
	if count != 1 || kind != safesql.Select {
		return errNotSelect
	}
	return nil
}

// reason is the short text for a failed statement: Postgres' message, or the error text.
func reason(err error) string {
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		return pe.Message
	}
	return err.Error()
}

// statsError explains a failed read of pg_stat_statements.
func statsError(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "55000" {
		return errors.New("pg_stat_statements is installed but not loaded: add it to shared_preload_libraries and restart PostgreSQL")
	}
	return fmt.Errorf("read pg_stat_statements: %w", err)
}
