package tokens

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestParseResult(t *testing.T) {
	tests := []struct {
		file    string
		want    Result
		wantErr bool
	}{
		{"result-ok.json", Result{Input: 2 + 4713 + 649, Model: "claude-opus-5-5", CostUSD: 0.0380261, Text: "x"}, false},
		{"result-small.json", Result{Input: 800, Model: "claude-opus-5-5", CostUSD: 0.0065763, Text: "x"}, false}, // the side model has more input
		{"result-error.json", Result{}, true},
		{"result-nousage.json", Result{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := os.ReadFile("testdata/" + tt.file)
			if err != nil {
				t.Fatal(err)
			}
			got, err := ParseResult(b)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("ParseResult = %+v, %v; want %+v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
	if _, err := ParseResult([]byte("not json")); err == nil {
		t.Error("garbage parsed")
	}
}

func TestDifference(t *testing.T) {
	tests := []struct {
		name       string
		text, base []int
		want       int
		wantErr    bool
	}{
		{"median of three", []int{3909, 3914, 3907}, []int{810, 812, 808}, 3909 - 810, false},
		{"single", []int{100}, []int{40}, 60, false},
		{"no text sample", nil, []int{1}, 0, true},
		{"below baseline", []int{10, 10, 10}, []int{20, 20, 20}, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Difference(tt.text, tt.base)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("Difference = %d, %v; want %d, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

// fake is a counter whose probe totals 100 + len(text)/4 and records how often each text is probed.
func fake(calls map[string]int) *Counter {
	c := &Counter{Label: results.CounterClaude, cache: map[[32]byte]counted{}}
	c.probe = func(_ context.Context, text string) (Result, error) {
		calls[text[:min(len(text), 8)]]++
		return Result{Input: 100 + len(text)/4, Model: "m", CostUSD: 0.5}, nil
	}
	return c
}

func TestClaudeText(t *testing.T) {
	calls := map[string]int{}
	c := fake(calls)
	ctx := context.Background()
	if err := c.Prepare(ctx); err != nil {
		t.Fatal(err)
	}
	if c.Calls != samples || c.Model != "m" {
		t.Fatalf("baseline: %d calls, model %q", c.Calls, c.Model)
	}
	text := strings.Repeat("abcd", 50)
	for range 3 { // the same text is measured once
		n, est, err := c.TextEst(ctx, text)
		if n != 50 || est || err != nil {
			t.Fatalf("TextEst = %d, %v, %v; want 50, false", n, est, err)
		}
	}
	if calls["abcdabcd"] != samples {
		t.Errorf("text probed %d times, want %d", calls["abcdabcd"], samples)
	}
	if c.Calls != 2*samples || c.CostUSD != 0.5*float64(2*samples) {
		t.Errorf("calls %d, cost %v", c.Calls, c.CostUSD)
	}
	if double, _, _ := c.TextEst(ctx, text+text); double != 100 {
		t.Errorf("double text = %d, want 100", double)
	}
}

func TestClaudeTextTooLarge(t *testing.T) {
	c := fake(map[string]int{})
	big := strings.Repeat("a", maxClaudeText+1)
	n, est, err := c.TextEst(context.Background(), big)
	if err != nil || !est || n != Estimate(big) {
		t.Fatalf("TextEst = %d, %v, %v; want estimate %d", n, est, err, Estimate(big))
	}
	if c.Calls != samples { // only the baseline
		t.Errorf("%d calls, want only the baseline", c.Calls)
	}
}

func TestClaudeProbeError(t *testing.T) {
	c := fake(map[string]int{})
	boom := errors.New("boom")
	c.probe = func(context.Context, string) (Result, error) { return Result{}, boom }
	if _, _, err := c.TextEst(context.Background(), "x"); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want boom", err)
	}
}

func TestNewKind(t *testing.T) {
	env := func(string) string { return "" }
	tests := []struct {
		kind    string
		label   string
		wantErr bool
	}{
		{"claude", results.CounterClaude, false},
		{"", results.CounterEstimate, false},
		{"gpt", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			c, err := New(env, tt.kind)
			if (err != nil) != tt.wantErr || (err == nil && c.Label != tt.label) {
				t.Fatalf("New(%q) = %v, %v", tt.kind, c, err)
			}
		})
	}
}
