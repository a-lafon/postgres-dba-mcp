package servers

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Pinned is one server of protocol v2 (PROTOCOL.md §2): the release measured, how it is pinned, how it stays
// read-only, and the one mapping the protocol allows, which tool runs SQL and which argument holds the SQL. Every
// server runs in a container on Network, in the configuration its README recommends for PostgreSQL, with its read-only
// mode on when it has one and its default tool set.
type Pinned struct {
	Name     string // results key and --servers name
	Version  string
	Image    string // what `docker run` starts: a registry image pinned by digest, or a local tag built from Build
	Build    string // folder under the bench folder holding the Dockerfile of a local image; empty for a registry image
	Pin      string // what fixes the code that runs, in words
	ReadOnly string // the read-only model, in one phrase: parser, read-only transaction, database role, config flag
	SQLTool  string // the tool that runs SQL
	SQLArg   string // its argument that holds the SQL
	Doc      string // the documentation the configuration follows
	argv     func(uri string) []string
}

// Registry holds the five servers of PROTOCOL.md §2, in the order of the report.
var Registry = []Pinned{
	{
		Name:     results.DBA,
		Version:  "0.3.3",
		Image:    "ghcr.io/a-lafon/postgres-dba-mcp:0.3.3@sha256:83ac7320968d0aa04dd4609bd0066ede9d3c6917859e5adbd279290c2fb70b49",
		Pin:      "GHCR image of the v0.3.3 release, by digest (linux/amd64, linux/arm64)",
		ReadOnly: "parser: --access-mode=restricted parses the statement (libpg_query), allows read statements without volatile functions, and runs them in a read-only transaction",
		SQLTool:  "execute_sql",
		SQLArg:   "sql",
		Doc:      "https://github.com/a-lafon/postgres-dba-mcp/blob/v0.3.3/README.md",
		argv: func(uri string) []string {
			return []string{"-e", "DATABASE_URI=" + uri, "@image", "--access-mode=restricted"}
		},
	},
	{
		Name:     results.Crystal,
		Version:  "0.3.0",
		Image:    CrystalImage,
		Pin:      "Docker Hub image by digest (linux/amd64, linux/arm64); the PyPI install of 0.3.0 is broken",
		ReadOnly: "parser: --access-mode=restricted validates the statement with pglast, then runs it in a read-only transaction",
		SQLTool:  "execute_sql",
		SQLArg:   "sql",
		Doc:      "https://github.com/crystaldba/postgres-mcp/blob/v0.3.0/README.md",
		argv: func(uri string) []string {
			return []string{"-e", "DATABASE_URI=" + uri, "@image", "--access-mode=restricted"}
		},
	},
	{
		Name:     results.DBHub,
		Version:  "2.0.0",
		Image:    "bytebase/dbhub:2.0.0@sha256:1b722af2af2293736ce3df7e4411b043f61ca7737839c7b9fe4414764c89b68c",
		Pin:      "Docker Hub image by digest (linux/amd64, linux/arm64)",
		ReadOnly: "config flag: readonly = true on execute_sql in dbhub.toml, a keyword allow-list then a BEGIN READ ONLY transaction",
		SQLTool:  "execute_sql",
		SQLArg:   "sql",
		Doc:      "https://dbhub.ai/config/read-only-mode",
		argv: func(uri string) []string {
			// The image's entrypoint is `node dist/index.js` in /app. The TOML is written inside the container from an
			// environment variable, so the harness needs no file on the host.
			const script = `printf '%s' "$DBHUB_TOML" > /tmp/dbhub.toml && exec node dist/index.js --config=/tmp/dbhub.toml`
			return []string{"-e", "DBHUB_TOML=" + DBHubTOML(uri), "--entrypoint", "sh", "@image", "-c", script}
		},
	},
	{
		Name:     results.Toolbox,
		Version:  "1.17.0",
		Image:    "us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:1.17.0@sha256:7b3274f99391e60e6ece5cab407712e6c8101153114449f4112f04c44704c103",
		Pin:      "Artifact Registry image by digest (linux/amd64, linux/arm64)",
		ReadOnly: "database role: the postgres source and the prebuilt tool set have no read-only option",
		SQLTool:  "execute_sql",
		SQLArg:   "sql",
		Doc:      "https://github.com/googleapis/mcp-toolbox/blob/v1.17.0/internal/prebuiltconfigs/tools/postgres.yaml",
		argv: func(uri string) []string {
			var a []string
			for _, e := range ToolboxEnv(uri) {
				a = append(a, "-e", e)
			}
			// --disable-version-check: the startup check calls the network and changes no tool.
			return append(a, "@image", "--prebuilt", "postgres", "--stdio", "--disable-version-check")
		},
	},
	{
		Name:     results.Reference,
		Version:  "0.6.2",
		Image:    "bench-reference:0.6.2",
		Build:    "internal/servers/reference",
		Pin:      "npm 0.6.2 and its dependencies by package-lock.json, on node:22-alpine by digest (linux/amd64, linux/arm64)",
		ReadOnly: "read-only transaction: BEGIN TRANSACTION READ ONLY around the query, which a COMMIT inside the query ends",
		SQLTool:  "query",
		SQLArg:   "sql",
		Doc:      "https://www.npmjs.com/package/@modelcontextprotocol/server-postgres/v/0.6.2",
		argv: func(uri string) []string {
			return []string{"@image", uri}
		},
	},
}

// Lookup returns the registry entry of a server name.
func Lookup(name string) (Pinned, bool) {
	for _, p := range Registry {
		if p.Name == name {
			return p, true
		}
	}
	return Pinned{}, false
}

// Select parses a --servers value, comma-separated names of Registry, into their entries in the order given. An empty
// value selects def.
func Select(list, def string) ([]Pinned, error) {
	if strings.TrimSpace(list) == "" {
		list = def
	}
	var out []Pinned
	seen := map[string]bool{}
	for name := range strings.SplitSeq(list, ",") {
		name = strings.TrimSpace(name)
		p, ok := Lookup(name)
		if !ok {
			return nil, fmt.Errorf("unknown server %q (known: %s)", name, Names())
		}
		if seen[name] {
			return nil, fmt.Errorf("server %q given twice", name)
		}
		seen[name] = true
		out = append(out, p)
	}
	return out, nil
}

// Names returns the names of Registry, comma-separated.
func Names() string {
	names := make([]string, len(Registry))
	for i, p := range Registry {
		names[i] = p.Name
	}
	return strings.Join(names, ",")
}

// Env is what a result records about the server.
func (p Pinned) Env() results.ServerEnv {
	return results.ServerEnv{Name: p.Name, Version: p.Version, Image: p.Image, ReadOnly: p.ReadOnly}
}

// Spec returns the spec that starts the server in a container on Network, connected to uri, the database as a
// container sees it (BenchURI, BenchOwnerURI). The container is named so that Cleanup can remove it.
func (p Pinned) Spec(uri string) Spec {
	name := fmt.Sprintf("bench-%s-%d-%d", p.Name, os.Getpid(), time.Now().UnixNano()%1_000_000)
	argv := []string{"docker", "run", "-i", "--rm", "--name", name, "--network", Network}
	for _, a := range p.argv(uri) {
		if a == "@image" {
			a = p.Image
		}
		argv = append(argv, a)
	}
	return Spec{
		Name:      p.Name,
		Argv:      argv,
		Cleanup:   func() { _ = exec.Command("docker", "rm", "-f", name).Run() },
		Container: name,
	}
}

// Prepare makes the image present locally, so that no server start includes a pull or a build: it builds a local
// image from its Dockerfile under benchDir, and pulls a registry image.
func (p Pinned) Prepare(ctx context.Context, benchDir string) error {
	if exec.CommandContext(ctx, "docker", "image", "inspect", p.Image).Run() == nil {
		return nil
	}
	args := []string{"pull", "-q", p.Image}
	if p.Build != "" {
		args = []string{"build", "-q", "-t", p.Image, filepath.Join(benchDir, filepath.FromSlash(p.Build))}
	}
	if out, err := exec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("prepare the image of %s: docker %s: %w: %s", p.Name, args[0], err, out)
	}
	return nil
}

// DBHubTOML is the dbhub.toml of the bench: one source, and the two default tools, execute_sql with readonly = true as
// https://dbhub.ai/config/read-only-mode shows, and search_objects, which a [[tools]] list must name to keep. No
// max_rows: the default has none.
func DBHubTOML(uri string) string {
	if !strings.Contains(uri, "?") { // the bench databases have no TLS
		uri += "?sslmode=disable"
	}
	return fmt.Sprintf("[[sources]]\nid = \"bench\"\ndsn = %q\n\n"+
		"[[tools]]\nname = \"execute_sql\"\nsource = \"bench\"\nreadonly = true\n\n"+
		"[[tools]]\nname = \"search_objects\"\nsource = \"bench\"\n", uri)
}

// ToolboxEnv turns a Postgres URI into the variables the prebuilt postgres source of Toolbox reads.
func ToolboxEnv(uri string) []string {
	u, err := url.Parse(uri)
	if err != nil { // the bench passes its own constants; a bad one must fail loudly
		panic("servers: parse the toolbox database URI: " + err.Error())
	}
	pw, _ := u.User.Password()
	return []string{
		"POSTGRES_HOST=" + u.Hostname(),
		"POSTGRES_PORT=" + u.Port(),
		"POSTGRES_DATABASE=" + path.Base(u.Path),
		"POSTGRES_USER=" + u.User.Username(),
		"POSTGRES_PASSWORD=" + pw,
	}
}
