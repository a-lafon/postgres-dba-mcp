package servers

import (
	"slices"
	"strings"
	"testing"
)

func TestToolboxEnv(t *testing.T) {
	got := ToolboxEnv("postgres://bench_ro:bench@pg:5432/bench")
	want := []string{"POSTGRES_HOST=pg", "POSTGRES_PORT=5432", "POSTGRES_DATABASE=bench", "POSTGRES_USER=bench_ro", "POSTGRES_PASSWORD=bench"}
	if !slices.Equal(got, want) {
		t.Errorf("ToolboxEnv = %v, want %v", got, want)
	}
}

func TestToolboxSpecs(t *testing.T) {
	for _, name := range []string{Toolbox, ToolboxSubset} {
		f, ok := Lookup(name)
		if !ok {
			t.Fatalf("%s is not registered", name)
		}
		spec := f(Target{URI: "postgres://bench_ro:bench@pg:5432/bench"})
		argv := strings.Join(spec.Argv, " ")
		if !strings.Contains(argv, ToolboxImage) || !strings.Contains(argv, "--stdio") || !strings.Contains(argv, "--disable-version-check") {
			t.Errorf("%s argv = %s", name, argv)
		}
		if mounted := strings.Contains(argv, containerConfig); mounted != (name == ToolboxSubset) {
			t.Errorf("%s argv = %s, mounted = %v", name, argv, mounted)
		}
		if prebuilt := strings.Contains(argv, "--prebuilt postgres"); prebuilt != (name == Toolbox) {
			t.Errorf("%s argv = %s, prebuilt = %v", name, argv, prebuilt)
		}
		if _, ok := spec.ToolName("execute_sql"); !ok {
			t.Errorf("%s has no execute_sql", name)
		}
		for _, missing := range []string{"explain_query", "analyze_db_health", "analyze_query_indexes"} {
			if _, ok := spec.ToolName(missing); ok {
				t.Errorf("%s maps %s, which Toolbox cannot answer", name, missing)
			}
		}
	}
}

// The subset file is what the 10-tool variant measures: it must hold a source and ten tools.
func TestToolboxSubsetYAML(t *testing.T) {
	if n := strings.Count(toolboxSubsetYAML, "\nkind: tool\n"); n != 10 {
		t.Errorf("subset has %d tools, want 10", n)
	}
	if !strings.Contains(toolboxSubsetYAML, "\nkind: source\n") {
		t.Error("subset has no source")
	}
}
