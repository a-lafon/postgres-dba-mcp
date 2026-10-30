// Package report turns the JSON of a bench run into bench/report.md: one table per level, the targets met or not,
// and the go/no-go verdict.
package report

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
)

// Met values of a criterion.
const (
	Yes        = "yes"
	No         = "no"
	Unverified = "unverified"
)

// Criterion is one target line.
type Criterion struct {
	Key    string // stable id used by the verdict rules
	Name   string
	Target string
	Result string
	Met    string
	// Num is the measured value behind Result when there is one: a change as a fraction for KeyN3Tokens, the
	// dba/crystal ratio for KeyN2Tokens.
	Num    float64
	HasNum bool
}

// Criterion keys read by the verdict.
const (
	KeySecurity = "security"
	KeyN2Tokens = "n2-tokens"
	KeyDetect   = "n2-detection"
	KeyN3Rate   = "n3-success"
	KeyN3Tokens = "n3-tokens"
	KeyN3Turns  = "n3-turns"
)

// Verdicts.
const (
	NoGo         = "NO-GO"
	Go           = "GO"
	Iterate      = "ITERATE"
	Inconclusive = "INCONCLUSIVE"
)

// IterateCause is the line added to an ITERATE verdict.
const IterateCause = "Likely cause: truncation too aggressive or output too terse; adjust --max-rows or formats and rerun."

// Verdict is the go/no-go decision with the lines that explain it.
type Verdict struct {
	Name  string
	Lines []string
}

// Decide applies the verdict rules to the criteria:
//   - NO-GO when the security target is not met;
//   - GO when N3 success, N3 tokens, N3 turns and N2 trap detection are met and no criterion is unverified;
//   - ITERATE when tokens decreased but N3 success, N3 turns or N2 detection fell short;
//   - INCONCLUSIVE otherwise, listing the unverified criteria.
//
// N1 is not an input.
func Decide(cs []Criterion) Verdict {
	by := map[string]Criterion{}
	var unverified []string
	for _, c := range cs {
		by[c.Key] = c
		if c.Met == Unverified {
			unverified = append(unverified, c.Name)
		}
	}
	met := func(k string) bool { return by[k].Met == Yes }
	switch {
	case by[KeySecurity].Met == No:
		return Verdict{Name: NoGo}
	case met(KeyN3Rate) && met(KeyN3Tokens) && met(KeyN3Turns) && met(KeyDetect) && len(unverified) == 0:
		return Verdict{Name: Go}
	}
	var tokensDown bool
	switch n3, n2 := by[KeyN3Tokens], by[KeyN2Tokens]; {
	case n3.HasNum:
		tokensDown = n3.Num < 0
	case n2.HasNum:
		tokensDown = n2.Num < 1
	}
	short := by[KeyN3Rate].Met == No || by[KeyN3Turns].Met == No || by[KeyDetect].Met == No
	if tokensDown && short {
		return Verdict{Name: Iterate, Lines: []string{IterateCause}}
	}
	sort.Strings(unverified)
	v := Verdict{Name: Inconclusive}
	if len(unverified) > 0 {
		v.Lines = []string{"Unverified: " + strings.Join(unverified, "; ")}
	} else {
		v.Lines = []string{"Targets are mixed but tokens did not decrease."}
	}
	return v
}

// median of a list of ints, as a float.
func medianInts(v []int) float64 {
	f := make([]float64, len(v))
	for i, n := range v {
		f[i] = float64(n)
	}
	return scenarios.Median(f)
}

func pct(f float64) string { return fmt.Sprintf("%.1f%%", f*100) }

func pctChange(f float64) string {
	s := fmt.Sprintf("%+.1f%%", f*100)
	if s == "+0.0%" || s == "-0.0%" {
		return "0.0%"
	}
	return s
}

func changeOf(a, b float64) (float64, bool) {
	if b == 0 || math.IsNaN(a) || math.IsNaN(b) {
		return 0, false
	}
	return (a - b) / b, true
}
