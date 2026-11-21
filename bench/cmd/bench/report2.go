package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/report"
)

// cmdReport2 writes report-v2.md from the stored results of protocol v2, read from --results-dir (default
// bench/results) and the run folders --part-a to --part-d. It calls no database, no server and no model.
func cmdReport2(_ context.Context, s *session) error {
	root := s.opts.resultsDir
	if root == "" {
		root = s.resultsRoot()
	}
	runs := report.V2Runs{A: s.opts.partA, B: s.opts.partB, C: s.opts.partC, D: s.opts.partD}
	in, err := report.LoadV2(root, runs)
	if err != nil {
		return err
	}
	out := filepath.Join(s.benchDir, "report-v2.md")
	if err := os.WriteFile(out, []byte(report.RenderV2(in)), 0o644); err != nil {
		return fmt.Errorf("write report-v2.md: %w", err)
	}
	_, _ = fmt.Fprintf(s.out, "report-v2.md written from %s (parts %s, %s, %s, %s)\n", root, runs.A, runs.B, runs.C, runs.D)
	return nil
}
