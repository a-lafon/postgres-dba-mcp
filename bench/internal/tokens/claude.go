package tokens

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// maxClaudeText is the largest text counted through claude -p; above it the count is an estimate.
const maxClaudeText = 1 << 20

// samples is the number of claude -p calls per measured text; the median is kept because the reported
// usage jitters by a few tokens between identical calls.
const samples = 3

// probeArgs run claude with no tool, no MCP server, no settings and no default system prompt, for one turn.
var probeArgs = []string{
	"-p", "Reply with the single character x.",
	"--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
	"--system-prompt", "You are a tokenizer probe.",
	"--max-turns", "1", "--output-format", "json", "--no-session-persistence",
}

// Result is what one claude -p call reports.
type Result struct {
	// Input is input_tokens + cache_creation_input_tokens + cache_read_input_tokens.
	Input int
	// Model is the model that served the prompt.
	Model string
	// CostUSD is total_cost_usd.
	CostUSD float64
	// Text is the reply of the model.
	Text string
}

// ParseResult reads the JSON result of `claude -p --output-format json`.
func ParseResult(b []byte) (Result, error) {
	var r struct {
		Type    string  `json:"type"`
		IsError bool    `json:"is_error"`
		Result  string  `json:"result"`
		Cost    float64 `json:"total_cost_usd"`
		Usage   struct {
			Input    int `json:"input_tokens"`
			Creation int `json:"cache_creation_input_tokens"`
			Read     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		ModelUsage map[string]struct {
			Input    int `json:"inputTokens"`
			Creation int `json:"cacheCreationInputTokens"`
			Read     int `json:"cacheReadInputTokens"`
		} `json:"modelUsage"`
	}
	if err := json.Unmarshal(b, &r); err != nil {
		return Result{}, fmt.Errorf("parse claude result: %w", err)
	}
	if r.Type != "result" || r.IsError {
		return Result{}, fmt.Errorf("claude did not answer: type %q, is_error %v, result %q", r.Type, r.IsError, r.Result)
	}
	out := Result{Input: r.Usage.Input + r.Usage.Creation + r.Usage.Read, CostUSD: r.Cost, Text: r.Result}
	if out.Input == 0 {
		return Result{}, errors.New("claude result has no input tokens")
	}
	// The prompt's model is the one whose input is the reported total; a small side model may also appear.
	for m, mu := range r.ModelUsage {
		if mu.Input+mu.Creation+mu.Read == out.Input && (out.Model == "" || m < out.Model) {
			out.Model = m
		}
	}
	return out, nil
}

// Difference returns the count of a text from the totals measured with it and with the empty placeholder,
// each reduced to the median of its samples.
func Difference(withText, baseline []int) (int, error) {
	if len(withText) == 0 || len(baseline) == 0 {
		return 0, errors.New("no sample to compare")
	}
	n := median(withText) - median(baseline)
	if n < 0 {
		return 0, fmt.Errorf("negative count %d: the text measured below the baseline", n)
	}
	return n, nil
}

func median(v []int) int {
	s := slices.Clone(v)
	slices.Sort(s)
	return s[len(s)/2]
}

type counted struct {
	n         int
	estimated bool
}

// claudeProbe runs claude -p with text on stdin from an empty directory and parses its result.
func claudeProbe(ctx context.Context, dir, text string) (Result, error) {
	cmd := exec.CommandContext(ctx, "claude", probeArgs...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(text)
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		return Result{}, fmt.Errorf("claude -p: %w: %s", err, strings.TrimSpace(errb.String()))
	}
	return ParseResult(out.Bytes())
}

// sample measures text `samples` times and returns the totals.
func (c *Counter) sample(ctx context.Context, text string) ([]int, error) {
	var totals []int
	for range samples {
		r, err := c.probe(ctx, text)
		if err != nil {
			return nil, err
		}
		c.Calls++
		c.CostUSD += r.CostUSD
		if c.Calls == 1 {
			c.Model = r.Model
		}
		totals = append(totals, r.Input)
	}
	return totals, nil
}

// Prepare measures the empty-text baseline once and learns the model. It does nothing for the other counters.
func (c *Counter) Prepare(ctx context.Context) error {
	if c.probe == nil || c.baseline != nil {
		return nil
	}
	b, err := c.sample(ctx, "")
	if err != nil {
		return fmt.Errorf("measure the claude -p baseline: %w", err)
	}
	c.baseline = b
	return nil
}

// claudeText counts s through claude -p, once per distinct text. estimated reports the fallback for a text too large.
func (c *Counter) claudeText(ctx context.Context, s string) (n int, estimated bool, err error) {
	if err := c.Prepare(ctx); err != nil {
		return 0, false, err
	}
	key := sha256.Sum256([]byte(s))
	if v, ok := c.cache[key]; ok {
		return v.n, v.estimated, nil
	}
	v := counted{n: Estimate(s), estimated: true}
	if len(s) <= maxClaudeText {
		totals, err := c.sample(ctx, s)
		if err != nil {
			return 0, false, err
		}
		if v.n, err = Difference(totals, c.baseline); err != nil {
			return 0, false, err
		}
		v.estimated = false
	}
	c.cache[key] = v
	return v.n, v.estimated, nil
}

// claudeTools counts a tool set as the JSON of its definitions, because claude -p cannot be handed bare tools.
func (c *Counter) claudeTools(ctx context.Context, tools []*mcp.Tool) (int, error) {
	b, err := json.Marshal(tools)
	if err != nil {
		return 0, fmt.Errorf("encode tools: %w", err)
	}
	n, _, err := c.claudeText(ctx, string(b))
	return n, err
}

// newClaude builds the claude -p counter.
func newClaude() *Counter {
	c := &Counter{Label: results.CounterClaude, cache: map[[32]byte]counted{}}
	c.probe = func(ctx context.Context, text string) (Result, error) { return claudeProbe(ctx, os.TempDir(), text) }
	return c
}
