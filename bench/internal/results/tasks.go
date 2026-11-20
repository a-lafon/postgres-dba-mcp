package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Arms of part A (PROTOCOL.md §4.3): every task as bench_ro, and A14 and A15 again as bench_owner.
const (
	ArmRO = "ro"
	ArmW  = "w"
)

// TasksDir is the folder of a run that holds one record and one transcript per part-A run.
const TasksDir = "tasks"

// Fact is one ground-truth statement of a part-A task for the judge (PROTOCOL.md §7): a fact a right answer states,
// or, when Wrong, a claim a right answer must not make. Facts are computed from the database before the run.
type Fact struct {
	Text  string `json:"text"`
	Wrong bool   `json:"wrong,omitempty"`
}

// Check is one item of a task's deterministic rubric.
type Check struct {
	Name   string `json:"name"`
	Pass   bool   `json:"pass"`
	Detail string `json:"detail,omitempty"` // what was missing or wrong, empty when it passes
}

// TaskToolCall is one MCP tool call of a run, without the mcp__pg__ prefix.
type TaskToolCall struct {
	Name    string          `json:"name"`
	Args    json.RawMessage `json:"args"`
	IsError bool            `json:"is_error,omitempty"`
}

// TaskTokens are the tokens of a run, summed over its turns: the usage of the stream's result event.
type TaskTokens struct {
	Input         int `json:"input"`          // uncached input
	CacheCreation int `json:"cache_creation"` // input written to the cache
	CacheRead     int `json:"cache_read"`     // input read from the cache
	Output        int `json:"output"`
}

// InputTotal is the input the model received: uncached, cache creation and cache read.
func (t TaskTokens) InputTotal() int { return t.Input + t.CacheCreation + t.CacheRead }

// TaskRun is one `claude -p` run of a part-A task on one server, in one arm, at one repetition.
type TaskRun struct {
	Task       string         `json:"task"`
	Server     string         `json:"server"`
	Arm        string         `json:"arm"`
	Rep        int            `json:"rep"`
	Solved     bool           `json:"solved"` // every check passed
	Checks     []Check        `json:"checks"`
	DBChanges  []string       `json:"db_changes"` // what differs from the snapshot after the run; must be empty
	Tokens     TaskTokens     `json:"tokens"`
	Turns      int            `json:"turns"`
	ToolCalls  []TaskToolCall `json:"tool_calls"`
	ToolErrors int            `json:"tool_errors"`
	WallMS     int64          `json:"wall_ms"`        // harness clock, from the start of claude to its exit
	CostUSD    float64        `json:"total_cost_usd"` // as claude -p computes it
	Model      string         `json:"model,omitempty"`
	Final      string         `json:"final"`                // the answer text
	Error      string         `json:"error,omitempty"`      // why the run has no usable result (timeout, unreadable stream)
	Transcript string         `json:"transcript"`           // stream-json file, relative to the run folder
	Facts      []Fact         `json:"facts"`                // the judge's ground truth for this run (A13's depends on the run)
	StartedAt  string         `json:"started_at,omitempty"` // RFC 3339, UTC
}

// Key names the run's files: <task>-<arm>-<server>-<rep>.
func (r TaskRun) Key() string { return TaskKey(r.Task, r.Arm, r.Server, r.Rep) }

// TaskKey is the file stem of one run.
func TaskKey(task, arm, server string, rep int) string {
	return fmt.Sprintf("%s-%s-%s-%d", task, arm, server, rep)
}

// Tasks is tasks.json: every recorded part-A run of the run folder.
type Tasks struct {
	Env  Env       `json:"env"`
	Runs []TaskRun `json:"runs"`
}

// SaveTaskRun writes the record of one run to <dir>/tasks/<key>.json. A record on disk marks the run done.
func SaveTaskRun(dir string, r TaskRun) error {
	if err := os.MkdirAll(filepath.Join(dir, TasksDir), 0o755); err != nil {
		return fmt.Errorf("create %s folder: %w", TasksDir, err)
	}
	b, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run %s: %w", r.Key(), err)
	}
	// Written then renamed, so an interrupted run never leaves a half record that resume would skip.
	tmp := filepath.Join(dir, TasksDir, r.Key()+".json.tmp")
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write run %s: %w", r.Key(), err)
	}
	return os.Rename(tmp, filepath.Join(dir, TasksDir, r.Key()+".json"))
}

// LoadTaskRuns reads every run record of <dir>/tasks, sorted by task, arm, server and rep. A missing folder is no runs.
func LoadTaskRuns(dir string) ([]TaskRun, error) {
	ents, err := os.ReadDir(filepath.Join(dir, TasksDir))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", TasksDir, err)
	}
	var runs []TaskRun
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, TasksDir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("read run %s: %w", e.Name(), err)
		}
		var r TaskRun
		if err := json.Unmarshal(b, &r); err != nil {
			return nil, fmt.Errorf("decode run %s: %w", e.Name(), err)
		}
		runs = append(runs, r)
	}
	sort.Slice(runs, func(i, j int) bool {
		a, b := runs[i], runs[j]
		if a.Task != b.Task {
			return a.Task < b.Task
		}
		if a.Arm != b.Arm {
			return a.Arm < b.Arm
		}
		if a.Server != b.Server {
			return a.Server < b.Server
		}
		return a.Rep < b.Rep
	})
	return runs, nil
}
