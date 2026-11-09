package report

import (
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func n2Of(cells map[string]map[string]results.N2Cell) *results.N2 {
	var rows []results.N2Row
	for _, id := range []string{"S01", "S02", "S03"} {
		rows = append(rows, results.N2Row{ID: id, Tool: "execute_sql", Cells: cells[id]})
	}
	return &results.N2{Rows: rows}
}

// The median may only compare the scenarios every server answered: a scenario one server has no tool for, or
// failed, is not a scenario the others may be measured against.
func TestCommonScenarios(t *testing.T) {
	ok := results.N2Cell{Status: results.StatusOK, Tokens: 10}
	n2 := n2Of(map[string]map[string]results.N2Cell{
		"S01": {"dba": ok, "dbhub": ok},
		"S02": {"dba": ok, "dbhub": {Status: results.StatusNoTool}},
		"S03": {"dba": ok, "dbhub": {Status: results.StatusOversized}},
	})
	got := commonScenarios(n2, []string{"dba", "dbhub"})
	if len(got) != 1 || got[0] != "S01" {
		t.Errorf("common scenarios = %v, want [S01]", got)
	}
	if got := commonScenarios(n2, []string{"dba"}); len(got) != 3 {
		t.Errorf("one server alone answers %v, want all three", got)
	}
}

// A server measured on n1 alone must not empty the common set of the scenarios.
func TestCompareKeepsTheCommonSetWhenAServerSkippedN2(t *testing.T) {
	ok := results.N2Cell{Status: results.StatusOK, Tokens: 10}
	in := Inputs{
		RunID: "test",
		N1:    &results.N1{Servers: []results.N1Server{{Server: "dba", Tools: 10, Full: 100}, {Server: "dbhub-default", Tools: 2, Full: 20}}},
		N2:    n2Of(map[string]map[string]results.N2Cell{"S01": {"dba": ok}, "S02": {"dba": ok}, "S03": {"dba": ok}}),
	}
	md := Compare(in)
	if !strings.Contains(md, "S01, S02, S03") {
		t.Errorf("compare.md dropped the common scenarios:\n%s", md)
	}
	if !strings.Contains(md, "dbhub-default") {
		t.Error("compare.md leaves out a server measured on n1 only")
	}
}
