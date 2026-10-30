package judge

import (
	"context"
	_ "embed" // the prompt
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"text/template"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

//go:embed attribution.md
var attributionText string

var attributionTemplate = template.Must(template.New("attribution").Option("missingkey=error").Parse(attributionText))

// A tool result longer than resultHead + resultTail characters is given to the attribution pass as its head and its
// tail, with the cut marked.
const (
	resultHead = 1800
	resultTail = 600
)

// Shorten returns s unchanged when it fits the limit of a tool result, else its head and tail around a marker.
func Shorten(s string) string {
	r := []rune(s)
	if len(r) <= resultHead+resultTail {
		return s
	}
	return string(r[:resultHead]) + fmt.Sprintf("\n[... %d characters cut by the benchmark, not by the tool ...]\n", len(r)-resultHead-resultTail) + string(r[len(r)-resultTail:])
}

// renderCalls lists the tool calls with their shortened results, numbered from 1.
func renderCalls(calls []Call) string {
	if len(calls) == 0 {
		return "(no tool call)"
	}
	var sb strings.Builder
	for i, c := range calls {
		status := ""
		if c.IsError {
			status = " (the tool reported an error)"
		}
		fmt.Fprintf(&sb, "### Call %d: %s%s\n\nInput: %s\n\nResult:\n\n%s\n\n", i+1, c.Name, status, Shorten(c.Input), Shorten(c.Result))
	}
	return strings.TrimSpace(sb.String())
}

// AttributionPrompt renders the attribution prompt of one answer. claimed lists the errors a first reviewer
// reported for it.
func AttributionPrompt(question, facts string, calls []Call, answer string, claimed []string) (string, error) {
	list := "none"
	if len(claimed) > 0 {
		list = "- " + strings.Join(claimed, "\n- ")
	}
	var sb strings.Builder
	err := attributionTemplate.Execute(&sb, map[string]string{"Question": question, "Facts": facts, "Calls": renderCalls(calls),
		"Answer": answer, "Claimed": list})
	return sb.String(), err
}

type attributedDTO struct {
	Claim  *string `json:"claim"`
	Origin *string `json:"origin"`
	Call   *int    `json:"call"`
	Why    *string `json:"why"`
}

// ParseAttribution parses an attribution reply strictly, like ParseReply: one JSON object {"errors": [...]},
// each error with a claim, an origin among the four, a call number and a reason. calls is the number of tool calls
// shown: tool and misread need a call between 1 and calls, the other origins need 0.
func ParseAttribution(text string, calls int) ([]results.AttributedError, error) {
	s := strings.TrimSpace(text)
	if strings.HasPrefix(s, "```") && strings.HasSuffix(s, "```") {
		s = strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(s, "```json"), "```"), "```"))
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.DisallowUnknownFields()
	var d struct {
		Errors *[]attributedDTO `json:"errors"`
	}
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("attribution reply is not the expected JSON: %w", err)
	}
	if dec.More() {
		return nil, errors.New("attribution reply has text after the JSON object")
	}
	if d.Errors == nil {
		return nil, errors.New("attribution reply lacks errors")
	}
	out := make([]results.AttributedError, 0, len(*d.Errors))
	for _, e := range *d.Errors {
		if e.Claim == nil || e.Origin == nil || e.Call == nil || e.Why == nil {
			return nil, errors.New("attribution error lacks claim, origin, call or why")
		}
		needsCall := false
		switch *e.Origin {
		case results.OriginTool, results.OriginMisread:
			needsCall = true
		case results.OriginUnsupported, results.OriginNone:
		default:
			return nil, fmt.Errorf("attribution origin %q is not tool, misread, unsupported or none", *e.Origin)
		}
		if needsCall != (*e.Call != 0) || *e.Call < 0 || *e.Call > calls {
			return nil, fmt.Errorf("attribution call %d does not fit origin %q with %d calls", *e.Call, *e.Origin, calls)
		}
		out = append(out, results.AttributedError{Claim: *e.Claim, Origin: *e.Origin, Call: *e.Call, Why: *e.Why})
	}
	return out, nil
}

// claimed returns the errors the pairwise pass listed for server in both orders of a pair, without duplicates.
func claimed(p results.JudgePair, server string) []string {
	var out []string
	for _, o := range p.Orders {
		for _, e := range o.Scores[server].Errors {
			if !slices.Contains(out, e) {
				out = append(out, e)
			}
		}
	}
	return out
}

// Attribute classifies the origin of the errors of every answer of cases: the ones the judge finds from the
// tool calls and the ones the pairwise pass listed in pairs (same order as cases). This pass is not blind: it
// reads the tool calls, whose names reveal the server. It returns the answers in the order of cases, dba first.
func (j *Judge) Attribute(ctx context.Context, cases []Case, pairs []results.JudgePair) ([]results.JudgeAttribution, error) {
	servers := []string{results.DBA, results.Crystal}
	out := make([]results.JudgeAttribution, len(cases)*len(servers))
	err := j.each(ctx, len(out), func(ctx context.Context, k int) error {
		c, server := cases[k/len(servers)], servers[k%len(servers)]
		trail := c.Trails[server]
		prompt, err := AttributionPrompt(c.Question, c.Facts, trail.Calls, c.Answers[server], claimed(pairs[k/len(servers)], server))
		if err != nil {
			return err
		}
		file := filepath.Join(j.Dir, fmt.Sprintf("%s-%d-attr-%s.json", c.Task, c.Run, server))
		errs, err := ask(ctx, j, file, prompt, func(t string) ([]results.AttributedError, error) { return ParseAttribution(t, len(trail.Calls)) })
		if err != nil {
			return fmt.Errorf("attribute %s run %d (%s): %w", c.Task, c.Run, server, err)
		}
		for i := range errs {
			if n := errs[i].Call; n > 0 {
				errs[i].Tool = trail.Calls[n-1].Name
			}
		}
		out[k] = results.JudgeAttribution{Task: c.Task, Run: c.Run, Server: server, File: trail.File, Errors: errs}
		return nil
	})
	return out, err
}
