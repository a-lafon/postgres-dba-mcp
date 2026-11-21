package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/judge"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tasks"
)

// judgeInput reads the judge input of the run: --input, or judge-input.json in the run folder.
func (s *session) judgeInput() (string, []judge.Item, error) {
	if s.opts.runID == "" {
		return "", nil, fmt.Errorf("%s needs --run=<id>", s.opts.command)
	}
	dir, err := s.runFolder()
	if err != nil {
		return "", nil, err
	}
	file := s.opts.input
	if file == "" {
		file = filepath.Join(dir, "judge-input.json")
	}
	b, err := os.ReadFile(file)
	if errors.Is(err, fs.ErrNotExist) && s.opts.input == "" {
		b, err = writePartAInput(dir, file)
	}
	if err != nil {
		return "", nil, fmt.Errorf("read judge input: %w", err)
	}
	items, err := judge.ReadItems(b)
	return dir, items, err
}

// writePartAInput adapts the part-A records of the run folder to the judge input and writes it to file, so that
// judge2 and calibration read the same answers.
func writePartAInput(dir, file string) ([]byte, error) {
	runs, err := results.LoadTaskRuns(dir)
	if err != nil {
		return nil, err
	}
	if len(runs) == 0 {
		return nil, fmt.Errorf("%s: %w, and no part A run in %s", file, fs.ErrNotExist, dir)
	}
	questions := map[string]string{}
	for _, t := range tasks.All {
		questions[t.ID] = t.Question
	}
	b, err := json.MarshalIndent(judge.ItemsFromTaskRuns(runs, questions), "", "  ")
	if err != nil {
		return nil, err
	}
	return b, os.WriteFile(file, b, 0o644)
}

// cmdJudge2 grades every answer of the judge input with both judges of protocol v2 and writes judge2.json. Replies
// are kept under judge2/, so that an interrupted run resumes without paying again. With --dry-run it prints the
// prompts and calls nothing.
func cmdJudge2(ctx context.Context, s *session) error {
	dir, items, err := s.judgeInput()
	if err != nil {
		return err
	}
	if s.opts.dryRun {
		for _, it := range items {
			p, err := judge.PromptV2(it)
			if err != nil {
				return err
			}
			_, _ = fmt.Fprintf(s.out, "===== %s %s run %d, sent to %v =====\n%s\n", it.Task, it.Server, it.Run, judge.JudgeModels, p)
		}
		return nil
	}
	cache := filepath.Join(dir, "judge2")
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return fmt.Errorf("create judge folder: %w", err)
	}
	effort := s.getenv("BENCH_JUDGE_EFFORT")
	j := &judge.Judge{Dir: cache, Log: s.err, Budget: s.opts.maxCost, Parallel: 4, Models: map[string]judge.CallFunc{}}
	for _, m := range judge.JudgeModels {
		j.Models[m] = judge.Claude(m, effort)
	}
	out, err := j.Grade(ctx, items)
	_, _ = fmt.Fprintf(s.err, "judge2 cost: %.4f USD (%s)\n", j.Spent(), j.Model())
	if err != nil {
		return err
	}
	return results.Save(dir, "judge2", out)
}

// cmdCalibration2 exports the blind calibration sheet of judge2.json, or imports the filled grid and writes
// calibration.json. It calls nothing.
func cmdCalibration2(_ context.Context, s *session) error {
	if s.opts.runID == "" {
		return errors.New("calibration needs --run=<id>")
	}
	dir, err := s.runFolder()
	if err != nil {
		return err
	}
	var judged results.Judge2
	if ok, err := results.Load(dir, "judge2", &judged); err != nil || !ok {
		return errors.Join(errors.New("calibration needs judge2.json: run judge2 first"), err)
	}
	keyFile, gridFile := filepath.Join(dir, "calibration2-key.json"), filepath.Join(dir, "calibration2.csv")
	if s.opts.sub == "export" {
		_, items, err := s.judgeInput()
		if err != nil {
			return err
		}
		key := judge.Key2{Seed: judge.CalibrationSeed, Answers: judge.SampleCalibration(judged.Answers, judge.CalibrationSize, judge.CalibrationSeed)}
		var md, grid bytes.Buffer
		if err := judge.CalibrationSheet(items, key.Answers, &md, &grid); err != nil {
			return err
		}
		raw, err := json.MarshalIndent(key, "", "  ")
		if err != nil {
			return fmt.Errorf("encode calibration key: %w", err)
		}
		for name, b := range map[string][]byte{"calibration2.md": md.Bytes(), gridFile: grid.Bytes(), keyFile: raw} {
			if err := os.WriteFile(filepath.Join(dir, filepath.Base(name)), b, 0o644); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		}
		_, _ = fmt.Fprintf(s.out, "%d answers on %s; fill the label column of %s, then run calibration import\n",
			len(key.Answers), filepath.Join(dir, "calibration2.md"), gridFile)
		return nil
	}
	raw, err := os.ReadFile(keyFile)
	if err != nil {
		return fmt.Errorf("read calibration key: %w", err)
	}
	key, err := judge.ReadKey2(raw)
	if err != nil {
		return err
	}
	f, err := os.Open(gridFile)
	if err != nil {
		return fmt.Errorf("read calibration grid: %w", err)
	}
	defer func() { _ = f.Close() }()
	cal, err := judge.Calibrate(f, key, judged)
	if err != nil {
		return err
	}
	kappa := func(p *float64) string {
		if p == nil {
			return "undefined"
		}
		return fmt.Sprintf("%.2f", *p)
	}
	_, _ = fmt.Fprintf(s.out, "%d fact labels filled on %d answers; kappa between the judges on every answer: %s\n", cal.Filled, cal.Sheet, kappa(cal.JudgesFactKappa))
	for _, m := range judged.Judges {
		pub := "published"
		if !cal.Published[m] {
			pub = fmt.Sprintf("NOT published (fact kappa below %.1f)", judge.KappaFloor)
		}
		_, _ = fmt.Fprintf(s.out, "%s: fact kappa %s, doubt kappa %s, %s\n", m, kappa(cal.FactKappa[m]), kappa(cal.DoubtKappa[m]), pub)
	}
	return results.Save(dir, "calibration", cal)
}
