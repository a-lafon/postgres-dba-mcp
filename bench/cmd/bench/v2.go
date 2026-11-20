package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/load"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/security"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// v2Commands are the commands of protocol v2, which measure the --servers of the registry (default: all five) in
// their registry configuration, the release image of postgres-dba-mcp included.
var v2Commands = map[string]bool{"tasks": true, "receive": true, "readonly": true}

// targets returns the --servers as registry containers connected to uri, each with its memory capped, and records them
// in env in place of the levels' local build of postgres-dba-mcp.
func (s *session) targets(ctx context.Context, env *results.Env, uri string) ([]security.Target, error) {
	env.Servers = nil
	var out []security.Target
	for _, p := range s.opts.servers {
		if err := p.Prepare(ctx, s.benchDir); err != nil {
			return nil, err
		}
		env.Servers = append(env.Servers, p.Env())
		out = append(out, security.Target{Spec: scenarios.Bounded(p.Spec(uri)), Tool: p.SQLTool, Arg: p.SQLArg, ReadOnly: p.ReadOnly})
	}
	return out, nil
}

// cmdReceive runs part B (PROTOCOL.md §5): B1-B7 through each server's SQL tool, as bench_ro.
func cmdReceive(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	defer s.logCost(l.counter)
	targets, err := s.targets(ctx, &l.env, servers.BenchURI)
	if err != nil {
		return err
	}
	r := &scenarios.Receiver{DB: l.db, Counter: l.counter, Log: s.err, OutDir: filepath.Join(l.dir, "receive")}
	tools, rows, err := r.Run(ctx, targets)
	if err != nil {
		return err
	}
	bounds := fmt.Sprintf("call timeout %s, response cap %d MiB, container memory %s", mcpc.CallTimeout, scenarios.ReceiveLimit>>20, scenarios.ReceiveMemory)
	return results.Save(l.dir, "receive", results.Receive{Env: l.env, Bounds: bounds, Tools: tools, Rows: rows})
}

// cmdReadOnly runs part C (PROTOCOL.md §6): every corpus through each server's SQL tool in its read-only mode,
// connected as bench_owner, with a snapshot after every statement. It restores the bench database before each server,
// after every lasting effect, and at the end: run it alone on the bench database.
func cmdReadOnly(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	type corpus struct {
		meta  results.ReadOnlyCorpus
		cases []security.Case
	}
	var corpora []corpus
	for _, c := range []results.ReadOnlyCorpus{
		{Name: results.CorpusOwn, Label: "postgres-dba-mcp's own regression corpus", File: "testdata/safesql/attacks.sql"},
		{Name: results.CorpusExternal, Label: "external attacks, each with its source", File: "bench/data/external-attacks.sql"},
		{Name: results.CorpusLegit, Label: "legitimate statements", File: "testdata/safesql/legit.sql"},
		{Name: results.CorpusAgents, Label: "the SQL the agents sent in part A", File: s.opts.agentSQL},
	} {
		if c.File == "" {
			continue
		}
		path := c.File
		if !filepath.IsAbs(path) && c.Name != results.CorpusAgents {
			path = filepath.Join(s.repoDir, filepath.FromSlash(path))
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read corpus %s: %w", c.Name, err)
		}
		cases, err := security.ParseCorpus(string(b))
		if err != nil {
			return fmt.Errorf("corpus %s: %w", c.Name, err)
		}
		c.Cases = len(cases)
		corpora = append(corpora, corpus{c, cases})
	}
	var all []security.Case
	for _, c := range corpora {
		all = append(all, c.cases...)
	}
	targets, err := s.targets(ctx, &l.env, servers.BenchOwnerURI)
	if err != nil {
		return err
	}
	reset := func(ctx context.Context) error {
		start := time.Now()
		err := load.Restore(ctx, s.benchDir, s.err)
		_, _ = fmt.Fprintf(s.err, "readonly: restored the bench database in %s\n", time.Since(start).Round(100*time.Millisecond))
		return err
	}
	r := &security.Replayer{AdminDSN: servers.BenchAdminDSN, Channels: security.Channels(all), Log: s.err, Reset: reset}
	defer r.Close(ctx)
	out := results.ReadOnly{Env: l.env, Role: "bench_owner"}
	for _, c := range corpora {
		out.Corpora = append(out.Corpora, c.meta)
	}
	for _, tg := range targets {
		base, err := r.Baseline(ctx)
		if err != nil {
			return err
		}
		srv := results.ReadOnlyServer{Server: tg.Spec.Name, ReadOnly: tg.ReadOnly, Corpora: map[string][]results.ReadOnlyCase{}}
		for _, c := range corpora {
			var got []results.ReadOnlyCase
			if got, base, err = r.ReplayReadOnly(ctx, tg, c.cases, base); err != nil {
				return fmt.Errorf("%s, corpus %s: %w", tg.Spec.Name, c.meta.Name, err)
			}
			srv.Corpora[c.meta.Name] = got
		}
		out.Servers = append(out.Servers, srv)
		if err := results.Save(l.dir, "readonly", out); err != nil { // keep what is done if a later server fails
			return err
		}
	}
	r.Close(ctx)
	return reset(ctx)
}
