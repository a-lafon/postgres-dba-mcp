// Command bench measures postgres-dba-mcp against crystaldba/postgres-mcp 0.3.0 and writes the go/no-go report.
// Run it from the bench folder: go run ./cmd/bench <command>.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

const usage = `usage: go run ./cmd/bench <command> [flags]

commands:
  up                         start the bench databases (docker compose up) and build the server binary
  load [--scale=<f>] [--no-pagila]
                             load the data set and replay the workload
  n1 | n2 | security | n3 | ops
                             run one level
  n3 --regrade --run=<id>    grade again the stored n3 transcripts of a run, without calling claude
  n2 --recheck --run=<id>    grade again the stored n2 outputs of a run, without calling any server or claude
  n3 --tasks=core|extra|all  the six tasks of the verdict (default), the schema and index-tuning ones, or both
  judge --run=<id> [--also=<id>,<id>] [--max-cost=<usd>] [--repeat=<n>]
                             blind pairwise LLM judge of the stored n3 answers, then an attribution pass of the errors
                             to the tool calls (calls claude -p, costs model calls); also writes calibration.md;
                             --also reads the tasks that --run lacks from other runs, --repeat judges each pair n times
  judge --calibration=<file> compare the filled calibration sheet with the judge's verdicts (no call)
  report [--fail-on-blocking]
                             write report.md from the latest run
  all                        up, load, n1, n2, security, ops, then report (never n3)

flags:
  --run=<id>                 run id of the results folder (default: a new UTC timestamp; report: the latest run)
  --tokens=claude            count tokens through claude -p, without any API key (n1, n2, all)
  --with-pgedge              also measure pgEdge on S01-S06 (n2)
`

// errUsage marks an invocation error: usage text, exit status 2.
var errUsage = errors.New("usage")

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// options are the parsed command line.
type options struct {
	command     string
	runID       string
	scale       float64
	noPagila    bool
	withPgEdge  bool
	failOnBlk   bool
	regrade     bool
	recheck     bool
	tokens      string
	tasks       string
	also        string
	maxCost     float64
	repeat      int
	calibration string
}

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errUsage
	}
	o.command = args[0]
	fs := flag.NewFlagSet(o.command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.StringVar(&o.runID, "run", "", "")
	fs.Float64Var(&o.scale, "scale", 1, "")
	fs.BoolVar(&o.noPagila, "no-pagila", false, "")
	fs.BoolVar(&o.withPgEdge, "with-pgedge", false, "")
	fs.BoolVar(&o.failOnBlk, "fail-on-blocking", false, "")
	fs.BoolVar(&o.regrade, "regrade", false, "")
	fs.BoolVar(&o.recheck, "recheck", false, "")
	fs.StringVar(&o.tokens, "tokens", "", "")
	fs.StringVar(&o.tasks, "tasks", "", "")
	fs.StringVar(&o.also, "also", "", "")
	fs.Float64Var(&o.maxCost, "max-cost", 0, "")
	fs.IntVar(&o.repeat, "repeat", 1, "")
	fs.StringVar(&o.calibration, "calibration", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return o, errUsage
	}
	if fs.NArg() > 0 || o.scale <= 0 || o.maxCost < 0 || o.repeat < 1 || (o.tokens != "" && o.tokens != "claude") ||
		(o.tasks != "" && o.tasks != "core" && o.tasks != "extra" && o.tasks != "all") {
		return o, errUsage
	}
	return o, nil
}

var commands = map[string]func(context.Context, *session) error{
	"up":       cmdUp,
	"load":     cmdLoad,
	"n1":       cmdN1,
	"n2":       cmdN2,
	"security": cmdSecurity,
	"n3":       cmdN3,
	"judge":    cmdJudge,
	"ops":      cmdOps,
	"report":   cmdReport,
	"all":      cmdAll,
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	o, err := parse(args, stderr)
	if err != nil {
		_, _ = fmt.Fprint(stderr, usage)
		return 2
	}
	cmd, known := commands[o.command]
	if !known {
		_, _ = fmt.Fprintf(stderr, "unknown command %q\n%s", o.command, usage)
		return 2
	}
	s, err := newSession(o, getenv, stdout, stderr)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	if err := cmd(ctx, s); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// session is what every command shares: where the bench lives and which run it writes to.
type session struct {
	opts     options
	getenv   func(string) string
	out, err io.Writer
	benchDir string // the working directory, which must hold compose.yml
	repoDir  string // the repository root, one level up
	runID    string
	runDir   string // set by runFolder
}

func newSession(o options, getenv func(string) string, out, errw io.Writer) (*session, error) {
	dir, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("find working directory: %w", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		return nil, fmt.Errorf("run the bench from the bench folder (no compose.yml in %s)", dir)
	}
	s := &session{opts: o, getenv: getenv, out: out, err: errw, benchDir: dir, repoDir: filepath.Dir(dir), runID: o.runID}
	if s.runID == "" && o.command != "report" {
		s.runID = results.NewRunID()
	}
	return s, nil
}

func (s *session) resultsRoot() string { return filepath.Join(s.benchDir, "results") }

// runFolder returns the folder of the run, creating it. For report without --run it is the latest run.
func (s *session) runFolder() (string, error) {
	if s.runDir != "" {
		return s.runDir, nil
	}
	if s.runID == "" {
		id, err := results.Latest(s.resultsRoot())
		if err != nil {
			return "", err
		}
		if id == "" {
			return "", errors.New("no run in bench/results: run a level first")
		}
		s.runID = id
	}
	dir, err := results.Dir(s.resultsRoot(), s.runID)
	if err != nil {
		return "", err
	}
	s.runDir = dir
	return dir, nil
}
