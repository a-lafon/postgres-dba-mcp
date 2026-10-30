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
		if srv == nil {
			var err error
			if srv, err = spec.Start(ctx); err != nil {
				return err
			}
		}
		_, _ = fmt.Fprintf(r.Log, "n2 %s %s\n", s.ID, spec.Name)
		cell, failed, err := r.measure(ctx, srv, spec.Name, s, calls[i])
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
func (r *Runner) measure(ctx context.Context, srv *servers.Server, name string, s Scenario, c Call) (cell results.N2Cell, failed bool, err error) {
	tool := s.Tool
	if name == results.PgEdge {
		tool = "query_database" // pgEdge's SQL tool; only S01-S06 run on it
		c.Args = map[string]any{"query": c.Args["sql"]}
	}
	cell.Correct = true
	cell.Truncation = results.TruncNone
	cell.TruncExpected = s.Trunc != TruncNone
	var lat []float64
	var toks []float64
	for run := 0; run < Runs; run++ {
		for _, st := range c.Setup {
			if _, err := srv.Call(ctx, st.Tool, st.Args); err != nil {
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
		gradeCell(&cell, name, s, c, res.Text, res.IsError)
	}
	cell.LatencyMS = lat
	cell.P50MS, cell.P95MS = Percentile(lat, 50), Percentile(lat, 95)
	cell.Tokens = int(Median(toks))
	return cell, false, nil
}

// gradeCell applies the checks of s to the text of a first run and sets the verdict fields of cell.
func gradeCell(cell *results.N2Cell, name string, s Scenario, c Call, text string, isError bool) {
	cell.Status = results.StatusOK
	out := c.Check(name, text)
	cell.Missing, cell.Detected, cell.Advice = out.Missing, out.Detected, out.Advice
	cell.Correct = len(out.Missing) == 0
	if class := servers.Classify(name, isError, text); class == results.ClassRefused && s.ID != "S07" {
		cell.FalseRefusal, cell.Correct = true, false
	}
	if name == results.Crystal {
		return
	}
	signalled := Truncated(text)
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
			gradeCell(&cell, name, s, c, string(text), strings.HasPrefix(string(text), "ERROR:"))
			prev[i].Cells[name] = cell
		}
	}
	return prev, nil
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
