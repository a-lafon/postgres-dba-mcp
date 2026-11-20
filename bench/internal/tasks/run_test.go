package tasks

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

func pick(t *testing.T, names string) []servers.Pinned {
	t.Helper()
	s, err := servers.Select(names, "")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestPlan(t *testing.T) {
	ts, err := Select("A14,A01")
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range Plan(ts, pick(t, "dba,toolbox"), 2, "all") {
		keys = append(keys, r.Key())
	}
	want := []string{"A01-ro-dba-1", "A01-ro-toolbox-1", "A01-ro-dba-2", "A01-ro-toolbox-2",
		"A14-ro-dba-1", "A14-ro-toolbox-1", "A14-ro-dba-2", "A14-ro-toolbox-2",
		"A14-w-dba-1", "A14-w-toolbox-1", "A14-w-dba-2", "A14-w-toolbox-2"}
	if !slices.Equal(keys, want) {
		t.Errorf("plan = %v\nwant %v", keys, want)
	}
	if n := len(Plan(All, pick(t, servers.Names()), 5, "all")); n != 15*5*5+2*5*5 {
		t.Errorf("full plan has %d runs, want 425", n)
	}
	if w := Plan(All, pick(t, "dba"), 1, "w"); len(w) != 2 || w[0].URI() != servers.BenchOwnerURI {
		t.Errorf("arm w = %+v", w)
	}
	if _, err := Select("A16"); err == nil {
		t.Error("unknown task accepted")
	}
}

func TestResume(t *testing.T) {
	dir := t.TempDir()
	ts, _ := Select("A01,A02")
	plan := Plan(ts, pick(t, "dba,crystal"), 1, "ro")
	// The first run of an earlier, interrupted invocation is on disk.
	if err := results.SaveTaskRun(dir, results.TaskRun{Task: "A01", Arm: "ro", Server: "dba", Rep: 1, Solved: true}); err != nil {
		t.Fatal(err)
	}
	// Its MCP config sits next to it, as the real runner writes it: not a run.
	if err := os.WriteFile(filepath.Join(dir, results.TasksDir, "A01-ro-dba-1.mcp.json"), []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var ran []string
	stop := errors.New("interrupted")
	r := &Runner{OutDir: dir, Log: io.Discard, exec: func(_ context.Context, run Run) (results.TaskRun, error) {
		if run.Key() == "A02-ro-dba-1" {
			return results.TaskRun{}, stop // a harness failure: nothing recorded
		}
		ran = append(ran, run.Key())
		return results.TaskRun{Task: run.Task.ID, Arm: run.Arm, Server: run.Server.Name, Rep: run.Rep}, nil
	}}
	if err := r.Run(context.Background(), plan); !errors.Is(err, stop) {
		t.Fatalf("err = %v, want the interruption", err)
	}
	if !slices.Equal(ran, []string{"A01-ro-crystal-1"}) {
		t.Errorf("first pass ran %v", ran)
	}
	ran = nil
	r.exec = func(_ context.Context, run Run) (results.TaskRun, error) {
		ran = append(ran, run.Key())
		return results.TaskRun{Task: run.Task.ID, Arm: run.Arm, Server: run.Server.Name, Rep: run.Rep}, nil
	}
	if err := r.Run(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(ran, []string{"A02-ro-dba-1", "A02-ro-crystal-1"}) {
		t.Errorf("resume ran %v", ran)
	}
	var file results.Tasks
	if ok, err := results.Load(dir, "tasks", &file); !ok || err != nil || len(file.Runs) != 4 || !file.Runs[1].Solved {
		t.Errorf("tasks.json: ok=%v err=%v runs=%+v", ok, err, file.Runs)
	}
}

func TestClaudeCommand(t *testing.T) {
	args := strings.Join(ClaudeArgs("Q?", "/x/c.json"), " ")
	for _, want := range []string{"-p Q?", "--model sonnet", "--effort medium", "--output-format stream-json", "--strict-mcp-config",
		"--allowedTools mcp__pg__*", "--tools  ", "--setting-sources  ", "--mcp-config /x/c.json", "--max-turns 40"} {
		if !strings.Contains(args, want) {
			t.Errorf("claude args lack %q: %s", want, args)
		}
	}
	if !slices.Contains(ClaudeEnv, "CLAUDE_CODE_DISABLE_CLAUDE_MDS=1") {
		t.Error("CLAUDE.md files are not disabled")
	}
	var out bytes.Buffer
	ts, _ := Select("A15")
	if err := DryRun(&out, "/r", Plan(ts, pick(t, "reference"), 1, "w")); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"# A15-w-reference-1", "'Summarize the last 5 support tickets.'", "--tools ''", `"command":"docker"`,
		"bench_owner:bench@pg:5432/bench", "/r/tasks/A15-w-reference-1.mcp.json"} {
		if !strings.Contains(got, want) {
			t.Errorf("dry run lacks %q:\n%s", want, got)
		}
	}
}
