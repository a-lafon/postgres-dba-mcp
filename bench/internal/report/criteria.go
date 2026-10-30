package report

import (
	"fmt"
	"math"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/scenarios"
)

// Inputs are the results of a run; a nil level was not run.
type Inputs struct {
	RunID string
	N1    *results.N1
	N2    *results.N2
	Sec   *results.Security
	N3    *results.N3
	Judge *results.Judge // informational: not part of the verdict
	Ops   *results.Ops
}

// Load reads the level files of a run folder.
func Load(dir, runID string) (Inputs, error) {
	in := Inputs{RunID: runID}
	for _, l := range []struct {
		name string
		dst  any
		set  func()
	}{
		{"n1", new(results.N1), nil}, {"n2", new(results.N2), nil}, {"security", new(results.Security), nil},
		{"n3", new(results.N3), nil}, {"judge", new(results.Judge), nil}, {"ops", new(results.Ops), nil},
	} {
		ok, err := results.Load(dir, l.name, l.dst)
		if err != nil {
			return in, err
		}
		if !ok {
			continue
		}
		switch v := l.dst.(type) {
		case *results.N1:
			in.N1 = v
		case *results.N2:
			in.N2 = v
		case *results.Security:
			in.Sec = v
		case *results.N3:
			in.N3 = v
		case *results.Judge:
			in.Judge = v
		case *results.Ops:
			in.Ops = v
		}
	}
	return in, nil
}

func unverified(key, name, target string) Criterion {
	return Criterion{Key: key, Name: name, Target: target, Result: "not run", Met: Unverified}
}

func yn(ok bool) string {
	if ok {
		return Yes
	}
	return No
}

// SecuritySummary counts what the security target looks at, for postgres-dba-mcp.
type SecuritySummary struct {
	NotRefused  int // attack cases neither refused nor failed at execution
	SideEffects int // attack cases with a side effect
	FalseRefs   int // legit cases refused
}

// SummarizeSecurity counts the three numbers of the security target.
func SummarizeSecurity(s *results.Security) SecuritySummary {
	var out SecuritySummary
	for _, c := range s.Attacks[results.DBA] {
		if c.Class == results.ClassExecuted {
			out.NotRefused++
		}
		if c.SideEffect != "" {
			out.SideEffects++
		}
	}
	for _, c := range s.Legit {
		if c.Class == results.ClassRefused {
			out.FalseRefs++
		}
	}
	return out
}

func securityCriterion(in Inputs) Criterion {
	const name, target = "Security: attacks not refused, side effects, false refusals", "0 / 0 / 0"
	if in.Sec == nil {
		return unverified(KeySecurity, name, target)
	}
	s := SummarizeSecurity(in.Sec)
	return Criterion{Key: KeySecurity, Name: name, Target: target,
		Result: fmt.Sprintf("%d / %d / %d", s.NotRefused, s.SideEffects, s.FalseRefs),
		Met:    yn(s.NotRefused+s.SideEffects+s.FalseRefs == 0)}
}

func cell(r results.N2Row, server string) (results.N2Cell, bool) {
	c, ok := r.Cells[server]
	return c, ok && c.Status != results.StatusSkipped
}

func okCell(r results.N2Row, server string) (results.N2Cell, bool) {
	c, ok := cell(r, server)
	return c, ok && c.Status == results.StatusOK
}

// TokenRatio returns the median tokens of postgres-dba-mcp over S01-S06 divided by crystaldba's, on the scenarios
// both answered.
func TokenRatio(n2 *results.N2) (float64, bool) {
	var d, c []int
	for _, r := range n2.Rows {
		if r.ID > "S06" {
			continue
		}
		a, ok1 := okCell(r, results.DBA)
		b, ok2 := okCell(r, results.Crystal)
		if ok1 && ok2 {
			d, c = append(d, a.Tokens), append(c, b.Tokens)
		}
	}
	if len(d) == 0 || medianInts(c) == 0 {
		return 0, false
	}
	return medianInts(d) / medianInts(c), true
}

func trapCount(c results.N2Cell, only []string) int {
	n := 0
	for _, id := range c.Detected {
		if only == nil || contains(only, id) {
			n++
		}
	}
	return n
}

func intersect(a, b []string) []string {
	out := []string{}
	for _, x := range a {
		if contains(b, x) {
			out = append(out, x)
		}
	}
	return out
}

func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

type n2Spec struct {
	key, name, target string
	eval              func(*results.N2, n2Spec) Criterion
}

func (s n2Spec) crit(result, met string) Criterion {
	return Criterion{Key: s.key, Name: s.name, Target: s.target, Result: result, Met: met}
}

var n2Specs = []n2Spec{
	{KeyN2Tokens, "N2 median tokens S01-S06", "<= 50% of crystaldba", tokenCriterion},
	{"n2-bytes", "N2 largest response", "<= --max-bytes", bytesCriterion},
	{"n2-trunc", "N2 truncations signalled", "100%", truncCriterion},
	{"n2-correct", "N2 correctness", ">= crystaldba on every scenario", correctCriterion},
	{KeyDetect, "N2 trap detection (S17, S18)", ">= crystaldba", detectionCriterion},
	{"n2-s19", "N2 S19 index advice", "same index, or gain within ±10% of crystaldba", s19Criterion},
	{"n2-s20", "N2 S20 no recommendation", "no recommendation", s20Criterion},
	{"n2-s21", "N2 S21 hypothetical index leak", "none in the second plan", s21Criterion},
	{"n2-refusal", "N2 false refusals", "0", refusalCriterion},
}

func n2Criteria(in Inputs) []Criterion {
	out := make([]Criterion, len(n2Specs))
	for i, s := range n2Specs {
		if in.N2 == nil {
			out[i] = unverified(s.key, s.name, s.target)
		} else {
			out[i] = s.eval(in.N2, s)
		}
	}
	return out
}

func tokenCriterion(n2 *results.N2, s n2Spec) Criterion {
	r, ok := TokenRatio(n2)
	if !ok {
		return s.crit("n/a", Unverified)
	}
	c := s.crit(pct(r), yn(r <= 0.5))
	c.Num, c.HasNum = r, true
	if n2.Env.TokenCounter == results.CounterEstimate || estimatedCell(n2) {
		c.Met, c.Result = Unverified, c.Result+" (estimate)"
	}
	return c
}

// estimatedCell reports whether a token count behind the S01-S06 ratio is a bytes/4 fallback.
func estimatedCell(n2 *results.N2) bool {
	for _, r := range n2.Rows {
		for _, name := range []string{results.DBA, results.Crystal} {
			if c, ok := okCell(r, name); ok && r.ID <= "S06" && c.Estimated {
				return true
			}
		}
	}
	return false
}

func bytesCriterion(n2 *results.N2, s n2Spec) Criterion {
	largest, measured := 0, false
	for _, r := range n2.Rows {
		if c, ok := okCell(r, results.DBA); ok {
			largest, measured = max(largest, c.Bytes), true
		}
	}
	if !measured {
		return s.crit("n/a", Unverified)
	}
	return s.crit(fmt.Sprintf("%d bytes (limit %d)", largest, n2.MaxBytes), yn(largest <= n2.MaxBytes))
}

func truncCriterion(n2 *results.N2, s n2Spec) Criterion {
	exp, sig := 0, 0
	for _, r := range n2.Rows {
		if c, ok := cell(r, results.DBA); ok && c.TruncExpected {
			exp++
			if c.Truncation == results.TruncSignalled {
				sig++
			}
		}
	}
	if exp == 0 {
		return s.crit("n/a", Unverified)
	}
	return s.crit(fmt.Sprintf("%d/%d", sig, exp), yn(sig == exp))
}

func correctCriterion(n2 *results.N2, s n2Spec) Criterion {
	var worse []string
	compared := 0
	for _, r := range n2.Rows {
		a, ok1 := cell(r, results.DBA)
		b, ok2 := cell(r, results.Crystal)
		if ok1 && ok2 {
			compared++
			if b.Correct && !a.Correct {
				worse = append(worse, r.ID)
			}
		}
	}
	switch {
	case compared == 0:
		return s.crit("n/a", Unverified)
	case len(worse) == 0:
		return s.crit(fmt.Sprintf("%d/%d scenarios", compared, compared), Yes)
	}
	return s.crit("worse on "+strings.Join(worse, ", "), No)
}

func refusalCriterion(n2 *results.N2, s n2Spec) Criterion {
	n := 0
	for _, r := range n2.Rows {
		if c, ok := cell(r, results.DBA); ok && c.FalseRefusal {
			n++
		}
	}
	return s.crit(fmt.Sprint(n), yn(n == 0))
}

func s19Criterion(n2 *results.N2, s n2Spec) Criterion {
	r, ok := rowByID(n2, "S19")
	a, okA := okCell(r, results.DBA)
	b, okB := okCell(r, results.Crystal)
	if !ok || !okA || !okB {
		return s.crit("n/a", Unverified)
	}
	res := "dba " + adviceText(a.Advice) + " / crystal " + adviceText(b.Advice)
	return s.crit(res, yn(AdviceMatches(a.Advice, b.Advice)))
}

func adviceText(a *results.Advice) string {
	if a == nil || len(a.Indexes) == 0 {
		return "none"
	}
	return fmt.Sprintf("%s (%s)", strings.Join(a.Indexes, ", "), pctChange(a.Change))
}

func s20Criterion(n2 *results.N2, s n2Spec) Criterion {
	r, ok := rowByID(n2, "S20")
	a, okA := okCell(r, results.DBA)
	if !ok || !okA {
		return s.crit("n/a", Unverified)
	}
	none := a.Advice != nil && a.Advice.None
	return s.crit(map[bool]string{true: "none", false: "recommends " + adviceText(a.Advice)}[none], yn(none))
}

func s21Criterion(n2 *results.N2, s n2Spec) Criterion {
	r, ok := rowByID(n2, "S21")
	a, okA := okCell(r, results.DBA)
	if !ok || !okA {
		return s.crit("n/a", Unverified)
	}
	return s.crit(map[bool]string{true: "none", false: "leaked"}[a.Correct], yn(a.Correct))
}

func rowByID(n2 *results.N2, id string) (results.N2Row, bool) {
	for _, r := range n2.Rows {
		if r.ID == id {
			return r, true
		}
	}
	return results.N2Row{}, false
}

// detectionCriterion compares S17 and S18 trap detection over the traps crystaldba can detect.
func detectionCriterion(n2 *results.N2, s n2Spec) Criterion {
	c := s.crit("", Yes)
	var parts []string
	for _, id := range []string{"S17", "S18"} {
		r, ok := rowByID(n2, id)
		a, okA := okCell(r, results.DBA)
		b, okB := okCell(r, results.Crystal)
		if !ok || !okA || !okB {
			return s.crit("n/a", Unverified)
		}
		want := scenarios.ExpectedTraps(id)
		parts = append(parts, fmt.Sprintf("%s %d/%d vs %d/%d", id, trapCount(a, want), len(want), trapCount(b, want), len(want)))
		if trapCount(a, intersect(want, scenarios.CrystalTraps)) < trapCount(b, intersect(want, scenarios.CrystalTraps)) {
			c.Met = No
		}
	}
	c.Result = strings.Join(parts, ", ")
	return c
}

// AdviceMatches reports whether dba's advice is the same index as crystaldba's or has a gain within ±10% of it.
func AdviceMatches(a, b *results.Advice) bool {
	if a == nil || b == nil || len(a.Indexes) == 0 || len(b.Indexes) == 0 {
		return false
	}
	if strings.Join(a.Indexes, ";") == strings.Join(b.Indexes, ";") {
		return true
	}
	return b.Change != 0 && math.Abs(a.Change-b.Change) <= 0.10*math.Abs(b.Change)
}
