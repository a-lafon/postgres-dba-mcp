package format

import "testing"

func TestSummarizeVector(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		ok             bool
	}{
		{"one", `[1]`, "[vector: 1 dim]", true},
		{"three", `[0.019487105,-0.02782618,3e-05]`, "[vector: 3 dims]", true},
		{"signs and exponents", `[+1,-1,1E+5,1e-5,.5]`, "[vector: 5 dims]", true},
		{"empty", `[]`, "", false},
		{"missing element", `[1,,2]`, "", false},
		{"trailing comma", `[1,2,]`, "", false},
		{"leading comma", `[,1]`, "", false},
		{"spaced like json", `[1, 2]`, "", false},
		{"not bracketed", `1,2,3`, "", false},
		{"postgres array", `{1,2,3}`, "", false},
		{"text inside", `["a","b"]`, "", false},
		{"too short", `[`, "", false},
		{"empty string", ``, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SummarizeVector(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("SummarizeVector(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
