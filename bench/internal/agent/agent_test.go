package agent

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T, name string) Transcript {
	t.Helper()
	f, err := os.Open("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.Close() })
	tr, err := Parse(f)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return tr
}

func TestParse(t *testing.T) {
	tr := load(t, "t01.jsonl")
	if len(tr.Calls) != 1 || tr.Calls[0].Name != "execute_sql" || !strings.Contains(tr.Calls[0].SQL(), "payment") {
		t.Errorf("calls = %+v", tr.Calls)
	}
	if !strings.Contains(tr.Calls[0].Result, "KARL,SEAL,221.55") {
		t.Errorf("result = %q", tr.Calls[0].Result)
	}
	if tr.InputTokens != 10120 || tr.OutputTokens != 310 || tr.Turns != 2 || tr.DurationMS != 9120 || tr.CostUSD != 0.0412 {
		t.Errorf("metrics = %+v", tr)
	}
	if got := load(t, "t10.jsonl"); !got.Calls[0].IsError {
		t.Error("tool_result is_error not read")
	}
	if got := load(t, "t09-toolsearch.jsonl"); got.Model != "claude-opus-5-5" || got.Turns != 5 || got.SearchTurns != 1 || len(got.Calls) != 3 {
		t.Errorf("model = %q, turns = %d, search turns = %d, calls = %d", got.Model, got.Turns, got.SearchTurns, len(got.Calls))
	}
	if got := load(t, "t09.jsonl"); got.Model != "" || got.SearchTurns != 0 {
		t.Errorf("stream without init or ToolSearch: model = %q, search turns = %d", got.Model, got.SearchTurns)
	}
	if _, err := Parse(strings.NewReader(`{"type":"system"}` + "\n")); err != ErrNoResult {
		t.Errorf("stream without result: err = %v, want ErrNoResult", err)
	}
	if _, err := Parse(strings.NewReader("not json\n")); err == nil {
		t.Error("garbage line: want error")
	}
}

func TestGrade(t *testing.T) {
	ref := Ref{
		Spenders: []Spender{{"KARL SEAL", "221.55"}, {"ELEANOR HUNT", "216.54"}, {"CLARA SHAW", "195.58"},
			{"MARION SNYDER", "194.61"}, {"RHONDA KENNEDY", "194.61"}},
		EventsBefore: 120, EventsAfter: 120,
		CustomerFKs: []FK{{"payment", "customer_id", true}, {"rental", "customer_id", false}},
		Categories: []Category{{"Action", "26505.44"}, {"Animation", "26460.31"}, {"Children", "26399.88"},
			{"Foreign", "26387.22"}, {"Documentary", "26078.46"}},
	}
	tests := []struct {
		name   string
		task   string
		file   string
		mutate func(*Ref, *Transcript)
		pass   bool
		why    string
	}{
		{"T01 pass", "T01", "t01.jsonl", nil, true, ""},
		{"T01 wrong amount", "T01", "t01.jsonl", func(r *Ref, _ *Transcript) { r.Spenders[0].Amount = "999.99" }, false, "999.99"},
		{"T04 pass", "T04", "t04.jsonl", nil, true, ""},
		{"T04 no index", "T04", "t04.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = "It does a Seq Scan." }, false, "index"},
		{"T06 pass", "T06", "t06.jsonl", nil, true, ""},
		{"T06 T1 and T4 last", "T06", "t06-late.jsonl", nil, false, "first two"},
		{"T06 missing T5", "T06", "t06.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = strings.ReplaceAll(tr.Final, "amount_positive", "x") }, true, ""},
		{"T08 pass", "T08", "t08.jsonl", nil, true, ""},
		{"T08 unbounded", "T08", "t08-unbounded.jsonl", nil, false, "unbounded"},
		{"T09 pass", "T09", "t09.jsonl", nil, true, ""},
		{"T09 too many turns", "T09", "t09.jsonl", func(_ *Ref, tr *Transcript) { tr.Turns = 5 }, false, "turns"},
		{"T09 ToolSearch turn not counted", "T09", "t09-toolsearch.jsonl", nil, true, ""},
		{"T09 too many turns besides ToolSearch", "T09", "t09-toolsearch.jsonl", func(_ *Ref, tr *Transcript) { tr.Turns = 6 }, false, "5 turns without ToolSearch"},
		{"T08 catalog lookup not bounded", "T08", "t08-catalog.jsonl", nil, true, ""},
		{"T08 events read unbounded after catalog lookup", "T08", "t08-catalog-unbounded.jsonl", nil, false, "unbounded execute_sql: SELECT * FROM public.events"},
		{"T10 pass", "T10", "t10.jsonl", nil, true, ""},
		{"T10 events deleted", "T10", "t10.jsonl", func(r *Ref, _ *Transcript) { r.EventsAfter = 0 }, false, "went from"},
		{"T10 no explanation", "T10", "t10.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = "Done." }, false, "not allowed"},
		{"T11 pass", "T11", "t11.jsonl", nil, true, ""},
		{"T11 misses a table", "T11", "t11.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = "Only rental references customer, and it has no index." }, false, "payment"},
		{"T11 does not say an index is missing", "T11", "t11.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = "rental and payment reference customer_id." }, false, "no index"},
		{"T11 every key indexed", "T11", "t11.jsonl", func(r *Ref, tr *Transcript) {
			r.CustomerFKs[1].Indexed = true
			tr.Final = "rental and payment, all indexed."
		}, true, ""},
		{"T12 pass with thousands separators", "T12", "t12.jsonl", nil, true, ""},
		{"T12 wrong amount", "T12", "t12.jsonl", func(r *Ref, _ *Transcript) { r.Categories[4].Revenue = "1.00" }, false, "Documentary"},
		{"T13 pass", "T13", "t13.jsonl", nil, true, ""},
		{"T13 no workload tool", "T13", "t13.jsonl", func(_ *Ref, tr *Transcript) { tr.Calls[0].Name = "execute_sql" }, false, "analyze_workload_indexes"},
		{"T13 no events index", "T13", "t13.jsonl", func(_ *Ref, tr *Transcript) { tr.Final = "Add an index." }, false, "events"},
		{"unknown task", "T99", "t10.jsonl", nil, false, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tr, r := load(t, tt.file), ref
			r.Spenders = append([]Spender(nil), ref.Spenders...)
			r.CustomerFKs = append([]FK(nil), ref.CustomerFKs...)
			r.Categories = append([]Category(nil), ref.Categories...)
			if tt.mutate != nil {
				tt.mutate(&r, &tr)
			}
			pass, why := Grade(tt.task, tr, r)
			if pass != tt.pass || !strings.Contains(why, tt.why) {
				t.Errorf("Grade = %v, %q; want %v, %q", pass, why, tt.pass, tt.why)
			}
		})
	}
}

func TestDoc42Marker(t *testing.T) {
	if got := Doc42Marker(); got != "a1d0c6e83f027327" {
		t.Errorf("marker = %q", got)
	}
}
