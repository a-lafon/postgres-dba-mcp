package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/load"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tasks"
)

// cmdTasks runs part A of PROTOCOL.md (§4): every task, arm, repetition and server, resuming a run folder.
func cmdTasks(ctx context.Context, s *session) error {
	ts, err := tasks.Select(s.opts.only)
	if err != nil {
		return err
	}
	if s.opts.regrade {
		return regradeTasks(ctx, s, ts)
	}
	plan := tasks.Plan(ts, s.opts.servers, s.opts.reps, s.opts.arm)
	if s.opts.dryRun {
		return tasks.DryRun(s.out, filepath.Join(s.resultsRoot(), s.runID), plan)
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return errors.New("the claude CLI is not installed or not in PATH: install Claude Code to run tasks")
	}
	db, err := s.pool(ctx)
	if err != nil {
		return err
	}
	env := results.Env{RunID: s.runID, TokenCounter: "claude -p usage", Model: tasks.Model + ", effort " + tasks.Effort}
	var comment string
	err = db.QueryRow(ctx, "SELECT coalesce(shobj_description(oid, 'pg_database'), '') FROM pg_database WHERE datname = 'bench'").Scan(&comment)
	if err == nil {
		env.Scale, env.Pagila, err = load.ParseMeta(comment)
	}
	if err == nil {
		err = db.QueryRow(ctx, "SHOW server_version").Scan(&env.PGVersion)
	}
	db.Close()
	if err != nil {
		return fmt.Errorf("read the bench database (run load): %w", err)
	}
	env.PGVersion, _, _ = strings.Cut(env.PGVersion, " ")
	if !env.Pagila {
		return errors.New("tasks needs the bench database loaded with Pagila")
	}
	for _, p := range s.opts.servers {
		if err := p.Prepare(ctx, s.benchDir); err != nil {
			return err
		}
		env.Servers = append(env.Servers, p.Env())
	}
	dir, err := s.runFolder()
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(s.err, "tasks: %d runs in %s (resume with --run=%s)\n", len(plan), dir, s.runID)
	r := &tasks.Runner{BenchDir: s.benchDir, OutDir: dir, Env: env, Log: s.err, Claude: claude}
	return r.Run(ctx, plan)
}

// regradeTasks grades again the stored runs of --run with the rubrics of now, and prints the solved table. It calls
// no model and no server, but restores the database to compute the reference from the snapshot.
func regradeTasks(ctx context.Context, s *session, ts []tasks.Task) error {
	if s.opts.runID == "" {
		return errors.New("tasks --regrade needs --run=<id>")
	}
	dir := filepath.Join(s.resultsRoot(), s.runID)
	if _, err := os.Stat(filepath.Join(dir, "tasks.json")); err != nil {
		return fmt.Errorf("no part A run in %s: %w", dir, err)
	}
	if err := load.Restore(ctx, s.benchDir, s.err); err != nil {
		return err
	}
	admin, err := pgx.Connect(ctx, servers.BenchAdminDSN)
	if err != nil {
		return fmt.Errorf("connect as postgres: %w", err)
	}
	ref, err := tasks.LoadRef(ctx, admin)
	_ = admin.Close(ctx)
	if err != nil {
		return err
	}
	runs, err := tasks.Regrade(dir, ref, ts)
	if err != nil {
		return err
	}
	_, _ = fmt.Fprint(s.out, tasks.Table(runs))
	return nil
}
