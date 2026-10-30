// Package tokens counts tokens for the bench: through claude -p when asked, else with an API key the Anthropic
// count_tokens endpoint, else ceil(bytes/4), and says which.
package tokens

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// DefaultModel is the model counted against unless BENCH_MODEL says otherwise.
const DefaultModel = "claude-sonnet-5-5"

// Estimate returns ceil(bytes/4), the fallback count of a text.
func Estimate(s string) int { return (len(s) + 3) / 4 }

// Counter counts tokens of texts and tool sets.
type Counter struct {
	// Label is results.CounterAPI or results.CounterEstimate.
	Label string
	// Model is the model the API counts for.
	Model  string
	client *anthropic.Client

	// claude -p counter state: probe runs one call, baseline holds the totals of the empty text.
	probe    func(ctx context.Context, text string) (Result, error)
	baseline []int
	cache    map[[32]byte]counted
	// Calls and CostUSD sum the claude -p calls made.
	Calls   int
	CostUSD float64
}

// New builds a Counter from the environment: ANTHROPIC_API_KEY selects the API, BENCH_MODEL the model. kind "claude"
// counts through claude -p instead, whatever the key; "" picks the API or the estimate.
func New(getenv func(string) string, kind string) (*Counter, error) {
	switch kind {
	case "claude":
		return newClaude(), nil
	case "":
	default:
		return nil, fmt.Errorf("unknown token counter %q (want claude)", kind)
	}
	model := getenv("BENCH_MODEL")
	if model == "" {
		model = DefaultModel
	}
	c := &Counter{Label: results.CounterEstimate, Model: model}
	if key := getenv("ANTHROPIC_API_KEY"); key != "" {
		cl := anthropic.NewClient(option.WithAPIKey(key))
		c.client, c.Label = &cl, results.CounterAPI
	}
	return c, nil
}

// Text returns the token count of s: the count of a one-message conversation holding s minus the empty one.
func (c *Counter) Text(ctx context.Context, s string) (int, error) {
	n, _, err := c.TextEst(ctx, s)
	return n, err
}

// TextEst is Text, and reports whether the count is the bytes/4 fallback of a text too large for claude -p.
func (c *Counter) TextEst(ctx context.Context, s string) (int, bool, error) {
	if c.probe != nil {
		return c.claudeText(ctx, s)
	}
	n, err := c.apiText(ctx, s)
	return n, false, err
}

func (c *Counter) apiText(ctx context.Context, s string) (int, error) {
	if c.client == nil {
		return Estimate(s), nil
	}
	full, err := c.count(ctx, s, nil)
	if err != nil {
		return 0, err
	}
	empty, err := c.count(ctx, "", nil)
	if err != nil {
		return 0, err
	}
	return full - empty, nil
}

// Tools returns the token count of a tool set: the count with the tools minus the count without.
func (c *Counter) Tools(ctx context.Context, tools []*mcp.Tool) (int, error) {
	if c.probe != nil {
		return c.claudeTools(ctx, tools)
	}
	if c.client == nil {
		n := 0
		for _, t := range tools {
			b, err := json.Marshal(t.InputSchema)
			if err != nil {
				return 0, fmt.Errorf("encode schema of %s: %w", t.Name, err)
			}
			n += Estimate(t.Name + t.Description + string(b))
		}
		return n, nil
	}
	var params []anthropic.MessageCountTokensToolUnionParam
	for _, t := range tools {
		b, err := json.Marshal(t.InputSchema)
		if err != nil {
			return 0, fmt.Errorf("encode schema of %s: %w", t.Name, err)
		}
		var schema anthropic.ToolInputSchemaParam
		if err := json.Unmarshal(b, &schema); err != nil {
			return 0, fmt.Errorf("convert schema of %s: %w", t.Name, err)
		}
		p := anthropic.MessageCountTokensToolParamOfTool(schema, t.Name)
		if t.Description != "" {
			p.OfTool.Description = anthropic.String(t.Description)
		}
		params = append(params, p)
	}
	with, err := c.count(ctx, "x", params)
	if err != nil {
		return 0, err
	}
	without, err := c.count(ctx, "x", nil)
	if err != nil {
		return 0, err
	}
	return with - without, nil
}

func (c *Counter) count(ctx context.Context, text string, tools []anthropic.MessageCountTokensToolUnionParam) (int, error) {
	if text == "" {
		text = " "
	}
	res, err := c.client.Messages.CountTokens(ctx, anthropic.MessageCountTokensParams{
		Model:    anthropic.Model(c.Model),
		Messages: []anthropic.MessageParam{anthropic.NewUserMessage(anthropic.NewTextBlock(text))},
		Tools:    tools,
	})
	if err != nil {
		return 0, fmt.Errorf("count_tokens: %w", err)
	}
	return int(res.InputTokens), nil
}
