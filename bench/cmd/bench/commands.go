package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/load"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/ops"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/report"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/security"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// maxBytes is postgres-dba-mcp's default --max-bytes, which the bench does not override.
const maxBytes = 32768

func (s *session) compose(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "docker", append([]string{"compose", "-f", filepath.Join(s.benchDir, "compose.yml")}, args...)...)
	cmd.Stdout, cmd.Stderr = s.err, s.err
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("docker compose %s: %w", strings.Join(args, " "), err)
	}
	return nil
}

func (s *session) bin() string { return servers.BinPath(s.benchDir) }

// ensureBinary builds postgres-dba-mcp from the repository when bench/bin has no binary.
func (s *session) ensureBinary(ctx context.Context) error {
	if _, err := os.Stat(s.bin()); err == nil {
		return nil
	}
	return s.build(ctx)
}

func (s *session) build(ctx context.Context) error {
	_, _ = fmt.Fprintln(s.err, "build postgres-dba-mcp")
	return servers.Build(ctx, s.repoDir, s.benchDir)
}

// buildImage builds the linux binary and the image `ops` measures.
func (s *session) buildImage(ctx context.Context) error {
	_, _ = fmt.Fprintln(s.err, "build linux image "+servers.ImageTag)
	start := time.Now()
	if err := servers.BuildLinux(ctx, s.repoDir, s.benchDir); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(s.err, "built %s in %s\n", servers.ImageTag, time.Since(start).Round(time.Second))
	return nil
}

func cmdUp(ctx context.Context, s *session) error {
	if err := s.compose(ctx, "up", "-d", "--build", "--wait"); err != nil {
		return err
	}
	if err := s.build(ctx); err != nil {
		return err
	}
	return s.buildImage(ctx)
}

func cmdLoad(ctx context.Context, s *session) error {
	var loaded bool
	if pool, err := s.pool(ctx); err == nil {
		_ = pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_namespace WHERE nspname = 'traps')").Scan(&loaded)
		pool.Close()
	}
	if loaded {
		return errors.New("the bench database is already loaded: run `docker compose -f compose.yml down -v`, then `up`, then `load`")
	}
	return load.Run(ctx, load.Options{BenchDir: s.benchDir, Scale: s.opts.scale, Pagila: !s.opts.noPagila, Log: s.err, Client: &http.Client{Timeout: 5 * time.Minute}})
}

// pool connects to the bench database as bench_ro.
func (s *session) pool(ctx context.Context) (*pgxpool.Pool, error) {
	p, err := pgxpool.New(ctx, servers.BenchDSN)
	if err != nil {
		return nil, fmt.Errorf("connect to the bench database: %w", err)
	}
	if err := p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("connect to the bench database (run `up` and `load`): %w", err)
	}
	return p, nil
}

// env describes the bench database, the build under test and the token counter, for the result files.
func (s *session) env(ctx context.Context, db *pgxpool.Pool, c *tokens.Counter) (results.Env, error) {
	e := results.Env{RunID: s.runID, TokenCounter: c.Label, Model: c.Model, CrystalDigest: digestOf(servers.CrystalImage), PgEdge: s.opts.withPgEdge}
	var comment *string
	if err := db.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'bench'").Scan(&comment); err != nil || comment == nil {
		return e, fmt.Errorf("read the bench database comment (run load): %v", err)
	}
	var err error
	if e.Scale, e.Pagila, err = load.ParseMeta(*comment); err != nil {
		return e, err
	}
	if err := db.QueryRow(ctx, "SHOW server_version").Scan(&e.PGVersion); err != nil {
		return e, fmt.Errorf("read the Postgres version: %w", err)
	}
	e.PGVersion, _, _ = strings.Cut(e.PGVersion, " ")
	if err := s.ensureBinary(ctx); err != nil {
		return e, err
	}
	info, err := servers.DBAInfo(ctx, s.bin(), s.repoDir)
	if err != nil {
		return e, err
	}
	e.DBAVersion, e.DBACommit = info.Version, info.Commit
	return e, nil
}

func digestOf(image string) string {
	_, d, _ := strings.Cut(image, "@")
	return d
}

// level is what every measuring command starts from.
type level struct {
	db      *pgxpool.Pool
	env     results.Env
	counter *tokens.Counter
	dir     string
}

func (s *session) begin(ctx context.Context) (*level, error) {
	db, err := s.pool(ctx)
	if err != nil {
		return nil, err
	}
	c, err := tokens.New(s.getenv, s.opts.tokens)
	if err == nil {
		err = c.Prepare(ctx)
	}
	if err != nil {
		db.Close()
		return nil, err
	}
	env, err := s.env(ctx, db, c)
	if err != nil {
		db.Close()
		return nil, err
	}
	dir, err := s.runFolder()
	if err != nil {
		db.Close()
		return nil, err
	}
	return &level{db: db, env: env, counter: c, dir: dir}, nil
}

func (l *level) close() { l.db.Close() }

// logCost says what the claude -p counting calls cost.
func (s *session) logCost(c *tokens.Counter) {
	if c.Calls > 0 {
		_, _ = fmt.Fprintf(s.err, "claude -p counting: %d calls, total_cost_usd %.4f\n", c.Calls, c.CostUSD)
	}
}

func (s *session) dbaSpec() servers.Spec { return servers.DBA(s.bin(), servers.BenchDSN) }

// containerDSN is the bench database as a container on the bench network sees it.
const containerDSN = "postgres://bench_ro:bench@pg:5432/bench"

func (s *session) crystalSpec() servers.Spec { return servers.Crystal(containerDSN) }

func cmdN1(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	defer s.logCost(l.counter)
	out := results.N1{Env: l.env}
	for _, spec := range []servers.Spec{s.dbaSpec(), s.crystalSpec()} {
		srv, err := spec.Start(ctx)
		if err != nil {
			return err
		}
		tools, err := srv.Tools(ctx)
		instructions := srv.Instructions
		srv.Close()
		if err != nil {
			return err
		}
		names := make([]string, len(tools))
		for i, t := range tools {
			names[i] = t.Name
		}
		full, err := l.counter.Tools(ctx, tools)
		if err != nil {
			return err
		}
		deferred, err := l.counter.Text(ctx, strings.Join(names, "\n")+"\n"+instructions)
		if err != nil {
			return err
		}
		out.Servers = append(out.Servers, results.N1Server{Server: spec.Name, Tools: len(tools), Full: full, Deferred: deferred})
	}
	return results.Save(l.dir, "n1", out)
}

// sameRole checks that the sessions of both servers run as bench_ro, and only as bench_ro.
func sameRole(ctx context.Context, db *pgxpool.Pool, specs []servers.Spec) (bool, error) {
	for _, spec := range specs {
		srv, err := spec.Start(ctx)
		if err != nil {
			return false, err
		}
		defer srv.Close()
		if _, err := srv.Call(ctx, "list_schemas", map[string]any{}); err != nil {
			return false, fmt.Errorf("list_schemas on %s: %w", spec.Name, err)
		}
	}
	rows, err := db.Query(ctx, "SELECT DISTINCT usename FROM pg_stat_activity WHERE datname = 'bench' AND backend_type = 'client backend'")
	if err != nil {
		return false, fmt.Errorf("read pg_stat_activity: %w", err)
	}
	defer rows.Close()
	var users []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return false, fmt.Errorf("read pg_stat_activity: %w", err)
		}
		users = append(users, u)
	}
	return len(users) == 1 && users[0] == "bench_ro", rows.Err()
}

// cmdN2Recheck grades again the kept n2 outputs of a run: the bench database gives the facts, nothing else is called.
func cmdN2Recheck(ctx context.Context, s *session) error {
	if s.opts.runID == "" {
		return errors.New("n2 --recheck needs --run=<id>")
	}
	dir, err := s.runFolder()
	if err != nil {
		return err
	}
	var n2 results.N2
	if ok, err := results.Load(dir, "n2", &n2); err != nil || !ok {
		return fmt.Errorf("no n2.json in run %s: %v", s.runID, err)
	}
	db, err := s.pool(ctx)
	if err != nil {
		return err
	}
	defer db.Close()
	r := &scenarios.Runner{DB: db, OutDir: filepath.Join(dir, "n2")}
	if n2.Rows, err = r.Recheck(ctx, n2.Rows); err != nil {
		return err
	}
	return results.Save(dir, "n2", n2)
}

func cmdN2(ctx context.Context, s *session) error {
	if s.opts.recheck {
		return cmdN2Recheck(ctx, s)
	}
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	defer s.logCost(l.counter)
	specs := []servers.Spec{s.dbaSpec(), s.crystalSpec()}
	same, err := sameRole(ctx, l.db, specs)
	if err != nil {
		return err
	}
	if s.opts.withPgEdge {
		if p, err := servers.PgEdge(servers.BenchDSN); err == nil {
			specs = append(specs, p)
		} else if !errors.Is(err, servers.ErrMissing) {
			return err
		}
	}
	r := &scenarios.Runner{DB: l.db, Counter: l.counter, Specs: specs, Pagila: l.env.Pagila, Log: s.err, OutDir: filepath.Join(l.dir, "n2")}
	rows, err := r.Run(ctx)
	if err != nil {
		return err
	}
	if s.opts.withPgEdge && len(specs) == 2 { // binary missing: its cells read skipped
		for _, row := range rows {
			if row.ID <= "S06" {
				row.Cells[results.PgEdge] = results.N2Cell{Status: results.StatusSkipped}
			}
		}
	}
	return results.Save(l.dir, "n2", results.N2{Env: l.env, MaxBytes: maxBytes, SameRole: same, Rows: rows})
}

func cmdSecurity(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	attacks, err := readCorpus(s, "attacks.sql")
	if err != nil {
		return err
	}
	legit, err := readCorpus(s, "legit.sql")
	if err != nil {
		return err
	}
	if err := s.compose(ctx, "up", "-d", "--wait", "fixture"); err != nil {
		return err
	}
	r := &security.Replayer{Compose: filepath.Join(s.benchDir, "compose.yml"), AdminDSN: servers.FixtureAdminDSN, Channels: security.Channels(attacks), Log: s.err}
	if err := r.Open(ctx); err != nil {
		return err
	}
	defer r.Close(ctx)
	out := results.Security{Env: l.env, Attacks: map[string][]results.SecurityCase{}}
	dba := servers.DBA(s.bin(), servers.FixtureDSN)
	crystal := servers.Crystal("postgres://pgdba_ro:pgdba@fixture:5432/pgdba")
	for _, spec := range []servers.Spec{dba, crystal} {
		if out.Attacks[spec.Name], err = r.Replay(ctx, spec, attacks, true); err != nil {
			return err
		}
	}
	if out.Legit, err = r.Replay(ctx, dba, legit, false); err != nil {
		return err
	}
	return results.Save(l.dir, "security", out)
}

func readCorpus(s *session, name string) ([]security.Case, error) {
	b, err := os.ReadFile(filepath.Join(s.repoDir, "testdata", "safesql", name))
	if err != nil {
		return nil, fmt.Errorf("read corpus: %w", err)
	}
	return security.ParseCorpus(string(b))
}

func cmdN3(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	if !l.env.Pagila {
		return errors.New("n3 needs the bench database loaded with Pagila")
	}
	admin, err := pgxpool.New(ctx, servers.BenchAdminDSN)
	if err != nil {
		return fmt.Errorf("connect to the bench database as postgres: %w", err)
	}
	defer admin.Close()
	r := &agent.Runner{DB: l.db, Admin: admin, RepoDir: s.repoDir, OutDir: l.dir, Log: s.err, Servers: []string{results.DBA, results.Crystal}, Tasks: s.n3Tasks()}
	run := r.Run
	if s.opts.regrade {
		run = r.Regrade
	}
	runs, err := run(ctx)
	if err != nil {
		return err
	}
	return results.Save(l.dir, "n3", results.N3{Env: l.env, Runs: runs})
}

func cmdOps(ctx context.Context, s *session) error {
	l, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer l.close()
	if !servers.ImageExists(ctx) {
		if err := s.buildImage(ctx); err != nil {
			return err
		}
	}
	out, err := ops.Run(ctx, ops.Options{DBA: s.dbaSpec(), Image: servers.DBAImage(containerDSN), Crystal: s.crystalSpec(), Bin: s.bin(), DSN: servers.BenchDSN, Pagila: l.env.Pagila, Log: s.err})
	if err != nil {
		return err
	}
	out.Env = l.env
	return results.Save(l.dir, "ops", out)
}

func cmdReport(_ context.Context, s *session) error {
	dir, err := s.runFolder()
	if err != nil {
		return err
	}
	in, err := report.Load(dir, s.runID)
	if err != nil {
		return err
	}
	md, v := report.Render(in)
	if err := os.WriteFile(filepath.Join(s.benchDir, "report.md"), []byte(md), 0o644); err != nil {
		return fmt.Errorf("write report.md: %w", err)
	}
	_, _ = fmt.Fprintf(s.out, "report.md written for run %s: %s\n", s.runID, v.Name)
	if s.opts.failOnBlk {
		for _, c := range report.Criteria(in) {
			if (c.Key == report.KeySecurity || c.Key == "n2-bytes") && c.Met == report.No {
				return fmt.Errorf("blocking target not met: %s (%s)", c.Name, c.Result)
			}
		}
	}
	return nil
}

// cmdAll runs every level except n3, then the report.
func cmdAll(ctx context.Context, s *session) error {
	for _, step := range []struct {
		name string
		f    func(context.Context, *session) error
	}{{"up", cmdUp}, {"load", cmdLoad}, {"n1", cmdN1}, {"n2", cmdN2}, {"security", cmdSecurity}, {"ops", cmdOps}, {"report", cmdReport}} {
		_, _ = fmt.Fprintf(s.err, "== %s\n", step.name)
		if err := step.f(ctx, s); err != nil {
			return fmt.Errorf("%s: %w", step.name, err)
		}
	}
	return nil
}
