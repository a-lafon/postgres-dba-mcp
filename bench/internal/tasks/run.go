package tasks

import (
	"bytes"
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
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/load"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// The agent, the same for every server (PROTOCOL.md §4.3, decision D1).
const (
	Model    = "sonnet" // Claude Sonnet 5.5; the stream records the exact model of each run
	Effort   = "medium"
	MaxTurns = 40
	Timeout  = 10 * time.Minute
)

// Run is one cell of the matrix: a task, an arm, a server and a repetition.
type Run struct {
	Task   Task
	Arm    string
	Server servers.Pinned
	Rep    int
}

// Key names the files of the run.
func (r Run) Key() string { return results.TaskKey(r.Task.ID, r.Arm, r.Server.Name, r.Rep) }

// URI is the database as the server's container sees it: bench_ro, or bench_owner in arm W.
func (r Run) URI() string {
	if r.Arm == results.ArmW {
		return servers.BenchOwnerURI
	}
	return servers.BenchURI
}

// Plan orders the runs: task by task, the arm ro then W, repetition by repetition, and within a repetition every
// server in turn, so that no server meets a different state of the model service (PROTOCOL.md §4.3). arm is ro, w or
// all; arm W holds A14 and A15 only.
func Plan(ts []Task, srvs []servers.Pinned, reps int, arm string) []Run {
	var plan []Run
	for _, t := range ts {
		for _, a := range []string{results.ArmRO, results.ArmW} {
			if (arm != "all" && arm != a) || (a == results.ArmW && !slices.Contains(ArmW, t.ID)) {
				continue
			}
			for rep := 1; rep <= reps; rep++ {
				for _, s := range srvs {
					plan = append(plan, Run{Task: t, Arm: a, Server: s, Rep: rep})
				}
			}
		}
	}
	return plan
}

// ClaudeArgs are the arguments of `claude` for one run: the question verbatim, the agent of §4.3, only the server's
// tools (no built-in tool, no other MCP server), and no settings file of the user, the project or the folder.
func ClaudeArgs(question, mcpConfig string) []string {
	return []string{"-p", question,
		"--model", Model, "--effort", Effort, "--max-turns", strconv.Itoa(MaxTurns),
		"--output-format", "stream-json", "--verbose",
		"--mcp-config", mcpConfig, "--strict-mcp-config",
		"--allowedTools", "mcp__pg__*", "--tools", "",
		"--setting-sources", "", "--no-session-persistence"}
}

// ClaudeEnv is added to the environment of claude: no CLAUDE.md of any level (managed, user, project) is read, and no
// auto-memory.
var ClaudeEnv = []string{"CLAUDE_CODE_DISABLE_CLAUDE_MDS=1", "CLAUDE_CODE_DISABLE_AUTO_MEMORY=1"}

// MCPConfig is the --mcp-config of a run: one server, pg, started in its container as the registry says.
func MCPConfig(spec servers.Spec) ([]byte, error) {
	cfg := map[string]any{"mcpServers": map[string]any{"pg": map[string]any{"command": spec.Argv[0], "args": spec.Argv[1:]}}}
	return json.MarshalIndent(cfg, "", "  ")
}

// Runner runs a plan. It needs the claude CLI, a model account, Docker and the bench database with its snapshot.
type Runner struct {
	BenchDir string      // the bench folder, for compose.yml
	OutDir   string      // the run folder: records and transcripts go to OutDir/tasks
	Env      results.Env // written in tasks.json
	Log      io.Writer
	Claude   string // path of the claude CLI

	// exec runs one cell; nil is the real run. Tests replace it.
	exec   func(context.Context, Run) (results.TaskRun, error)
	ref    *Ref   // computed after the first restore: every restore gives the same database
	before *State // likewise
}

// Done reports whether the run already has a record, which resume skips.
func (r *Runner) Done(run Run) bool {
	_, err := os.Stat(filepath.Join(r.OutDir, results.TasksDir, run.Key()+".json"))
	return err == nil
}

// Run runs every cell of plan that has no record yet, saves each record as soon as it exists, and rewrites tasks.json
// after each one. An interrupted plan continues where it stopped when run again on the same folder.
func (r *Runner) Run(ctx context.Context, plan []Run) error {
	do := r.exec
	if do == nil {
		do = r.once
	}
	for i, run := range plan {
		if r.Done(run) {
			continue
		}
		_, _ = fmt.Fprintf(r.Log, "tasks %d/%d %s\n", i+1, len(plan), run.Key())
		rec, err := do(ctx, run)
		if err != nil {
			return fmt.Errorf("%s: %w", run.Key(), err)
		}
		if err := results.SaveTaskRun(r.OutDir, rec); err != nil {
			return err
		}
		runs, err := results.LoadTaskRuns(r.OutDir)
		if err != nil {
			return err
		}
		if err := results.Save(r.OutDir, "tasks", results.Tasks{Env: r.Env, Runs: runs}); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(r.Log, "  solved=%v turns=%d tokens in=%d out=%d cost=%.4f wall=%s db_changes=%d %s\n", rec.Solved, rec.Turns,
			rec.Tokens.InputTotal(), rec.Tokens.Output, rec.CostUSD, time.Duration(rec.WallMS)*time.Millisecond, len(rec.DBChanges), rec.Error)
	}
	return nil
}

// once restores the database, sets up what the task needs, runs claude, then grades the run and diffs the database.
// An error is a failure of the harness, and the run is not recorded; a failure of the agent is a record.
func (r *Runner) once(ctx context.Context, run Run) (rec results.TaskRun, err error) {
	rec = results.TaskRun{Task: run.Task.ID, Server: run.Server.Name, Arm: run.Arm, Rep: run.Rep,
		StartedAt: time.Now().UTC().Format(time.RFC3339), Transcript: filepath.ToSlash(filepath.Join(results.TasksDir, run.Key()+".jsonl"))}
	if err := load.Restore(ctx, r.BenchDir, r.Log); err != nil {
		return rec, err
	}
	admin, err := pgx.Connect(ctx, servers.BenchAdminDSN)
	if err != nil {
		return rec, fmt.Errorf("connect as postgres: %w", err)
	}
	defer func() { _ = admin.Close(context.Background()) }()
	if r.ref == nil {
		ref, err := LoadRef(ctx, admin)
		if err != nil {
			return rec, err
		}
		st, err := TakeState(ctx, admin)
		if err != nil {
			return rec, err
		}
		r.ref, r.before = &ref, &st
	}
	ref := *r.ref
	var lc *LockChain
	if run.Task.ID == "A13" {
		if lc, err = StartLockChain(ctx, servers.BenchOwnerDSN); err != nil {
			return rec, err
		}
		ref.LockHolderPID, ref.LockHolderQuery, ref.LockWaiterPIDs, ref.LockTable = lc.Holder, holderQuery, lc.Waiters, lockTable
	}
	stopLock := func() {
		if lc != nil {
			lc.Stop()
			lc = nil
		}
	}
	defer stopLock()

	dir := filepath.Join(r.OutDir, results.TasksDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return rec, err
	}
	spec := run.Server.Spec(run.URI())
	defer spec.Cleanup()
	cfg, err := MCPConfig(spec)
	if err != nil {
		return rec, err
	}
	cfgPath := filepath.Join(dir, run.Key()+".mcp.json")
	if err := os.WriteFile(cfgPath, cfg, 0o600); err != nil {
		return rec, err
	}
	// An empty working folder: no CLAUDE.md, .claude or .mcp.json to find in it or its parents.
	cwd, err := os.MkdirTemp("", "bench-tasks-")
	if err != nil {
		return rec, err
	}
	defer func() { _ = os.RemoveAll(cwd) }()
	out, err := os.Create(filepath.Join(r.OutDir, filepath.FromSlash(rec.Transcript)))
	if err != nil {
		return rec, err
	}
	defer func() { _ = out.Close() }()

	tctx, cancel := context.WithTimeout(ctx, Timeout)
	defer cancel()
	cmd := exec.CommandContext(tctx, r.Claude, ClaudeArgs(run.Task.Question, cfgPath)...)
	cmd.Dir, cmd.Env, cmd.Stdout = cwd, append(os.Environ(), ClaudeEnv...), out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	cmd.WaitDelay = 10 * time.Second
	start := time.Now()
	runErr := cmd.Run()
	rec.WallMS = time.Since(start).Milliseconds()
	if ctx.Err() != nil { // interrupted: the run is not recorded and resume runs it again
		return rec, ctx.Err()
	}
	spec.Cleanup()
	stopLock()

	after, err := TakeState(ctx, admin)
	if err != nil {
		return rec, err
	}
	rec.DBChanges = Changes(*r.before, after)

	if _, err := out.Seek(0, io.SeekStart); err != nil {
		return rec, err
	}
	tr, perr := agent.Parse(out)
	switch {
	case errors.Is(tctx.Err(), context.DeadlineExceeded):
		rec.Error = fmt.Sprintf("timeout after %s", Timeout)
	case perr != nil:
		rec.Error = fmt.Sprintf("unreadable stream (%v); claude: %v %s", perr, runErr, tail(stderr.String(), 2000))
	}
	if rec.Error != "" {
		tr.IsError = true
	}
	rec.Checks = Grade(run.Task.ID, tr, ref, rec.DBChanges)
	rec.Solved = Solved(run.Task.ID, run.Arm, rec.Checks)
	rec.Facts = Facts(run.Task.ID, ref)
	rec.Final, rec.Turns, rec.CostUSD, rec.Model = tr.Final, tr.Turns, tr.CostUSD, tr.Model
	rec.Tokens = results.TaskTokens{Input: tr.Usage.Input, CacheCreation: tr.Usage.CacheCreation, CacheRead: tr.Usage.CacheRead, Output: tr.Usage.Output}
	rec.ToolCalls = []results.TaskToolCall{}
	for _, c := range tr.Calls {
		rec.ToolCalls = append(rec.ToolCalls, results.TaskToolCall{Name: c.Name, Args: c.Input, IsError: c.IsError})
		if c.IsError {
			rec.ToolErrors++
		}
	}
	return rec, nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return "…" + s[len(s)-n:]
	}
	return s
}

// DryRun writes, for every run of plan, the exact claude command line and the MCP config it would get, without
// restoring the database or calling the model. The container name in each config changes at every real run.
func DryRun(w io.Writer, outDir string, plan []Run) error {
	for _, run := range plan {
		cfg, err := MCPConfig(run.Server.Spec(run.URI()))
		if err != nil {
			return err
		}
		cfgPath := filepath.Join(outDir, results.TasksDir, run.Key()+".mcp.json")
		var compact bytes.Buffer
		if err := json.Compact(&compact, cfg); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(w, "# %s\n%s %s\n%s\n", run.Key(), strings.Join(ClaudeEnv, " "), shellJoin(append([]string{"claude"}, ClaudeArgs(run.Task.Question, cfgPath)...)), compact.String())
	}
	return nil
}

var reShellSafe = regexp.MustCompile(`^[A-Za-z0-9_./=:,@%+-]+$`)

func shellJoin(args []string) string {
	q := make([]string, len(args))
	for i, a := range args {
		if reShellSafe.MatchString(a) {
			q[i] = a
		} else {
			q[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
	}
	return strings.Join(q, " ")
}

// Regrade grades again the recorded runs of outDir whose task is in ts, from their stored transcripts and database
// changes, with the rubrics and facts of now; ref is the reference of the restored database. It calls neither claude
// nor a server. It rewrites each record and tasks.json, and returns every record of the folder.
func Regrade(outDir string, ref Ref, ts []Task) ([]results.TaskRun, error) {
	runs, err := results.LoadTaskRuns(outDir)
	if err != nil {
		return nil, err
	}
	for _, rec := range runs {
		if !slices.ContainsFunc(ts, func(t Task) bool { return t.ID == rec.Task }) {
			continue
		}
		f, err := os.Open(filepath.Join(outDir, filepath.FromSlash(rec.Transcript)))
		if err != nil {
			return nil, err
		}
		tr, perr := agent.Parse(f)
		_ = f.Close()
		tr.IsError = tr.IsError || perr != nil || rec.Error != ""
		r := ref
		if rec.Task == "A13" { // the lock chain is per run: its facts hold it
			if r.LockHolderPID, r.LockTable, r.LockWaiterPIDs, err = lockFromFacts(rec.Facts); err != nil {
				return nil, fmt.Errorf("%s: %w", rec.Key(), err)
			}
			r.LockHolderQuery = holderQuery
		}
		rec.Checks = Grade(rec.Task, tr, r, rec.DBChanges)
		rec.Solved = Solved(rec.Task, rec.Arm, rec.Checks)
		rec.Facts = Facts(rec.Task, r)
		if err := results.SaveTaskRun(outDir, rec); err != nil {
			return nil, err
		}
	}
	var file results.Tasks
	if _, err := results.Load(outDir, "tasks", &file); err != nil {
		return nil, err
	}
	if file.Runs, err = results.LoadTaskRuns(outDir); err != nil {
		return nil, err
	}
	return file.Runs, results.Save(outDir, "tasks", file)
}

// lockFromFacts reads back the lock chain of an A13 run from the facts Facts wrote for it.
func lockFromFacts(facts []results.Fact) (holder int32, table string, waiters []int32, err error) {
	for _, f := range facts {
		if strings.HasPrefix(f.Text, "Session ") && holder == 0 {
			if _, err := fmt.Sscanf(f.Text, "Session %d holds a row lock on %s", &holder, &table); err != nil {
				return 0, "", nil, fmt.Errorf("lock holder fact %q: %w", f.Text, err)
			}
		}
		if head, _, ok := strings.Cut(f.Text, " (application_name checkout)"); ok && strings.HasPrefix(head, "Sessions ") {
			for _, p := range regexp.MustCompile(`\d+`).FindAllString(head, -1) {
				n, _ := strconv.Atoi(p)
				waiters = append(waiters, int32(n))
			}
		}
	}
	if holder == 0 || table == "" {
		return 0, "", nil, errors.New("no lock holder in the recorded facts")
	}
	return holder, table, waiters, nil
}

// Table is the solved count per task and arm (rows) and server (columns), as markdown.
func Table(runs []results.TaskRun) string {
	var srvs, rows []string
	type cell struct{ solved, n int }
	cells := map[string]*cell{}
	for _, r := range runs {
		row := r.Task + " " + r.Arm
		if !slices.Contains(srvs, r.Server) {
			srvs = append(srvs, r.Server)
		}
		if !slices.Contains(rows, row) {
			rows = append(rows, row)
		}
		c := cells[row+"|"+r.Server]
		if c == nil {
			c = &cell{}
			cells[row+"|"+r.Server] = c
		}
		c.n++
		if r.Solved {
			c.solved++
		}
	}
	var sb strings.Builder
	sb.WriteString("| task | " + strings.Join(srvs, " | ") + " |\n|---|" + strings.Repeat("---|", len(srvs)) + "\n")
	total := map[string]*cell{}
	for _, row := range rows {
		sb.WriteString("| " + row + " |")
		for _, s := range srvs {
			c := cells[row+"|"+s]
			if c == nil {
				sb.WriteString(" - |")
				continue
			}
			if total[s] == nil {
				total[s] = &cell{}
			}
			total[s].solved, total[s].n = total[s].solved+c.solved, total[s].n+c.n
			fmt.Fprintf(&sb, " %d/%d |", c.solved, c.n)
		}
		sb.WriteString("\n")
	}
	sb.WriteString("| solved |")
	for _, s := range srvs {
		fmt.Fprintf(&sb, " %d/%d |", total[s].solved, total[s].n)
	}
	return sb.String() + "\n"
}
