package hypo

import (
	"errors"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/internal/db"
)

func TestDDL(t *testing.T) {
	cols33 := make([]string, 33)
	for i := range cols33 {
		cols33[i] = "c"
	}
	tests := []struct {
		name string
		idx  Index
		want string // CREATE INDEX text, or the error text when it starts with "invalid"
	}{
		{"qualified", Index{Table: "public.events", Columns: []string{"account_id", "created_at"}},
			`CREATE INDEX ON "public"."events" USING btree ("account_id", "created_at")`},
		{"folded", Index{Table: "Events", Columns: []string{"Account_ID"}, Using: "HASH"},
			`CREATE INDEX ON "events" USING hash ("account_id")`},
		{"quoted kept", Index{Table: `"My Table"`, Columns: []string{`"Col ""x"""`}},
			`CREATE INDEX ON "My Table" USING btree ("Col ""x""")`},
		{"quoted dot", Index{Table: `s."a.b"`, Columns: []string{"c"}},
			`CREATE INDEX ON "s"."a.b" USING btree ("c")`},
		{"expression", Index{Table: "users", Columns: []string{"lower(email)"}},
			`invalid hypothetical index: column "lower(email)" is not a valid identifier`},
		{"injection", Index{Table: "events) ; DROP TABLE x; --", Columns: []string{"a"}},
			`invalid hypothetical index: table "events) ; DROP TABLE x; --" is not a valid [schema.]name`},
		{"empty table", Index{Table: "", Columns: []string{"a"}},
			`invalid hypothetical index: table "" is not a valid [schema.]name`},
		{"bad schema part", Index{Table: "1s.events", Columns: []string{"a"}},
			`invalid hypothetical index: table "1s.events" is not a valid [schema.]name`},
		{"three parts", Index{Table: "a.b.c", Columns: []string{"a"}},
			`invalid hypothetical index: table "a.b.c" is not a valid [schema.]name`},
		{"empty quoted", Index{Table: `""`, Columns: []string{"a"}},
			`invalid hypothetical index: table "\"\"" is not a valid [schema.]name`},
		{"unterminated quote", Index{Table: `"abc`, Columns: []string{"a"}},
			`invalid hypothetical index: table "\"abc" is not a valid [schema.]name`},
		{"no columns", Index{Table: "events"}, `invalid hypothetical index: columns must not be empty`},
		{"33 columns", Index{Table: "events", Columns: cols33}, `invalid hypothetical index: too many columns (max 32)`},
		{"dotted column", Index{Table: "events", Columns: []string{"a.b"}},
			`invalid hypothetical index: column "a.b" is not a valid identifier`},
		{"bad using", Index{Table: "events", Columns: []string{"a"}, Using: "btree; x"},
			`invalid hypothetical index: using "btree; x" is not a valid access method name`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ddl(tt.idx)
			if strings.HasPrefix(tt.want, "invalid") {
				if err == nil || err.Error() != tt.want {
					t.Fatalf("err = %v, want %q", err, tt.want)
				}
				if Validate(tt.idx) == nil {
					t.Fatal("Validate accepted it")
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}

func TestOptions(t *testing.T) {
	tests := []struct {
		format  string
		analyze bool
		want    string
	}{
		{"JSON", false, "FORMAT JSON, VERBOSE"},
		{"JSON", true, "FORMAT JSON, VERBOSE, ANALYZE, BUFFERS"},
		{"TEXT", false, "FORMAT TEXT, VERBOSE"},
	}
	for _, tt := range tests {
		if got := options(tt.format, tt.analyze); got != tt.want {
			t.Errorf("options(%q, %v) = %q, want %q", tt.format, tt.analyze, got, tt.want)
		}
	}
}

func TestHasParams(t *testing.T) {
	for q, want := range map[string]bool{
		"SELECT * FROM t WHERE a = $1 LIMIT $2": true,
		"SELECT '$1'":                           false,
		"SELECT $$text $1$$":                    false,
		"SELECT 1":                              false,
	} {
		if got := HasParams(q); got != want {
			t.Errorf("HasParams(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestAvailable(t *testing.T) {
	if !Available(db.Info{Extensions: map[string]string{"hypopg": "1.4"}}) || Available(db.Info{}) {
		t.Fatal("Available wrong")
	}
}

func TestParsePlan(t *testing.T) {
	const seq = `[{"Plan": {"Node Type": "Seq Scan", "Parallel Aware": false, "Schema": "public", "Relation Name": "events",
		"Startup Cost": 0.00, "Total Cost": 4000.5, "Plan Rows": 40, "Filter": "(events.account_id = 42)"},
		"Planning Time": 0.2}]`
	const join = `[{"Plan": {"Node Type": "Hash Join", "Join Type": "Inner", "Hash Cond": "(a.id = b.id)", "Total Cost": 10, "Plan Rows": 5,
		"Actual Rows": 4, "Actual Loops": 3, "Actual Total Time": 1.5,
		"Plans": [
		 {"Node Type": "Sort", "Sort Space Type": "Disk", "Sort Space Used": 2048, "Total Cost": 4, "Plan Rows": 1},
		 {"Node Type": "Seq Scan", "Rows Removed by Filter": 99, "Parallel Aware": true, "Scan Direction": "Backward", "Total Cost": 3, "Plan Rows": 1}]},
		"Planning Time": 0.1, "Execution Time": 2.5}]`

	n, tot, err := parsePlan(seq, []string{"x"})
	if err != nil || n.NodeType != "Seq Scan" || n.Schema != "public" || n.Relation != "events" || n.Filter == "" || n.TotalCost != 4000.5 ||
		n.ActualRows != nil || tot.PlanningMs == nil || tot.ExecutionMs != nil || tot.HypoIndexNames[0] != "x" {
		t.Fatalf("seq: %+v %+v %v", n, tot, err)
	}
	n, tot, err = parsePlan(join, nil)
	if err != nil || n.HashCond == "" || *n.ActualRows != 4 || *n.Loops != 3 || *n.ActualTotalMs != 1.5 || *tot.ExecutionMs != 2.5 ||
		len(n.Plans) != 2 || n.Plans[0].SortSpaceType != "Disk" || *n.Plans[0].SortSpaceUsedKB != 2048 ||
		*n.Plans[1].RowsRemovedByFilter != 99 || !n.Plans[1].ParallelAware || n.Plans[1].ScanDirection != "Backward" {
		t.Fatalf("join: %+v %+v %v", n, tot, err)
	}
	for _, bad := range []string{``, `{}`, `[]`, `[{}]`, `[{"Plan":{}},{"Plan":{}}]`, `nope`} {
		if _, _, err := parsePlan(bad, nil); !errors.Is(err, ErrUnexpectedOutput) {
			t.Errorf("parsePlan(%q) err = %v", bad, err)
		}
	}
}
