package servers

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// ToolboxImage is MCP Toolbox 1.16.0, pinned by digest.
const ToolboxImage = "us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox@sha256:190bd9d9b3cfe81bed19470bef8f9991d64c113313ec499234e0dd38ca11a841"

// Toolbox names the two measured configurations: the prebuilt postgres one, and the same tools cut to ten.
const (
	Toolbox       = "toolbox"
	ToolboxSubset = "toolbox-subset"
)

// toolboxSubsetYAML is the configuration of ToolboxSubset; its header says which tools it keeps and why.
//
//go:embed toolbox-subset.yaml
var toolboxSubsetYAML string

func init() {
	Register(Toolbox, func(t Target) Spec { return toolboxSpec(Toolbox, t.URI, "") })
	Register(ToolboxSubset, func(t Target) Spec { return toolboxSpec(ToolboxSubset, t.URI, subsetPath()) })
}

// toolboxTools maps the canonical bench tools to the names Toolbox gives them in --prebuilt postgres.
//
// The three missing keys are tools the harness cannot reach:
//   - explain_query: Toolbox has get_query_plan, but its required parameter is named query, not sql, so the
//     canonical call is rejected before it runs. It also has no ANALYZE and no hypothetical index (S14, S21).
//   - analyze_db_health: Toolbox has no health tool, it has a health toolset of eight separate checks.
//   - analyze_query_indexes: Toolbox recommends no index at all.
//
// list_tables answers both exploration tools: it is the only one Toolbox has. It takes neither schema_name,
// object_type nor object_name, and ignores the arguments it does not know, so both scenarios get every user
// table in full detail.
var toolboxTools = map[string]string{
	"execute_sql":        "execute_sql",
	"list_schemas":       "list_schemas",
	"list_objects":       "list_tables",
	"get_object_details": "list_tables",
	"get_top_queries":    "list_query_stats",
}

// ToolboxArgv returns the command line of a Toolbox configuration: the prebuilt one when config is empty, else
// the tools file at that path inside the container.
func ToolboxArgv(config string) []string {
	if config == "" {
		return []string{"--prebuilt", "postgres", "--stdio", "--disable-version-check"}
	}
	return []string{"--config", config, "--stdio", "--disable-version-check"}
}

// containerConfig is where a tools file is mounted inside the container.
const containerConfig = "/config/tools.yaml"

// toolboxSpec returns the spec of the Toolbox image on the bench network, started like Crystal. uri is the
// database as the container sees it; config, when set, is a host tools file mounted in place of --prebuilt.
func toolboxSpec(name, uri, config string) Spec {
	cname := fmt.Sprintf("bench-toolbox-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	argv := []string{"docker", "run", "-i", "--rm", "--name", cname, "--network", Network}
	for _, e := range ToolboxEnv(uri) {
		argv = append(argv, "-e", e)
	}
	in := ""
	if config != "" {
		argv = append(argv, "-v", config+":"+containerConfig+":ro")
		in = containerConfig
	}
	argv = append(append(argv, ToolboxImage), ToolboxArgv(in)...)
	return Spec{
		Name:      name,
		Argv:      argv,
		Cleanup:   func() { _ = exec.Command("docker", "rm", "-f", cname).Run() },
		Container: cname,
		Tools:     toolboxTools,
		// Classify stays nil: the default fits what Toolbox answers. It has no refusal path, so every case is
		// either a result (executed) or `error processing request: unable to execute query: ERROR: <message>
		// (SQLSTATE <code>)`, which the default reads as failed at execution. Checked over the 131 attacks:
		// 0 refused, 93 failed (54 of them SQLSTATE 42501, permission denied), 38 executed.
	}
}

// ToolboxEnv turns a Postgres URI into the five variables the prebuilt postgres source reads.
func ToolboxEnv(uri string) []string {
	u, err := url.Parse(uri)
	if err != nil { // the bench passes its own constants; a bad one must fail loudly
		panic("servers: parse the toolbox database URI: " + err.Error())
	}
	pw, _ := u.User.Password()
	return []string{
		"POSTGRES_HOST=" + u.Hostname(),
		"POSTGRES_PORT=" + u.Port(),
		"POSTGRES_DATABASE=" + filepath.Base(u.Path),
		"POSTGRES_USER=" + u.User.Username(),
		"POSTGRES_PASSWORD=" + pw,
	}
}

// subsetPath writes the subset configuration where the Docker daemon can mount it, once per process.
// shortcut: a temporary file of the host, so the harness run inside the `bench` compose service would hand the
// daemon a path of its own filesystem; move it under bench/bin if that run is ever needed.
var subsetPath = sync.OnceValue(func() string {
	path := filepath.Join(os.TempDir(), "bench-toolbox-subset.yaml")
	if err := os.WriteFile(path, []byte(toolboxSubsetYAML), 0o644); err != nil {
		panic("servers: write the toolbox subset configuration: " + err.Error())
	}
	return path
})
