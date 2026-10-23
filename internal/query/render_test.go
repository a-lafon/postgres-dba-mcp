package query

import (
	"strings"
	"testing"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

func cells(vals ...string) []format.Cell {
	row := make([]format.Cell, len(vals))
	for i, v := range vals {
		if v != `\N` {
			row[i] = format.Cell(v)
		}
	}
	return row
}

func TestRender(t *testing.T) {
	cfg := testConfig(t, "--max-rows=2")
	d := 4 * time.Millisecond
	tests := []struct {
		name    string
		res     result
		maxRows int
		want    string
	}{
		{"complete", result{Columns: []string{"x"}, Rows: [][]format.Cell{cells("1")}, Elapsed: d}, 2, "x\n1\n-- 1 row, 4 ms\n"},
		{"truncated", result{Columns: []string{"x"}, Rows: [][]format.Cell{cells("1"), cells("2")}, More: true, Elapsed: d}, 2,
			"x\n1\n2\n-- 2 rows, truncated (more than 2), 4 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n"},
		{"empty", result{Columns: []string{"x"}, Elapsed: d}, 2, "x\n-- 0 rows, 4 ms\n"},
		{"null and empty", result{Columns: []string{"a", "b"}, Rows: [][]format.Cell{cells(`\N`, "")}, Elapsed: d}, 2, "a,b\n\\N,\"\"\n-- 1 row, 4 ms\n"},
		{"command tag", result{Tag: "UPDATE 3", Elapsed: 3 * time.Millisecond}, 2, "-- UPDATE 3, 3 ms\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := render(tt.res, cfg, tt.maxRows)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderHeaderOverBudget(t *testing.T) {
	cfg := testConfig(t, "--max-bytes=1024")
	res := result{Columns: []string{strings.Repeat("c", 2000)}}
	if _, err := render(res, cfg, 10); err == nil {
		t.Fatal("want an error when the header alone exceeds --max-bytes")
	}
}
