package agent

import (
	"encoding/json"
	"os"
	"slices"
	"testing"
)

func TestNamedConfig(t *testing.T) {
	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile("../../n3/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	tests := []struct {
		name  string
		cfg   []byte
		named bool
		args  []string // expected args of server pg
	}{
		{"crystal container is named", read("crystal"), true, []string{"run", "--name", "n", "-i", "--rm"}},
		{"binary is left alone", read("dba"), false, []string{"--access-mode=restricted"}},
		{"no server", []byte(`{}`), false, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, named, err := NamedConfig(tt.cfg, "n")
			if err != nil || named != tt.named {
				t.Fatalf("named = %v, err = %v; want %v", named, err, tt.named)
			}
			var cfg struct {
				MCPServers map[string]struct {
					Args []string `json:"args"`
				} `json:"mcpServers"`
			}
			if err := json.Unmarshal(out, &cfg); err != nil {
				t.Fatal(err)
			}
			got := cfg.MCPServers["pg"].Args
			if len(got) < len(tt.args) || !slices.Equal(got[:len(tt.args)], tt.args) {
				t.Errorf("args = %v, want prefix %v", got, tt.args)
			}
		})
	}
	if _, _, err := NamedConfig([]byte("nope"), "n"); err == nil {
		t.Error("invalid JSON: want error")
	}
}
