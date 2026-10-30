package servers

import (
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
