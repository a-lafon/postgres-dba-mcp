package query

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

const (
	cursorName    = "pgdba_cursor"
	declarePrefix = "DECLARE " + cursorName + " NO SCROLL CURSOR FOR "

	oidTSVector = 3614 // built-in type tsvector
)

// result is what one statement returned: at most maxRows rows, or a command tag when it has no columns.
type result struct {
	Columns   []string
	Rows      [][]format.Cell
	Shortened [][]bool // cells cut on the way in, for the footer
	More      bool     // the server had at least one row beyond the limit
	Tag       string   // command tag, for statements without columns
	Elapsed   time.Duration
}

// limits bound one statement: the rows it may return, the code points one cell may keep, and the bytes all the
// cells kept may take. The byte bound is the renderer's own, so a result stopped by it was going to be
// truncated by the renderer anyway.
type limits struct{ rows, cell, bytes int }

// txRunner is db.DB.ReadOnly or db.DB.ReadWrite.
type txRunner func(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error

// withVolatilityGate wraps run so that the gate settles refs in the very
// transaction about to run the statement: the names then resolve against the
// same search_path, and nothing can be created in between.
func withVolatilityGate(run txRunner, refs []safesql.FuncRef) txRunner {
	return func(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error {
		return run(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if err := safesql.Gate(ctx, tx, refs); err != nil {
				return err
			}
			return fn(ctx, tx)
		})
	}
}

// runStatement runs sql in a transaction started by run, through a cursor when cursor is set, drained to its
// end when drain is set.
func runStatement(ctx context.Context, run txRunner, sql string, cursor, drain bool, lim limits) (result, error) {
	var res result
	err := run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		start := time.Now()
		var err error
		if cursor {
			res, err = fetchCursor(ctx, tx, sql, drain, lim)
		} else {
			res, err = collect(ctx, tx, sql, lim)
		}
		res.Elapsed = time.Since(start)
		return err
	})
	return res, err
}

// fetchCursor declares a cursor on sql and fetches maxRows+1 rows, so the server computes no more than that.
// When drain is set the rest of the cursor is run and discarded, so that a statement whose rows have side
// effects is not committed half done.
func fetchCursor(ctx context.Context, tx pgx.Tx, sql string, drain bool, lim limits) (result, error) {
	if _, err := tx.Exec(ctx, declarePrefix+sql); err != nil {
		return result{}, shiftPosition(err)
	}
	res, err := collect(ctx, tx, fmt.Sprintf("FETCH FORWARD %d FROM %s", lim.rows+1, cursorName), lim)
	if err == nil && drain {
		_, err = tx.Exec(ctx, "MOVE FORWARD ALL IN "+cursorName)
	}
	if err == nil && len(res.Columns) == 0 {
		res.Tag = fmt.Sprintf("SELECT %d", len(res.Rows))
	}
	return res, err
}

// collect runs sql and keeps at most maxRows+1 rows, each cell cut to what the renderer can show and the whole
// within the byte budget; rows.Close drains the rest. Cutting here is what keeps a result of large values
// bounded: a cell kept whole is held whole, however little of it is rendered.
func collect(ctx context.Context, tx pgx.Tx, sql string, lim limits) (result, error) {
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
	kept, size, seen := 0, 0, 0
	for rows.Next() {
		seen++
		if kept > lim.rows || size > lim.bytes {
			continue
		}
		raw := rows.RawValues()
		row := make([]format.Cell, len(raw))
		short := make([]bool, len(raw))
		for i, v := range raw {
			if v != nil {
				if tsvectors[i] {
					if s, ok := format.SummarizeTSVector(string(v)); ok {
						v = []byte(s)
					}
				}
				row[i], short[i] = format.Cut(v, lim.cell) // keeps an empty value distinct from NULL
				size += len(row[i])
			}
		}
		res.Rows = append(res.Rows, row)
		res.Shortened = append(res.Shortened, short)
		kept++
	}
	if err := rows.Err(); err != nil {
		return result{}, err
	}
	if len(res.Rows) > lim.rows {
		res.Rows, res.Shortened = res.Rows[:lim.rows], res.Shortened[:lim.rows]
	}
	// Rows left behind by either bound: the byte budget stops before the row limit does, and a caller that
	// must not commit a statement half done has only More to tell it that rows went unread.
	res.More = seen > len(res.Rows)
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
