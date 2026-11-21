// Package stats holds the statistics of PROTOCOL.md §8 as pure functions: the Wilson interval of a solved rate,
// pass^k per task, the paired bootstrap of the gap between two servers, and Cohen's kappa.
package stats

import (
	"math"
	"math/rand/v2"
	"slices"
)

// Z95 is the standard normal quantile of a two-sided 95 % interval.
const Z95 = 1.959963984540054

// BootstrapDraws and BootstrapSeed are the draws and the seed of the paired bootstrap of the protocol.
const (
	BootstrapDraws = 10000
	BootstrapSeed  = 20261120
)

// Wilson returns the 95 % Wilson score interval of k successes out of n. It is (0, 0) when n is 0.
func Wilson(k, n int) (lo, hi float64) {
	if n <= 0 {
		return 0, 0
	}
	p, nf, z2 := float64(k)/float64(n), float64(n), Z95*Z95
	d := 1 + z2/nf
	c := (p + z2/(2*nf)) / d
	h := Z95 * math.Sqrt(p*(1-p)/nf+z2/(4*nf*nf)) / d
	return max(0, c-h), min(1, c+h)
}

// PassK is pass^k of a task solved c times out of n runs: the chance that k runs drawn without replacement from the
// n are all solved, C(c,k)/C(n,k) (Yao et al., τ-bench, 2024). With k = n it is 1 when every run solved the task, else
// 0. It is 0 when k is not between 1 and n.
func PassK(n, c, k int) float64 {
	if k < 1 || k > n || c < 0 || c > n {
		return 0
	}
	r := 1.0
	for i := range k { // C(c,k)/C(n,k) = Π (c-i)/(n-i)
		r *= float64(c-i) / float64(n-i)
	}
	return max(r, 0)
}

// Gap is the paired bootstrap estimate of the gap a - b.
type Gap struct {
	Mean   float64 // the observed mean of a - b over the tasks
	Lo, Hi float64 // the 95 % percentile interval of the bootstrap means
}

// Significant reports the protocol's rule: a gap is called a gap only if its interval excludes 0.
func (g Gap) Significant() bool { return g.Lo > 0 || g.Hi < 0 }

// PairedBootstrap returns the gap a - b, where a[i] and b[i] are two servers' values on task i (the solved rate of
// the task, for instance). Each of draws resamples the tasks with replacement, from a PCG seeded with seed, and keeps
// the mean of the differences; the interval is the 2.5th and 97.5th percentiles of those means (nearest rank). It
// panics when a and b differ in length, a caller bug.
func PairedBootstrap(a, b []float64, draws int, seed uint64) Gap {
	if len(a) != len(b) {
		panic("stats: paired bootstrap of slices of different lengths")
	}
	n := len(a)
	if n == 0 || draws < 1 {
		return Gap{}
	}
	d := make([]float64, n)
	var sum float64
	for i := range a {
		d[i] = a[i] - b[i]
		sum += d[i]
	}
	rng := rand.New(rand.NewPCG(seed, seed))
	means := make([]float64, draws)
	for r := range means {
		var s float64
		for range n {
			s += d[rng.IntN(n)]
		}
		means[r] = s / float64(n)
	}
	slices.Sort(means)
	rank := func(q float64) float64 { return means[min(draws-1, max(0, int(math.Ceil(q*float64(draws)))-1))] }
	return Gap{Mean: sum / float64(n), Lo: rank(0.025), Hi: rank(0.975)}
}

// Kappa returns Cohen's kappa of two raters' labels of the same items, a[i] and b[i] for item i: (po - pe) / (1 - pe).
// It is NaN when there is no item or when pe is 1 (both raters gave one and the same label to every item), where
// kappa is undefined. It panics when a and b differ in length, a caller bug.
func Kappa(a, b []string) float64 {
	if len(a) != len(b) {
		panic("stats: kappa of slices of different lengths")
	}
	n := float64(len(a))
	if n == 0 {
		return math.NaN()
	}
	ca, cb := map[string]float64{}, map[string]float64{}
	var agree float64
	for i := range a {
		ca[a[i]]++
		cb[b[i]]++
		if a[i] == b[i] {
			agree++
		}
	}
	var pe float64
	for l, x := range ca {
		pe += x / n * cb[l] / n
	}
	if pe >= 1 {
		return math.NaN()
	}
	return (agree/n - pe) / (1 - pe)
}
