package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/security"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// ReceiveStatement is one statement of part B (PROTOCOL.md §5). Key is a column whose name appears once per row in a
// keyed format (JSON, Python dicts), to count the rows of a response.
type ReceiveStatement struct {
	ID, SQL, Key string
}

// ReceiveStatements are B1-B7 of PROTOCOL.md §5; B5 is dropped (no pgvector in the database image).
var ReceiveStatements = []ReceiveStatement{
	{"B1", "SELECT * FROM film LIMIT 10", "film_id"},
	{"B2", "SELECT * FROM rental", "rental_id"},
	{"B3", "SELECT * FROM events", "account_id"},
	{"B4", "SELECT id, payload FROM docs WHERE id = 42", "payload"},
	{"B6", "SELECT * FROM precision", "big"},
	{"B7", "SELECT nme FROM film", ""},
}

// probeTimeout bounds the SELECT 1 that checks a server is still up after a call.
const probeTimeout = 30 * time.Second

// truth is what the database says a statement returns.
type truth struct {
	rows      int
	cell      string     // B4: the blob value inside the payload, which a complete response must hold whole
	cellBytes int        // B4: length of the payload text
	cols      []string   // B6: columns of public.precision
	values    [][]string // B6: the text of every value, by row, id first
}

// Receiver runs part B.
type Receiver struct {
	DB      *pgxpool.Pool // bench_ro: the truth
	Counter *tokens.Counter
	Log     io.Writer
	OutDir  string // receives every response as <id>-<server>.txt
}

// Run sends every statement of ReceiveStatements to each target's SQL tool and returns the size of each target's tool
// definitions and one row per statement.
func (r *Receiver) Run(ctx context.Context, targets []security.Target) ([]results.ReceiveTools, []results.ReceiveRow, error) {
	truths := make([]truth, len(ReceiveStatements))
	rows := make([]results.ReceiveRow, len(ReceiveStatements))
	for i, s := range ReceiveStatements {
		t, err := r.truth(ctx, s.ID)
		if err != nil {
			return nil, nil, fmt.Errorf("truth of %s: %w", s.ID, err)
		}
		truths[i] = t
		rows[i] = results.ReceiveRow{ID: s.ID, SQL: s.SQL, TrueRows: t.rows, TrueCellBytes: t.cellBytes, Cells: map[string]results.ReceiveCell{}}
	}
	var tools []results.ReceiveTools
	for _, tg := range targets {
		tt, err := r.runServer(ctx, tg, truths, rows)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", tg.Spec.Name, err)
		}
		tools = append(tools, tt)
	}
	return tools, rows, nil
}

func (r *Receiver) truth(ctx context.Context, id string) (truth, error) {
	var t truth
	switch id {
	case "B1":
		t.rows = 10
	case "B2":
		return t, r.DB.QueryRow(ctx, "SELECT count(*) FROM rental").Scan(&t.rows)
	case "B3":
		return t, r.DB.QueryRow(ctx, "SELECT count(*) FROM events").Scan(&t.rows)
	case "B4":
		t.rows = 1
		return t, r.DB.QueryRow(ctx, "SELECT payload->>'blob', length(payload::text) FROM docs WHERE id = 42").Scan(&t.cell, &t.cellBytes)
	case "B6":
		t.cols = []string{"id", "big", "num", "ts", "iv", "m"}
		// The text output of the database, as psql prints it: the session has the server defaults (Etc/UTC).
		rs, err := r.DB.Query(ctx, "SELECT id::text, big::text, num::text, ts::text, iv::text, m::text FROM precision ORDER BY id")
		if err != nil {
			return t, err
		}
		defer rs.Close()
		for rs.Next() {
			v := make([]string, len(t.cols))
			ptrs := make([]any, len(v))
			for i := range v {
				ptrs[i] = &v[i]
			}
			if err := rs.Scan(ptrs...); err != nil {
				return t, err
			}
			t.values = append(t.values, v)
		}
		t.rows = len(t.values)
		return t, rs.Err()
	}
	return t, nil
}

func (r *Receiver) runServer(ctx context.Context, tg security.Target, truths []truth, rows []results.ReceiveRow) (results.ReceiveTools, error) {
	name := tg.Spec.Name
	tt := results.ReceiveTools{Server: name}
	var srv *servers.Server
	defer func() {
		if srv != nil {
			srv.Close()
		}
	}()
	start := func() error {
		var err error
		srv, err = tg.Spec.StartLimit(ctx, security.ResponseLimit)
		return err
	}
	if err := start(); err != nil {
		return tt, err
	}
	tools, err := srv.Tools(ctx)
	if err != nil {
		return tt, err
	}
	b, err := json.Marshal(tools)
	if err != nil {
		return tt, fmt.Errorf("encode tools: %w", err)
	}
	tt.Tools, tt.Bytes = len(tools), len(b)
	if tt.Tokens, err = r.Counter.Tools(ctx, tools); err != nil {
		return tt, err
	}
	tt.Estimated = r.Counter.Label == results.CounterEstimate
	for i, s := range ReceiveStatements {
		if srv == nil {
			if err := start(); err != nil {
				return tt, err
			}
		}
		_, _ = fmt.Fprintf(r.Log, "receive %s %s\n", s.ID, name)
		res, callErr := srv.Call(ctx, tg.Tool, map[string]any{tg.Arg: s.SQL})
		text, isError, transport := security.Answer(name, res, callErr)
		cell := results.ReceiveCell{MS: float64(res.Latency.Microseconds()) / 1000, Rows: -1}
		switch {
		case transport != nil:
			// The connection is gone: the server is up when its container still runs, and its state says why not.
			state := containerState(ctx, tg.Spec.Container)
			cell.Outcome, cell.Note = transportOutcome(transport), transport.Error()+"; container "+state
			cell.StayedUp = state == "running"
		default:
			cell.Outcome = results.ReceiveOK
			if isError {
				// White space collapsed: a JSON error (DBHub) has nothing on its first line.
				cell.Outcome, cell.Note = results.ReceiveError, firstLine(strings.Join(strings.Fields(text), " "))
			}
			cell.Bytes = len(text)
			var est bool
			if cell.Tokens, est, err = r.Counter.TextEst(ctx, text); err != nil {
				return tt, err
			}
			cell.Estimated = est || r.Counter.Label == results.CounterEstimate
			if cell.File, err = r.keep(s.ID, name, text); err != nil {
				return tt, err
			}
			grade(&cell, s, truths[i], text, isError)
			pctx, cancel := context.WithTimeout(ctx, probeTimeout)
			_, perr := srv.Call(pctx, tg.Tool, map[string]any{tg.Arg: "SELECT 1"})
			cancel()
			_, _, ptransport := security.Answer(name, mcpc.Result{}, perr)
			cell.StayedUp = ptransport == nil
		}
		rows[i].Cells[name] = cell
		if !cell.StayedUp || transport != nil {
			srv.Close()
			srv = nil
		}
	}
	return tt, nil
}

func transportOutcome(err error) string {
	switch {
	case errors.Is(err, mcpc.ErrTimeout):
		return results.ReceiveTimeout
	case errors.Is(err, mcpc.ErrOversized):
		return results.ReceiveOversized
	}
	return results.ReceiveNoAnswer
}

// containerState returns "running", or how the container ended: its exit code and whether it was killed for memory.
func containerState(ctx context.Context, container string) string {
	if container == "" {
		return "unknown"
	}
	out, err := exec.CommandContext(ctx, "docker", "inspect", "-f", "{{.State.Status}} exit={{.State.ExitCode}} oom_killed={{.State.OOMKilled}}", container).Output()
	if err != nil {
		return "gone"
	}
	state := strings.TrimSpace(string(out))
	if strings.HasPrefix(state, "running ") {
		return "running"
	}
	return state
}

// grade fills the measures of a response the server answered.
func grade(cell *results.ReceiveCell, s ReceiveStatement, t truth, text string, isError bool) {
	switch s.ID {
	case "B7":
		cell.NamesColumn, cell.Hint = strings.Contains(text, "nme"), reHint.MatchString(text)
		return
	case "B6":
		cell.Values, cell.Wrong, cell.Reformatted = ComparePrecision(text, t.cols, t.values)
	}
	if isError {
		return
	}
	cell.Rows = CountRows(text, s.Key)
	complete := cell.Rows >= t.rows
	if s.ID == "B4" {
		complete = strings.Contains(text, t.cell)
	}
	cell.Truncation = Truncation(complete, text)
}

// reHint matches an error that helps the caller fix a misspelled column: a Postgres HINT, a suggestion, or the list
// of the columns that exist.
var reHint = regexp.MustCompile(`(?i)\bhint\b|perhaps you meant|did you mean|available columns|existing columns|columns (are|of)`)

// reStated matches a response that says it holds less than the result.
var reStated = regexp.MustCompile(`(?i)truncat|…\[\+|\bmore rows\b|rows? (omitted|not shown)|showing (the )?first|limited to \d+|row limit`)

// Truncation classifies a response with a result: none when it holds the whole result, stated when it holds less and
// says so, silent when it holds less and does not.
func Truncation(complete bool, text string) string {
	switch {
	case complete:
		return results.TruncNone
	case reStated.MatchString(text):
		return results.TruncStated
	}
	return results.TruncSilent
}

// CountRows counts the result rows in a response, whatever its format: the elements of the first JSON array of
// objects that have the key column, else the occurrences of the key column as a JSON key ("key") or a Python dict key ('key'),
// else the lines of a table (CSV or aligned text) minus the header and the `--` footer lines.
func CountRows(text, key string) int {
	var v any
	if json.Unmarshal([]byte(text), &v) == nil {
		if n, ok := jsonRows(v, key); ok {
			return n
		}
	}
	if key != "" {
		for _, q := range []string{`"` + key + `"`, `'` + key + `'`} {
			if n := strings.Count(text, q); n > 0 {
				return n
			}
		}
	}
	n := 0
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) != "" && !strings.HasPrefix(line, "--") {
			n++
		}
	}
	return max(n-1, 0)
}

// jsonRows finds, depth first, an array of objects whose first object has key (any array of objects when key is
// empty), and returns its length. An empty top-level array is 0 rows.
func jsonRows(v any, key string) (int, bool) {
	switch x := v.(type) {
	case []any:
		if len(x) == 0 {
			return 0, true
		}
		if m, ok := x[0].(map[string]any); ok {
			if _, has := m[key]; has || key == "" {
				return len(x), true
			}
		}
		for _, e := range x {
			if n, ok := jsonRows(e, key); ok && n > 0 {
				return n, true
			}
		}
	case map[string]any:
		for _, e := range x {
			if n, ok := jsonRows(e, key); ok && n > 0 {
				return n, true
			}
		}
	}
	return 0, false
}

// precisionNotations are other notations of exactly the same values of public.precision, as the servers' formats
// write them with the white space removed: RFC 3339 times, a JavaScript time when its milliseconds are exact, Python
// reprs, the interval objects of node-postgres and of pgx. A notation that drops a digit, or turns months or hours into
// days, is not one of them: interval '1 year 2 mons' is not 428 days, and '36:00:00' is not '1 day 12:00:00' across a
// DST change.
// shortcut: written for the three rows of bench/data/40_fixtures.sql; a value of another fixture has no notation but
// its exact text. Parse the notations if the fixture grows.
var precisionNotations = map[string][]string{
	"0.0000000001":                  {"1E-10", "1e-10"},
	"2026-03-29 00:59:59.999999+00": {"2026-03-29T00:59:59.999999Z", "2026-03-29T00:59:59.999999+00:00", "datetime.datetime(2026,3,29,0,59,59,999999,tzinfo="},
	"2026-03-29 01:00:00+00":        {"2026-03-29T01:00:00Z", "2026-03-29T01:00:00+00:00", "2026-03-29T01:00:00.000Z", "datetime.datetime(2026,3,29,1,0,tzinfo="},
	"2026-10-25 00:59:59.000001+00": {"2026-10-25T00:59:59.000001Z", "2026-10-25T00:59:59.000001+00:00", "datetime.datetime(2026,10,25,0,59,59,1,tzinfo="},
	"1 year 2 mons 3 days 04:05:06.789012": {`"years":1,"months":2,"days":3,"hours":4,"minutes":5,"seconds":6,"milliseconds":789.012`,
		`"Microseconds":14706789012,"Days":3,"Months":14`, "P1Y2M3DT4H5M6.789012S"},
	"-1 days +02:00:00": {`"days":-1,"hours":2`, `"Microseconds":7200000000,"Days":-1,"Months":0`, "datetime.timedelta(days=-1,seconds=7200)", "P-1DT2H"},
	"36:00:00":          {`"hours":36`, `"Microseconds":129600000000,"Days":0,"Months":0`, "PT36H"},
}

// ComparePrecision compares each value of public.precision with the response. A value is exact when its text, as the
// database prints it, is in the response as a whole value, not inside a longer number; reformatted when only one of
// its precisionNotations is; wrong otherwise. It returns the number of values compared, the wrong ones and the
// reformatted ones.
// shortcut: a value is looked for anywhere, not in its own row; the values of the fixture are all distinct, so a value
// found elsewhere cannot hide a wrong one. Locate rows per format if the fixture gets repeated values.
func ComparePrecision(text string, cols []string, values [][]string) (n int, wrong, reformatted []string) {
	compact := strings.Join(strings.Fields(text), "")
	for _, row := range values {
		for j, v := range row {
			if j == 0 { // the id identifies the row
				continue
			}
			n++
			label := fmt.Sprintf("row %s %s: want %s", row[0], cols[j], v)
			switch {
			case containsValue(text, v):
			case slices.ContainsFunc(precisionNotations[v], func(alt string) bool { return containsValue(compact, alt) }):
				reformatted = append(reformatted, label)
			default:
				wrong = append(wrong, label)
			}
		}
	}
	return n, wrong, reformatted
}

// containsValue reports whether v is in text with no digit, sign or decimal point glued to it, so that
// 9007199254740993 is not found inside -9007199254740993 or 19007199254740993.
func containsValue(text, v string) bool {
	glued := func(b byte) bool { return b >= '0' && b <= '9' || b == '.' }
	for i := 0; ; {
		j := strings.Index(text[i:], v)
		if j < 0 {
			return false
		}
		start, end := i+j, i+j+len(v)
		if (start == 0 || !glued(text[start-1]) && text[start-1] != '-') && (end == len(text) || !glued(text[end])) {
			return true
		}
		i = start + 1
	}
}

// keep writes a response to OutDir and returns its file name.
func (r *Receiver) keep(id, server, text string) (string, error) {
	if r.OutDir == "" {
		return "", nil
	}
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return "", fmt.Errorf("keep responses: %w", err)
	}
	name := id + "-" + server + ".txt"
	if err := os.WriteFile(filepath.Join(r.OutDir, name), []byte(text), 0o644); err != nil {
		return "", fmt.Errorf("keep the response of %s on %s: %w", id, server, err)
	}
	return name, nil
}
