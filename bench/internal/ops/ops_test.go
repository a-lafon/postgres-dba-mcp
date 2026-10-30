package ops

import (
	"os"
	"path/filepath"
	"testing"
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

func TestParseSamples(t *testing.T) {
	tests := []struct {
		file    string
		parse   func(string) (float64, error)
		want    float64
		wantErr bool
	}{
		{"footprint-darwin.txt", parseFootprint, 801.0 / 1024, false},
		{"footprint-darwin-mb.txt", parseFootprint, 36, false},
		{"proc-status-linux.txt", parseVmRSS, 30, false},
		{"proc-status-linux.txt", parseFootprint, 0, true},
		{"footprint-darwin.txt", parseVmRSS, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			got, err := tt.parse(string(b))
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Errorf("got %v, %v; want %v, err %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
