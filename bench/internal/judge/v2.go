package judge

import (
	"context"
	"crypto/sha256"
	_ "embed" // the prompt
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// The fact-list judge of protocol v2 (PROTOCOL.md §7): each answer is checked alone against the facts expected for
// its task, blind, by two judges, and a fact counts as present or contradicted only when both agree.

//go:embed prompt-v2.md
var promptV2Text string

var promptV2Template = template.Must(template.New("prompt-v2").Option("missingkey=error").Parse(promptV2Text))

// PromptV2SHA256 identifies the frozen prompt in judge2.json.
var PromptV2SHA256 = func() string { s := sha256.Sum256([]byte(promptV2Text)); return hex.EncodeToString(s[:]) }()

// JudgeModels are the two judges of decision D2, through claude -p.
var JudgeModels = []string{"claude-opus-5-5", "claude-sonnet-5-5"}

// Fact is one fact expected in the answer to a task, computed from the database before the run.
type Fact struct {
	ID        string `json:"id"`        // unique within the task, such as "F1"
	Statement string `json:"statement"` // the fact, with its exact values
	Recognize string `json:"recognize"` // how to recognize it in an answer: accepted wordings, rounding; may be empty
}

// Item is one answer to grade: the input the part-A run records are adapted to. Server is never shown to the judges,
// and Answer is blinded before it is.
type Item struct {
	Task     string   `json:"task"`
	Server   string   `json:"server"`
	Run      int      `json:"run"`
	Solved   bool     `json:"solved"`   // the deterministic rubric's verdict
	Question string   `json:"question"` // as asked to the agent
	Answer   string   `json:"answer"`   // the agent's final answer, raw
	Facts    []Fact   `json:"facts"`
	Wrong    []string `json:"wrong"` // claims that would be wrong
}

// ItemsFromTaskRuns adapts part-A records to judge items. questions maps a task to its question. An arm-W run is the
// task "<task>-w", so that it does not collide with the arm-RO run of the same repetition.
func ItemsFromTaskRuns(runs []results.TaskRun, questions map[string]string) []Item {
	items := make([]Item, 0, len(runs))
	for _, r := range runs {
		it := Item{Task: r.Task, Server: r.Server, Run: r.Rep, Solved: r.Solved, Question: questions[r.Task], Answer: r.Final}
		if r.Arm != results.ArmRO {
			it.Task += "-" + r.Arm
		}
		for _, f := range r.Facts {
			if f.Wrong {
				it.Wrong = append(it.Wrong, f.Text)
				continue
			}
			it.Facts = append(it.Facts, Fact{ID: fmt.Sprintf("F%d", len(it.Facts)+1), Statement: f.Text})
		}
		items = append(items, it)
	}
	return items
}

// ReadItems decodes a JSON array of items and checks that each can be graded.
func ReadItems(b []byte) ([]Item, error) {
	var items []Item
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("decode judge input: %w", err)
	}
	seen := map[string]bool{}
	for _, it := range items {
		key := fmt.Sprintf("%s/%s/%d", it.Task, it.Server, it.Run)
		if it.Task == "" || it.Server == "" || it.Run < 1 || seen[key] {
			return nil, fmt.Errorf("judge input %q: task, server and run (from 1) must be set and unique", key)
		}
		seen[key] = true
		if len(it.Facts) == 0 {
			return nil, fmt.Errorf("judge input %s: no expected fact", key)
		}
		ids := map[string]bool{}
		for _, f := range it.Facts {
			if f.ID == "" || f.Statement == "" || ids[f.ID] {
				return nil, fmt.Errorf("judge input %s: fact %q needs a unique id and a statement", key, f.ID)
			}
			ids[f.ID] = true
		}
	}
	return items, nil
}

// PromptV2 renders the judge prompt of one item, its answer blinded.
func PromptV2(it Item) (string, error) {
	var sb strings.Builder
	err := promptV2Template.Execute(&sb, map[string]any{"Question": it.Question, "Facts": it.Facts, "Wrong": it.Wrong, "Answer": Blind(it.Answer)})
	return sb.String(), err
}

type gradeDTO struct {
	Facts *[]struct {
		ID       *string `json:"id"`
		Label    *string `json:"label"`
		Evidence *string `json:"evidence"`
	} `json:"facts"`
	Unsupported *[]string `json:"unsupported"`
	Doubt       *bool     `json:"doubt"`
	DoubtQuote  *string   `json:"doubt_quote"`
}

// ParseGrade parses a judge reply strictly: one JSON object with exactly the expected fields, one entry per expected
// fact in the given order, and a label that is present, absent or contradicted. A single surrounding code fence is
// the only tolerated decoration.
func ParseGrade(text string, facts []Fact) (results.JudgeGrade, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```"), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d gradeDTO
	if err := dec.Decode(&d); err != nil {
		return results.JudgeGrade{}, fmt.Errorf("judge reply is not the expected JSON: %w", err)
	}
	if dec.More() {
		return results.JudgeGrade{}, errors.New("judge reply has text after the JSON object")
	}
	if d.Facts == nil || d.Unsupported == nil || d.Doubt == nil || d.DoubtQuote == nil {
		return results.JudgeGrade{}, errors.New("judge reply lacks facts, unsupported, doubt or doubt_quote")
	}
	if len(*d.Facts) != len(facts) {
		return results.JudgeGrade{}, fmt.Errorf("judge reply has %d facts, want %d", len(*d.Facts), len(facts))
	}
	g := results.JudgeGrade{Unsupported: *d.Unsupported, Doubt: *d.Doubt, DoubtQuote: *d.DoubtQuote}
	for i, f := range *d.Facts {
		if f.ID == nil || f.Label == nil || f.Evidence == nil {
			return results.JudgeGrade{}, fmt.Errorf("judge reply: fact %d lacks id, label or evidence", i+1)
		}
		if *f.ID != facts[i].ID {
			return results.JudgeGrade{}, fmt.Errorf("judge reply: fact %d is %q, want %q", i+1, *f.ID, facts[i].ID)
		}
		switch *f.Label {
		case results.FactPresent, results.FactAbsent, results.FactContradicted:
		default:
			return results.JudgeGrade{}, fmt.Errorf("judge reply: fact %s label %q is not present, absent or contradicted", *f.ID, *f.Label)
		}
		g.Facts = append(g.Facts, results.FactGrade{ID: *f.ID, Label: *f.Label, Evidence: *f.Evidence})
	}
	return g, nil
}

// Consensus fills the consensus of an answer from its grades, one per judge: a fact keeps its label when every judge
// gave it, else it is disputed; the doubt is yes or no when every judge says so, else disputed; the answer is
// confidently wrong when the rubric failed and every judge found no doubt or limit.
func Consensus(a *results.GradedAnswer) {
	a.Facts = map[string]string{}
	if len(a.Grades) == 0 {
		return
	}
	for i, f := range a.Grades[0].Facts {
		label := f.Label
		for _, g := range a.Grades[1:] {
			if i >= len(g.Facts) || g.Facts[i].Label != label {
				label = results.FactDisputed
			}
		}
		a.Facts[f.ID] = label
	}
	doubt := a.Grades[0].Doubt
	a.Doubt = map[bool]string{true: results.DoubtYes, false: results.DoubtNo}[doubt]
	for _, g := range a.Grades[1:] {
		if g.Doubt != doubt {
			a.Doubt = results.DoubtDisputed
		}
	}
	a.ConfidentlyWrong = !a.Solved && a.Doubt == results.DoubtNo
}

// Grade grades every item with every judge of Models, in the order of model id, at most Parallel calls at a time.
// A reply kept in Dir is read back instead of asked again. A reply that does not parse is asked for once more, with
// the parse error appended to the prompt.
func (j *Judge) Grade(ctx context.Context, items []Item) (results.Judge2, error) {
	models := slices.Sorted(maps.Keys(j.Models))
	out := results.Judge2{Judges: models, PromptSHA256: PromptV2SHA256, Answers: make([]results.GradedAnswer, len(items))}
	err := j.each(ctx, len(items)*len(models), func(ctx context.Context, k int) error {
		it, model := items[k/len(models)], models[k%len(models)]
		g, err := j.gradeOne(ctx, it, model)
		if err != nil {
			return fmt.Errorf("grade %s %s run %d with %s: %w", it.Task, it.Server, it.Run, model, err)
		}
		j.mu.Lock()
		defer j.mu.Unlock()
		a := &out.Answers[k/len(models)]
		if a.Grades == nil {
			*a = results.GradedAnswer{Task: it.Task, Server: it.Server, Run: it.Run, Solved: it.Solved, Grades: make([]results.JudgeGrade, len(models))}
		}
		a.Grades[k%len(models)] = g
		return nil
	})
	for i := range out.Answers {
		Consensus(&out.Answers[i])
	}
	out.CostUSD = j.Spent()
	return out, err
}

func (j *Judge) gradeOne(ctx context.Context, it Item, model string) (results.JudgeGrade, error) {
	prompt, err := PromptV2(it)
	if err != nil {
		return results.JudgeGrade{}, err
	}
	file := filepath.Join(j.Dir, fmt.Sprintf("v2-%s-%s-%d-%s.json", it.Task, it.Server, it.Run, model))
	var costs []float64
	for attempt := 0; ; attempt++ {
		res, err := j.reply(ctx, j.Models[model], file, prompt)
		if err != nil {
			return results.JudgeGrade{}, err
		}
		costs = append(costs, res.CostUSD)
		g, perr := ParseGrade(res.Text, it.Facts)
		if perr == nil {
			g.Model, g.Served, g.CallsUSD = model, res.Model, costs
			return g, nil
		}
		j.discard(file)
		if attempt == 1 {
			return results.JudgeGrade{}, perr
		}
		prompt += fmt.Sprintf("\n\nYour previous reply could not be read (%v). Reply again with the JSON object only.\n", perr)
	}
}
