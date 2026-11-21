package ops

import (
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestParseMem(t *testing.T) {
	tests := []struct {
		in      string
		want    float64
		wantErr bool
	}{
		{"123.5MiB / 7.6GiB", 123.5, false},
		{"2.0GiB / 7.6GiB", 2048, false},
		{"512KiB / 7.6GiB", 0.5, false},
		{"n/a", 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseMem(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("parseMem = %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestMedianMeasure(t *testing.T) {
	tests := []struct {
		name    string
		samples []float64
		want    float64
	}{
		{"one outlier does not move the median", []float64{14.2, 30.3, 15.8}, 15.8},
		{"order does not matter", []float64{30.3, 15.8, 14.2}, 15.8},
		{"equal samples", []float64{16, 16, 16}, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := medianMeasure(tt.samples, "note")
			if got.Value != tt.want || got.Note != "note" || len(got.Samples) != len(tt.samples) {
				t.Errorf("medianMeasure = %+v, want value %v with %d samples", got, tt.want, len(tt.samples))
			}
		})
	}
}

func TestParseHost(t *testing.T) {
	tests := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{`{"OperatingSystem":"Ubuntu 24.04.3 LTS","Architecture":"x86_64","NCPU":4,"MemTotal":16766734336,"Name":"runner"}`,
			"Ubuntu 24.04.3 LTS, x86_64, 4 CPUs, 15.6 GiB", false},
		{`{"OperatingSystem":"Docker Desktop","Architecture":"aarch64","NCPU":0,"MemTotal":0}`, "", true},
		{`not json`, "", true},
	}
	for _, tt := range tests {
		got, err := parseHost([]byte(tt.in))
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("parseHost(%s) = %q, %v; want %q, err %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestTable(t *testing.T) {
	o := results.Ops{
		Env:         results.Env{Servers: []results.ServerEnv{{Name: "dba", Version: "0.3.3"}, {Name: "crystal", Version: "0.3.0"}}},
		Host:        "Ubuntu, x86_64, 4 CPUs, 15.6 GiB",
		SizeMB:      map[string]results.Measure{"dba": {Value: 23.4}, "crystal": {Value: 395}},
		ColdStartMS: map[string]results.Measure{"dba": {Value: 512.2}, "crystal": {Value: 2337}},
		RSSMB: map[string]results.Measure{
			"dba":     medianMeasure([]float64{24.6, 9.3, 8.7}, MemNote),
			"crystal": {Skipped: true, Note: "needs Pagila"},
		},
	}
	got := Table(o)
	for _, want := range []string{
		"Host: Ubuntu, x86_64, 4 CPUs, 15.6 GiB",
		"| dba 0.3.3 | 23 MB | 512 ms | 9.3 MB (24.6, 9.3, 8.7) |",
		"| crystal 0.3.0 | 395 MB | 2337 ms | skipped (needs Pagila) |",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("table lacks %q:\n%s", want, got)
		}
	}
}
