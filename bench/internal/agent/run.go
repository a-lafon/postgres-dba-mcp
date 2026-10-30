package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Runs is the number of times each task runs per server.
const Runs = 3

// resetServerStatements drops the pg_stat_statements entries of bench_ro, the role of both servers, so that the
// workload tools see the pgbench workload (run as postgres) and not the queries of earlier runs.
const resetServerStatements = `SELECT pg_stat_statements_reset((SELECT oid FROM pg_roles WHERE rolname = 'bench_ro'))`

// ErrNoClaude is returned when the claude CLI is not installed.
var ErrNoClaude = errors.New("the claude CLI is not installed or not in PATH: install Claude Code to run n3")

// Runner runs the N3 tasks. It needs the claude CLI, a model account, and the bench database loaded with Pagila.
type Runner struct {
	DB      *pgxpool.Pool // bench_ro connection, for the references
	Admin   *pgxpool.Pool // superuser connection, to reset the servers' statement statistics before each run
	RepoDir string        // working directory of claude: the repository root, where bench/n3/<server>.json resolves
	OutDir  string        // folder of the run, where transcripts are kept under n3/
	Log     io.Writer
	Servers []string // result server names; each has a bench/n3/<name>.json config
	Tasks   []Task   // tasks to run; the six of Tasks when empty
}

func (r *Runner) tasks() []Task {
	if len(r.Tasks) == 0 {
		return Tasks
	}
	return r.Tasks
}

// Run runs every task Runs times on every server and grades each run.
func (r *Runner) Run(ctx context.Context) ([]results.N3Run, error) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return nil, ErrNoClaude
	}
	if err := os.MkdirAll(filepath.Join(r.OutDir, "n3"), 0o755); err != nil {
		return nil, fmt.Errorf("create transcript folder: %w", err)
	}
	var runs []results.N3Run
	for _, task := range r.tasks() {
		for _, server := range r.Servers {
			for i := 1; i <= Runs; i++ {
				_, _ = fmt.Fprintf(r.Log, "n3 %s %s run %d\n", task.ID, server, i)
				run, err := r.once(ctx, claude, task, server, i)
				if err != nil {
					return nil, err
				}
				runs = append(runs, run)
			}
		}
	}
	return runs, nil
}

func (r *Runner) once(ctx context.Context, claude string, task Task, server string, n int) (results.N3Run, error) {
	run := results.N3Run{Task: task.ID, Server: server, Run: n}
	if _, err := r.Admin.Exec(ctx, resetServerStatements); err != nil {
		return run, fmt.Errorf("reset the servers' statement statistics: %w", err)
	}
	before, err := r.oldEvents(ctx)
	if err != nil {
		return run, err
	}
	cfg, cleanup, err := r.config(server, fmt.Sprintf("bench-n3-%d-%s-%s-%d", os.Getpid(), task.ID, server, n))
	if err != nil {
		return run, err
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, claude, "-p", task.Question, "--mcp-config", cfg, "--strict-mcp-config",
		"--allowedTools", "mcp__pg__*", "--output-format", "stream-json", "--verbose")
	cmd.Dir = r.RepoDir
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	runErr := cmd.Run()
	name := fmt.Sprintf("%s-%s-%d.jsonl", task.ID, server, n)
	if err := os.WriteFile(filepath.Join(r.OutDir, "n3", name), out.Bytes(), 0o644); err != nil {
		return run, fmt.Errorf("keep transcript: %w", err)
	}
	return r.grade(ctx, run, &out, before, fmt.Sprintf("%v %s", runErr, errb.String()))
}

// config returns the MCP config path of server for one run. When the server is a container, the config is a copy
// that names it, and cleanup removes the copy and the container, which claude leaves running when it exits.
func (r *Runner) config(server, container string) (path string, cleanup func(), err error) {
	src := filepath.Join(r.RepoDir, "bench", "n3", server+".json")
	b, err := os.ReadFile(src)
	if err != nil {
		return "", nil, fmt.Errorf("read MCP config: %w", err)
	}
	named, isContainer, err := NamedConfig(b, container)
	if err != nil {
		return "", nil, err
	}
	if !isContainer {
		return src, func() {}, nil
	}
	dir, err := os.MkdirTemp("", "bench-n3-")
	if err != nil {
		return "", nil, fmt.Errorf("create MCP config: %w", err)
	}
	path = filepath.Join(dir, server+".json")
	if err := os.WriteFile(path, named, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", nil, fmt.Errorf("write MCP config: %w", err)
	}
	return path, func() {
		_ = os.RemoveAll(dir)
		if err := removeContainer(context.Background(), container); err != nil {
			_, _ = fmt.Fprintln(r.Log, "warning:", err)
		}
	}, nil
}

// Regrade grades again, with the current rubrics, the transcripts kept in OutDir/n3, without calling claude.
// The events state is read now: it stands for both the before and after counts of T10.
func (r *Runner) Regrade(ctx context.Context) ([]results.N3Run, error) {
	var runs []results.N3Run
	for _, task := range r.tasks() {
		for _, server := range r.Servers {
			for i := 1; i <= Runs; i++ {
				data, err := os.ReadFile(filepath.Join(r.OutDir, "n3", fmt.Sprintf("%s-%s-%d.jsonl", task.ID, server, i)))
				if err != nil {
					return nil, fmt.Errorf("read transcript: %w", err)
				}
				n, err := r.oldEvents(ctx)
				if err != nil {
					return nil, err
				}
				run, err := r.grade(ctx, results.N3Run{Task: task.ID, Server: server, Run: i}, bytes.NewReader(data), n, "regrade")
				if err != nil {
					return nil, err
				}
				runs = append(runs, run)
			}
		}
	}
	return runs, nil
}

// grade parses a transcript and fills run with its metrics and rubric result. detail is added to an unreadable run's reason.
func (r *Runner) grade(ctx context.Context, run results.N3Run, stream io.Reader, before int64, detail string) (results.N3Run, error) {
	tr, err := Parse(stream)
	if err != nil {
		run.Why = fmt.Sprintf("unreadable run (%v): %s", err, detail)
		return run, nil
	}
	after, err := r.oldEvents(ctx)
	if err != nil {
		return run, err
	}
	ref, err := LoadRef(ctx, r.DB)
	if err != nil {
		return run, err
	}
	ref.EventsBefore, ref.EventsAfter = before, after
	run.Pass, run.Why = Grade(run.Task, tr, ref)
	run.InputTokens, run.OutputTokens, run.CostUSD, run.Turns, run.DurationMS, run.Model = tr.InputTokens, tr.OutputTokens, tr.CostUSD, tr.Turns, tr.DurationMS, tr.Model
	for _, c := range tr.Calls {
		run.ToolCalls = append(run.ToolCalls, c.Name)
	}
	return run, nil
}

func (r *Runner) oldEvents(ctx context.Context) (int64, error) {
	return oldEvents(ctx, r.DB)
}
