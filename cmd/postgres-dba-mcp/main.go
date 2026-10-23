// Command postgres-dba-mcp is an MCP server that gives an AI client bounded, compact access to one PostgreSQL database.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/httpx"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

// version is set at build time with -ldflags "-X main.version=<v>".
var version = "dev"

// effectiveVersion returns v, or the module version from the build info when v is the default "dev" and the
// binary was installed from a module version (go install ...@v0.1.0). A build in a VCS checkout records
// vcs.revision and a pseudo-version: that is a local build and reports "dev".
func effectiveVersion(v string, read func() (*debug.BuildInfo, bool)) string {
	if v != "dev" {
		return v
	}
	info, ok := read()
	if !ok || info.Main.Version == "" || info.Main.Version == "(devel)" {
		return v
	}
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			return v
		}
	}
	return info.Main.Version
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Getenv, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, getenv func(string) string, stdout, stderr io.Writer) int {
	version := effectiveVersion(version, debug.ReadBuildInfo)
	cfg, err := config.Parse(args, getenv)
	if errors.Is(err, flag.ErrHelp) {
		_, _ = fmt.Fprint(stdout, usage)
		return 0
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	if cfg.ShowVersion {
		_, _ = fmt.Fprintf(stdout, "postgres-dba-mcp %s\n", version)
		return 0
	}
	d, err := db.New(cfg)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 2
	}
	defer d.Close()

	_, _ = fmt.Fprintf(stderr, "postgres-dba-mcp %s: target %s, access mode %s, transport %s\n",
		version, cfg.Redacted(), cfg.AccessMode, cfg.Transport)
	if cfg.AccessMode == config.Unrestricted {
		_, _ = fmt.Fprintln(stderr, "WARNING: unrestricted mode: tools can modify the database; use a read-only role or --access-mode=restricted outside development")
	}

	reg := server.New(cfg, d, version)
	registerTools(reg)
	if err := serve(ctx, cfg, reg); err != nil && !errors.Is(err, context.Canceled) {
		_, _ = fmt.Fprintf(stderr, "error: %v\n", err)
		return 1
	}
	return 0
}

// serve runs the transport chosen by --transport until ctx is canceled.
func serve(ctx context.Context, cfg *config.Config, reg *server.Registry) error {
	if cfg.Transport == config.Stdio {
		return reg.MCP().Run(ctx, &mcp.StdioTransport{})
	}
	return httpx.Serve(ctx, cfg, reg)
}
