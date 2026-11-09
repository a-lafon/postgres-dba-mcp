package scenarios

import (
	"reflect"
	"testing"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Literal outputs: the crystaldba ones were recorded from crystaldba/postgres-mcp 0.3.0, the postgres-dba-mcp ones
// from the output format documented in README.md.
const (
	dbaTypes = "id,txt,num,arr,ts,bin\n1,hello,532.10,\"{1,2}\",2026-03-02 10:14:00+00,\\xdeadbeef\n2,\\N,\\N,\\N,\\N,\\N\n-- 2 rows, 3 ms\n"
	pyTypes  = "[{'id': 1, 'txt': 'hello', 'num': Decimal('532.10'), 'arr': [1, 2], 'ts': datetime.datetime(2026, 3, 2, 10, 14, tzinfo=zoneinfo.ZoneInfo(key='UTC')), 'bin': b'\\xde\\xad\\xbe\\xef'}, {'id': 2, 'txt': None}]"
)

func TestFactForms(t *testing.T) {
	ts := time.Date(2026, 3, 2, 10, 14, 0, 0, time.UTC)
	facts := []Fact{Str("hello"), Num("532.10"), IntArray([]int32{1, 2}), Time(ts), Bytea([]byte{0xde, 0xad, 0xbe, 0xef}), Null()}
	for _, srv := range []struct{ name, text string }{{results.DBA, dbaTypes}, {results.Crystal, pyTypes}} {
		t.Run(srv.name, func(t *testing.T) {
			if miss := Missing(srv.name, srv.text, facts); len(miss) != 0 {
				t.Errorf("missing %q", miss)
			}
		})
	}
	if miss := Missing(results.Crystal, "[{'id': 1}]", facts[:1]); !reflect.DeepEqual(miss, []string{"'hello'"}) {
		t.Errorf("absent fact not reported: %q", miss)
	}
}

func TestForms(t *testing.T) {
	tests := []struct {
		name    string
		fact    Fact
		dba     string
		crystal string
	}{
		{"decimal from docs", Num("1.5"), "1.5", "Decimal('1.5')"},
		{"datetime from docs", Time(time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)), "2024-01-01 00:00:00+00", "datetime.datetime(2024, 1, 1, 0, 0"},
		{"datetime with seconds", Time(time.Date(2024, 1, 1, 0, 0, 7, 500000000, time.UTC)), "2024-01-01 00:00:07.5+00", "datetime.datetime(2024, 1, 1, 0, 0, 7, 500000"},
		{"empty", Empty(), `""`, "''"},
		{"text with comma", Str("a, b"), `"a, b"`, "'a, b'"},
		{"text with apostrophe", Str("it's"), "it's", `"it's"`},
		{"text with quote", Str(`say "hi"`), `"say ""hi"""`, `'say "hi"'`},
		{"row", Row([]string{"id", "n"}, Int(7), Int(9)), "\n7,9", "'id': 7, 'n': 9"},
		{"interval", Interval("1 day 02:00:00", 1, 7200_000_000), "1 day 02:00:00", "datetime.timedelta(days=1, seconds=7200)"},
		{"bytea 4 bytes", Bytea([]byte{0xde, 0xad, 0xbe, 0xef}), `\xdeadbeef`, `b'\xde\xad\xbe\xef'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.fact.DBA != tt.dba || tt.fact.Crystal != tt.crystal {
				t.Errorf("got dba=%q crystal=%q, want dba=%q crystal=%q", tt.fact.DBA, tt.fact.Crystal, tt.dba, tt.crystal)
			}
		})
	}
}

func TestParseAdvice(t *testing.T) {
	dba := "Workload cost 4812.0 → 8.4 (-99.8%), 1 index, +4.4 MB, 1.2 s\n#,cost_change,size,ddl\n1,-99.8%,4.4 MB,\"CREATE INDEX ON public.events (account_id, created_at);\"\nPer query: Q1 -99.8%\n"
	crystal := "{'summary': {'total_recommendations': 1, 'base_cost': '1834.0', 'new_cost': '8.3', 'total_size_bytes': '16.4 kB', 'improvement_multiple': '221.0'}, 'recommendations': [{'index_apply_order': 1, 'index_target_table': 'events', 'index_target_columns': ('account_id', 'created_at'), 'index_definition': 'CREATE INDEX crystaldba_idx_events_account_id_created_at_2 ON events USING btree (account_id, created_at)'}]}"
	tests := []struct {
		name   string
		server string
		text   string
		want   results.Advice
	}{
		{"dba", results.DBA, dba, results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: (8.4 - 4812.0) / 4812.0}},
		{"crystal", results.Crystal, crystal, results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: (8.3 - 1834.0) / 1834.0}},
		{"dba large costs", results.DBA, "Workload cost 38B → 604M (-98.4%), 1 index, +1.0 MB, 4.0 s\n#,cost_change,size,ddl\n1,-98.4%,1.0 MB,\"CREATE INDEX ON public.events (account_id, created_at);\"\nPer query: Q1 -98.4%\nNot recommended: other indexes would each lower the workload cost by less than 1%.\n", results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: (604e6 - 38e9) / 38e9}},
		{"dba unserved query", results.DBA, dba + "Not recommended, small workload share: CREATE INDEX ON public.orders (created_at); Q2 -99.9%, workload -0.4%, 33 MB\n", results.Advice{Indexes: []string{"events(account_id,created_at)"}, Change: (8.4 - 4812.0) / 4812.0}},
		{"dba none", results.DBA, "No index recommendation: no candidate lowers the workload cost by at least 10%.\nWorkload cost 8.4 (1 query, 4 candidates evaluated), 0.2 s\n", results.Advice{None: true}},
		{"crystal none", results.Crystal, "{'recommendations': 'No index recommendations found.', '_langfuse_trace': []}", results.Advice{None: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ParseAdvice(tt.server, tt.text)
			if !reflect.DeepEqual(got.Indexes, tt.want.Indexes) || got.None != tt.want.None || (got.Change-tt.want.Change) > 1e-9 || (tt.want.Change-got.Change) > 1e-9 {
				t.Errorf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestRootCost(t *testing.T) {
	tests := []struct {
		name   string
		server string
		text   string
		want   float64
	}{
		{"dba plain", results.DBA, "Total cost 5512.34, estimated rows 20\n-> Parallel Seq Scan on public.events", 5512.34},
		{"dba hypothetical", results.DBA, "Total cost 82.10, estimated rows 20\nWith hypothetical indexes: total cost 5512.34 -> 82.10 (-98.5%)\n", 82.10},
		{"crystal", results.Crystal, "Planning Time: 0.081 ms\n→ Limit (Cost: 0.43..45.30) [Rows: 20]\n  → Index Scan (Cost: 0.43..45.30) on events [Rows: 20]", 45.30},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, ok := RootCost(tt.server, tt.text); !ok || got != tt.want {
				t.Errorf("RootCost = %v, %v, want %v", got, ok, tt.want)
			}
		})
	}
}

func TestNormalizeQuery(t *testing.T) {
	got := NormalizeQuery(`SELECT *\n  FROM events   WHERE id = $1`)
	if want := "SELECT * FROM events WHERE id = $1"; got != want {
		t.Errorf("NormalizeQuery = %q, want %q", got, want)
	}
}

func TestPercentile(t *testing.T) {
	v := []float64{9, 1, 5, 3, 7}
	tests := []struct {
		name string
		p    float64
		want float64
	}{{"p50", 50, 5}, {"p95", 95, 9}, {"p0", 0, 1}, {"p100", 100, 9}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Percentile(v, tt.p); got != tt.want {
				t.Errorf("Percentile(%v) = %v, want %v", tt.p, got, tt.want)
			}
		})
	}
	if Percentile(nil, 50) != 0 || Median([]float64{1, 2, 3, 4}) != 2.5 || Median([]float64{3, 1, 2}) != 2 {
		t.Error("empty percentile or median wrong")
	}
}

// Recorded answers to `SELECT * FROM types` of the two servers that answer in JSON: DBHub 1.6.0 (indented, one
// object per row, numeric as text, bytea as a node Buffer, interval split into days and hours) and MCP Toolbox
// 1.16.0 (one compact object per line, numeric as a number, bytea in base64, interval as pgtype.Interval).
const (
	dbhubTypes = `{
  "success": true,
  "data": {
    "statements": [
      {
        "sql": "SELECT * FROM types",
        "rows": [
          {
            "id": 1,
            "txt": "hello",
            "empty": "",
            "num": "532.10",
            "arr": [
              1,
              2
            ],
            "iv": {
              "days": 1,
              "hours": 2
            },
            "ts": "2026-03-02 10:14:00+00",
            "bin": {
              "type": "Buffer",
              "data": [
                222,
                173,
                190,
                239
              ]
            }
          },
          {
            "id": 2,
            "txt": null
          }
        ],
        "count": 2
      }
    ],
    "source_id": "default"
  }
}`
	toolboxTypes = `{"id":1,"txt":"hello","empty":"","num":532.10,"arr":[1,2],"iv":{"Microseconds":7200000000,"Days":1,"Months":0,"Valid":true},"ts":"2026-03-02T10:14:00Z","bin":"3q2+7w=="}
{"id":2,"txt":null,"empty":null,"num":null,"arr":null,"iv":null,"ts":null,"bin":null}`
)

// A fact is graded on the value, not on the rendering: the same eight cells must be found in the CSV of
// postgres-dba-mcp, in the Python repr of crystaldba and in both JSON renderings.
func TestFactFormsJSON(t *testing.T) {
	ts := time.Date(2026, 3, 2, 10, 14, 0, 0, time.UTC)
	facts := []Fact{Str("hello"), Empty(), Num("532.10"), IntArray([]int32{1, 2}),
		Interval("1 day 02:00:00", 1, 7200_000_000), Time(ts), Bytea([]byte{0xde, 0xad, 0xbe, 0xef}), Null()}
	for _, srv := range []struct{ name, text string }{{"dbhub", dbhubTypes}, {"toolbox", toolboxTypes}} {
		t.Run(srv.name, func(t *testing.T) {
			if miss := Missing(srv.name, srv.text, facts); len(miss) != 0 {
				t.Errorf("missing %q", miss)
			}
		})
	}
}

// A fact must stay a fact: a JSON answer that holds another value does not pass.
func TestFactJSONRejectsAWrongValue(t *testing.T) {
	tests := []struct {
		name string
		fact Fact
		text string
	}{
		{"wrong string", Str("hello"), `{"txt":"hallo"}`},
		{"wrong number", Num("532.10"), `{"num":532.11}`},
		{"a longer number is not that number", Num("532.10"), `{"num":532.109}`},
		{"a longer integer is not that integer", Int(1), `{"id":15}`},
		{"string is not a prefix match", Str("hell"), `{"txt":"hello"}`},
		{"wrong array", IntArray([]int32{1, 2}), `{"arr":[1,3]}`},
		{"wrong bytes", Bytea([]byte{0xde, 0xad}), `{"bin":"3q0a"}`},
		{"wrong interval", Interval("1 day 02:00:00", 1, 7200_000_000), `{"iv":{"days":2,"hours":2}}`},
		{"wrong timestamp", Time(time.Date(2026, 3, 2, 10, 14, 0, 0, time.UTC)), `{"ts":"2026-03-02T10:15:00Z"}`},
		{"null is not an empty string", Null(), `{"txt":""}`},
		{"row values must belong to the same row", Row([]string{"customer_id", "sum"}, Int(526), Num("358.12")),
			`{"customer_id":526,"sum":"1.00"}` + "\n" + `{"customer_id":1,"sum":"358.12"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if miss := Missing("dbhub", tt.text, []Fact{tt.fact}); len(miss) != 1 {
				t.Errorf("%s accepted %s", tt.fact.JSON, tt.text)
			}
		})
	}
}

// A row is one row: its values must be found together, in one JSON object.
func TestRowJSON(t *testing.T) {
	row := Row([]string{"customer_id", "sum"}, Int(526), Num("358.12"))
	for _, text := range []string{
		`{"customer_id":526,"sum":358.12}`,
		"{\n            \"customer_id\": 526,\n            \"sum\": \"358.12\"\n          }",
	} {
		if miss := Missing("dbhub", text, []Fact{row}); len(miss) != 0 {
			t.Errorf("row not found in %s", text)
		}
	}
}

// The same Postgres message reaches the harness escaped inside DBHub's JSON and bare inside Toolbox's plain-text
// error: both are the server relaying it.
func TestSameJSONEscapes(t *testing.T) {
	f := Same(`column "nme" does not exist`)
	for _, text := range []string{
		`{"success":false,"error":"column \"nme\" does not exist"}`,
		`error processing request: unable to execute query: ERROR: column "nme" does not exist (SQLSTATE 42703)`,
	} {
		if miss := Missing("dbhub", text, []Fact{f}); len(miss) != 0 {
			t.Errorf("message not found in %s: %q", text, miss)
		}
	}
	if miss := Missing("dbhub", `{"error":"column "x" does not exist"}`, []Fact{f}); len(miss) != 1 {
		t.Error("another column's error was accepted")
	}
}

// A jsonb column puts braces inside the row, so a row fact must not be anchored on the row's own braces.
func TestRowJSONWithANestedObject(t *testing.T) {
	row := Row([]string{"id"}, Int(1))
	text := "{\n  \"id\": 1,\n  \"payload\": {\n    \"a\": {\"b\": 2}\n  }\n}"
	if miss := Missing("dbhub", text, []Fact{row}); len(miss) != 0 {
		t.Errorf("row with a jsonb column not found: %q", miss)
	}
}
