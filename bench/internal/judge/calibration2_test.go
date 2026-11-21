package judge

import (
	"bytes"
	"encoding/csv"
	"fmt"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

var calServers = []string{"dba", "crystal", "dbhub", "toolbox", "reference"}

func TestSampleCalibration(t *testing.T) {
	var answers []results.GradedAnswer
	for task := 1; task <= 15; task++ {
		for _, s := range calServers {
			for run := 1; run <= 5; run++ {
				answers = append(answers, results.GradedAnswer{Task: fmt.Sprintf("A%02d", task), Server: s, Run: run, Grades: []results.JudgeGrade{{}}})
			}
		}
	}
	key := SampleCalibration(answers, CalibrationSize, CalibrationSeed)
	if len(key) != CalibrationSize {
		t.Fatalf("drew %d answers, want %d", len(key), CalibrationSize)
	}
	perTask, strata, seen := map[string]int{}, map[string]bool{}, map[string]bool{}
	for i, k := range key {
		if k.N != i+1 {
			t.Errorf("answer %d numbered %d", i+1, k.N)
		}
		id := itemKey(k.Task, k.Server, k.Run)
		if seen[id] {
			t.Errorf("%s drawn twice", id)
		}
		seen[id] = true
		perTask[k.Task]++
		strata[k.Task+"/"+k.Server] = true
	}
	// 40 over 15 tasks: 10 tasks three times and 5 twice; and no stratum twice.
	for task, n := range perTask {
		if n < 2 || n > 3 {
			t.Errorf("task %s drawn %d times, want 2 or 3", task, n)
		}
	}
	if len(perTask) != 15 || len(strata) != CalibrationSize {
		t.Errorf("%d tasks and %d strata, want 15 and %d", len(perTask), len(strata), CalibrationSize)
	}
	if again := SampleCalibration(answers, CalibrationSize, CalibrationSeed); fmt.Sprint(again) != fmt.Sprint(key) {
		t.Error("same seed, different sample")
	}
	if other := SampleCalibration(answers, CalibrationSize, CalibrationSeed+1); fmt.Sprint(other) == fmt.Sprint(key) {
		t.Error("another seed, same sample")
	}
	// Fewer answers than asked: every one, once.
	if small := SampleCalibration(answers[:7], CalibrationSize, CalibrationSeed); len(small) != 7 {
		t.Errorf("drew %d of 7", len(small))
	}
}

func calFixture() ([]Item, results.Judge2) {
	P, A, C := results.FactPresent, results.FactAbsent, results.FactContradicted
	facts := []Fact{{ID: "F1", Statement: "one"}, {ID: "F2", Statement: "two"}, {ID: "F3", Statement: "three"}}
	items := []Item{
		{Task: "A01", Server: "dba", Run: 1, Question: "q1", Answer: "postgres-dba-mcp says one", Facts: facts},
		{Task: "A02", Server: "crystal", Run: 2, Question: "q2", Answer: "crystaldba says two", Facts: facts, Wrong: []string{"four"}},
	}
	judged := results.Judge2{Judges: []string{"opus", "sonnet"}, Answers: []results.GradedAnswer{
		{Task: "A01", Server: "dba", Run: 1, Grades: []results.JudgeGrade{grade(false, P, P, P), grade(false, P, P, A)}},
		{Task: "A02", Server: "crystal", Run: 2, Grades: []results.JudgeGrade{grade(true, A, A, C), grade(false, A, C, C)}},
	}}
	return items, judged
}

func TestCalibrationRoundTrip(t *testing.T) {
	items, judged := calFixture()
	key := Key2{Seed: CalibrationSeed, Answers: SampleCalibration(judged.Answers, CalibrationSize, CalibrationSeed)}
	var md, grid bytes.Buffer
	if err := CalibrationSheet(items, key.Answers, &md, &grid); err != nil {
		t.Fatal(err)
	}
	for _, leak := range []string{"postgres-dba-mcp", "crystaldba", "opus", "sonnet", "dba", "crystal"} {
		if strings.Contains(md.String()+grid.String(), leak) {
			t.Errorf("sheet leaks %q", leak)
		}
	}
	empty := grid.String()
	recs, err := csv.NewReader(&grid).ReadAll()
	if err != nil || len(recs) != 1+2*(3+2) {
		t.Fatalf("grid has %d rows (err %v), want 11", len(recs), err)
	}
	// The maintainer agrees with opus on every fact and with sonnet on 4 of 6: present, present, present, absent,
	// absent, contradicted against sonnet's present, present, absent, absent, contradicted, contradicted. The kappa of
	// that pair is 0.5 (stats.TestKappa, three labels). Opus and sonnet are the same pair, so their kappa is 0.5 too.
	// On the doubt, the maintainer says no and yes, like opus.
	filled := map[string]string{"A01/F1": "present", "A01/F2": "P", "A01/F3": "present", "A02/F1": "absent", "A02/F2": "a", "A02/F3": "Contradicted",
		"A01/doubt": "no", "A02/doubt": "yes", "A02/unsupported": "anything; at all"}
	var out bytes.Buffer
	w := csv.NewWriter(&out)
	for i, r := range recs {
		if i > 0 {
			r[4] = filled[r[1]+"/"+r[2]]
		}
		_ = w.Write(r)
	}
	w.Flush()
	cal, err := Calibrate(bytes.NewReader(out.Bytes()), key, judged)
	if err != nil {
		t.Fatal(err)
	}
	k := func(p *float64) string {
		if p == nil {
			return "nil"
		}
		return fmt.Sprintf("%.3f", *p)
	}
	if cal.Sheet != 2 || cal.Filled != 6 || k(cal.FactKappa["opus"]) != "1.000" || k(cal.FactKappa["sonnet"]) != "0.500" ||
		k(cal.JudgesFactKappa) != "0.500" || k(cal.DoubtKappa["opus"]) != "1.000" || k(cal.DoubtKappa["sonnet"]) != "0.000" ||
		!cal.Published["opus"] || cal.Published["sonnet"] {
		t.Errorf("calibration = %+v, fact kappa opus %s sonnet %s, judges %s, doubt opus %s sonnet %s", cal,
			k(cal.FactKappa["opus"]), k(cal.FactKappa["sonnet"]), k(cal.JudgesFactKappa), k(cal.DoubtKappa["opus"]), k(cal.DoubtKappa["sonnet"]))
	}

	// An unfilled sheet has no kappa and publishes nothing; a bad label is an error.
	cal, err = Calibrate(strings.NewReader(empty), key, judged)
	if err != nil || (cal.Filled != 0 || cal.FactKappa["opus"] != nil || cal.Published["opus"]) {
		t.Errorf("empty sheet: %+v", cal)
	}
	bad := strings.Replace(out.String(), ",present\n", ",maybe\n", 1)
	if _, err := Calibrate(strings.NewReader(bad), key, judged); err == nil {
		t.Error("accepted the label maybe")
	}
}
