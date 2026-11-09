// Command toolmeta writes bench/results/<run>/<out>.json: for every measured server, the command line it is started
// with, the tools it exposes and the token cost of each of their definitions, plus its image, the digest, the version
// the server reports and the environment the run was measured in.
//
// It is the per-tool detail the `n1` level does not break down; `n1` gives the total for the same tool sets. Run it
// from the bench folder, after `n1`, whose n1.json it reads the environment from:
//
//	go run ./cmd/toolmeta --run=<id> [--servers=dbhub,dbhub-rows100,...] [--out=dbhub]
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tokens"
)

// containerDSN is the bench database as a container on the bench network sees it, as cmd/bench uses it.
const containerDSN = "postgres://bench_ro:bench@pg:5432/bench"

// tool is one tool definition of a variant.
type tool struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Tokens      int    `json:"tokens"`
	Bytes       int    `json:"bytes"`
}

// variant is one measured server configuration.
type variant struct {
	Name string `json:"name"`
	// Image is the image the server runs in, with its digest, empty for a native process.
	Image string `json:"image,omitempty"`
	// Argv is the exact command line the harness starts the server with.
	Argv []string `json:"argv"`
	// Config is the dbhub.toml the command writes inside the container, empty for the untuned default.
	Config string `json:"config,omitempty"`
	// Server is the name and version from the initialize handshake.
	Server string `json:"server"`
	// Canonical maps the bench's canonical tool names to this variant's own; a canonical tool that is missing here
	// has no equivalent and its scenarios are reported as "no tool".
	Canonical map[string]string `json:"canonical_tools"`
	Tools     []tool            `json:"tools"`
	// Total is the token cost of the whole tool set, counted in one call as n1 counts it.
	Total int `json:"total_tokens"`
}

// out is <out>.json.
type out struct {
	Env     results.Env `json:"env"`
	Date    string      `json:"date"`
	Variant []variant   `json:"variants"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	runID := flag.String("run", "", "run id of the results folder")
	names := flag.String("servers", strings.Join([]string{servers.DBHubRef, servers.DBHubRows100, servers.DBHub4Tools, servers.DBHubDefault}, ","), "servers to measure")
	outName := flag.String("out", "dbhub", "name of the JSON file written in the run folder")
	flag.Parse()
	if *runID == "" {
		return fmt.Errorf("--run=<id> is required")
	}
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(wd, "compose.yml")); err != nil {
		return fmt.Errorf("run it from the bench folder (no compose.yml in %s)", wd)
	}
	dir, err := results.Dir(filepath.Join(wd, "results"), *runID)
	if err != nil {
		return err
	}
	var n1 results.N1
	if ok, err := results.Load(dir, "n1", &n1); err != nil || !ok {
		return fmt.Errorf("no n1.json in run %s, run `go run ./cmd/bench n1` first: %v", *runID, err)
	}
	counter, err := tokens.New(os.Getenv, "")
	if err != nil {
		return err
	}
	o := out{Env: n1.Env, Date: time.Now().UTC().Format(time.RFC3339)}
	ctx := context.Background()
	for _, name := range strings.Split(*names, ",") {
		v, err := measure(ctx, counter, name, servers.BinPath(wd))
		if err != nil {
			return err
		}
		o.Variant = append(o.Variant, v)
	}
	return results.Save(dir, *outName, o)
}

func measure(ctx context.Context, c *tokens.Counter, name, bin string) (variant, error) {
	specs, err := servers.Of([]string{name}, servers.Target{DSN: servers.BenchDSN, URI: containerDSN}, bin)
	if err != nil {
		return variant{}, err
	}
	spec := specs[0]
	v := variant{Name: name, Image: spec.Image, Argv: spec.Argv, Canonical: spec.Tools}
	for i, a := range spec.Argv {
		if cfg, ok := strings.CutPrefix(a, "DBHUB_TOML="); ok && i > 0 && spec.Argv[i-1] == "-e" {
			v.Config = cfg
		}
	}
	srv, err := spec.Start(ctx)
	if err != nil {
		return variant{}, err
	}
	defer srv.Close()
	v.Server = srv.Info
	tools, err := srv.Tools(ctx)
	if err != nil {
		return variant{}, err
	}
	for _, t := range tools {
		one, err := c.Tools(ctx, []*mcp.Tool{t})
		if err != nil {
			return variant{}, err
		}
		b, err := json.Marshal(t.InputSchema)
		if err != nil {
			return variant{}, err
		}
		v.Tools = append(v.Tools, tool{Name: t.Name, Description: t.Description, Tokens: one, Bytes: len(t.Name + t.Description + string(b))})
	}
	if v.Total, err = c.Tools(ctx, tools); err != nil {
		return variant{}, err
	}
	fmt.Fprintf(os.Stderr, "%s: %d tools, %d tokens\n", name, len(v.Tools), v.Total)
	return v, nil
}
