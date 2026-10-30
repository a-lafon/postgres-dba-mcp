// Package servers knows how to start each server under test and which connection strings the bench uses.
package servers

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Connection strings of the bench databases, as seen from the host.
const (
	BenchDSN        = "postgres://bench_ro:bench@localhost:55433/bench"
	BenchAdminDSN   = "postgres://postgres:bench@localhost:55433/bench"
	FixtureDSN      = "postgres://pgdba_ro:pgdba@localhost:55434/pgdba"
	FixtureAdminDSN = "postgres://postgres:pgdba@localhost:55434/pgdba"
)

// CrystalImage is crystaldba/postgres-mcp 0.3.0, pinned by digest.
const CrystalImage = "crystaldba/postgres-mcp@sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b"

// Network is the compose network of bench/compose.yml, project name `bench`.
const Network = "bench_bench"

// ErrMissing is returned by Spec.Start when an optional server's binary is not installed.
var ErrMissing = errors.New("server binary not found")

// Spec says how to start one server over stdio.
type Spec struct {
	Name    string
	Argv    []string
	Env     []string
	Cleanup func() // run after Close, to stop what killing the client process leaves behind
	// Container is the name of the docker container the server runs in, empty for a native process.
	Container string
}

// Start starts the server and connects to it.
func (s Spec) Start(ctx context.Context) (*Server, error) {
	return s.StartLimit(ctx, mcpc.MaxResponse)
}

// StartLimit is Start with another response-size cap.
func (s Spec) StartLimit(ctx context.Context, limit int) (*Server, error) {
	c, err := mcpc.StartStdioLimit(ctx, s.Argv, s.Env, limit)
	if err != nil {
		if s.Cleanup != nil {
			s.Cleanup()
		}
		return nil, fmt.Errorf("start %s: %w", s.Name, err)
	}
	return &Server{Client: c, cleanup: s.Cleanup}, nil
}

// Server is a started server.
type Server struct {
	*mcpc.Client
	cleanup func()
}

// Close stops the server.
func (s *Server) Close() {
	s.Client.Close()
	if s.cleanup != nil {
		s.cleanup()
	}
}

// BinPath returns where the bench builds postgres-dba-mcp, relative to the bench folder.
func BinPath(benchDir string) string { return filepath.Join(benchDir, "bin", "postgres-dba-mcp") }

// DBA returns the spec of the native postgres-dba-mcp binary in restricted mode, connected to dsn.
func DBA(bin, dsn string, extra ...string) Spec {
	argv := append([]string{bin, "--access-mode=restricted"}, extra...)
	return Spec{Name: results.DBA, Argv: argv, Env: []string{"DATABASE_URI=" + dsn}}
}

// Crystal returns the spec of the crystaldba image on the bench network. host is the database host as the container
// sees it ("pg" or "fixture"): anything but localhost, so the image's entrypoint does not rewrite the URI.
func Crystal(uri string) Spec {
	name := fmt.Sprintf("bench-crystal-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	return Spec{
		Name: results.Crystal,
		Argv: []string{"docker", "run", "-i", "--rm", "--name", name, "--network", Network,
			"-e", "DATABASE_URI=" + uri, CrystalImage, "--access-mode=restricted"},
		Cleanup:   func() { _ = exec.Command("docker", "rm", "-f", name).Run() },
		Container: name,
	}
}

// PgEdgeBinary is the executable name looked up for the optional pgEdge server.
const PgEdgeBinary = "pgedge-postgres-mcp"

// PgEdge returns the spec of pgEdge's server, or ErrMissing when its binary is not installed.
func PgEdge(dsn string) (Spec, error) {
	path, err := exec.LookPath(PgEdgeBinary)
	if err != nil {
		return Spec{}, fmt.Errorf("%s: %w", PgEdgeBinary, ErrMissing)
	}
	return Spec{Name: results.PgEdge, Argv: []string{path}, Env: []string{"PGEDGE_POSTGRES_CONNECTION_STRING=" + dsn}}, nil
}

// Info is what the report records about the build under test.
type Info struct {
	Version string
	Commit  string
}

// DBAInfo returns the version printed by the binary and the commit of the repository it was built from.
func DBAInfo(ctx context.Context, bin, repoDir string) (Info, error) {
	out, err := exec.CommandContext(ctx, bin, "--version").Output()
	if err != nil {
		return Info{}, fmt.Errorf("run %s --version: %w", bin, err)
	}
	ver := strings.TrimPrefix(strings.TrimSpace(string(out)), "postgres-dba-mcp ")
	rev, err := exec.CommandContext(ctx, "git", "-C", repoDir, "rev-parse", "--short", "HEAD").Output()
	if err != nil {
		return Info{}, fmt.Errorf("read commit: %w", err)
	}
	return Info{Version: ver, Commit: strings.TrimSpace(string(rev))}, nil
}

// Classify says what happened to a SQL statement sent to execute_sql in restricted mode: ClassRefused when the server
// answered before executing it, ClassFailed when Postgres rejected it, ClassExecuted when it succeeded.
func Classify(server string, isError bool, text string) string {
	if server == results.Crystal {
		switch {
		case strings.HasPrefix(text, "Error: Error validating query"), strings.HasPrefix(text, "Error: Failed to parse SQL statement"):
			return results.ClassRefused
		case strings.HasPrefix(text, "Error"):
			return results.ClassFailed
		}
		return results.ClassExecuted
	}
	switch {
	case !isError:
		return results.ClassExecuted
	case strings.Contains(text, "(SQLSTATE "):
		return results.ClassFailed
	}
	return results.ClassRefused
}

// Build compiles postgres-dba-mcp from repoDir into BinPath(benchDir), with the release flags of .goreleaser.yaml.
func Build(ctx context.Context, repoDir, benchDir string) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-trimpath", "-ldflags", "-s -w", "-o", BinPath(benchDir), "./cmd/postgres-dba-mcp")
	cmd.Dir = repoDir
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build postgres-dba-mcp: %w: %s", err, out)
	}
	return nil
}

// Linux image: what ships to containers, built with the tags and flags of .goreleaser.yaml.
const (
	// ImageTag is the local tag of the postgres-dba-mcp image built by BuildLinux.
	ImageTag = "postgres-dba-mcp:bench"
	// buildImage compiles the linux binary. It is Alpine, so the C toolchain is musl and the architecture the host's Docker.
	buildImage = "golang:1.26-alpine"
)

// ImageDir returns the Docker build context of the image: a folder under bin/ that holds the binary only.
func ImageDir(benchDir string) string { return filepath.Join(benchDir, "bin", "image") }

// linuxBuildArgs returns the `docker run` arguments that build the static musl cgo binary of repoDir into outDir, the
// way .goreleaser.yaml builds the linux binaries (Alpine's musl gcc instead of zig cc). -buildvcs=false because the
// .git of a git worktree points outside the mounted folder.
func linuxBuildArgs(repoDir, outDir string) []string {
	script := `apk add --no-cache gcc musl-dev >/dev/null && go build -buildvcs=false -trimpath -tags pgquery_cgo,netgo,osusergo ` +
		`-ldflags '-s -w -linkmode=external -extldflags "-static -s"' -o /out/postgres-dba-mcp ./cmd/postgres-dba-mcp`
	return []string{"run", "--rm", "-e", "CGO_ENABLED=1",
		"-v", repoDir + ":/src:ro", "-v", outDir + ":/out", "-v", "pgmcp-gocache:/root/.cache/go-build", "-v", "pgmcp-gomod:/go/pkg/mod",
		"-w", "/src", buildImage, "sh", "-c", script}
}

// BuildLinux builds the linux cgo binary of repoDir inside buildImage, then the image ImageTag from the root Dockerfile.
func BuildLinux(ctx context.Context, repoDir, benchDir string) error {
	dir, err := filepath.Abs(ImageDir(benchDir))
	if err != nil {
		return fmt.Errorf("resolve image folder: %w", err)
	}
	repo, err := filepath.Abs(repoDir)
	if err != nil {
		return fmt.Errorf("resolve repository folder: %w", err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create image folder: %w", err)
	}
	if out, err := exec.CommandContext(ctx, "docker", linuxBuildArgs(repo, dir)...).CombinedOutput(); err != nil {
		return fmt.Errorf("build linux binary: %w: %s", err, out)
	}
	// The Dockerfile also copies these, as goreleaser's extra_files do.
	for _, name := range []string{"LICENSE", "NOTICE"} {
		b, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), b, 0o644); err != nil {
			return fmt.Errorf("copy %s: %w", name, err)
		}
	}
	cmd := exec.CommandContext(ctx, "docker", "build", "-q", "-t", ImageTag, "-f", filepath.Join(repoDir, "Dockerfile"), dir)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("build image %s: %w: %s", ImageTag, err, out)
	}
	return nil
}

// ImageExists reports whether the image ImageTag is present locally.
func ImageExists(ctx context.Context) bool {
	return exec.CommandContext(ctx, "docker", "image", "inspect", ImageTag).Run() == nil
}

// DBAImage returns the spec of the postgres-dba-mcp image in restricted mode on the bench network, started like
// Crystal. uri is the database as the container sees it.
func DBAImage(uri string) Spec {
	name := fmt.Sprintf("bench-dba-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	return Spec{
		Name: results.DBA,
		Argv: []string{"docker", "run", "-i", "--rm", "--name", name, "--network", Network,
			"-e", "DATABASE_URI=" + uri, ImageTag, "--access-mode=restricted"},
		Cleanup:   func() { _ = exec.Command("docker", "rm", "-f", name).Run() },
		Container: name,
	}
}
