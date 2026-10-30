package scenarios

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
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
			gradeCell(&cell, results.DBA, tt.scenario, factCall(nil, tt.facts), tt.text, false)
			if cell.Truncation != tt.wantTrunc || cell.Correct != tt.correct {
				t.Errorf("got truncation %q correct %v (missing %q), want %q %v", cell.Truncation, cell.Correct, cell.Missing, tt.wantTrunc, tt.correct)
			}
		})
	}
}
