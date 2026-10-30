//go:build integration

package security

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// TestLargeObjectIsASideEffect runs lo_create through postgres-dba-mcp in unrestricted mode, against the bench fixture
// service (docker compose -f bench/compose.yml up -d --wait fixture) with a built bench/bin/postgres-dba-mcp.
func TestLargeObjectIsASideEffect(t *testing.T) {
	ctx := context.Background()
	bin := servers.BinPath(filepath.Join("..", ".."))
	if _, err := os.Stat(bin); err != nil {
		t.Skipf("no binary: %v", err)
	}
	r := &Replayer{Compose: filepath.Join("..", "..", "compose.yml"), AdminDSN: servers.FixtureAdminDSN, Log: io.Discard}
	if err := r.Open(ctx); err != nil {
		t.Skipf("fixture service not reachable: %v", err)
	}
	t.Cleanup(func() { r.Close(ctx) })

	spec := servers.Spec{Name: "dba", Argv: []string{bin, "--access-mode=unrestricted"}, Env: []string{"DATABASE_URI=" + servers.FixtureAdminDSN}}
	got, err := r.Replay(ctx, spec, []Case{{ID: "lo-create", Category: "fn", SQL: "SELECT lo_create(0)"}}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !strings.Contains(got[0].SideEffect, "large objects +1") {
		t.Fatalf("result = %+v, want a side effect with large objects +1", got)
	}
}
