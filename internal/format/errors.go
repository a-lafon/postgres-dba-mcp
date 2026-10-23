package format

import (
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/db"
)

// maxErrorBytes caps a rendered error.
const maxErrorBytes = 2000

// sqlError ties a failure to the statement that caused it, so that Error can point at the error position.
type sqlError struct {
	sql string
	err error
}

func (e *sqlError) Error() string { return e.err.Error() }
func (e *sqlError) Unwrap() error { return e.err }

// WithSQL attaches the statement text to err; Error then shows the line holding a Postgres error position.
func WithSQL(err error, sql string) error {
	if err == nil {
		return nil
	}
	return &sqlError{sql: sql, err: err}
}

// ctxRadius is the number of characters kept on each side of the error position in a long line.
const ctxRadius = 40

// sqlContext renders the psql-style context of a 1-based character position: "LINE n: <line>" and a caret line.
// A long line is cut around the position. It returns "" when the position is outside sql.
func sqlContext(sql string, pos int) string {
	r := []rune(sql)
	i := pos - 1
	if i < 0 || i > len(r) {
		return ""
	}
	start, line := 0, 1
	for k := 0; k < i; k++ {
		if r[k] == '\n' {
			start, line = k+1, line+1
		}
	}
	end := i
	for end < len(r) && r[end] != '\n' {
		end++
	}
	lo, hi, head, tail := start, end, "", ""
	if i-lo > ctxRadius {
		lo, head = i-ctxRadius, "..."
	}
	if hi-i > ctxRadius {
		hi, tail = i+ctxRadius, "..."
	}
	text := strings.NewReplacer("\r", " ", "\t", " ").Replace(string(r[lo:hi]))
	label := "LINE " + strconv.Itoa(line) + ": "
	return label + head + text + tail + "\n" + strings.Repeat(" ", len(label)+len(head)+i-lo) + "^"
}

// Error renders err for a tool result: "ERROR: ..." lines, at most 2000 bytes. A connection error is
// rendered as such even when it wraps a Postgres error.
func Error(err error) string {
	var out string
	var ce *db.ConnectError
	var pe *pgconn.PgError
	var se *sqlError
	switch {
	case errors.As(err, &ce):
		out = "ERROR: " + ce.Error()
	case errors.As(err, &pe):
		var b strings.Builder
		b.WriteString("ERROR: " + pe.Message + " (SQLSTATE " + pe.Code + ")")
		if errors.As(err, &se) && pe.Position > 0 {
			if c := sqlContext(se.sql, int(pe.Position)); c != "" {
				b.WriteString("\n" + c)
			}
		}
		if pe.Detail != "" {
			b.WriteString("\nDETAIL: " + pe.Detail)
		}
		if pe.Hint != "" {
			b.WriteString("\nHINT: " + pe.Hint)
		}
		if pe.Position > 0 {
			b.WriteString("\nPOSITION: " + strconv.Itoa(int(pe.Position)))
		}
		out = b.String()
	default:
		out = "ERROR: " + err.Error()
	}
	if len(out) > maxErrorBytes {
		out = out[:maxErrorBytes]
		for !utf8.ValidString(out) {
			out = out[:len(out)-1]
		}
	}
	return out
}
