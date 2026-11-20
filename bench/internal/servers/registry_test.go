package servers

import (
	"slices"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestSelect(t *testing.T) {
	got, err := Select(" dbhub,reference ", "dba,crystal")
	if err != nil || len(got) != 2 || got[0].Name != results.DBHub || got[1].Name != results.Reference {
		t.Fatalf("Select = %+v, %v", got, err)
	}
	if got, err := Select("", "dba,crystal"); err != nil || len(got) != 2 || got[0].Name != results.DBA {
		t.Fatalf("Select default = %+v, %v", got, err)
	}
	for _, bad := range []string{"pgedge", "dba,dba", "dba,"} {
		if _, err := Select(bad, ""); err == nil {
			t.Errorf("Select(%q) accepted", bad)
		}
	}
}

// Every server is pinned, says how it stays read-only, and names the tool and the argument that carry the SQL: the
// only mapping protocol v2 allows.
func TestRegistryPinned(t *testing.T) {
	if len(Registry) != 5 {
		t.Fatalf("%d servers, want the five of PROTOCOL.md §2", len(Registry))
	}
	for _, p := range Registry {
		if p.Version == "" || p.ReadOnly == "" || p.SQLTool == "" || p.SQLArg == "" || p.Doc == "" || p.Pin == "" {
			t.Errorf("%s: incomplete entry %+v", p.Name, p)
		}
		if p.Build == "" && !strings.Contains(p.Image, "@sha256:") {
			t.Errorf("%s: image %q is not pinned by digest", p.Name, p.Image)
		}
	}
}

func TestSpecRunsTheImageOnTheBenchNetwork(t *testing.T) {
	const uri = "postgres://u:p@pg:5432/db"
	for _, p := range Registry {
		s := p.Spec(uri)
		if s.Name != p.Name || s.Cleanup == nil || !strings.HasPrefix(s.Container, "bench-"+p.Name+"-") {
			t.Errorf("%s: name %q container %q", p.Name, s.Name, s.Container)
		}
		joined := strings.Join(s.Argv, " ")
		if !strings.HasPrefix(joined, "docker run -i --rm --name "+s.Container+" --network "+Network+" ") ||
			!slices.Contains(s.Argv, p.Image) || slices.Contains(s.Argv, "@image") {
			t.Errorf("%s: argv %s", p.Name, joined)
		}
	}
}

func TestReadOnlyConfiguration(t *testing.T) {
	dba, _ := Lookup(results.DBA)
	crystal, _ := Lookup(results.Crystal)
	for _, p := range []Pinned{dba, crystal} {
		if !slices.Contains(p.Spec("postgres://u:p@pg:5432/db").Argv, "--access-mode=restricted") {
			t.Errorf("%s does not run in restricted mode", p.Name)
		}
	}
	toml := DBHubTOML("postgres://u:p@pg:5432/db")
	for _, want := range []string{`dsn = "postgres://u:p@pg:5432/db?sslmode=disable"`, "name = \"execute_sql\"\nsource = \"bench\"\nreadonly = true", `name = "search_objects"`} {
		if !strings.Contains(toml, want) {
			t.Errorf("dbhub.toml lacks %q:\n%s", want, toml)
		}
	}
	if strings.Contains(toml, "max_rows") {
		t.Errorf("dbhub.toml sets max_rows, which the default does not:\n%s", toml)
	}
	env := strings.Join(ToolboxEnv("postgres://u:p@pg:5432/db"), " ")
	if env != "POSTGRES_HOST=pg POSTGRES_PORT=5432 POSTGRES_DATABASE=db POSTGRES_USER=u POSTGRES_PASSWORD=p" {
		t.Errorf("toolbox env = %s", env)
	}
}
