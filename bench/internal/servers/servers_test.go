package servers

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestLinuxBuildArgs(t *testing.T) {
	args := linuxBuildArgs("/repo", "/out")
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"CGO_ENABLED=1", "/repo:/src:ro", "/out:/out", "pgmcp-gocache:", "pgmcp-gomod:", "golang:1.26-alpine",
		"-trimpath", "-tags pgquery_cgo,netgo,osusergo", "-s -w", "-linkmode=external", `-extldflags "-static -s"`, "apk add --no-cache gcc musl-dev",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("build args lack %q: %s", want, joined)
		}
	}
}

// The root Dockerfile is goreleaser's: it reads the binary under $TARGETPLATFORM. BuildKit sets that variable and the
// legacy builder leaves it empty, so the bench passes it and puts the binary where it points, or the build fails on
// one builder and silently reads another folder on the other.
func TestImageBuildTargetsThePlatformFolder(t *testing.T) {
	const plat = "linux/amd64"
	if got, want := imageBinDir("/ctx", plat), filepath.Join("/ctx", "linux", "amd64"); got != want {
		t.Errorf("binary folder = %q, want %q", got, want)
	}
	args := imageBuildArgs("/repo", "/ctx", plat)
	joined := strings.Join(args, " ")
	for _, want := range []string{"--build-arg TARGETPLATFORM=" + plat, "-t " + ImageTag, filepath.Join("/repo", "Dockerfile"), "/ctx"} {
		if !strings.Contains(joined, want) {
			t.Errorf("build args lack %q: %s", want, joined)
		}
	}
}

func TestDBAImage(t *testing.T) {
	s := DBAImage("postgres://u:p@pg:5432/db")
	if s.Container == "" || !strings.HasPrefix(s.Container, "bench-") {
		t.Errorf("container name %q, want a bench- name that Cleanup and docker stats can use", s.Container)
	}
	joined := strings.Join(s.Argv, " ")
	for _, want := range []string{"docker run -i --rm --name " + s.Container, "--network " + Network, "DATABASE_URI=postgres://u:p@pg:5432/db", ImageTag + " --access-mode=restricted"} {
		if !strings.Contains(joined, want) {
			t.Errorf("argv lacks %q: %s", want, joined)
		}
	}
	if s.Name != results.DBA || s.Cleanup == nil {
		t.Errorf("name %q, cleanup nil=%v", s.Name, s.Cleanup == nil)
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name    string
		server  string
		isError bool
		text    string
		want    string
	}{
		{"dba refusal", results.DBA, true, "ERROR: restricted mode: DeleteStmt statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW)", results.ClassRefused},
		{"dba postgres error", results.DBA, true, `ERROR: column "nme" does not exist (SQLSTATE 42703)`, results.ClassFailed},
		{"dba success", results.DBA, false, "id\n1\n-- 1 row, 2 ms", results.ClassExecuted},
		{"crystal validation", results.Crystal, false, "Error: Error validating query: DELETE FROM public.events", results.ClassRefused},
		{"crystal parse", results.Crystal, false, "Error: Failed to parse SQL statement", results.ClassRefused},
		{"crystal read-only failure", results.Crystal, false, "Error: cannot execute INSERT in a read-only transaction", results.ClassFailed},
		{"crystal success", results.Crystal, false, "[{'id': 1}]", results.ClassExecuted},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Classify(tt.server, tt.isError, tt.text); got != tt.want {
				t.Errorf("Classify = %q, want %q", got, tt.want)
			}
		})
	}
}

// The tool mapping and the classification hook are what a new server plugs into: a nil map keeps the canonical
// names and the default classification, which is what dba and crystal rely on.
func TestSpecToolNameAndClass(t *testing.T) {
	dba := Spec{Name: results.DBA}
	if n, ok := dba.ToolName("execute_sql"); n != "execute_sql" || !ok {
		t.Errorf("nil map: got %q %v, want execute_sql true", n, ok)
	}
	if got := dba.Class(true, `ERROR: column "nme" does not exist (SQLSTATE 42703)`); got != results.ClassFailed {
		t.Errorf("nil Classify must fall back to Classify(Name, …): got %q", got)
	}
	other := Spec{Name: "other", Tools: map[string]string{"execute_sql": "run_sql"},
		Classify: func(bool, string) string { return results.ClassRefused }}
	if n, ok := other.ToolName("execute_sql"); n != "run_sql" || !ok {
		t.Errorf("got %q %v, want run_sql true", n, ok)
	}
	if _, ok := other.ToolName("analyze_db_health"); ok {
		t.Error("a tool absent from the map must read as absent")
	}
	if got := other.Class(false, "anything"); got != results.ClassRefused {
		t.Errorf("Classify not used: got %q", got)
	}
}

func TestRegister(t *testing.T) {
	Register("test-server", func(tg Target) Spec { return Spec{Argv: []string{"x", tg.URI}} })
	t.Cleanup(func() { delete(registry, "test-server") })
	f, ok := Lookup("test-server")
	if !ok {
		t.Fatal("registered server not found")
	}
	if got := f(Target{URI: "uri"}).Argv[1]; got != "uri" {
		t.Errorf("factory got %q, want the target uri", got)
	}
	if _, ok := Lookup("absent"); ok {
		t.Error("Lookup must report an unknown server")
	}
}
