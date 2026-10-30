package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
)

// NamedConfig returns an MCP config in which every server started with `docker run` is given `--name name`.
// claude -p stops its MCP servers with a kill that leaves the container of `docker run -i --rm` running (the
// docker client dies, the container does not exit on its own), so the run removes the container by name.
// named is false when the config starts no container.
func NamedConfig(cfg []byte, name string) (out []byte, named bool, err error) {
	var doc map[string]any
	if err := json.Unmarshal(cfg, &doc); err != nil {
		return nil, false, fmt.Errorf("parse MCP config: %w", err)
	}
	servers, _ := doc["mcpServers"].(map[string]any)
	for _, s := range servers {
		srv, _ := s.(map[string]any)
		args, _ := srv["args"].([]any)
		if srv["command"] != "docker" || len(args) == 0 || args[0] != "run" {
			continue
		}
		srv["args"] = append([]any{"run", "--name", name}, args[1:]...)
		named = true
	}
	out, err = json.MarshalIndent(doc, "", "  ")
	return out, named, err
}

// removeContainer force-removes a container, whether it still runs or is already gone.
func removeContainer(ctx context.Context, name string) error {
	// An absent container makes docker rm fail; the output tells the two cases apart.
	if out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput(); err != nil {
		if exec.CommandContext(ctx, "docker", "container", "inspect", name).Run() == nil {
			return fmt.Errorf("remove container %s: %w: %s", name, err, out)
		}
	}
	return nil
}
