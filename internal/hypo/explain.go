package hypo

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

const (
	preparedName = "pgdba_explain"
	// cleanupTimeout bounds the cleanup statements, which run even when the call's context is done.
	cleanupTimeout = 5 * time.Second
)

// ErrUnexpectedOutput is returned when EXPLAIN (FORMAT JSON) does not return one plan.
var ErrUnexpectedOutput = errors.New("unexpected EXPLAIN output")

// HasParams reports whether query contains $n parameter references, in which case a generic plan is explained.
// A $n inside a string literal, a dollar-quoted body, a quoted identifier or a comment is not a parameter.
func HasParams(query string) bool { return safesql.HasParams(query) }

// cleanupContext returns a context for the statements that undo a call's session state: it keeps the values of
// ctx but ignores its cancellation, so a canceled call still resets hypopg and deallocates on the connection
// that holds the state (behind a transaction pooler, the pool's release hook may reach another server connection).
func cleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
}

// Explain returns the JSON plan of query, as ExplainWithTotals does.
func Explain(ctx context.Context, tx pgx.Tx, query string, idxs []Index, analyze bool) (*Node, error) {
	n, _, err := ExplainWithTotals(ctx, tx, query, idxs, analyze)
	return n, err
}

// ExplainWithTotals runs EXPLAIN (FORMAT JSON, VERBOSE) on query with the hypothetical indexes idxs created and
// returns the plan with its totals. It works inside a READ ONLY transaction.
func ExplainWithTotals(ctx context.Context, tx pgx.Tx, query string, idxs []Index, analyze bool) (*Node, Totals, error) {
	out, names, err := run(ctx, tx, query, idxs, analyze, "JSON")
	if err != nil {
		return nil, Totals{}, err
	}
	return parsePlan(out, names)
}

// parsePlan decodes the one-element JSON array that EXPLAIN (FORMAT JSON) returns.
func parsePlan(out string, names []string) (*Node, Totals, error) {
	var plans []struct {
		Plan        *Node    `json:"Plan"`
		PlanningMs  *float64 `json:"Planning Time"`
		ExecutionMs *float64 `json:"Execution Time"`
	}
	if err := json.Unmarshal([]byte(out), &plans); err != nil || len(plans) != 1 || plans[0].Plan == nil {
		return nil, Totals{}, ErrUnexpectedOutput
	}
	return plans[0].Plan, Totals{PlanningMs: plans[0].PlanningMs, ExecutionMs: plans[0].ExecutionMs, HypoIndexNames: names}, nil
}

// ExplainText returns the Postgres TEXT plan of query, with the same options as ExplainWithTotals.
func ExplainText(ctx context.Context, tx pgx.Tx, query string, idxs []Index, analyze bool) (string, error) {
	out, _, err := run(ctx, tx, query, idxs, analyze, "TEXT")
	return out, err
}

// options builds the parenthesised EXPLAIN option list.
func options(format string, analyze bool) string {
	opts := "FORMAT " + format + ", VERBOSE"
	if analyze {
		opts += ", ANALYZE, BUFFERS"
	}
	return opts
}

// session runs work in a savepoint, rolled back on error so that the transaction stays usable. When reset is
// true, hypopg state is reset before and after, on success and on error.
func session(ctx context.Context, tx pgx.Tx, reset bool, work func(ctx context.Context, tx pgx.Tx) error) (err error) {
	if reset {
		if _, err := tx.Exec(ctx, "SELECT hypopg_reset()"); err != nil {
			return fmt.Errorf("reset hypothetical indexes: %w", err)
		}
		defer func() {
			cctx, cancel := cleanupContext(ctx)
			defer cancel()
			if _, rerr := tx.Exec(cctx, "SELECT hypopg_reset()"); rerr != nil {
				err = errors.Join(err, fmt.Errorf("reset hypothetical indexes: %w", rerr))
			}
		}()
	}
	sp, err := tx.Begin(ctx)
	if err != nil {
		return fmt.Errorf("open savepoint: %w", err)
	}
	if err := work(ctx, sp); err != nil {
		cctx, cancel := cleanupContext(ctx)
		defer cancel()
		if rerr := sp.Rollback(cctx); rerr != nil {
			err = errors.Join(err, fmt.Errorf("roll back savepoint: %w", rerr))
		}
		return err
	}
	return sp.Commit(ctx)
}

// createAll creates the hypothetical indexes from their validated DDL and returns the names hypopg gave them.
func createAll(ctx context.Context, tx pgx.Tx, ddls []string) ([]string, error) {
	names := make([]string, 0, len(ddls))
	for _, d := range ddls {
		var oid uint32
		var name string
		if err := tx.QueryRow(ctx, "SELECT indexrelid, indexname FROM hypopg_create_index($1)", d).Scan(&oid, &name); err != nil {
			return nil, fmt.Errorf("create hypothetical index: %w", err)
		}
		names = append(names, name)
	}
	return names, nil
}

func validateAll(idxs []Index) ([]string, error) {
	ddls := make([]string, len(idxs))
	for i, idx := range idxs {
		d, err := ddl(idx)
		if err != nil {
			return nil, err
		}
		ddls[i] = d
	}
	return ddls, nil
}

// run executes the EXPLAIN and returns its raw output (one JSON document, or the TEXT lines) and the names of
// the hypothetical indexes it created.
func run(ctx context.Context, tx pgx.Tx, query string, idxs []Index, analyze bool, format string) (out string, names []string, err error) {
	if analyze && len(idxs) > 0 {
		return "", nil, errors.New("analyze cannot be combined with hypothetical indexes")
	}
	params := HasParams(query)
	if analyze && params {
		return "", nil, errors.New("analyze is not possible with $n parameters; replace them with values")
	}
	ddls, err := validateAll(idxs)
	if err != nil {
		return "", nil, err
	}
	opts := options(format, analyze)
	prepared := false
	if params {
		defer func() {
			if !prepared {
				return
			}
			cctx, cancel := cleanupContext(ctx)
			defer cancel()
			if _, derr := tx.Exec(cctx, "DEALLOCATE "+preparedName); derr != nil {
				err = errors.Join(err, fmt.Errorf("deallocate prepared statement: %w", derr))
			}
		}()
	}
	err = session(ctx, tx, len(idxs) > 0, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		if names, err = createAll(ctx, tx, ddls); err != nil {
			return err
		}
		stmt := query
		if params {
			if _, err := tx.Exec(ctx, "PREPARE "+preparedName+" AS "+query); err != nil {
				return fmt.Errorf("prepare: %w", err)
			}
			prepared = true
			if _, err := tx.Exec(ctx, "SET LOCAL plan_cache_mode = force_generic_plan"); err != nil {
				return fmt.Errorf("set plan_cache_mode: %w", err)
			}
			var n int
			if err := tx.QueryRow(ctx, "SELECT cardinality(parameter_types) FROM pg_prepared_statements WHERE name = $1", preparedName).Scan(&n); err != nil {
				return fmt.Errorf("read parameter count: %w", err)
			}
			stmt = "EXECUTE " + preparedName
			if n > 0 {
				stmt += "(" + strings.TrimSuffix(strings.Repeat("NULL, ", n), ", ") + ")"
			}
		}
		sql := "EXPLAIN (" + opts + ") " + stmt
		rows, err := tx.Query(ctx, sql)
		if err != nil {
			return err
		}
		defer rows.Close()
		var lines []string
		for rows.Next() {
			var l string
			if err := rows.Scan(&l); err != nil {
				return err
			}
			lines = append(lines, l)
		}
		out = strings.Join(lines, "\n")
		return rows.Err()
	})
	if err != nil {
		return "", nil, err
	}
	return out, names, nil
}
