package agent

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

func TestTasksMirror(t *testing.T) {
	b, err := os.ReadFile("../../n3/tasks.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, task := range AllTasks() {
		if !strings.Contains(string(b), "## "+task.ID) || !strings.Contains(string(b), "Question: "+task.Question) {
			t.Errorf("bench/n3/tasks.md does not mirror %s", task.ID)
		}
	}
}

func TestMCPConfigs(t *testing.T) {
	for _, name := range []string{"dba", "crystal"} {
		t.Run(name, func(t *testing.T) {
			b, err := os.ReadFile("../../n3/" + name + ".json")
			if err != nil {
				t.Fatal(err)
			}
			var cfg struct {
				MCPServers map[string]struct {
					Command string   `json:"command"`
					Args    []string `json:"args"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(b, &cfg); err != nil {
				t.Fatal(err)
			}
			srv, ok := cfg.MCPServers["pg"]
			if !ok || len(cfg.MCPServers) != 1 {
				t.Fatalf("want one server named pg, got %v", cfg.MCPServers)
			}
			if name == "crystal" && !strings.Contains(strings.Join(srv.Args, " "), servers.CrystalImage) {
				t.Errorf("crystal.json does not use the pinned image %s", servers.CrystalImage)
			}
			if name == "dba" && srv.Command != "bench/bin/postgres-dba-mcp" {
				t.Errorf("command = %q", srv.Command)
			}
		})
	}
}
