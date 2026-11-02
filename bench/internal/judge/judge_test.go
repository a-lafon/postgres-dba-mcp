package judge

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

const okReply = `{"a": {"correctness": 4, "completeness": 3, "actionability": 5, "errors": ["wrong name"]}, "b": {"correctness": 2, "completeness": 2, "actionability": 2, "errors": []}, "winner": "A", "reason": "A is right."}`

func TestParseReply(t *testing.T) {
	tests := []struct {
		name string
		in   string
		ok   bool
	}{
		{"plain", okReply, true},
		{"one code fence", "```json\n" + okReply + "\n```", true},
		{"prose around", "Here you go: " + okReply, false},
		{"text after", okReply + " done", false},
		{"unknown field", strings.Replace(okReply, `"reason"`, `"extra": 1, "reason"`, 1), false},
		{"missing errors", strings.Replace(okReply, `"errors": []`, `"x": 1`, 1), false},
		{"score out of range", strings.Replace(okReply, `"correctness": 4`, `"correctness": 6`, 1), false},
		{"score zero", strings.Replace(okReply, `"correctness": 4`, `"correctness": 0`, 1), false},
		{"bad winner", strings.Replace(okReply, `"winner": "A"`, `"winner": "C"`, 1), false},
		{"tie", strings.Replace(okReply, `"winner": "A"`, `"winner": "tie"`, 1), true},
		{"missing reason", strings.Replace(okReply, `, "reason": "A is right."`, ``, 1), false},
		{"not json", "A wins", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseReply(tt.in)
			if (err == nil) != tt.ok {
				t.Fatalf("err = %v, want ok = %v", err, tt.ok)
			}
			if tt.name == "plain" && (r.A.Correctness != 4 || r.A.Errors[0] != "wrong name" || r.B.Actionability != 2 || r.Winner != "A") {
				t.Errorf("reply = %+v", r)
			}
		})
	}
}

func TestOutcome(t *testing.T) {
	tests := []struct{ a, b, want string }{
		{results.DBA, results.DBA, results.DBA},
		{results.Crystal, results.Crystal, results.Crystal},
		{results.DBA, results.Crystal, Tie}, // position bias: each order favours its A
		{results.DBA, Tie, Tie},
		{Tie, Tie, Tie},
	}
	for _, tt := range tests {
		if got := Outcome(tt.a, tt.b); got != tt.want {
			t.Errorf("Outcome(%s, %s) = %s, want %s", tt.a, tt.b, got, tt.want)
		}
	}
}

// fake answers "A" when the prompt shows the marker of favoured as answer A, "B" otherwise; with positional it always answers A.
func fake(favoured string, positional bool, calls *atomic.Int32) func(context.Context, string) (tokens.Result, error) {
	return func(_ context.Context, prompt string) (tokens.Result, error) {
		calls.Add(1)
		winner := "B"
		if positional || strings.Contains(prompt, "## Answer A\n\n"+favoured) {
			winner = "A"
		}
		text := strings.Replace(okReply, `"winner": "A"`, `"winner": "`+winner+`"`, 1)
		return tokens.Result{Text: text, Model: "m", CostUSD: 0.5}, nil
	}
}

func TestRun(t *testing.T) {
	cases := []Case{{Task: "T01", Run: 1, Question: "q", Facts: "f", Answers: map[string]string{results.DBA: "answer-dba", results.Crystal: "answer-crystal"}}}
	tests := []struct {
		name       string
		favoured   string
		positional bool
		want       string
	}{
		{"dba preferred in both orders", "answer-dba", false, results.DBA},
		{"crystal preferred in both orders", "answer-crystal", false, results.Crystal},
		{"always A is a tie", "", true, Tie},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32
			j := &Judge{Call: fake(tt.favoured, tt.positional, &calls), Dir: t.TempDir(), Parallel: 2}
			pairs, err := j.Run(context.Background(), cases, 1)
			if err != nil {
				t.Fatal(err)
			}
			if pairs[0].Outcome != tt.want || calls.Load() != 2 || pairs[0].Orders[0].A == pairs[0].Orders[1].A {
				t.Errorf("outcome = %s, calls = %d, orders = %+v", pairs[0].Outcome, calls.Load(), pairs[0].Orders)
			}
			if j.Spent() != 1 || j.Model() != "m" {
				t.Errorf("spent = %v, model = %q", j.Spent(), j.Model())
			}
			// A second run reads the kept replies and calls nothing.
			if _, err := j.Run(context.Background(), cases, 1); err != nil || calls.Load() != 2 {
				t.Errorf("second run: err = %v, calls = %d", err, calls.Load())
			}
		})
	}
}

func TestRunFailures(t *testing.T) {
	cases := []Case{{Task: "T01", Run: 1, Answers: map[string]string{}}, {Task: "T01", Run: 2, Answers: map[string]string{}}}
	t.Run("budget", func(t *testing.T) {
		var calls atomic.Int32
		j := &Judge{Call: fake("", true, &calls), Dir: t.TempDir(), Budget: 0.4}
		if _, err := j.Run(context.Background(), cases, 1); !errors.Is(err, ErrBudget) {
			t.Errorf("err = %v, want ErrBudget", err)
		}
	})
	t.Run("unparsable reply is asked again once then fails", func(t *testing.T) {
		var calls atomic.Int32
		j := &Judge{Call: func(context.Context, string) (tokens.Result, error) {
			calls.Add(1)
			return tokens.Result{Text: "A wins"}, nil
		}, Dir: t.TempDir()}
		if _, err := j.Run(context.Background(), cases[:1], 1); err == nil || calls.Load() != 2 {
			t.Errorf("err = %v, calls = %d", err, calls.Load())
		}
	})
}

func TestPromptAndFacts(t *testing.T) {
	p, err := Prompt("the question", "the facts", "answer one", "answer two")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"the question", "the facts", "## Answer A\n\nanswer one", "## Answer B\n\nanswer two", "Reply with one JSON object"} {
		if !strings.Contains(p, want) {
			t.Errorf("prompt lacks %q", want)
		}
	}
	ref := agent.Ref{Spenders: []agent.Spender{{Name: "KARL SEAL", Amount: "221.55"}}, CustomerFKs: []agent.FK{{Table: "rental", Column: "customer_id"}},
		Categories: []agent.Category{{Name: "Action", Revenue: "1.00"}}, EventsIndexes: []string{"idx"}}
	for _, task := range agent.AllTasks() {
		if f := Facts(task.ID, ref); f == "" || strings.Contains(f, "No reference facts") || strings.Contains(f, "%!") {
			t.Errorf("facts of %s = %q", task.ID, f)
		}
	}
	if f := Facts("T11", ref); !strings.Contains(f, "rental.customer_id: has NO index") {
		t.Errorf("T11 facts = %q", f)
	}
}
