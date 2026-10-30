package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/judge"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// n3Tasks returns the tasks selected by --tasks.
func (s *session) n3Tasks() []agent.Task {
	switch s.opts.tasks {
	case "extra":
		return agent.ExtraTasks
	case "all":
		return agent.AllTasks()
	}
	return agent.Tasks
}

// answer is the final answer of one stored N3 run and what is worth showing next to it.
type answer struct {
	text  string
	calls []string
	trail judge.Trail
	turns int
	cost  float64
}

// loadAnswers reads the transcripts of task and run index n of both servers, from dirs in order of preference.
// ok is false when no folder holds the task.
func loadAnswers(dirs []string, task string, n int) (map[string]answer, bool, error) {
	out := map[string]answer{}
	for _, server := range []string{results.DBA, results.Crystal} {
		name := fmt.Sprintf("%s-%s-%d.jsonl", task, server, n)
		for _, dir := range dirs {
			f, err := os.Open(filepath.Join(dir, "n3", name))
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return nil, false, err
			}
			tr, err := agent.Parse(f)
			_ = f.Close()
			a := answer{text: "(no answer: the run did not finish)", trail: judge.Trail{File: name}}
			if err == nil && !tr.IsError && strings.TrimSpace(tr.Final) != "" {
				a = answer{text: tr.Final, turns: tr.Turns, cost: tr.CostUSD, trail: judge.Trail{File: name}}
				for _, c := range tr.Calls {
					a.calls = append(a.calls, c.Name)
					a.trail.Calls = append(a.trail.Calls, judge.Call{Name: c.Name, Input: string(c.Input), Result: c.Result, IsError: c.IsError})
				}
			}
			out[server] = a
			break
		}
	}
	switch len(out) {
	case 0:
		return nil, false, nil
	case 2:
		return out, true, nil
	}
	return nil, false, fmt.Errorf("transcript of %s run %d exists for only one server", task, n)
}

// cmdCalibration prints how the verdicts of a filled calibration sheet compare with the judge's, read from the
// calibration-key.json next to it. It calls nothing.
func cmdCalibration(s *session) error {
	sheet, err := os.ReadFile(s.opts.calibration)
	if err != nil {
		return fmt.Errorf("read calibration sheet: %w", err)
	}
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(s.opts.calibration), "calibration-key.json"))
	if err != nil {
		return fmt.Errorf("read calibration key: %w", err)
	}
	key, err := judge.ReadKey(raw)
	if err != nil {
		return err
	}
	ag, err := judge.Compare(string(sheet), key)
	if err != nil {
		return err
	}
	for _, l := range ag.Lines {
		_, _ = fmt.Fprintln(s.out, l)
	}
	_, _ = fmt.Fprintf(s.out, "agreement: %d of %d filled pairs (%d pairs on the sheet)\n", ag.Agree, ag.Filled, len(key.Pairs))
	return nil
}

// cmdJudge judges the stored N3 answers of --run (and of the runs of --also, a comma-separated list, for the tasks
// --run lacks), writes judge.json, one side-by-side file per task under compare/ and the calibration sheet and key.
// It calls claude for every pass that has no kept reply. With --calibration=<file> it only reads a filled sheet.
func cmdJudge(ctx context.Context, s *session) error {
	if s.opts.calibration != "" {
		return cmdCalibration(s)
	}
	if s.opts.runID == "" {
		return errors.New("judge needs --run=<id>")
	}
	dir, err := s.runFolder()
	if err != nil {
		return err
	}
	dirs := []string{dir}
	for _, id := range strings.Split(s.opts.also, ",") {
		if id == "" {
			continue
		}
		also, err := results.Dir(s.resultsRoot(), id)
		if err != nil {
			return err
		}
		dirs = append(dirs, also)
	}
	db, err := s.pool(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	ref, err := agent.LoadRef(ctx, db)
	if err != nil {
		return err
	}
	answers := map[string]map[string]answer{} // "T01-1" -> server -> answer
	var cases []judge.Case
	var tasks []agent.Task
	for _, task := range agent.AllTasks() {
		found := false
		for n := 1; n <= agent.Runs; n++ {
			a, ok, err := loadAnswers(dirs, task.ID, n)
			if err != nil {
				return err
			}
			if !ok {
				continue
			}
			found = true
			answers[fmt.Sprintf("%s-%d", task.ID, n)] = a
			cases = append(cases, judge.Case{Task: task.ID, Run: n, Question: task.Question, Facts: judge.Facts(task.ID, ref),
				Answers: map[string]string{results.DBA: a[results.DBA].text, results.Crystal: a[results.Crystal].text},
				Trails:  map[string]judge.Trail{results.DBA: a[results.DBA].trail, results.Crystal: a[results.Crystal].trail}})
		}
		if found {
			tasks = append(tasks, task)
		}
	}
	if len(cases) == 0 {
		return fmt.Errorf("no N3 transcript in %s", strings.Join(dirs, ", "))
	}
	cache := filepath.Join(dir, "judge")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return fmt.Errorf("create judge folder: %w", err)
	}
	effort := s.getenv("BENCH_JUDGE_EFFORT")
	j := &judge.Judge{Call: judge.Claude(s.getenv("BENCH_JUDGE_MODEL"), effort), Dir: cache, Log: s.err, Budget: s.opts.maxCost, Parallel: 4}
	// First pass, then attribution, then the repeats: a budget that runs out costs the repeats first.
	spent := func() { _, _ = fmt.Fprintf(s.err, "judge cost so far: %.2f USD (%s)\n", j.Spent(), j.Model()) }
	pairs, err := j.Run(ctx, cases, 1)
	if err == nil {
		var attr []results.JudgeAttribution
		if attr, err = j.Attribute(ctx, cases, pairs); err == nil {
			if pairs, err = j.Run(ctx, cases, s.opts.repeat); err == nil {
				err = results.Save(dir, "judge", results.Judge{Model: j.Model(), Effort: effort, CostUSD: j.Spent(), Pairs: pairs, Attributions: attr})
			}
		}
	}
	spent()
	if err != nil {
		return err
	}
	if err := writeCalibration(dir, cases, pairs); err != nil {
		return err
	}
	return writeCompare(filepath.Join(dir, "compare"), tasks, answers, pairs)
}

// writeCalibration writes calibration.md, the blind sheet for the human, and calibration-key.json.
func writeCalibration(dir string, cases []judge.Case, pairs []results.JudgePair) error {
	sheet, key := judge.Sheet(cases, pairs, 10)
	raw, err := json.MarshalIndent(key, "", "  ")
	if err != nil {
		return fmt.Errorf("encode calibration key: %w", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "calibration.md"), []byte(sheet), 0o644); err != nil {
		return fmt.Errorf("write calibration sheet: %w", err)
	}
	return os.WriteFile(filepath.Join(dir, "calibration-key.json"), raw, 0o644)
}

// writeCompare writes compare/<task>.md: for every run index, the final answers of both servers side by side
// and the judge's verdict on them.
func writeCompare(dir string, tasks []agent.Task, answers map[string]map[string]answer, pairs []results.JudgePair) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create compare folder: %w", err)
	}
	for _, task := range tasks {
		var sb strings.Builder
		fmt.Fprintf(&sb, "# %s\n\n%s\n\n", task.ID, task.Question)
		for _, p := range pairs {
			if p.Task != task.ID {
				continue
			}
			a := answers[fmt.Sprintf("%s-%d", p.Task, p.Run)]
			fmt.Fprintf(&sb, "## Run %d: %s\n\n", p.Run, outcomeText(p.Outcome))
			sb.WriteString("<table>\n<tr><th>dba</th><th>crystal</th></tr>\n<tr>\n")
			for _, server := range []string{results.DBA, results.Crystal} {
				x := a[server]
				fmt.Fprintf(&sb, "<td valign=\"top\">\n\n<sub>%d turns, %.2f USD, tools: %s</sub>\n\n<pre>%s</pre>\n\n</td>\n",
					x.turns, x.cost, html.EscapeString(strings.Join(x.calls, ", ")), html.EscapeString(x.text))
			}
			sb.WriteString("</tr>\n</table>\n\n")
			for _, o := range p.Orders {
				fmt.Fprintf(&sb, "Judge, %s shown as A: winner %s. %s\n\n", o.A, o.Winner, o.Reason)
				for _, server := range []string{results.DBA, results.Crystal} {
					sc := o.Scores[server]
					fmt.Fprintf(&sb, "- %s: correctness %d, completeness %d, actionability %d, errors: %s\n", server,
						sc.Correctness, sc.Completeness, sc.Actionability, errorsText(sc.Errors))
				}
				sb.WriteString("\n")
			}
		}
		if err := os.WriteFile(filepath.Join(dir, task.ID+".md"), []byte(sb.String()), 0o644); err != nil {
			return fmt.Errorf("write compare file: %w", err)
		}
	}
	return nil
}

func outcomeText(o string) string {
	if o == judge.Tie {
		return "tie"
	}
	return o + " wins"
}

func errorsText(e []string) string {
	if len(e) == 0 {
		return "none"
	}
	return strings.Join(e, "; ")
}
