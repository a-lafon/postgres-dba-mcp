//go:build integration

package servers

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRegistryServersAnswer starts every server of the registry against the loaded bench database (`up`, `load`),
// lists its tools, checks that its SQL tool takes the SQL argument the registry names, and runs SELECT 1 through it.
func TestRegistryServersAnswer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	for _, p := range Registry {
		t.Run(p.Name, func(t *testing.T) {
			if err := p.Prepare(ctx, filepath.Join("..", "..")); err != nil {
				t.Fatal(err)
			}
			srv, err := p.Spec(BenchURI).Start(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer srv.Close()
			tools, err := srv.Tools(ctx)
			if err != nil {
				t.Fatal(err)
			}
			var names []string
			found := false
			for _, tool := range tools {
				names = append(names, tool.Name)
				if tool.Name != p.SQLTool {
					continue
				}
				b, _ := json.Marshal(tool.InputSchema)
				var schema struct {
					Properties map[string]any `json:"properties"`
				}
				if err := json.Unmarshal(b, &schema); err != nil {
					t.Fatal(err)
				}
				if _, ok := schema.Properties[p.SQLArg]; !ok {
					t.Errorf("tool %s has no argument %q: %s", p.SQLTool, p.SQLArg, b)
				}
				found = true
			}
			if !found {
				t.Fatalf("no tool %s in %v", p.SQLTool, names)
			}
			t.Logf("%s %s: %d tools: %s", p.Name, srv.Info, len(tools), strings.Join(names, ", "))
			res, err := srv.Call(ctx, p.SQLTool, map[string]any{p.SQLArg: "SELECT 1 AS one"})
			if err != nil {
				t.Fatal(err)
			}
			if res.IsError || !strings.Contains(res.Text, "1") {
				t.Fatalf("SELECT 1: isError=%v text=%q", res.IsError, res.Text)
			}
			t.Logf("SELECT 1 -> %q", res.Text)
		})
	}
}
