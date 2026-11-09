package scenarios

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// Runs is the number of times each scenario runs per server.
const Runs = 5

// pgEdgeLast is the last scenario pgEdge is measured on.
const pgEdgeLast = "S06"

// Runner runs the N2 scenarios.
type Runner struct {
	DB      *pgxpool.Pool
	Counter *tokens.Counter
	Specs   []servers.Spec // in report order
	Pagila  bool
	Log     io.Writer
	// OutDir receives the response text of each scenario and server (first run) as <id>-<server>.txt.
	OutDir string
}

// Run runs every scenario on every server and returns one row per scenario.
func (r *Runner) Run(ctx context.Context) ([]results.N2Row, error) {
	all := All()
	calls := make([]Call, len(all))
	for i, s := range all {
		if s.Pagila && !r.Pagila {
			continue
		}
		c, err := s.Prepare(ctx, r.DB)
		if err != nil {
			return nil, fmt.Errorf("prepare %s: %w", s.ID, err)
		}
		calls[i] = c
	}
	rows := make([]results.N2Row, len(all))
	for i, s := range all {
		rows[i] = results.N2Row{ID: s.ID, Tool: s.Tool, Cells: map[string]results.N2Cell{}}
	}
	for _, spec := range r.Specs {
		if err := r.runServer(ctx, spec, all, calls, rows); err != nil {
			return nil, err
		}
	}
	return rows, nil
}

func (r *Runner) runServer(ctx context.Context, spec servers.Spec, all []Scenario, calls []Call, rows []results.N2Row) error {
	var srv *servers.Server
	defer func() {
		if srv != nil {
			srv.Close()
		}
	}()
	for i, s := range all {
		switch {
		case s.Pagila && !r.Pagila, spec.Name == results.PgEdge && s.ID > pgEdgeLast:
			rows[i].Cells[spec.Name] = results.N2Cell{Status: results.StatusSkipped}
			continue
		}
		// A tool the server does not have is counted apart: neither a failure nor a wrong answer.
		if _, ok := spec.ToolName(s.Tool); !ok {
			rows[i].Cells[spec.Name] = results.N2Cell{Status: results.StatusNoTool}
			continue
		}
		if srv == nil {
			var err error
			if srv, err = spec.Start(ctx); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(r.Log, "n2 %s %s\n", s.ID, spec.Name)
		cell, failed, err := r.measure(ctx, srv, spec, s, calls[i])
		if err != nil {
			return fmt.Errorf("%s on %s: %w", s.ID, spec.Name, err)
		}
		rows[i].Cells[spec.Name] = cell
		if failed {
			srv.Close()
			srv = nil
		}
	}
	return nil
}

// measure runs one scenario Runs times on srv. failed reports that the server must be restarted.
func (r *Runner) measure(ctx context.Context, srv *servers.Server, spec servers.Spec, s Scenario, c Call) (cell results.N2Cell, failed bool, err error) {
	name := spec.Name
	tool, _ := spec.ToolName(s.Tool) // runServer skipped the scenario when the server has no such tool
	if name == results.PgEdge {
		c.Args = map[string]any{"query": c.Args["sql"]} // pgEdge names the SQL argument differently; only S01-S06 run on it
	}
	setup, ok := setupTools(spec, c.Setup)
	if !ok {
		return results.N2Cell{Status: results.StatusNoTool}, false, nil
	}
	cell.Correct = true
	cell.Truncation = results.TruncNone
	cell.TruncExpected = s.Trunc != TruncNone
	var lat []float64
	var toks []float64
	for run := 0; run < Runs; run++ {
		for i, st := range c.Setup {
			if _, err := srv.Call(ctx, setup[i], st.Args); err != nil {
				return failCell(cell, err), true, nil
			}
		}
		res, err := srv.Call(ctx, tool, c.Args)
		if err != nil {
			return failCell(cell, err), true, nil
		}
		lat = append(lat, float64(res.Latency.Microseconds())/1000)
		cell.Bytes = max(cell.Bytes, len(res.Text))
		n, estimated, err := r.Counter.TextEst(ctx, res.Text)
		if err != nil {
			return cell, false, err
		}
		cell.Estimated = cell.Estimated || estimated
		toks = append(toks, float64(n))
		cell.TokenRuns = append(cell.TokenRuns, n)
		if run > 0 {
			continue
		}
		if err := r.keep(name, s.ID, res.Text); err != nil {
			return cell, false, err
		}
		gradeCell(&cell, spec, s, c, res.Text, res.IsError)
	}
	cell.LatencyMS = lat
	cell.P50MS, cell.P95MS = Percentile(lat, 50), Percentile(lat, 95)
	cell.Tokens = int(Median(toks))
	return cell, false, nil
}

// setupTools returns the names spec gives to the setup steps, in order. ok is false when it has no such tool: a
// setup step is a tool call like any other, and calling it by its canonical name fails on a server that renames it
// (explain_query is explain_sql on dbhub-4tools).
func setupTools(spec servers.Spec, setup []Step) ([]string, bool) {
	out := make([]string, len(setup))
	for i, st := range setup {
		name, ok := spec.ToolName(st.Tool)
		if !ok {
			return nil, false
		}
		out[i] = name
	}
	return out, true
}

// gradeCell applies the checks of s to the text of a first run and sets the verdict fields of cell.
func gradeCell(cell *results.N2Cell, spec servers.Spec, s Scenario, c Call, text string, isError bool) {
	name := spec.Name
	cell.Status = results.StatusOK
	out := c.Check(name, text)
	cell.Missing, cell.Detected, cell.Advice = out.Missing, out.Detected, out.Advice
	cell.Correct = len(out.Missing) == 0
	// S07 is the one scenario whose expected answer is an error. Everywhere else a server that answers with an error
	// did not answer the question: counting the tokens of its error message as a response would reward failing fast,
	// and a scenario that declares no fact would even grade it correct.
	if class := spec.Class(isError, text); s.ID != "S07" {
		switch class {
		case results.ClassRefused:
			cell.FalseRefusal, cell.Correct = true, false
		case results.ClassFailed:
			cell.Status, cell.Correct, cell.Note = results.StatusError, false, firstLine(text)
		}
	}
	if name == results.Crystal {
		return
	}
	truncated := spec.Truncated
	if truncated == nil {
		truncated = Truncated
	}
	signalled := truncated(text)
	switch {
	case s.Trunc != TruncNone && !signalled:
		cell.Truncation, cell.Correct = results.TruncMissing, false
	case signalled:
		cell.Truncation = results.TruncSignalled
	}
}

// Truncated reports whether a postgres-dba-mcp text signals a bound: a footer line (`-- ` prefix) that says
// `truncated`, with or without a planner estimate, or a shortened cell.
func Truncated(text string) bool {
	if strings.Contains(text, "…[+") {
		return true
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "-- ") && strings.Contains(line, "truncated") {
			return true
		}
	}
	return false
}

// Recheck grades again, with the current checks, the response texts kept in OutDir for the cells of prev, which keep
// their tokens, bytes and latencies. Cells that did not complete are left as they are. No server is called. A kept
// text is an error for postgres-dba-mcp when it starts with `ERROR:`.
func (r *Runner) Recheck(ctx context.Context, prev []results.N2Row) ([]results.N2Row, error) {
	all := All()
	if len(prev) != len(all) {
		return nil, fmt.Errorf("n2.json has %d scenarios, want %d", len(prev), len(all))
	}
	for i, s := range all {
		var c Call
		prepared := false
		for name, cell := range prev[i].Cells {
			if cell.Status != results.StatusOK {
				continue
			}
			if !prepared {
				var err error
				if c, err = s.Prepare(ctx, r.DB); err != nil {
					return nil, fmt.Errorf("prepare %s: %w", s.ID, err)
				}
				prepared = true
			}
			text, err := os.ReadFile(filepath.Join(r.OutDir, s.ID+"-"+name+".txt"))
			if err != nil {
				return nil, fmt.Errorf("read kept output: %w", err)
			}
			cell.Correct, cell.FalseRefusal, cell.Truncation = true, false, results.TruncNone
			gradeCell(&cell, r.specOf(name), s, c, string(text), strings.HasPrefix(string(text), "ERROR:"))
			prev[i].Cells[name] = cell
		}
	}
	return prev, nil
}

// specOf returns the spec of a server among Specs, or a bare one with the default classification when Specs is
// empty, as `n2 --recheck` leaves it.
func (r *Runner) specOf(name string) servers.Spec {
	for _, s := range r.Specs {
		if s.Name == name {
			return s
		}
	}
	return servers.Spec{Name: name}
}

// keep writes the response text of the first run to OutDir.
func (r *Runner) keep(server, id, text string) error {
	if r.OutDir == "" {
		return nil
	}
	if err := os.MkdirAll(r.OutDir, 0o755); err != nil {
		return fmt.Errorf("keep raw outputs: %w", err)
	}
	if err := os.WriteFile(filepath.Join(r.OutDir, id+"-"+server+".txt"), []byte(text), 0o644); err != nil {
		return fmt.Errorf("keep raw output of %s on %s: %w", id, server, err)
	}
	return nil
}

// firstLine returns the first line of text, cut to what a report cell can show.
func firstLine(text string) string {
	line, _, _ := strings.Cut(text, "\n")
	if len(line) > 120 {
		line = line[:120] + "…"
	}
	return line
}

func failCell(cell results.N2Cell, err error) results.N2Cell {
	cell.Correct = false
	switch {
	case errors.Is(err, mcpc.ErrTimeout):
		cell.Status = results.StatusTimeout
	case errors.Is(err, mcpc.ErrOversized):
		cell.Status = results.StatusOversized
	default:
		cell.Status, cell.Note = results.StatusError, err.Error()
	}
	return cell
}
