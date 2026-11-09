// Command toolboxmeta writes bench/results/<run>/toolbox.json: what MCP Toolbox was measured as, and what its
// tool definitions cost one tool at a time, which n1.json only totals. Run it from the bench folder:
//
//	go run ./cmd/toolboxmeta --run=<id>
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/load"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// containerDSN is the bench database as a container on the bench network sees it, as cmd/bench uses it.
const containerDSN = "postgres://bench_ro:bench@pg:5432/bench"

// tool is one tool definition with what it costs on its own.
type tool struct {
	Name   string `json:"name"`
	Tokens int    `json:"tokens"`
	Bytes  int    `json:"bytes"` // name + description + encoded input schema
}

// variant is one measured configuration of the server.
type variant struct {
	Name    string `json:"name"`
	Argv    string `json:"argv"`
	Version string `json:"server_version"` // from the initialize handshake
	Tools   []tool `json:"tools"`
	Total   int    `json:"total_tokens"` // the whole tool set, as n1 counts it
}

// sum adds the definitions counted one by one.
func (v variant) sum() int {
	n := 0
	for _, t := range v.Tools {
		n += t.Tokens
	}
	return n
}

// out is toolbox.json.
type out struct {
	RunID        string    `json:"run_id"`
	Date         string    `json:"date"`
	Image        string    `json:"image"`
	Digest       string    `json:"digest"`
	CLIVersion   string    `json:"cli_version"` // `toolbox --version` of that image
	DBACommit    string    `json:"dba_commit"`
	TokenCounter string    `json:"token_counter"`
	Model        string    `json:"model"`
	PGVersion    string    `json:"pg_version"`
	Scale        float64   `json:"scale"`
	Pagila       bool      `json:"pagila"`
	CanonicalMap []string  `json:"canonical_map"` // canonical bench tool -> Toolbox tool, "-" when there is none
	Variants     []variant `json:"variants"`
}

func main() {
	if err := run(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context) error {
	runID := flag.String("run", "", "run id of the results folder")
	flag.Parse()
	if *runID == "" {
		return fmt.Errorf("usage: go run ./cmd/toolboxmeta --run=<id>")
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(dir, "compose.yml")); err != nil {
		return fmt.Errorf("run this from the bench folder (no compose.yml in %s)", dir)
	}
	counter, err := tokens.New(os.Getenv, "")
	if err != nil {
		return err
	}
	o := out{RunID: *runID, Date: time.Now().UTC().Format(time.RFC3339), Image: servers.ToolboxImage,
		TokenCounter: counter.Label, Model: counter.Model}
	_, o.Digest, _ = strings.Cut(servers.ToolboxImage, "@")
	if o.CLIVersion, err = cliVersion(ctx); err != nil {
		return err
	}
	if o.DBACommit, err = commit(ctx, filepath.Dir(dir)); err != nil {
		return err
	}
	if err := database(ctx, &o); err != nil {
		return err
	}
	for _, name := range []string{servers.Toolbox, servers.ToolboxSubset} {
		v, err := measure(ctx, counter, name)
		if err != nil {
			return err
		}
		// The estimate counter counts each definition on its own, so the parts must add up to the whole; the
		// count_tokens API adds a fixed tool-use overhead to a set, which they would not.
		if sum := v.sum(); counter.Label == results.CounterEstimate && sum != v.Total {
			return fmt.Errorf("%s: the %d tool definitions add up to %d tokens, the whole set counts %d", name, len(v.Tools), sum, v.Total)
		}
		o.Variants = append(o.Variants, v)
		if o.CanonicalMap == nil {
			o.CanonicalMap = canonicalMap(name)
		}
	}
	d, err := results.Dir(filepath.Join(dir, "results"), *runID)
	if err != nil {
		return err
	}
	if err := results.Save(d, "toolbox", o); err != nil {
		return err
	}
	fmt.Printf("toolbox.json written for run %s\n", *runID)
	return nil
}

// canonical names the bench tools, in the order the scenarios use them.
var canonical = []string{"execute_sql", "list_schemas", "list_objects", "get_object_details", "explain_query",
	"get_top_queries", "analyze_db_health", "analyze_query_indexes"}

func canonicalMap(server string) []string {
	f, _ := servers.Lookup(server)
	spec := f(servers.Target{DSN: servers.BenchDSN, URI: containerDSN})
	m := make([]string, 0, len(canonical))
	for _, c := range canonical {
		name, ok := spec.ToolName(c)
		if !ok {
			name = "-"
		}
		m = append(m, c+" -> "+name)
	}
	return m
}

// measure lists the tools of one variant and counts each definition on its own, then the whole set.
func measure(ctx context.Context, counter *tokens.Counter, name string) (variant, error) {
	f, ok := servers.Lookup(name)
	if !ok {
		return variant{}, fmt.Errorf("server %q is not registered", name)
	}
	spec := f(servers.Target{DSN: servers.BenchDSN, URI: containerDSN})
	spec.Name = name
	srv, err := spec.Start(ctx)
	if err != nil {
		return variant{}, err
	}
	defs, err := srv.Tools(ctx)
	info := srv.Info
	srv.Close()
	if err != nil {
		return variant{}, err
	}
	v := variant{Name: name, Argv: strings.Join(spec.Argv, " "), Version: info}
	for _, d := range defs {
		one, err := counter.Tools(ctx, []*mcp.Tool{d})
		if err != nil {
			return variant{}, err
		}
		b, err := json.Marshal(d.InputSchema)
		if err != nil {
			return variant{}, err
		}
		v.Tools = append(v.Tools, tool{Name: d.Name, Tokens: one, Bytes: len(d.Name) + len(d.Description) + len(b)})
	}
	if v.Total, err = counter.Tools(ctx, defs); err != nil {
		return variant{}, err
	}
	return v, nil
}

// cliVersion runs `toolbox --version` in the pinned image.
func cliVersion(ctx context.Context) (string, error) {
	b, err := exec.CommandContext(ctx, "docker", "run", "--rm", servers.ToolboxImage, "--version").Output()
	if err != nil {
		return "", fmt.Errorf("toolbox --version: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

func commit(ctx context.Context, repoDir string) (string, error) {
	b, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("read commit: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// database records what the bench database holds, the way cmd/bench records it.
func database(ctx context.Context, o *out) error {
	conn, err := pgx.Connect(ctx, servers.BenchDSN)
	if err != nil {
		return fmt.Errorf("connect to the bench database: %w", err)
	}
	defer func() { _ = conn.Close(ctx) }()
	var comment *string
	if err := conn.QueryRow(ctx, "SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = 'bench'").Scan(&comment); err != nil || comment == nil {
		return fmt.Errorf("read the bench database comment (run load): %v", err)
	}
	if o.Scale, o.Pagila, err = load.ParseMeta(*comment); err != nil {
		return err
	}
	if err := conn.QueryRow(ctx, "SHOW server_version").Scan(&o.PGVersion); err != nil {
		return fmt.Errorf("read the Postgres version: %w", err)
	}
	o.PGVersion, _, _ = strings.Cut(o.PGVersion, " ")
	return nil
}
