package scenarios

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

func sample(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestTruncated(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{"S02 footer with estimate", sample(t, "S02-dba.txt"), true},
		{"S03 footer with estimate", sample(t, "S03-dba.txt"), true},
		{"S06 complete result", sample(t, "S06-dba.txt"), false},
		{"row limit without estimate", "a\n1\n-- 100 rows, truncated (more than 100), 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n", true},
		{"byte budget", "a\n1\n-- 57 rows, truncated (32768-byte limit), 38 ms\n", true},
		{"byte budget with estimate", "a\n1\n-- 57 of ~16k rows (planner estimate), truncated (32768-byte limit), 38 ms\n", true},
		{"shortened cell", "a\nabc…[+30 chars]\n-- 1 row, 1 cell shortened, 3 ms\n", true},
		{"data row saying truncated", "a\ntruncated\n-- 1 row, 3 ms\n", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncated(tt.text); got != tt.want {
				t.Errorf("Truncated = %v, want %v", got, tt.want)
			}
		})
	}
}

// A server that answers with an error has not answered the scenario. crystaldba 0.3.0 does this on S03, where its own
// 30 s limit returns an error text in a few milliseconds: graded as an answer, those 44 tokens enter the median and
// turn the token ratio of S01-S06 upside down, and a scenario that declares no fact even grades them correct.
func TestGradeCellRejectsAnErrorAnswer(t *testing.T) {
	tests := []struct {
		name       string
		server     string
		id         string
		text       string
		isError    bool
		wantStatus string
		correct    bool
	}{
		{"crystal timeout", results.Crystal, "S03", "Error: Query execution timed out after 30 seconds in restricted mode.", false, results.StatusError, false},
		{"crystal answer", results.Crystal, "S03", "id,kind\n1,click\n", false, results.StatusOK, true},
		{"dba statement timeout", results.DBA, "S03", "ERROR: canceling statement due to statement timeout (SQLSTATE 57014)", true, results.StatusError, false},
		{"S07 is the scenario whose answer is an error", results.Crystal, "S07", `Error: column "nme" does not exist`, false, results.StatusOK, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cell := results.N2Cell{Correct: true, Truncation: results.TruncNone}
			gradeCell(&cell, servers.Spec{Name: tt.server}, Scenario{ID: tt.id}, factCall(nil, nil), tt.text, tt.isError)
			if cell.Status != tt.wantStatus || cell.Correct != tt.correct {
				t.Errorf("got status %q correct %v, want %q %v", cell.Status, cell.Correct, tt.wantStatus, tt.correct)
			}
		})
	}
}

func TestGradeCell(t *testing.T) {
	types := []Fact{Str("hello"), Empty(), Num("532.10"), IntArray([]int32{1, 2}), Interval("1 day 02:00:00", 1, 0), Bytea([]byte{0xde, 0xad, 0xbe, 0xef}), Null()}
	tests := []struct {
		name      string
		scenario  Scenario
		facts     []Fact
		text      string
		wantTrunc string
		correct   bool
	}{
		{"S02 truncated with estimate", Scenario{ID: "S02", Trunc: TruncRows}, rowIDs("rental_id", []int64{2, 3, 4}), sample(t, "S02-dba.txt"), results.TruncSignalled, true},
		{"S03 truncated with estimate", Scenario{ID: "S03", Trunc: TruncRows}, nil, sample(t, "S03-dba.txt"), results.TruncSignalled, true},
		{"S06 hex bytea", Scenario{ID: "S06"}, types, sample(t, "S06-dba.txt"), results.TruncNone, true},
		{"S02 truncation missing", Scenario{ID: "S02", Trunc: TruncRows}, nil, "a\n1\n-- 1 row, 2 ms\n", results.TruncMissing, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cell := results.N2Cell{Correct: true, Truncation: results.TruncNone}
			gradeCell(&cell, servers.Spec{Name: results.DBA}, tt.scenario, factCall(nil, tt.facts), tt.text, false)
			if cell.Truncation != tt.wantTrunc || cell.Correct != tt.correct {
				t.Errorf("got truncation %q correct %v (missing %q), want %q %v", cell.Truncation, cell.Correct, cell.Missing, tt.wantTrunc, tt.correct)
			}
		})
	}
}

// A server that signals a cut result its own way must be read its own way. DBHub sets a `"truncated": true` field
// in the JSON it returns; read with the postgres-dba-mcp footer alone, S02, S03 and S04 read `truncation missing`
// although the server did signal the cut.
func TestGradeCellReadsTheServerTruncationSignal(t *testing.T) {
	const answer = `{"success":true,"data":{"statements":[{"rows":[{"rental_id":2}],"count":1,"truncated":true}]}}`
	s := Scenario{ID: "S02", Trunc: TruncRows}
	for _, tt := range []struct {
		name string
		spec servers.Spec
		want string
	}{
		{"default reading misses it", servers.Spec{Name: "dbhub"}, results.TruncMissing},
		{"the server's own reading finds it", servers.Spec{Name: "dbhub", Truncated: servers.TruncatedDBHub}, results.TruncSignalled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cell := results.N2Cell{Correct: true, Truncation: results.TruncNone}
			gradeCell(&cell, tt.spec, s, factCall(nil, nil), answer, false)
			if cell.Truncation != tt.want {
				t.Errorf("truncation = %q, want %q", cell.Truncation, tt.want)
			}
		})
	}
}

// A setup step is a tool call like any other: calling it by its canonical name kills S21 on a server that renames
// it (`Tool explain_query not found` on dbhub-4tools, which calls it explain_sql).
func TestSetupToolsAreRenamedLikeAnyOtherCall(t *testing.T) {
	setup := []Step{{Tool: "explain_query"}}
	tests := []struct {
		name string
		spec servers.Spec
		want []string
		ok   bool
	}{
		{"canonical names", servers.Spec{Name: results.DBA}, []string{"explain_query"}, true},
		{"renamed", servers.Spec{Name: "dbhub-4tools", Tools: map[string]string{"explain_query": "explain_sql"}}, []string{"explain_sql"}, true},
		{"absent", servers.Spec{Name: "toolbox", Tools: map[string]string{"execute_sql": "execute_sql"}}, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := setupTools(tt.spec, setup)
			if ok != tt.ok || (ok && got[0] != tt.want[0]) {
				t.Errorf("setupTools = %v, %v; want %v, %v", got, ok, tt.want, tt.ok)
			}
		})
	}
}
