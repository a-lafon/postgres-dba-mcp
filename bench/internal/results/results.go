// Package results defines the JSON each bench level writes to bench/results/<run-id>/<level>.json, and reads it back
// for the report.
package results

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Server names used as keys in every result.
const (
	DBA     = "dba"
	Crystal = "crystal"
	PgEdge  = "pgedge"
)

// Token counter labels.
const (
	CounterAPI      = "count_tokens"
	CounterEstimate = "estimate"
	CounterClaude   = "claude -p"
)

// Env describes the environment a level ran in; every level result embeds it.
type Env struct {
	RunID         string  `json:"run_id"`
	Scale         float64 `json:"scale"`
	Pagila        bool    `json:"pagila"`
	TokenCounter  string  `json:"token_counter"`
	Model         string  `json:"model"`
	DBAVersion    string  `json:"dba_version"`
	DBACommit     string  `json:"dba_commit"`
	CrystalDigest string  `json:"crystal_digest"`
	PGVersion     string  `json:"pg_version"`
	PgEdge        bool    `json:"pgedge"`
}

// N1Server is the tool-definition cost of one server.
type N1Server struct {
	Server   string `json:"server"`
	Tools    int    `json:"tools"`
	Full     int    `json:"full"`
	Deferred int    `json:"deferred"`
}

// N1 is n1.json.
type N1 struct {
	Env     Env        `json:"env"`
	Servers []N1Server `json:"servers"`
}

// Cell statuses of an N2 cell.
const (
	StatusOK        = "ok"
	StatusTimeout   = "failed (timeout)"
	StatusOversized = "failed (oversized)"
	StatusError     = "failed (error)"
	StatusSkipped   = "skipped"
)

// Truncation states of an N2 cell.
const (
	TruncNone      = "none"
	TruncSignalled = "signalled"
	TruncMissing   = "missing"
)

// N2Cell is the outcome of one scenario on one server.
type N2Cell struct {
	Status        string    `json:"status"`
	Tokens        int       `json:"tokens"`
	Bytes         int       `json:"bytes"`
	P50MS         float64   `json:"p50_ms"`
	P95MS         float64   `json:"p95_ms"`
	Correct       bool      `json:"correct"`
	Missing       []string  `json:"missing,omitempty"`
	FalseRefusal  bool      `json:"false_refusal,omitempty"`
	Truncation    string    `json:"truncation"`
	TruncExpected bool      `json:"trunc_expected,omitempty"`
	Detected      []string  `json:"detected,omitempty"`
	Advice        *Advice   `json:"advice,omitempty"`
	Note          string    `json:"note,omitempty"`
	TokenRuns     []int     `json:"token_runs,omitempty"`
	Estimated     bool      `json:"estimated,omitempty"` // tokens are bytes/4 because the text was too large for claude -p
	LatencyMS     []float64 `json:"latency_ms,omitempty"`
}

// Advice is what an index-advisor response recommends.
type Advice struct {
	Indexes []string `json:"indexes"`
	Change  float64  `json:"change"` // fraction, -0.99 for -99 %
	None    bool     `json:"none"`
}

// N2Row is one scenario with its cell per server.
type N2Row struct {
	ID    string            `json:"id"`
	Tool  string            `json:"tool"`
	Cells map[string]N2Cell `json:"cells"`
}

// N2 is n2.json.
type N2 struct {
	Env      Env     `json:"env"`
	MaxBytes int     `json:"max_bytes"`
	SameRole bool    `json:"same_role"`
	Rows     []N2Row `json:"rows"`
}

// Security classes of one case.
const (
	ClassRefused  = "refused"
	ClassFailed   = "failed at execution"
	ClassExecuted = "executed"
)

// SecurityCase is the outcome of one attack case on one server.
type SecurityCase struct {
	ID         string `json:"id"`
	Category   string `json:"category"`
	Class      string `json:"class"`
	SideEffect string `json:"side_effect,omitempty"`
	Text       string `json:"text,omitempty"`
}

// Security is security.json.
type Security struct {
	Env     Env                       `json:"env"`
	Attacks map[string][]SecurityCase `json:"attacks"`
	Legit   []SecurityCase            `json:"legit"`
}

// N3Run is one `claude -p` run.
type N3Run struct {
	Task         string   `json:"task"`
	Server       string   `json:"server"`
	Run          int      `json:"run"`
	InputTokens  int      `json:"input_tokens"`
	OutputTokens int      `json:"output_tokens"`
	CostUSD      float64  `json:"cost_usd"`
	Turns        int      `json:"turns"`
	Model        string   `json:"model,omitempty"` // model the session ran on, from the stream
	DurationMS   int      `json:"duration_ms"`
	ToolCalls    []string `json:"tool_calls"`
	Pass         bool     `json:"pass"`
	Why          string   `json:"why,omitempty"`
}

// N3 is n3.json.
type N3 struct {
	Env  Env     `json:"env"`
	Runs []N3Run `json:"runs"`
}

// JudgeScores are the marks the judge gave one answer.
type JudgeScores struct {
	Correctness   int      `json:"correctness"`
	Completeness  int      `json:"completeness"`
	Actionability int      `json:"actionability"`
	Errors        []string `json:"errors"` // factual errors and hallucinations, checked against the facts
}

// JudgeOrder is one judge call: the answers of the two servers in one A/B order.
type JudgeOrder struct {
	A      string                 `json:"a"`      // server whose answer was shown as A
	Winner string                 `json:"winner"` // server name, or "tie"
	Scores map[string]JudgeScores `json:"scores"` // by server
	Reason string                 `json:"reason"`
}

// JudgePair is the verdict on the answers of both servers to one task at one run index.
type JudgePair struct {
	Task    string        `json:"task"`
	Run     int           `json:"run"`
	Outcome string        `json:"outcome"`           // server that won in both orders, or "tie"
	Orders  [2]JudgeOrder `json:"orders"`            // of the first pass; the scores and the outcome come from it
	Repeats []string      `json:"repeats,omitempty"` // outcome of every pass, the first included, when judged more than once
}

// Origins of an error an answer contains, as the attribution pass classifies them.
const (
	OriginTool        = "tool"        // the tool output was wrong, ambiguous or lossy and the answer relayed it
	OriginMisread     = "misread"     // the tool output said otherwise
	OriginUnsupported = "unsupported" // nothing in the tool results or the facts supports it
	OriginNone        = "none"        // not an error after all
)

// AttributedError is one error of an answer with the origin the attribution pass found for it.
type AttributedError struct {
	Claim  string `json:"claim"`
	Origin string `json:"origin"`
	Call   int    `json:"call,omitempty"` // 1-based tool call the error comes from, for tool and misread
	Tool   string `json:"tool,omitempty"` // name of that call
	Why    string `json:"why"`
}

// JudgeAttribution is the attribution of the errors of one answer: the server, task and run index, the transcript
// it was read from, and the errors with their origin.
type JudgeAttribution struct {
	Task   string            `json:"task"`
	Run    int               `json:"run"`
	Server string            `json:"server"`
	File   string            `json:"file"` // transcript file name, under n3/
	Errors []AttributedError `json:"errors"`
}

// Judge is judge.json.
type Judge struct {
	Model        string             `json:"model"`
	Effort       string             `json:"effort,omitempty"` // BENCH_JUDGE_EFFORT; empty means the account default
	CostUSD      float64            `json:"cost_usd"`
	Pairs        []JudgePair        `json:"pairs"`
	Attributions []JudgeAttribution `json:"attributions,omitempty"`
}

// Measure is one ops value: a number, or a skip.
type Measure struct {
	Value   float64 `json:"value"`
	Skipped bool    `json:"skipped,omitempty"`
	Note    string  `json:"note,omitempty"`
	// Samples are the readings whose median is Value; absent in an ops.json written before the median.
	Samples []float64 `json:"samples,omitempty"`
}

// Ops is ops.json. Maps are keyed by server name.
type Ops struct {
	Env         Env                `json:"env"`
	ColdStartMS map[string]Measure `json:"cold_start_ms"`
	// RSSMB is the memory after the S02 calls that the target reads: docker stats, for postgres-dba-mcp the linux image.
	RSSMB map[string]Measure `json:"rss_mb"`
	// NativeRSSMB is postgres-dba-mcp's native binary after the same calls (informational).
	NativeRSSMB Measure            `json:"native_rss_mb"`
	SizeMB      map[string]Measure `json:"size_mb"`
	Concurrency struct {
		Calls   int  `json:"calls"`
		Errors  int  `json:"errors"`
		Skipped bool `json:"skipped,omitempty"`
	} `json:"concurrency"`
}

// NewRunID returns a UTC timestamp run id, YYYYMMDDTHHMMSSZ.
func NewRunID() string { return time.Now().UTC().Format("20060102T150405Z") }

// Dir returns the folder of a run under root, created when missing.
func Dir(root, runID string) (string, error) {
	d := filepath.Join(root, runID)
	if err := os.MkdirAll(d, 0o755); err != nil {
		return "", fmt.Errorf("create run folder: %w", err)
	}
	return d, nil
}

// Latest returns the run id sorting last under root, or "" when there is none.
func Latest(root string) (string, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("list runs: %w", err)
	}
	var ids []string
	for _, e := range ents {
		if e.IsDir() {
			ids = append(ids, e.Name())
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "", nil
	}
	return ids[len(ids)-1], nil
}

// Save writes v as <dir>/<level>.json.
func Save(dir, level string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s: %w", level, err)
	}
	if err := os.WriteFile(filepath.Join(dir, level+".json"), append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("write %s: %w", level, err)
	}
	return nil
}

// Load reads <dir>/<level>.json into v. It reports false, with no error, when the file does not exist.
func Load(dir, level string, v any) (bool, error) {
	b, err := os.ReadFile(filepath.Join(dir, level+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("read %s: %w", level, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, fmt.Errorf("decode %s: %w", level, err)
	}
	return true, nil
}
