package safesql

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type corpusCase struct {
	ID, Family, SQL string
}

var (
	attackFamilies = map[string]bool{
		"write": true, "disguised-write": true, "multi": true, "file": true,
		"txn": true, "fn": true, "lock": true, "evasion": true,
	}
	fnCaseID = regexp.MustCompile(`^fn-[a-z]+-(from|sub|cte|arg|lateral)$`)
)

// parseCorpus reads the "-- case: <id> <family>" format. A family outside
// families, a malformed header, a duplicate id or text before the first case
// is an error.
func parseCorpus(text string, families map[string]bool) ([]corpusCase, error) {
	var cases []corpusCase
	seen := map[string]bool{}
	var cur *corpusCase
	flush := func() {
		if cur != nil {
			cur.SQL = strings.TrimSpace(cur.SQL)
			cases = append(cases, *cur)
		}
	}
	for i, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(line, "-- case:")
		if !ok {
			if cur != nil {
				cur.SQL += line + "\n"
			} else if strings.TrimSpace(line) != "" {
				return nil, fmt.Errorf("line %d: text before the first case", i+1)
			}
			continue
		}
		f := strings.Fields(rest)
		if len(f) != 2 {
			return nil, fmt.Errorf("line %d: want \"-- case: <id> <family>\"", i+1)
		}
		if !families[f[1]] {
			return nil, fmt.Errorf("line %d: unknown family %q", i+1, f[1])
		}
		if seen[f[0]] {
			return nil, fmt.Errorf("line %d: duplicate case id %q", i+1, f[0])
		}
		seen[f[0]] = true
		flush()
		cur = &corpusCase{ID: f[0], Family: f[1]}
	}
	flush()
	return cases, nil
}

func loadCorpus(t testing.TB, name string, families map[string]bool) []corpusCase {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "safesql", name))
	if err != nil {
		t.Fatal(err)
	}
	cases, err := parseCorpus(string(b), families)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return cases
}

func TestParseCorpus(t *testing.T) {
	fam := map[string]bool{"legit": true}
	tests := []struct {
		name, in string
		want     []corpusCase
		wantErr  string
	}{
		{"two cases", "-- case: a legit\nSELECT 1;\n\n-- case: b legit\nSELECT 2;\n--x\n",
			[]corpusCase{{"a", "legit", "SELECT 1;"}, {"b", "legit", "SELECT 2;\n--x"}}, ""},
		{"unknown family", "-- case: a bogus\nSELECT 1;", nil, `unknown family "bogus"`},
		{"missing family", "-- case: a\nSELECT 1;", nil, "want"},
		{"duplicate id", "-- case: a legit\nx\n-- case: a legit\ny", nil, "duplicate"},
		{"text before case", "SELECT 1;\n-- case: a legit\nx", nil, "before the first case"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCorpus(tt.in, fam)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if fmt.Sprint(got) != fmt.Sprint(tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

func TestCorpora(t *testing.T) {
	p, err := NewPolicy(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	attacks := loadCorpus(t, "attacks.sql", attackFamilies)
	legit := loadCorpus(t, "legit.sql", map[string]bool{"legit": true})

	if len(legit) < 30 {
		t.Errorf("legit corpus has %d cases, want at least 30", len(legit))
	}
	refused := 0
	fnLoc := map[string]bool{}
	famCount := map[string]int{}
	for _, c := range attacks {
		famCount[c.Family]++
		if c.Family == "fn" {
			if !fnCaseID.MatchString(c.ID) {
				t.Errorf("fn case id %q does not match fn-<family>-<loc>", c.ID)
			}
			fnLoc[c.ID[strings.LastIndex(c.ID, "-")+1:]] = true
		}
		t.Run("attack/"+c.ID, func(t *testing.T) {
			if _, err := Check(c.SQL, p); err == nil {
				t.Errorf("accepted: %s", c.SQL)
			}
		})
		if _, err := Check(c.SQL, p); err != nil {
			refused++
		}
	}
	for f := range attackFamilies {
		if famCount[f] < 2 {
			t.Errorf("attack family %s has %d cases, want several", f, famCount[f])
		}
	}
	for _, loc := range []string{"from", "sub", "cte", "arg", "lateral"} {
		if !fnLoc[loc] {
			t.Errorf("no fn case at location %q", loc)
		}
	}
	accepted := 0
	for _, c := range legit {
		t.Run("legit/"+c.ID, func(t *testing.T) {
			if _, err := Check(c.SQL, p); err != nil {
				t.Errorf("refused: %v\n%s", err, c.SQL)
			}
		})
		if _, err := Check(c.SQL, p); err == nil {
			accepted++
		}
	}
	t.Logf("attacks refused %d/%d, legit accepted %d/%d", refused, len(attacks), accepted, len(legit))
}

func TestDenylistFamiliesCovered(t *testing.T) {
	// One "-from" case per denylist family of the design (D5).
	want := []string{"lo", "settings", "advisory", "notify", "sessions", "files", "admin", "replication", "statreset", "index", "stats", "dynsql", "ext"}
	have := map[string]bool{}
	for _, c := range loadCorpus(t, "attacks.sql", attackFamilies) {
		if c.Family == "fn" && strings.HasSuffix(c.ID, "-from") {
			have[strings.Split(c.ID, "-")[1]] = true
		}
	}
	for _, f := range want {
		if !have[f] {
			t.Errorf("no fn-%s-from case", f)
		}
	}
}
