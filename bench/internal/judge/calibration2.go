package judge

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"math"
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/stats"
)

// Calibration of the fact-list judge (PROTOCOL.md §7): the maintainer grades a blind sample of answers on the
// judges' grid, and Cohen's kappa says how far each judge agrees with them.

// CalibrationSize is the number of answers on the sheet, and KappaFloor the kappa under which a judge's measures are
// not published.
const (
	CalibrationSize = 40
	KappaFloor      = 0.6
)

// Rows of the grid that are not facts.
const (
	RowDoubt       = "doubt"
	RowUnsupported = "unsupported"
)

// KeyAnswer says which answer is number N of the sheet.
type KeyAnswer struct {
	N      int    `json:"n"`
	Task   string `json:"task"`
	Server string `json:"server"`
	Run    int    `json:"run"`
}

// Key2 is calibration-key.json of the fact-list judge.
type Key2 struct {
	Seed    uint64      `json:"seed"`
	Answers []KeyAnswer `json:"answers"`
}

// SampleCalibration draws up to n of the graded answers, stratified by task and server: the tasks take turns, each
// turn takes the next server of the task in a seeded order, and that server's next run in a seeded order. Every task
// is drawn about as often, and within a task every server before any server twice.
func SampleCalibration(answers []results.GradedAnswer, n int, seed uint64) []KeyAnswer {
	rng := rand.New(rand.NewPCG(seed, seed))
	strata := map[string]map[string][]results.GradedAnswer{} // task -> server -> answers
	for _, a := range answers {
		if len(a.Grades) == 0 {
			continue
		}
		if strata[a.Task] == nil {
			strata[a.Task] = map[string][]results.GradedAnswer{}
		}
		strata[a.Task][a.Server] = append(strata[a.Task][a.Server], a)
	}
	shuffle := func(s []string) { rng.Shuffle(len(s), func(i, j int) { s[i], s[j] = s[j], s[i] }) }
	// Map order is random: every seeded shuffle starts from a sorted order.
	tasks := slices.Sorted(maps.Keys(strata))
	shuffle(tasks)
	servers := map[string][]string{}
	for _, t := range tasks {
		servers[t] = slices.Sorted(maps.Keys(strata[t]))
		for _, s := range servers[t] {
			runs := strata[t][s]
			slices.SortFunc(runs, func(a, b results.GradedAnswer) int { return a.Run - b.Run })
			rng.Shuffle(len(runs), func(i, j int) { runs[i], runs[j] = runs[j], runs[i] })
		}
		shuffle(servers[t])
	}
	used := map[string]map[string]int{}
	var out []KeyAnswer
	for round := 0; len(out) < n; round++ {
		before := len(out)
		for _, t := range tasks {
			if len(out) == n {
				break
			}
			// The round's server for t is the next one, in order, that still has an answer left.
			ss := servers[t]
			for k := range ss {
				s := ss[(round+k)%len(ss)]
				if used[t] == nil {
					used[t] = map[string]int{}
				}
				if used[t][s] < len(strata[t][s]) {
					a := strata[t][s][used[t][s]]
					used[t][s]++
					out = append(out, KeyAnswer{N: len(out) + 1, Task: a.Task, Server: a.Server, Run: a.Run})
					break
				}
			}
		}
		if len(out) == before {
			break
		}
	}
	return out
}

func itemKey(task, server string, run int) string { return fmt.Sprintf("%s/%s/%d", task, server, run) }

// CalibrationSheet writes the blind sheet of the sampled answers, as Markdown to read, and the grid to fill, as CSV
// with the columns answer, task, row, statement, label: one row per expected fact, one for the doubt and one for the
// unsupported claims, in the judges' grid. The sheet shows neither the server nor the judges' grades.
func CalibrationSheet(items []Item, key []KeyAnswer, md, grid io.Writer) error {
	byKey := map[string]Item{}
	for _, it := range items {
		byKey[itemKey(it.Task, it.Server, it.Run)] = it
	}
	if _, err := fmt.Fprint(md, "# Calibration sheet\n\nFor each answer, read the question, the expected facts and the answer, then fill the `label` column of "+
		"calibration2.csv: for each fact `present`, `absent` or `contradicted`, with the definitions of the judge's prompt (internal/judge/prompt-v2.md); "+
		"for `doubt`, `yes` when the answer signals a doubt or a limit, else `no`; for `unsupported`, the figures or objects the facts do not support, "+
		"separated by `;`, or leave it empty. Tool and server names are replaced by [tool] and [server].\n"); err != nil {
		return err
	}
	w := csv.NewWriter(grid)
	_ = w.Write([]string{"answer", "task", "row", "statement", "label"})
	for _, k := range key {
		it, ok := byKey[itemKey(k.Task, k.Server, k.Run)]
		if !ok {
			return fmt.Errorf("calibration: no input for %s %s run %d", k.Task, k.Server, k.Run)
		}
		var facts strings.Builder
		for _, f := range it.Facts {
			fmt.Fprintf(&facts, "- %s: %s", f.ID, f.Statement)
			if f.Recognize != "" {
				fmt.Fprintf(&facts, " How to recognize it: %s", f.Recognize)
			}
			facts.WriteString("\n")
			_ = w.Write([]string{strconv.Itoa(k.N), it.Task, f.ID, f.Statement, ""})
		}
		_ = w.Write([]string{strconv.Itoa(k.N), it.Task, RowDoubt, "Does the answer signal a doubt or a limit? yes or no", ""})
		_ = w.Write([]string{strconv.Itoa(k.N), it.Task, RowUnsupported, "Figures or objects the facts do not support, separated by ;", ""})
		wrong := "none listed"
		if len(it.Wrong) > 0 {
			wrong = "\n- " + strings.Join(it.Wrong, "\n- ")
		}
		if _, err := fmt.Fprintf(md, "\n## Answer %d (%s)\n\n**Question:** %s\n\n**Expected facts:**\n\n%s\n**Claims that would be wrong:** %s\n\n~~~~~~\n%s\n~~~~~~\n",
			k.N, it.Task, it.Question, facts.String(), wrong, Blind(it.Answer)); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// ReadKey2 decodes calibration-key.json of the fact-list judge.
func ReadKey2(b []byte) (Key2, error) {
	var k Key2
	if err := json.Unmarshal(b, &k); err != nil {
		return k, fmt.Errorf("decode calibration key: %w", err)
	}
	return k, nil
}

// humanLabel normalizes a label of the filled grid; "" means not filled.
func humanLabel(row, v string) (string, error) {
	v = strings.ToLower(strings.TrimSpace(v))
	if v == "" {
		return "", nil
	}
	if row == RowDoubt {
		switch v {
		case "yes", "y":
			return results.DoubtYes, nil
		case "no", "n":
			return results.DoubtNo, nil
		}
		return "", fmt.Errorf("doubt %q is not yes or no", v)
	}
	for _, l := range []string{results.FactPresent, results.FactAbsent, results.FactContradicted} {
		if v == l || v == l[:1] {
			return l, nil
		}
	}
	return "", fmt.Errorf("label %q is not present, absent or contradicted", v)
}

// kappaPtr is a kappa for JSON, nil when undefined.
func kappaPtr(k float64) *float64 {
	if math.IsNaN(k) {
		return nil
	}
	return &k
}

// Calibrate reads the filled grid and compares the maintainer's labels with each judge's: Cohen's kappa on the facts
// and on the doubt, per judge; the kappa between the judges on the facts of every graded answer; and whether each
// judge's measures may be published. An empty label is not filled and is left out.
func Calibrate(grid io.Reader, key Key2, judged results.Judge2) (results.Calibration, error) {
	recs, err := csv.NewReader(grid).ReadAll()
	if err != nil {
		return results.Calibration{}, fmt.Errorf("read calibration grid: %w", err)
	}
	if len(recs) == 0 || !slices.Equal(recs[0], []string{"answer", "task", "row", "statement", "label"}) {
		return results.Calibration{}, errors.New("calibration grid lacks its header answer,task,row,statement,label")
	}
	byN := map[int]KeyAnswer{}
	for _, k := range key.Answers {
		byN[k.N] = k
	}
	graded := map[string]results.GradedAnswer{}
	for _, a := range judged.Answers {
		graded[itemKey(a.Task, a.Server, a.Run)] = a
	}
	type pairs struct{ human, judge []string }
	facts, doubts := map[string]*pairs{}, map[string]*pairs{}
	for _, m := range judged.Judges {
		facts[m], doubts[m] = &pairs{}, &pairs{}
	}
	cal := results.Calibration{Sheet: len(key.Answers), FactKappa: map[string]*float64{}, DoubtKappa: map[string]*float64{}, Published: map[string]bool{}}
	for i, r := range recs[1:] {
		n, err := strconv.Atoi(r[0])
		k, ok := byN[n]
		if err != nil || !ok {
			return cal, fmt.Errorf("calibration grid line %d: answer %q is not on the key", i+2, r[0])
		}
		row := r[2]
		if row == RowUnsupported {
			continue // free text, kept in the published sheet only
		}
		h, err := humanLabel(row, r[4])
		if err != nil {
			return cal, fmt.Errorf("calibration grid line %d: %w", i+2, err)
		}
		if h == "" {
			continue
		}
		a, ok := graded[itemKey(k.Task, k.Server, k.Run)]
		if !ok {
			return cal, fmt.Errorf("calibration: answer %d (%s %s run %d) has no grade in judge2.json", n, k.Task, k.Server, k.Run)
		}
		if row != RowDoubt {
			cal.Filled++
		}
		for gi, g := range a.Grades {
			m := judged.Judges[gi]
			if row == RowDoubt {
				doubts[m].human = append(doubts[m].human, h)
				doubts[m].judge = append(doubts[m].judge, map[bool]string{true: results.DoubtYes, false: results.DoubtNo}[g.Doubt])
				continue
			}
			fi := slices.IndexFunc(g.Facts, func(f results.FactGrade) bool { return f.ID == row })
			if fi < 0 {
				return cal, fmt.Errorf("calibration grid line %d: fact %q is not in the grade of answer %d", i+2, row, n)
			}
			facts[m].human = append(facts[m].human, h)
			facts[m].judge = append(facts[m].judge, g.Facts[fi].Label)
		}
	}
	for _, m := range judged.Judges {
		cal.FactKappa[m] = kappaPtr(stats.Kappa(facts[m].human, facts[m].judge))
		cal.DoubtKappa[m] = kappaPtr(stats.Kappa(doubts[m].human, doubts[m].judge))
		cal.Published[m] = cal.FactKappa[m] != nil && *cal.FactKappa[m] >= KappaFloor
	}
	if len(judged.Judges) == 2 {
		var x, y []string
		for _, a := range judged.Answers {
			if len(a.Grades) != 2 {
				continue
			}
			for i, f := range a.Grades[0].Facts {
				if i < len(a.Grades[1].Facts) {
					x, y = append(x, f.Label), append(y, a.Grades[1].Facts[i].Label)
				}
			}
		}
		cal.JudgesFactKappa = kappaPtr(stats.Kappa(x, y))
	}
	return cal, nil
}
