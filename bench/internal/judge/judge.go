// Package judge grades the final answers of the N3 runs with an LLM judge: a blind pairwise pass shows the
// answers of both servers as A and B, in both orders, and keeps a win only when the two orders agree; an
// attribution pass (not blind) reads the tool calls behind each answer and says where each error comes from.
// The judge is `claude -p` with no tool and no MCP server.
package judge

import (
	"bytes"
	"context"
	_ "embed" // the prompt
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"text/template"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

//go:embed prompt.md
var promptText string

var promptTemplate = template.Must(template.New("prompt").Option("missingkey=error").Parse(promptText))

// Tie is the outcome of a pair on which the two orders disagree, or that the judge called equal.
const Tie = "tie"

// ErrBudget is returned when the judge calls have cost more than the budget.
var ErrBudget = errors.New("judge budget exceeded")

// Call is one tool call of a transcript, as the attribution pass reads it.
type Call struct {
	Name, Input, Result string
	IsError             bool
}

// Trail is what an answer was built from: the transcript file and its tool calls.
type Trail struct {
	File  string
	Calls []Call
}

// Case is one pair to judge: both answers to one question.
type Case struct {
	Task     string
	Run      int
	Question string
	Facts    string
	Answers  map[string]string // by server
	Trails   map[string]Trail  // by server; only the attribution pass reads it
}

// Judge runs the cases. Call is the model call; Claude is the real one.
type Judge struct {
	Call     func(ctx context.Context, prompt string) (tokens.Result, error)
	Dir      string    // where the raw replies are kept, so that an interrupted run resumes without paying again
	Log      io.Writer // progress
	Budget   float64   // stop above this many USD; 0 means no limit
	Parallel int       // concurrent calls; 1 when 0

	mu    sync.Mutex
	spent float64
	model map[string]bool
	paid  map[string]bool // reply files already counted in spent, so that a second pass over them is free
}

// Claude returns a model call through `claude -p`, with the account's default model and effort, or model and effort
// when they are set.
func Claude(model, effort string) func(context.Context, string) (tokens.Result, error) {
	args := []string{"-p", "--tools", "", "--strict-mcp-config", "--disable-slash-commands", "--setting-sources", "",
		"--system-prompt", "You are a strict, impartial grader of answers about PostgreSQL databases.",
		"--max-turns", "1", "--output-format", "json", "--no-session-persistence"}
	if model != "" {
		args = append(args, "--model", model)
	}
	if effort != "" {
		args = append(args, "--effort", effort)
	}
	return func(ctx context.Context, prompt string) (tokens.Result, error) {
		dir, err := os.MkdirTemp("", "bench-judge-")
		if err != nil {
			return tokens.Result{}, fmt.Errorf("create judge folder: %w", err)
		}
		defer func() { _ = os.RemoveAll(dir) }()
		cmd := exec.CommandContext(ctx, "claude", args...)
		cmd.Dir = dir // no project instructions to read
		cmd.Stdin = strings.NewReader(prompt)
		var out, errb bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &errb
		if err := cmd.Run(); err != nil {
			return tokens.Result{}, fmt.Errorf("claude -p: %w: %s", err, strings.TrimSpace(errb.String()))
		}
		return tokens.ParseResult(out.Bytes())
	}
}

// Prompt renders the judge prompt for a question, the reference facts and the two answers shown as A and B.
func Prompt(question, facts, a, b string) (string, error) {
	var sb strings.Builder
	err := promptTemplate.Execute(&sb, map[string]string{"Question": question, "Facts": facts, "A": a, "B": b})
	return sb.String(), err
}

type markDTO struct {
	Correctness   *int      `json:"correctness"`
	Completeness  *int      `json:"completeness"`
	Actionability *int      `json:"actionability"`
	Errors        *[]string `json:"errors"`
}

type replyDTO struct {
	A      *markDTO `json:"a"`
	B      *markDTO `json:"b"`
	Winner *string  `json:"winner"`
	Reason *string  `json:"reason"`
}

// Reply is a parsed judge reply.
type Reply struct {
	A, B   results.JudgeScores
	Winner string // "A", "B" or "tie"
	Reason string
}

// ParseReply parses a judge reply strictly: one JSON object with exactly the expected fields, scores from 1 to 5
// and a winner that is A, B or tie. A single surrounding code fence is the only tolerated decoration.
func ParseReply(text string) (Reply, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```"), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d replyDTO
	if err := dec.Decode(&d); err != nil {
		return Reply{}, fmt.Errorf("judge reply is not the expected JSON: %w", err)
	}
	if dec.More() {
		return Reply{}, errors.New("judge reply has text after the JSON object")
	}
	if d.A == nil || d.B == nil || d.Winner == nil || d.Reason == nil {
		return Reply{}, errors.New("judge reply lacks a, b, winner or reason")
	}
	if w := *d.Winner; w != "A" && w != "B" && w != Tie {
		return Reply{}, fmt.Errorf("judge winner %q is not A, B or tie", w)
	}
	a, err := d.A.scores("a")
	if err != nil {
		return Reply{}, err
	}
	b, err := d.B.scores("b")
	if err != nil {
		return Reply{}, err
	}
	return Reply{A: a, B: b, Winner: *d.Winner, Reason: *d.Reason}, nil
}

func (m *markDTO) scores(who string) (results.JudgeScores, error) {
	if m.Correctness == nil || m.Completeness == nil || m.Actionability == nil || m.Errors == nil {
		return results.JudgeScores{}, fmt.Errorf("judge reply lacks a field of %q", who)
	}
	for _, v := range []int{*m.Correctness, *m.Completeness, *m.Actionability} {
		if v < 1 || v > 5 {
			return results.JudgeScores{}, fmt.Errorf("judge score %d of %q is not between 1 and 5", v, who)
		}
	}
	return results.JudgeScores{Correctness: *m.Correctness, Completeness: *m.Completeness, Actionability: *m.Actionability, Errors: *m.Errors}, nil
}

// Outcome combines the winners of the two orders of a pair, each a server name or Tie: a server wins only when
// it won both.
func Outcome(first, second string) string {
	if first == second {
		return first
	}
	return Tie
}

// each calls f for every index below n, at most Parallel at a time, and stops at the first error.
func (j *Judge) each(ctx context.Context, n int, f func(ctx context.Context, i int) error) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	var once sync.Once
	var firstErr error
	slots := make(chan struct{}, max(j.Parallel, 1))
	for i := range n {
		wg.Add(1)
		slots <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-slots }()
			if err := f(ctx, i); err != nil {
				once.Do(func() { firstErr = err; cancel() })
			}
		}()
	}
	wg.Wait()
	return firstErr
}

// Run judges every case in both orders, repeats times (at least once). Pairs come back in the order of cases;
// the scores and the outcome of a pair are those of its first pass, Repeats holds the outcome of every pass.
func (j *Judge) Run(ctx context.Context, cases []Case, repeats int) ([]results.JudgePair, error) {
	repeats = max(repeats, 1)
	pairs := make([]results.JudgePair, len(cases))
	err := j.each(ctx, len(cases), func(ctx context.Context, i int) error {
		var p results.JudgePair
		for rep := 1; rep <= repeats; rep++ {
			q, err := j.pair(ctx, cases[i], rep)
			if err != nil {
				return err
			}
			if rep == 1 {
				p = q
			}
			if repeats > 1 {
				p.Repeats = append(p.Repeats, q.Outcome)
			}
		}
		pairs[i] = p
		return nil
	})
	return pairs, err
}

// Model returns the models that answered, joined; "" before any call.
func (j *Judge) Model() string {
	j.mu.Lock()
	defer j.mu.Unlock()
	return strings.Join(slices.Sorted(maps.Keys(j.model)), ", ")
}

// Spent returns the USD the calls cost, the replies read back from Dir included once each.
func (j *Judge) Spent() float64 {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.spent
}

func (j *Judge) pair(ctx context.Context, c Case, rep int) (results.JudgePair, error) {
	p := results.JudgePair{Task: c.Task, Run: c.Run}
	servers := []string{results.DBA, results.Crystal}
	for i, first := range servers {
		second := servers[1-i]
		order, err := j.once(ctx, c, first, second, rep)
		if err != nil {
			return p, fmt.Errorf("judge %s run %d (A=%s): %w", c.Task, c.Run, first, err)
		}
		p.Orders[i] = order
	}
	p.Outcome = Outcome(p.Orders[0].Winner, p.Orders[1].Winner)
	return p, nil
}

// once asks the judge about one order, with A the answer of first. A reply that does not parse is asked for once more.
func (j *Judge) once(ctx context.Context, c Case, first, second string, rep int) (results.JudgeOrder, error) {
	prompt, err := Prompt(c.Question, c.Facts, c.Answers[first], c.Answers[second])
	if err != nil {
		return results.JudgeOrder{}, err
	}
	file := filepath.Join(j.Dir, fmt.Sprintf("%s-%d-A%s%s.json", c.Task, c.Run, first, repSuffix(rep)))
	reply, err := ask(ctx, j, file, prompt, ParseReply)
	if err != nil {
		return results.JudgeOrder{}, err
	}
	winner := Tie
	switch reply.Winner {
	case "A":
		winner = first
	case "B":
		winner = second
	}
	return results.JudgeOrder{A: first, Winner: winner, Reason: reply.Reason,
		Scores: map[string]results.JudgeScores{first: reply.A, second: reply.B}}, nil
}

// repSuffix is the file name suffix of the repeats after the first, which keeps the names of a single pass.
func repSuffix(rep int) string {
	if rep <= 1 {
		return ""
	}
	return fmt.Sprintf("-r%d", rep)
}

// ask returns the parsed model reply kept in file, or asks the model and keeps it. A reply that does not parse
// is asked for once more.
func ask[T any](ctx context.Context, j *Judge, file, prompt string, parse func(string) (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < 2; attempt++ {
		res, err := j.reply(ctx, file, prompt)
		if err != nil {
			return zero, err
		}
		v, err := parse(res.Text)
		if err == nil {
			return v, nil
		}
		lastErr = err
		_ = os.Remove(file)
	}
	return zero, lastErr
}

// reply returns the model reply kept in file, or asks the model and keeps it.
func (j *Judge) reply(ctx context.Context, file, prompt string) (tokens.Result, error) {
	if b, err := os.ReadFile(file); err == nil {
		var res struct {
			Text, Model string
			CostUSD     float64
		}
		if json.Unmarshal(b, &res) == nil {
			j.record(file, res.Model, res.CostUSD)
			return tokens.Result{Text: res.Text, Model: res.Model, CostUSD: res.CostUSD}, nil
		}
	}
	j.mu.Lock()
	over := j.Budget > 0 && j.spent > j.Budget
	j.mu.Unlock()
	if over {
		return tokens.Result{}, ErrBudget
	}
	if j.Log != nil {
		_, _ = fmt.Fprintf(j.Log, "judge %s\n", strings.TrimSuffix(filepath.Base(file), ".json"))
	}
	res, err := j.Call(ctx, prompt)
	if err != nil {
		return res, err
	}
	j.record(file, res.Model, res.CostUSD)
	b, err := json.Marshal(struct {
		Text, Model string
		CostUSD     float64
	}{res.Text, res.Model, res.CostUSD})
	if err == nil {
		err = os.WriteFile(file, b, 0o644)
	}
	if err != nil {
		return res, fmt.Errorf("keep judge reply: %w", err)
	}
	return res, nil
}

func (j *Judge) record(file, model string, cost float64) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.model == nil {
		j.model, j.paid = map[string]bool{}, map[string]bool{}
	}
	if model != "" {
		j.model[model] = true
	}
	if !j.paid[file] {
		j.paid[file] = true
		j.spent += cost
	}
}
