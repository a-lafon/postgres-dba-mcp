package stats

import (
	"math"
	"testing"
)

func near(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

// The reference intervals are those of Newcombe, "Two-sided confidence intervals for the single proportion:
// comparison of seven methods", Statistics in Medicine 17 (1998), Table I, method 3 (Wilson score without continuity
// correction), to 4 decimals. 8/10 and 75/75 were computed by hand from the same formula.
func TestWilson(t *testing.T) {
	tests := []struct {
		k, n   int
		lo, hi float64
	}{
		{81, 263, 0.2553, 0.3662},
		{15, 148, 0.0624, 0.1605},
		{0, 20, 0, 0.1611},
		{1, 29, 0.0061, 0.1718},
		{8, 10, 0.4902, 0.9433},
		{75, 75, 0.9513, 1},
		{0, 0, 0, 0},
	}
	for _, tt := range tests {
		lo, hi := Wilson(tt.k, tt.n)
		if !near(lo, tt.lo, 5e-5) || !near(hi, tt.hi, 5e-5) {
			t.Errorf("Wilson(%d, %d) = (%.4f, %.4f), want (%.4f, %.4f)", tt.k, tt.n, lo, hi, tt.lo, tt.hi)
		}
	}
}

// The values are C(c,k)/C(n,k), computed by hand.
func TestPassK(t *testing.T) {
	tests := []struct {
		n, c, k int
		want    float64
	}{
		{5, 5, 5, 1},
		{5, 4, 5, 0},
		{5, 4, 2, 0.6}, // C(4,2)/C(5,2) = 6/10
		{5, 3, 1, 0.6}, // pass^1 is the solved rate
		{5, 2, 3, 0},   // fewer solved runs than k
		{5, 0, 1, 0},
		{5, 5, 0, 0},
		{5, 5, 6, 0},
	}
	for _, tt := range tests {
		if got := PassK(tt.n, tt.c, tt.k); !near(got, tt.want, 1e-12) {
			t.Errorf("PassK(%d, %d, %d) = %v, want %v", tt.n, tt.c, tt.k, got, tt.want)
		}
	}
}

func TestPairedBootstrap(t *testing.T) {
	// Differences {1, 0, 0, 0}: a bootstrap mean is X/4 with X ~ Binomial(4, 1/4). P(X = 0) = 81/256 = 0.316 > 0.025,
	// so the 2.5th percentile is 0; P(X <= 2) = 243/256 = 0.949 < 0.975 <= P(X <= 3) = 255/256, so the 97.5th is 3/4.
	// Both margins are over 10 standard errors of a 10,000-draw estimate.
	g := PairedBootstrap([]float64{1, 0, 0, 0}, []float64{0, 0, 0, 0}, BootstrapDraws, BootstrapSeed)
	if g.Mean != 0.25 || g.Lo != 0 || g.Hi != 0.75 || g.Significant() {
		t.Errorf("gap = %+v, want mean 0.25, interval [0, 0.75], not significant", g)
	}
	if again := PairedBootstrap([]float64{1, 0, 0, 0}, []float64{0, 0, 0, 0}, BootstrapDraws, BootstrapSeed); again != g {
		t.Errorf("same seed, different result: %+v and %+v", g, again)
	}
	// Every task solved by a and failed by b: every draw is 1.
	g = PairedBootstrap([]float64{1, 1, 1}, []float64{0, 0, 0}, BootstrapDraws, BootstrapSeed)
	if g.Lo != 1 || g.Hi != 1 || !g.Significant() {
		t.Errorf("gap = %+v, want [1, 1] and significant", g)
	}
	// b better everywhere: a negative gap is a gap too.
	g = PairedBootstrap([]float64{0, 0.2, 0.4}, []float64{0.6, 0.8, 1}, BootstrapDraws, BootstrapSeed)
	if !near(g.Mean, -0.6, 1e-12) || !near(g.Lo, g.Hi, 1e-12) || !g.Significant() {
		t.Errorf("gap = %+v, want -0.6 with a zero-width interval", g)
	}
	if g := PairedBootstrap(nil, nil, BootstrapDraws, BootstrapSeed); g != (Gap{}) {
		t.Errorf("empty gap = %+v", g)
	}
}

func TestKappa(t *testing.T) {
	rep := func(s string, n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = s
		}
		return out
	}
	cat := func(parts ...[]string) []string {
		var out []string
		for _, p := range parts {
			out = append(out, p...)
		}
		return out
	}
	// Wikipedia, "Cohen's kappa", worked example: 50 items, yes/yes 20, yes/no 5, no/yes 10, no/no 15; kappa = 0.4.
	a := cat(rep("yes", 20), rep("yes", 5), rep("no", 10), rep("no", 15))
	b := cat(rep("yes", 20), rep("no", 5), rep("yes", 10), rep("no", 15))
	if k := Kappa(a, b); !near(k, 0.4, 1e-12) {
		t.Errorf("Wikipedia example: kappa = %v, want 0.4", k)
	}
	// By hand, three labels: po = 4/6, pe = (3*2 + 2*2 + 1*2)/36 = 1/3, kappa = (2/3 - 1/3)/(2/3) = 0.5.
	a = []string{"present", "present", "present", "absent", "absent", "contradicted"}
	b = []string{"present", "present", "absent", "absent", "contradicted", "contradicted"}
	if k := Kappa(a, b); !near(k, 0.5, 1e-12) {
		t.Errorf("three labels: kappa = %v, want 0.5", k)
	}
	if k := Kappa(a, a); k != 1 {
		t.Errorf("perfect agreement: kappa = %v, want 1", k)
	}
	if k := Kappa(rep("present", 4), rep("present", 4)); !math.IsNaN(k) {
		t.Errorf("one label everywhere: kappa = %v, want NaN", k)
	}
	if k := Kappa(nil, nil); !math.IsNaN(k) {
		t.Errorf("no item: kappa = %v, want NaN", k)
	}
}
