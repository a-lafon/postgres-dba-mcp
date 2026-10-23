package format

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Cell is the Postgres text value of one field; nil means NULL.
type Cell []byte

// Table is a result set ready to render.
type Table struct {
	Columns []string
	Rows    [][]Cell // at most Limit rows
	More    bool     // the caller fetched Limit+1 rows and got the extra one
	Limit   int
	Prefix  string // lines written before the table, counted in the byte budget
	// Caps holds, per column, a cell limit that replaces the maxCell given to Render; 0 or a missing entry keeps maxCell.
	Caps []int
	// Estimate is the planner's estimate of the total number of rows when the result is truncated, 0 when unknown.
	// It is shown only when it exceeds the rows rendered.
	Estimate int64
}

// csvField quotes s when it contains a comma or a quote, starts or ends with a space, is empty, or is exactly \N.
func csvField(s string) string {
	if s == "" || s == `\N` || strings.ContainsAny(s, `,"`) || s[0] == ' ' || s[len(s)-1] == ' ' {
		return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	return s
}

// escape writes CR, LF and tab as \r, \n and \t. A backslash is doubled when it is followed by n, r, t, a
// backslash, CR, LF or tab, so that decoding (\\ to \, \n \r \t to the control characters, any other \ literal)
// restores the value. Other backslashes (bytea \x, paths) are left alone.
func escape(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\r':
			b.WriteString(`\r`)
		case '\n':
			b.WriteString(`\n`)
		case '\t':
			b.WriteString(`\t`)
		case '\\':
			if i+1 < len(s) && strings.IndexByte("nrt\\\r\n\t", s[i+1]) >= 0 {
				b.WriteByte('\\')
			}
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	return b.String()
}

// cellText renders one cell: NULL, shortening, escaping, then quoting.
func cellText(c Cell, maxCell int) (text string, shortened bool) {
	if c == nil {
		return `\N`, false
	}
	s, shortened := shorten(string(c), maxCell)
	return csvField(escape(s)), shortened
}

// CSVLine renders fields as one CSV line with the escaping and quoting of Render, ending with a newline.
// Callers that build a small table of their own use it instead of a second CSV writer.
func CSVLine(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = csvField(escape(f))
	}
	return strings.Join(out, ",") + "\n"
}

// cap returns the cell limit of column j.
func (t Table) cap(j, maxCell int) int {
	if j < len(t.Caps) && t.Caps[j] > 0 {
		return t.Caps[j]
	}
	return maxCell
}

// footer builds the footer lines. trunc is "" when the result is complete.
// est is the estimated total row count, 0 when unknown; it is shown only with a truncation.
func footer(rows int, est int64, trunc string, shortened int, d time.Duration) string {
	parts := []string{plural(rows, "row")}
	if trunc != "" && est > int64(rows) {
		parts[0] = fmt.Sprintf("%d of ~%s rows (planner estimate)", rows, Approx(est))
	}
	if trunc != "" {
		parts = append(parts, trunc)
	}
	if shortened > 0 {
		parts = append(parts, plural(shortened, "cell")+" shortened")
	}
	parts = append(parts, Duration(d))
	s := "-- " + strings.Join(parts, ", ") + "\n"
	if trunc != "" {
		s += "-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n"
	}
	return s
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// Render writes the table as CSV followed by its footer. The whole text, prefix and footer included, stays
// within maxBytes: rendering stops before the first row that would exceed the budget.
func Render(t Table, maxBytes, maxCell int, d time.Duration) (string, error) {
	prefix := t.Prefix
	if prefix != "" && !strings.HasSuffix(prefix, "\n") {
		prefix += "\n"
	}
	header := CSVLine(t.Columns...)

	rows, more := t.Rows, t.More
	if t.Limit > 0 && len(rows) > t.Limit {
		rows, more = rows[:t.Limit], true
	}
	limit := t.Limit
	if limit <= 0 {
		limit = len(rows)
	}
	byBytes := fmt.Sprintf("truncated (%d-byte limit)", maxBytes)
	moreThan := fmt.Sprintf("truncated (more than %d)", limit)
	est := t.Estimate
	if est > int64(len(rows)) {
		moreThan = "truncated" // "N of ~M rows" already says how many are shown
	}

	// The header must leave room for the longest footer it can need: an empty result, or none of the rows fitting.
	needed := footer(0, 0, "", 0, d)
	if len(rows) > 0 || more {
		needed = footer(0, est, byBytes, 0, d)
	}
	if len(prefix)+len(header)+len(needed) > maxBytes {
		return "", fmt.Errorf("result header alone exceeds --max-bytes (%d bytes); select fewer columns", len(prefix)+len(header))
	}

	var b strings.Builder
	b.WriteString(prefix)
	b.WriteString(header)
	shortened, written := 0, 0
	for i, row := range rows {
		var line strings.Builder
		cellsShortened := 0
		for j, c := range row {
			text, short := cellText(c, t.cap(j, maxCell))
			if j > 0 {
				line.WriteByte(',')
			}
			line.WriteString(text)
			if short {
				cellsShortened++
			}
		}
		line.WriteByte('\n')

		// The footer that would end the output if this row were the last one.
		last := i == len(rows)-1
		var f string
		switch {
		case !last:
			f = footer(i+1, est, byBytes, shortened+cellsShortened, d)
		case more:
			f = footer(i+1, est, moreThan, shortened+cellsShortened, d)
		default:
			f = footer(i+1, 0, "", shortened+cellsShortened, d)
		}
		if b.Len()+line.Len()+len(f) > maxBytes {
			break
		}
		b.WriteString(line.String())
		shortened += cellsShortened
		written++
	}

	switch {
	case written < len(rows):
		b.WriteString(footer(written, est, byBytes, shortened, d))
	case more:
		b.WriteString(footer(written, est, moreThan, shortened, d))
	default:
		b.WriteString(footer(written, 0, "", shortened, d))
	}
	return b.String(), nil
}

// RenderTag renders the footer of a statement that returns no rows: "-- UPDATE 3, 3 ms".
func RenderTag(tag string, d time.Duration) string {
	return "-- " + tag + ", " + Duration(d) + "\n"
}
