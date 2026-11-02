// Package agent runs the N3 tasks with `claude -p`, parses the stream-json output and grades each run with a
// deterministic rubric.
package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

// ToolCall is one MCP tool call of a run with its result.
type ToolCall struct {
	Name    string // without the mcp__pg__ prefix
	Input   json.RawMessage
	Result  string
	IsError bool
}

// SQL returns the `sql` argument of an execute_sql call, or "".
func (t ToolCall) SQL() string {
	var in struct {
		SQL string `json:"sql"`
	}
	_ = json.Unmarshal(t.Input, &in)
	return in.SQL
}

// Transcript is what a run produced.
type Transcript struct {
	Calls        []ToolCall
	Final        string // the answer, from the result event
	InputTokens  int    // input tokens including cache creation and reads
	OutputTokens int
	CostUSD      float64
	Turns        int
	SearchTurns  int    // turns whose only tool calls are ToolSearch, which loads the deferred MCP tools
	Model        string // model of the session, from the system init event
	DurationMS   int
	IsError      bool
}

type block struct {
	Type      string          `json:"type"`
	Text      string          `json:"text"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   bool            `json:"is_error"`
}

type usage struct {
	Input         int `json:"input_tokens"`
	Output        int `json:"output_tokens"`
	CacheCreation int `json:"cache_creation_input_tokens"`
	CacheRead     int `json:"cache_read_input_tokens"`
}

type event struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	Model   string `json:"model"`
	Message struct {
		ID      string  `json:"id"`
		Content []block `json:"content"`
	} `json:"message"`
	Result     string  `json:"result"`
	IsError    bool    `json:"is_error"`
	DurationMS int     `json:"duration_ms"`
	NumTurns   int     `json:"num_turns"`
	Cost       float64 `json:"total_cost_usd"`
	Usage      usage   `json:"usage"`
}

// ErrNoResult is returned when a stream ends without its final result event.
var ErrNoResult = errors.New("stream has no result event")

const toolPrefix = "mcp__pg__"

// Parse reads a `claude -p --output-format stream-json --verbose` stream.
func Parse(r io.Reader) (Transcript, error) {
	var tr Transcript
	var seen bool
	index := map[string]int{}                             // tool_use id -> position in tr.Calls
	search, other := map[string]bool{}, map[string]bool{} // message id -> has a ToolSearch call, has another tool call
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 1<<20), 64<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var ev event
		if err := json.Unmarshal(line, &ev); err != nil {
			return tr, fmt.Errorf("decode stream line: %w", err)
		}
		switch ev.Type {
		case "system":
			if ev.Subtype == "init" {
				tr.Model = ev.Model
			}
		case "assistant":
			for _, b := range ev.Message.Content {
				if b.Type == "tool_use" {
					if b.Name == "ToolSearch" {
						search[ev.Message.ID] = true
					} else {
						other[ev.Message.ID] = true
					}
				}
				if b.Type == "tool_use" && strings.HasPrefix(b.Name, toolPrefix) {
					index[b.ID] = len(tr.Calls)
					tr.Calls = append(tr.Calls, ToolCall{Name: strings.TrimPrefix(b.Name, toolPrefix), Input: b.Input})
				}
			}
		case "user":
			for _, b := range ev.Message.Content {
				if i, ok := index[b.ToolUseID]; ok && b.Type == "tool_result" {
					tr.Calls[i].Result, tr.Calls[i].IsError = resultText(b.Content), b.IsError
				}
			}
		case "result":
			seen = true
			tr.Final, tr.IsError, tr.DurationMS, tr.Turns, tr.CostUSD = ev.Result, ev.IsError, ev.DurationMS, ev.NumTurns, ev.Cost
			tr.InputTokens = ev.Usage.Input + ev.Usage.CacheCreation + ev.Usage.CacheRead
			tr.OutputTokens = ev.Usage.Output
		}
	}
	if err := sc.Err(); err != nil {
		return tr, fmt.Errorf("read stream: %w", err)
	}
	for id := range search {
		if !other[id] {
			tr.SearchTurns++
		}
	}
	if !seen {
		return tr, ErrNoResult
	}
	return tr, nil
}

// resultText reads a tool_result content, which is a string or a list of text blocks.
func resultText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var bs []block
	if json.Unmarshal(raw, &bs) != nil {
		return ""
	}
	var sb strings.Builder
	for _, b := range bs {
		sb.WriteString(b.Text)
	}
	return sb.String()
}
