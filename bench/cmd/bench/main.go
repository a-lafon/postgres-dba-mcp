// Command bench measures postgres-dba-mcp against other PostgreSQL MCP servers and writes the report.
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
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

const usage = `usage: go run ./cmd/bench <command> [flags]

commands:
  up                         start the bench databases (docker compose up) and build the server binary
  load [--scale=<f>] [--no-pagila]
                             load the data set, replay the workload, then take the snapshot
  snapshot                   copy the stopped bench database to the volume bench_pgsnap (load does it)
  restore                    put the snapshot back in place of the bench database (stops and restarts pg)
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
  judge2 --run=<id> [--input=<file>] [--max-cost=<usd>] [--dry-run]
                             protocol v2 judge: each answer of --input (default <run>/judge-input.json, a JSON array
                             of judge.Item) checked against its expected facts, blind, by Opus 5.5 and Sonnet 5.5
                             (calls claude -p twice per answer); writes judge2.json; --dry-run prints the prompts only
  calibration export --run=<id> [--input=<file>]
                             write calibration2.md (40 blind answers) and calibration2.csv (the grid to fill) from
                             judge2.json, with calibration2-key.json (no call)
  calibration import --run=<id>
                             read the filled calibration2.csv and write calibration.json: Cohen's kappa of each judge
                             against the maintainer and between the judges (no call)
  report [--fail-on-blocking]
                             write report.md from the latest run
  report2 [--results-dir=<dir>] [--part-a=<id>] [--part-b=<id>] [--part-c=<id>] [--part-d=<id>]
                             write report-v2.md from the protocol v2 results under --results-dir (default
                             bench/results), run folders part-a to part-d by default; a missing part says "not run
                             yet" (no database, server or model call)
  all                        up, load, n1, n2, security, ops, then report (never n3)
  tasks [--reps=<n>] [--arm=ro|w|all] [--only=A01,...] [--dry-run]
                             part A of PROTOCOL.md: the 15 agent tasks with claude -p on every --servers (default dba
                             and crystal, from their registry images), the database restored before each run (costs
                             model calls); --dry-run prints the claude command lines and MCP configs only; run again
                             with --run=<id> to resume
  tasks --regrade --run=<id> [--only=A01,...]
                             grade again the stored part A runs with the rubrics of now and print the solved table;
                             no model or server call, but restores the database to compute the reference

protocol v2 (PROTOCOL.md), every server of --servers (default: all five), no model call:
  receive                    part B: B1-B7 through each server's SQL tool as bench_ro (receive.json)
  readonly [--agent-sql=<file>]
                             part C: the corpora through each server's read-only mode as bench_owner (readonly.json);
                             restores the bench database before each server, after every lasting effect and at the
                             end; --agent-sql adds the SQL of part A, in the corpus format (-- case: <id> <category>)

flags:
  --run=<id>                 run id of the results folder (default: a new UTC timestamp; report: the latest run)
  --tokens=claude            count tokens through claude -p, without any API key (n1, n2, all)
  --with-pgedge              also measure pgEdge on S01-S06 (n2)
  --servers=<a,b,...>        servers of n1 and n3, from dba,crystal,dbhub,toolbox,reference (default dba,crystal);
                             n2, security and ops still measure dba and crystal only
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
	reps        int    // tasks: repetitions per task and server
	arm         string // tasks: ro, w or all
	only        string // tasks: comma-separated task ids
	calibration string
	input       string           // judge2 and calibration: the judge input file
	dryRun      bool             // judge2 and tasks: print what would run, call nothing
	sub         string           // calibration: export or import
	agentSQL    string           // readonly: part A SQL to replay as legitimate statements
	servers     []servers.Pinned // --servers, in the order given

	resultsDir                 string // report2: the results root, default bench/results
	partA, partB, partC, partD string // report2: the run folder of each part
}

// defaultServers are the servers of the levels written before protocol v2.
const defaultServers = results.DBA + "," + results.Crystal

func parse(args []string, stderr io.Writer) (options, error) {
	var o options
	if len(args) == 0 {
		return o, errUsage
	}
	o.command = args[0]
	if o.command == "calibration" {
		if len(args) < 2 || (args[1] != "export" && args[1] != "import") {
			return o, errUsage
		}
		o.sub, args = args[1], append([]string{o.command}, args[2:]...)
	}
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
	fs.IntVar(&o.reps, "reps", 5, "")
	fs.StringVar(&o.arm, "arm", "all", "")
	fs.StringVar(&o.only, "only", "", "")
	fs.StringVar(&o.calibration, "calibration", "", "")
	fs.StringVar(&o.input, "input", "", "")
	fs.BoolVar(&o.dryRun, "dry-run", false, "")
	fs.StringVar(&o.agentSQL, "agent-sql", "", "")
	fs.StringVar(&o.resultsDir, "results-dir", "", "")
	fs.StringVar(&o.partA, "part-a", "part-a", "")
	fs.StringVar(&o.partB, "part-b", "part-b", "")
	fs.StringVar(&o.partC, "part-c", "part-c", "")
	fs.StringVar(&o.partD, "part-d", "part-d", "")
	list := fs.String("servers", "", "")
	if err := fs.Parse(args[1:]); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return o, errUsage
	}
	if fs.NArg() > 0 || o.scale <= 0 || o.maxCost < 0 || o.repeat < 1 || (o.tokens != "" && o.tokens != "claude") ||
		(o.tasks != "" && o.tasks != "core" && o.tasks != "extra" && o.tasks != "all") ||
		o.reps < 1 || (o.arm != "ro" && o.arm != "w" && o.arm != "all") {
		return o, errUsage
	}
	var err error
	def := defaultServers
	if v2Commands[o.command] && o.command != "tasks" {
		def = servers.Names()
	}
	if o.servers, err = servers.Select(*list, def); err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return o, errUsage
	}
	return o, nil
}

var commands = map[string]func(context.Context, *session) error{
	"up":          cmdUp,
	"load":        cmdLoad,
	"snapshot":    cmdSnapshot,
	"restore":     cmdRestore,
	"n1":          cmdN1,
	"n2":          cmdN2,
	"security":    cmdSecurity,
	"n3":          cmdN3,
	"judge":       cmdJudge,
	"judge2":      cmdJudge2,
	"calibration": cmdCalibration2,
	"ops":         cmdOps,
	"report":      cmdReport,
	"report2":     cmdReport2,
	"tasks":       cmdTasks,
	"receive":     cmdReceive,
	"readonly":    cmdReadOnly,
	"all":         cmdAll,
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
