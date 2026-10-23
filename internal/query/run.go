package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

const (
	cursorName    = "pgdba_cursor"
	declarePrefix = "DECLARE " + cursorName + " NO SCROLL CURSOR FOR "

	oidTSVector = 3614 // built-in type tsvector
)

// result is what one statement returned: at most maxRows rows, or a command tag when it has no columns.
type result struct {
	Columns []string
	Rows    [][]format.Cell
	More    bool // the server had at least one row beyond the limit
	// Estimate is the planner's row estimate of the whole statement, set only when More and the estimate is known.
	Estimate int64
	Tag      string // command tag, for statements without columns
	Elapsed  time.Duration
}

// txRunner is db.DB.ReadOnly or db.DB.ReadWrite.
type txRunner func(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error

// runStatement runs sql in a transaction started by run, through a cursor when cursor is set.
func runStatement(ctx context.Context, run txRunner, sql string, cursor bool, maxRows int) (result, error) {
	var res result
	err := run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		start := time.Now()
		var err error
		if cursor {
			res, err = fetchCursor(ctx, tx, sql, maxRows)
		} else {
			res, err = collect(ctx, tx, sql, maxRows)
		}
		res.Elapsed = time.Since(start)
		if err == nil && cursor && res.More {
			res.Estimate = estimateRows(ctx, tx, sql)
		}
		return err
	})
	return res, err
}

// fetchCursor declares a cursor on sql and fetches maxRows+1 rows, so the server computes no more than that.
func fetchCursor(ctx context.Context, tx pgx.Tx, sql string, maxRows int) (result, error) {
	if _, err := tx.Exec(ctx, declarePrefix+sql); err != nil {
		return result{}, shiftPosition(err)
	}
	res, err := collect(ctx, tx, fmt.Sprintf("FETCH FORWARD %d FROM %s", maxRows+1, cursorName), maxRows)
	if err == nil && len(res.Columns) == 0 {
		res.Tag = fmt.Sprintf("SELECT %d", len(res.Rows))
	}
	return res, err
}

// collect runs sql and keeps at most maxRows+1 rows; rows.Close drains the rest.
func collect(ctx context.Context, tx pgx.Tx, sql string, maxRows int) (result, error) {
	rows, err := tx.Query(ctx, sql)
	if err != nil {
		return result{}, err
	}
	defer rows.Close()
	var res result
	var tsvectors []bool // columns whose cells are summarized
	for _, fd := range rows.FieldDescriptions() {
		res.Columns = append(res.Columns, fd.Name)
		tsvectors = append(tsvectors, fd.DataTypeOID == oidTSVector)
	}
	kept := 0
	for rows.Next() {
		if kept > maxRows {
			continue
		}
		raw := rows.RawValues()
		row := make([]format.Cell, len(raw))
		for i, v := range raw {
			if v != nil {
				row[i] = append(format.Cell{}, v...) // keeps an empty value distinct from NULL
				if tsvectors[i] {
					if s, ok := format.SummarizeTSVector(string(v)); ok {
						row[i] = format.Cell(s)
					}
				}
			}
		}
		res.Rows = append(res.Rows, row)
		kept++
	}
	if err := rows.Err(); err != nil {
		return result{}, err
	}
	if len(res.Rows) > maxRows {
		res.Rows, res.More = res.Rows[:maxRows], true
	}
	if len(res.Columns) == 0 {
		res.Tag = rows.CommandTag().String()
	}
	return res, nil
}

// shiftPosition makes the POSITION of a DECLARE error relative to the user's text.
func shiftPosition(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && int(pe.Position) > len(declarePrefix) {
		c := *pe
		c.Position -= int32(len(declarePrefix))
		return &c
	}
	return err
}

// estimateRows asks the planner how many rows sql returns, with EXPLAIN (FORMAT JSON) in a savepoint so that a
// failure leaves the transaction usable. It returns 0 when no estimate is available. The statement timeout of the
// transaction bounds it.
func estimateRows(ctx context.Context, tx pgx.Tx, sql string) int64 {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return 0
	}
	var plan string
	err = sp.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+sql).Scan(&plan)
	var p []struct {
		Plan struct {
			Rows float64 `json:"Plan Rows"`
		}
	}
	if err != nil || json.Unmarshal([]byte(plan), &p) != nil || len(p) == 0 {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return 0
	}
	if err := sp.Rollback(context.WithoutCancel(ctx)); err != nil {
		return 0
	}
	return int64(p[0].Plan.Rows)
}
