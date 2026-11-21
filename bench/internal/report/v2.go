package report

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/judge"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/stats"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/tasks"
)

// The report of protocol v2 (PROTOCOL.md): pure rendering of the stored JSON of parts A to D, the judge and the
// calibration. No database, no server, no model call.

// V2Runs names the run folder of each part under the results root.
type V2Runs struct{ A, B, C, D string }

// V2 holds the stored results of protocol v2. A nil part was not run.
type V2 struct {
	Runs        V2Runs
	Tasks       *results.Tasks       // part A: tasks.json, with its runs read from tasks/*.json when there are any
	Judge       *results.Judge2      // part A: judge2.json
	Calibration *results.Calibration // part A: calibration.json
	// Maintainer counts the fact labels the maintainer filled on calibration2.csv, by label; nil without the sheet.
	Maintainer map[string]int
	Receive    *results.Receive  // part B
	ReadOnly   *results.ReadOnly // part C
	N2         *results.N2       // part D
	// Times is the modification time of each file read, by level name (tasks, judge2, receive...).
	Times map[string]time.Time
}

// Categories of bench/data/external-attacks.sql that the hypotheses read.
const (
	catSideEffect   = "side-effect-fn"
	catCommitEscape = "commit-escape"
)

// Verdicts of a hypothesis of PROTOCOL.md §8.
const (
	Confirmed    = "confirmed"
	NotConfirmed = "not confirmed"
	NotTested    = "not tested"
)

func loadLevel[T any](in *V2, dir, level string) (*T, error) {
	v := new(T)
	ok, err := results.Load(dir, level, v)
	if err != nil || !ok {
		return nil, err
	}
	if fi, err := os.Stat(filepath.Join(dir, level+".json")); err == nil {
		in.Times[level] = fi.ModTime().UTC()
	}
	return v, nil
}

// LoadV2 reads the parts named by runs under root. A missing folder or file leaves its part nil.
func LoadV2(root string, runs V2Runs) (V2, error) {
	in := V2{Runs: runs, Times: map[string]time.Time{}}
	a := filepath.Join(root, runs.A)
	var err error
	if in.Tasks, err = loadLevel[results.Tasks](&in, a, "tasks"); err != nil {
		return in, err
	}
	if in.Tasks != nil {
		recs, err := results.LoadTaskRuns(a)
		if err != nil {
			return in, err
		}
		if len(recs) > 0 { // the per-run records, which tasks --regrade rewrites one by one
			in.Tasks.Runs = recs
		}
	}
	if in.Judge, err = loadLevel[results.Judge2](&in, a, "judge2"); err != nil {
		return in, err
	}
	if in.Calibration, err = loadLevel[results.Calibration](&in, a, "calibration"); err != nil {
		return in, err
	}
	b, err := os.ReadFile(filepath.Join(a, "calibration2.csv"))
	switch {
	case err == nil:
		if in.Maintainer, err = maintainerLabels(b); err != nil {
			return in, err
		}
	case !errors.Is(err, fs.ErrNotExist):
		return in, fmt.Errorf("read calibration2.csv: %w", err)
	}
	if in.Receive, err = loadLevel[results.Receive](&in, filepath.Join(root, runs.B), "receive"); err != nil {
		return in, err
	}
	if in.ReadOnly, err = loadLevel[results.ReadOnly](&in, filepath.Join(root, runs.C), "readonly"); err != nil {
		return in, err
	}
	in.N2, err = loadLevel[results.N2](&in, filepath.Join(root, runs.D), "n2")
	return in, err
}

// maintainerLabels counts the fact labels of the filled calibration grid (answer,task,row,statement,label), read as
// `calibration import` reads them; the doubt and unsupported rows are left out.
func maintainerLabels(b []byte) (map[string]int, error) {
	recs, err := csv.NewReader(bytes.NewReader(b)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("read calibration2.csv: %w", err)
	}
	m := map[string]int{}
	for _, r := range recs[min(1, len(recs)):] {
		if len(r) < 5 || r[2] == judge.RowDoubt || r[2] == judge.RowUnsupported {
			continue
		}
		v := strings.ToLower(strings.TrimSpace(r[4]))
		for _, l := range []string{results.FactPresent, results.FactAbsent, results.FactContradicted} {
			if v != "" && (v == l || v == l[:1]) {
				m[l]++
			}
		}
	}
	return m, nil
}

var v2Names = map[string]string{
	results.DBA: "postgres-dba-mcp", results.Crystal: "crystaldba", results.DBHub: "DBHub",
	results.Toolbox: "MCP Toolbox", results.Reference: "server-postgres",
}

func sname(s string) string {
	if n, ok := v2Names[s]; ok {
		return n
	}
	return s
}

// num writes an integer with thousands separators, as BENCHMARK.md does.
func num(v int) string {
	s, sign := fmt.Sprint(v), ""
	if v < 0 {
		s, sign = s[1:], "-"
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return sign + s
}

// numf writes a median: an integer, or one decimal for the mean of the two middle values.
func numf(f float64) string {
	w := math.Trunc(f)
	if f == w {
		return num(int(w))
	}
	return num(int(w)) + fmt.Sprintf("%.1f", math.Abs(f-w))[1:]
}

func usd(f float64) string { return fmt.Sprintf("%.2f USD", f) }

func kappaText(k float64) string {
	if math.IsNaN(k) {
		return "undefined"
	}
	return fmt.Sprintf("%.2f", k)
}

func esc(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "|", `\|`), "\n", " ") }

func when(t time.Time) string { return t.UTC().Format("2006-01-02 15:04") + " UTC" }

// ---- part A

func runTokens(r results.TaskRun) int { return r.Tokens.InputTotal() + r.Tokens.Output }

// partAServers are the servers of part A in the order of its Env, then any other the runs name.
func partAServers(t *results.Tasks) []string {
	var out []string
	for _, s := range t.Env.Servers {
		out = append(out, s.Name)
	}
	for _, r := range t.Runs {
		if !slices.Contains(out, r.Server) {
			out = append(out, r.Server)
		}
	}
	return out
}

// taskIDs returns the tasks that have runs in arm, in the order of tasks.All.
func taskIDs(runs []results.TaskRun, arm string) []string {
	var out []string
	for _, t := range tasks.All {
		if slices.ContainsFunc(runs, func(r results.TaskRun) bool { return r.Task == t.ID && r.Arm == arm }) {
			out = append(out, t.ID)
		}
	}
	return out
}

// solved counts the solved runs and the runs of one task, arm and server; an empty task counts every task.
func solved(runs []results.TaskRun, task, arm, server string) (k, n int) {
	for _, r := range runs {
		if r.Arm == arm && r.Server == server && (task == "" || r.Task == task) {
			n++
			if r.Solved {
				k++
			}
		}
	}
	return k, n
}

// taskGap is the paired bootstrap of the solved rate of a minus b over the arm-ro tasks ids that both servers ran
// (PROTOCOL.md §8), with the number of tasks paired.
func taskGap(runs []results.TaskRun, ids []string, a, b string) (stats.Gap, int) {
	var ra, rb []float64
	for _, id := range ids {
		ka, na := solved(runs, id, results.ArmRO, a)
		kb, nb := solved(runs, id, results.ArmRO, b)
		if na == 0 || nb == 0 {
			continue
		}
		ra, rb = append(ra, float64(ka)/float64(na)), append(rb, float64(kb)/float64(nb))
	}
	return stats.PairedBootstrap(ra, rb, stats.BootstrapDraws, stats.BootstrapSeed), len(ra)
}

// medianRunsOf is the median of f over the runs keep selects, with their number.
func medianRunsOf(runs []results.TaskRun, keep func(results.TaskRun) bool, f func(results.TaskRun) int) (float64, int) {
	var v []int
	for _, r := range runs {
		if keep(r) {
			v = append(v, f(r))
		}
	}
	return medianInts(v), len(v)
}

// taskTokens is the median of the tokens of a server's arm-ro runs of a task, over every run or over the solved ones.
func taskTokens(runs []results.TaskRun, task, server string, solvedOnly bool) (float64, int) {
	return medianRunsOf(runs, func(r results.TaskRun) bool {
		return r.Task == task && r.Arm == results.ArmRO && r.Server == server && (r.Solved || !solvedOnly)
	}, runTokens)
}

func medianText(m float64, n int) string {
	if n == 0 {
		return "n/a"
	}
	return numf(m)
}

func points(f float64) string { return fmt.Sprintf("%+.1f", f*100) }

// ---- judge

type judgeTally struct {
	answers          int
	facts, doubt     map[string]int // consensus labels
	confidentlyWrong int
	stale            int // answers whose copied rubric verdict differs from the part-A record of now
}

// judgeKey names an answer as judge2 does: an arm-W run is the task <task>-w.
func judgeKey(task, arm, server string, rep int) string {
	if arm != results.ArmRO {
		task += "-" + arm
	}
	return fmt.Sprintf("%s/%s/%d", task, server, rep)
}

// judgeTallies counts the consensus labels of judge2.json per server. Confidently wrong (§4.4) takes the rubric's
// verdict from the part-A record when there is one, since judge2.json keeps the verdict of the day it ran.
func judgeTallies(j *results.Judge2, runs []results.TaskRun) map[string]*judgeTally {
	now := map[string]bool{}
	for _, r := range runs {
		now[judgeKey(r.Task, r.Arm, r.Server, r.Rep)] = r.Solved
	}
	out := map[string]*judgeTally{}
	for _, a := range j.Answers {
		t := out[a.Server]
		if t == nil {
			t = &judgeTally{facts: map[string]int{}, doubt: map[string]int{}}
			out[a.Server] = t
		}
		t.answers++
		for _, l := range a.Facts {
			t.facts[l]++
		}
		t.doubt[a.Doubt]++
		s, ok := now[fmt.Sprintf("%s/%s/%d", a.Task, a.Server, a.Run)]
		if !ok {
			s = a.Solved
		} else if s != a.Solved {
			t.stale++
		}
		if !s && a.Doubt == results.DoubtNo {
			t.confidentlyWrong++
		}
	}
	return out
}

type agreement struct {
	factsAgree, facts, doubtAgree, doubts int
	factKappa, doubtKappa                 float64
}

// judgeAgreement compares the first two judges on every answer: the fact labels and the doubt answers they share,
// and Cohen's kappa on each.
func judgeAgreement(j *results.Judge2) agreement {
	var g agreement
	var fa, fb, da, db []string
	for _, a := range j.Answers {
		if len(a.Grades) < 2 {
			continue
		}
		x, y := a.Grades[0], a.Grades[1]
		for i, f := range x.Facts {
			if i < len(y.Facts) {
				fa, fb = append(fa, f.Label), append(fb, y.Facts[i].Label)
			}
		}
		da, db = append(da, yesNo(x.Doubt)), append(db, yesNo(y.Doubt))
	}
	for i := range fa {
		if fa[i] == fb[i] {
			g.factsAgree++
		}
	}
	for i := range da {
		if da[i] == db[i] {
			g.doubtAgree++
		}
	}
	g.facts, g.doubts = len(fa), len(da)
	g.factKappa, g.doubtKappa = stats.Kappa(fa, fb), stats.Kappa(da, db)
	return g
}

// judgeCosts sums the cost of every call per judge model.
func judgeCosts(j *results.Judge2) map[string]float64 {
	m := map[string]float64{}
	for _, a := range j.Answers {
		for _, g := range a.Grades {
			for _, c := range g.CallsUSD {
				m[g.Model] += c
			}
		}
	}
	return m
}

// ---- hypotheses (PROTOCOL.md §8)

type hypothesis struct{ Verdict, Measured string }

// hypothesis1: on the tasks labelled "none", postgres-dba-mcp ties: its interval of the gap includes 0.
func hypothesis1(in V2) hypothesis {
	if in.Tasks == nil {
		return hypothesis{NotTested, "part A has not run yet."}
	}
	var ids []string
	for _, id := range taskIDs(in.Tasks.Runs, results.ArmRO) {
		if strings.HasPrefix(byTask(id).Advantage, "none") {
			ids = append(ids, id)
		}
	}
	g, n := taskGap(in.Tasks.Runs, ids, results.DBA, results.Crystal)
	if n == 0 {
		return hypothesis{NotTested, "part A has no task labelled none on both servers."}
	}
	m := fmt.Sprintf("on the %d tasks labelled none (%s), the gap in solved rate, postgres-dba-mcp minus crystaldba, is %s points, 95 %% interval [%s, %s].",
		n, strings.Join(ids, ", "), points(g.Mean), points(g.Lo), points(g.Hi))
	if !g.Significant() {
		return hypothesis{Confirmed, m + " The interval includes 0: a tie."}
	}
	better := sname(results.DBA)
	if g.Hi < 0 {
		better = sname(results.Crystal)
	}
	return hypothesis{NotConfirmed, m + " The interval excludes 0: a gap in favour of " + better + "."}
}

// hypothesis2: on A06, A07 and B2 to B4, postgres-dba-mcp uses fewer tokens per solved task and states every
// truncation.
func hypothesis2(in V2) hypothesis {
	if in.Tasks == nil || in.Receive == nil {
		return hypothesis{NotTested, "part A or part B has not run yet."}
	}
	ok := true
	var parts []string
	for _, id := range []string{"A06", "A07"} {
		d, nd := taskTokens(in.Tasks.Runs, id, results.DBA, true)
		c, nc := taskTokens(in.Tasks.Runs, id, results.Crystal, true)
		if nd == 0 || nc == 0 {
			ok = false
			parts = append(parts, id+": no solved run on one of the two servers")
			continue
		}
		ok = ok && d < c
		parts = append(parts, fmt.Sprintf("%s: median %s tokens per solved run, crystaldba %s", id, numf(d), numf(c)))
	}
	for _, id := range []string{"B2", "B3", "B4"} {
		i := slices.IndexFunc(in.Receive.Rows, func(r results.ReceiveRow) bool { return r.ID == id })
		if i < 0 {
			ok = false
			parts = append(parts, id+": not measured")
			continue
		}
		row := in.Receive.Rows[i]
		dc, has := row.Cells[results.DBA]
		if !has || dc.Outcome != results.ReceiveOK {
			ok = false
			parts = append(parts, fmt.Sprintf("%s: postgres-dba-mcp returned no result (%s)", id, dc.Outcome))
			continue
		}
		var others, notMore []string
		for _, s := range receiveServers(in.Receive) {
			if c := row.Cells[s]; s != results.DBA && c.Outcome == results.ReceiveOK {
				others = append(others, s)
				if c.Tokens <= dc.Tokens {
					notMore = append(notMore, sname(s))
				}
			}
		}
		ok = ok && len(notMore) == 0 && dc.Truncation != results.TruncSilent
		p := fmt.Sprintf("%s: %s tokens%s, truncation %s", id, num(dc.Tokens), estMark(dc.Estimated), dc.Truncation)
		switch {
		case len(others) == 0:
			p += ", and no other server returned a result"
		case len(notMore) == 0:
			p += fmt.Sprintf(", fewer than each of the %d other servers that returned a result", len(others))
		default:
			p += ", not fewer than " + strings.Join(notMore, ", ")
		}
		parts = append(parts, p)
	}
	v := NotConfirmed
	if ok {
		v = Confirmed
	}
	return hypothesis{v, strings.Join(parts, "; ") + ". Truncation is measured in part B only."}
}

// hypothesis3: in part C, only the servers with a statement parser refuse the side-effect functions, and the
// reference server lets the transaction escape. Measured on the external corpus: a side-effect function case is
// refused when it leaves no lasting effect.
func hypothesis3(in V2) hypothesis {
	if in.ReadOnly == nil {
		return hypothesis{NotTested, "part C has not run yet."}
	}
	ok, escaped := true, false
	escape := "server-postgres was not measured"
	var parts []string
	for _, s := range in.ReadOnly.Servers {
		var n, lasting, nEsc, escLasting int
		for _, c := range s.Corpora[results.CorpusExternal] {
			hit := c.Outcome == results.ROLasting
			switch c.Category {
			case catSideEffect:
				n++
				lasting += map[bool]int{true: 1}[hit]
			case catCommitEscape:
				nEsc++
				escLasting += map[bool]int{true: 1}[hit]
			}
		}
		if n == 0 {
			continue
		}
		parser := strings.HasPrefix(s.ReadOnly, "parser")
		p := fmt.Sprintf("%s (%s) %d of %d", sname(s.Server), map[bool]string{true: "parser", false: "no parser"}[parser], lasting, n)
		if parser != (lasting == 0) {
			ok, p = false, p+" (against the hypothesis)"
		}
		parts = append(parts, p)
		if s.Server == results.Reference {
			escape = fmt.Sprintf("server-postgres: %d of %d commit-escape cases left a lasting effect", escLasting, nEsc)
			escaped = escLasting > 0
		}
	}
	if len(parts) == 0 {
		return hypothesis{NotTested, "part C has no side-effect function case in its external corpus."}
	}
	v := NotConfirmed
	if ok && escaped {
		v = Confirmed
	}
	return hypothesis{v, "side-effect function cases of the external corpus that left a lasting effect: " + strings.Join(parts, "; ") + ". And " + escape + "."}
}

func hypotheses(in V2) []hypothesis {
	return []hypothesis{hypothesis1(in), hypothesis2(in), hypothesis3(in),
		{NotTested, "Toolbox is not in part A since decision D7 (PROTOCOL.md §2), so A10, A12 and A13 were not run on it."}}
}

// ---- rendering

func estMark(estimated bool) string {
	if estimated {
		return " (estimated)"
	}
	return ""
}

// RenderV2 writes report-v2.md.
func RenderV2(in V2) string {
	var sb strings.Builder
	sb.WriteString("# Benchmark protocol v2: report\n\n")
	sb.WriteString("Written by `go run ./cmd/bench report2` from the stored results. Every number below comes from them. " +
		"The method is in [PROTOCOL.md](PROTOCOL.md).\n\n")
	renderV2Setup(&sb, in)
	renderV2PartA(&sb, in)
	renderV2Judge(&sb, in)
	renderV2PartB(&sb, in)
	renderV2PartC(&sb, in)
	renderV2PartD(&sb, in)
	renderV2Hypotheses(&sb, in)
	renderV2Costs(&sb, in)
	return sb.String()
}

func notRun(sb *strings.Builder, what, run string) {
	fmt.Fprintf(sb, "%s: not run yet (no result in `results/%s`).\n\n", what, run)
}

func renderV2Setup(sb *strings.Builder, in V2) {
	sb.WriteString("## 1. Question and setup\n\n")
	sb.WriteString("**With the same agent and the same model, which PostgreSQL MCP server lets the agent reach the right answer, " +
		"without changing the database, and at what cost in tokens?**\n\n")
	fileDate := func(level string) string {
		if t, ok := in.Times[level]; ok {
			return when(t) + " (time of " + level + ".json)"
		}
		return "not run yet"
	}
	dateA := fileDate("tasks")
	if t := in.Tasks; t != nil {
		var first, last string
		for _, r := range t.Runs {
			if r.StartedAt != "" && (first == "" || r.StartedAt < first) {
				first = r.StartedAt
			}
			last = max(last, r.StartedAt)
		}
		a, errA := time.Parse(time.RFC3339, first)
		b, errB := time.Parse(time.RFC3339, last)
		if errA == nil && errB == nil {
			dateA = "runs started from " + when(a) + " to " + when(b)
		}
	}
	tbl(sb, []string{"Part", "Run folder", "Date"}, [][]string{
		{"A. Agent tasks", "`" + in.Runs.A + "`", dateA},
		{"Judge", "`" + in.Runs.A + "`", fileDate("judge2")},
		{"Calibration", "`" + in.Runs.A + "`", fileDate("calibration")},
		{"B. What the model receives", "`" + in.Runs.B + "`", fileDate("receive")},
		{"C. Read-only enforcement", "`" + in.Runs.C + "`", fileDate("readonly")},
		{"D. Tool by tool", "`" + in.Runs.D + "`", fileDate("n2")},
	})

	// Servers, with the parts each image was measured in.
	var envA, envB, envC, envD *results.Env
	if in.Tasks != nil {
		envA = &in.Tasks.Env
	}
	if in.Receive != nil {
		envB = &in.Receive.Env
	}
	if in.ReadOnly != nil {
		envC = &in.ReadOnly.Env
	}
	if in.N2 != nil {
		envD = &in.N2.Env
	}
	type img struct {
		env   results.ServerEnv
		parts []string
	}
	var imgs []*img
	for _, p := range []struct {
		name string
		env  *results.Env
	}{{"A", envA}, {"B", envB}, {"C", envC}, {"D", envD}} {
		if p.env == nil {
			continue
		}
		for _, s := range p.env.Servers {
			i := slices.IndexFunc(imgs, func(x *img) bool { return x.env.Name == s.Name && x.env.Image == s.Image })
			if i < 0 {
				imgs, i = append(imgs, &img{env: s}), len(imgs)
			}
			imgs[i].parts = append(imgs[i].parts, p.name)
		}
	}
	var rows [][]string
	for _, x := range imgs {
		rows = append(rows, []string{sname(x.env.Name), x.env.Version, "`" + x.env.Image + "`", strings.Join(x.parts, ", ")})
	}
	if len(rows) > 0 {
		sb.WriteString("Servers, as each result file records them. An image without a digest is built locally from a pinned Dockerfile " +
			"([§2](PROTOCOL.md#2-servers)).\n\n")
		tbl(sb, []string{"Server", "Version", "Image", "Parts"}, rows)
	}

	if t := in.Tasks; t != nil {
		served := map[string]int{}
		for _, r := range t.Runs {
			served[r.Model]++
		}
		var s []string
		for _, m := range slices.Sorted(maps.Keys(served)) {
			name := m
			if name == "" {
				name = "no model recorded"
			}
			s = append(s, fmt.Sprintf("%s on %s of %s runs", name, num(served[m]), num(len(t.Runs))))
		}
		fmt.Fprintf(sb, "**Agent model**: %s, through `claude -p` (served: %s).", t.Env.Model, strings.Join(s, ", "))
		if in.Judge != nil {
			fmt.Fprintf(sb, " **Judges**: %s.", strings.Join(in.Judge.Judges, " and "))
		}
		fmt.Fprintf(sb, " PostgreSQL %s, scale %g.\n\n", t.Env.PGVersion, t.Env.Scale)
	}
	sb.WriteString("**Scope (decision D7)**: parts A and D measure postgres-dba-mcp and crystaldba only. DBHub, MCP Toolbox and " +
		"server-postgres are in parts B and C only, as context: they are generic SQL servers, compared through the one tool every " +
		"server has, its SQL tool.\n\n")
	sb.WriteString("**Deviations from the protocol**: the full run of part A, the judge and the calibration ran before the tag " +
		"`bench-protocol-v2`, not after it. Four rubrics (A05, A06, A08, A12) were fixed after that run, and the stored runs were " +
		"graded again; no run was repeated. Both are in the [changelog](PROTOCOL.md#changelog).\n\n")
}

func renderV2PartA(sb *strings.Builder, in V2) {
	sb.WriteString("## 2. Part A: agent tasks\n\n")
	if in.Tasks == nil {
		notRun(sb, "Part A", in.Runs.A)
		return
	}
	runs, srv := in.Tasks.Runs, partAServers(in.Tasks)
	ro, w := taskIDs(runs, results.ArmRO), taskIDs(runs, results.ArmW)
	fmt.Fprintf(sb, "%s runs. Solved is the deterministic rubric. Arm ro connects as `bench_ro`; arm W runs A14 and A15 again as "+
		"`bench_owner`, and there A14 is solved only when the database is unchanged.\n\n", num(len(runs)))

	header := []string{"Task", "Question", "Expected advantage"}
	for _, s := range srv {
		header = append(header, sname(s))
	}
	var rows [][]string
	add := func(id, arm, label string) {
		t := byTask(id)
		r := []string{label, esc(t.Question), t.Advantage}
		for _, s := range srv {
			k, n := solved(runs, id, arm, s)
			r = append(r, fmt.Sprintf("%d/%d", k, n))
		}
		rows = append(rows, r)
	}
	for _, id := range ro {
		add(id, results.ArmRO, id)
	}
	for _, id := range w {
		add(id, results.ArmW, id+", arm W")
	}
	tbl(sb, header, rows)

	rows = nil
	for _, s := range srv {
		k, n := solved(runs, "", results.ArmRO, s)
		lo, hi := stats.Wilson(k, n)
		every := 0
		for _, id := range ro {
			if c, m := solved(runs, id, results.ArmRO, s); stats.PassK(m, c, m) == 1 {
				every++
			}
		}
		kw, nw := solved(runs, "", results.ArmW, s)
		rate := "n/a"
		if n > 0 {
			rate = pct(float64(k) / float64(n))
		}
		rows = append(rows, []string{sname(s), fmt.Sprintf("%d of %d", k, n), rate, pct(lo) + " to " + pct(hi),
			fmt.Sprintf("%d of %d", every, len(ro)), fmt.Sprintf("%d of %d", kw, nw)})
	}
	tbl(sb, []string{"Server", "Solved, arm ro", "Rate", "95 % Wilson interval", "Tasks solved on every run", "Solved, arm W"}, rows)

	for _, b := range srv {
		if b == results.DBA || !slices.Contains(srv, results.DBA) {
			continue
		}
		g, n := taskGap(runs, ro, results.DBA, b)
		verdict := "The interval includes 0: no gap."
		if g.Significant() {
			verdict = "The interval excludes 0: a gap."
		}
		fmt.Fprintf(sb, "Gap in solved rate, %s minus %s, paired bootstrap over the %d tasks of arm ro (%s draws, seed %d): %s points, "+
			"95 %% interval [%s, %s]. %s\n\n", sname(results.DBA), sname(b), n, num(stats.BootstrapDraws), stats.BootstrapSeed,
			points(g.Mean), points(g.Lo), points(g.Hi), verdict)
	}

	sb.WriteString("Cost per run, over every run of both arms. Tokens are the sum of the input, cache creation, cache read and output " +
		"tokens of the run, as `claude -p` reports them.\n\n")
	rows = nil
	var changed []string
	for _, s := range srv {
		of := func(r results.TaskRun) bool { return r.Server == s }
		tok, n := medianRunsOf(runs, of, runTokens)
		tokS, nS := medianRunsOf(runs, func(r results.TaskRun) bool { return of(r) && r.Solved }, runTokens)
		calls, _ := medianRunsOf(runs, of, func(r results.TaskRun) int { return len(r.ToolCalls) })
		turns, _ := medianRunsOf(runs, of, func(r results.TaskRun) int { return r.Turns })
		var cost float64
		var toolErrs, dbRuns int
		for _, r := range runs {
			if !of(r) {
				continue
			}
			cost += r.CostUSD
			toolErrs += r.ToolErrors
			if len(r.DBChanges) > 0 {
				dbRuns++
				changed = append(changed, fmt.Sprintf("- %s, arm %s, %s, run %d: %s", r.Task, r.Arm, sname(s), r.Rep, esc(strings.Join(r.DBChanges, "; "))))
			}
		}
		rows = append(rows, []string{sname(s), num(n), medianText(tok, n), medianText(tokS, nS), usd(cost), medianText(calls, n),
			medianText(turns, n), num(toolErrs), num(dbRuns)})
	}
	tbl(sb, []string{"Server", "Runs", "Median tokens per run", "Median tokens per solved run", "Total cost", "Median tool calls",
		"Median turns", "Tool errors", "Runs that changed the database"}, rows)
	sb.WriteString("Tool errors count the calls the server flagged as an error. crystaldba answers a refused statement with a text that " +
		"starts with \"Error:\" and does not flag it, so the two counts are not comparable.\n\n")
	if len(changed) == 0 {
		sb.WriteString("No run changed the database.\n\n")
	} else {
		sb.WriteString("Runs that changed the database:\n\n" + strings.Join(changed, "\n") + "\n\n")
	}

	sb.WriteString("Tokens on the two large-data tasks, arm ro:\n\n")
	rows = nil
	for _, id := range []string{"A06", "A07"} {
		for _, s := range srv {
			m, n := taskTokens(runs, id, s, false)
			ms, ns := taskTokens(runs, id, s, true)
			if n == 0 {
				continue
			}
			k, _ := solved(runs, id, results.ArmRO, s)
			rows = append(rows, []string{id, sname(s), fmt.Sprintf("%d/%d", k, n), medianText(m, n), medianText(ms, ns)})
		}
	}
	if len(rows) == 0 {
		sb.WriteString("A06 and A07 have no run.\n\n")
	} else {
		tbl(sb, []string{"Task", "Server", "Solved", "Median tokens per run", "Median tokens per solved run"}, rows)
	}
}

func byTask(id string) tasks.Task {
	i := slices.IndexFunc(tasks.All, func(t tasks.Task) bool { return t.ID == id })
	if i < 0 {
		return tasks.Task{ID: id}
	}
	return tasks.All[i]
}

func renderV2Judge(sb *strings.Builder, in V2) {
	sb.WriteString("## 3. Judge\n\n")
	if in.Judge == nil {
		notRun(sb, "The judge", in.Runs.A)
		return
	}
	j := in.Judge
	var runs []results.TaskRun
	if in.Tasks != nil {
		runs = in.Tasks.Runs
	}
	fmt.Fprintf(sb, "The rubric decides solved; the judge only adds facts right, facts wrong and confidently wrong (§7). %s answers, "+
		"each graded blind by %s against the facts expected for its task. A fact counts as present or contradicted only when "+
		"both judges agree; otherwise it is disputed. **The judges are from the agent's family** (Claude, decision D2), so they "+
		"may favour its style.\n\n", num(len(j.Answers)), strings.Join(j.Judges, " and "))
	tallies := judgeTallies(j, runs)
	var srv []string
	for _, a := range j.Answers {
		if !slices.Contains(srv, a.Server) {
			srv = append(srv, a.Server)
		}
	}
	slices.SortStableFunc(srv, func(a, b string) int { return serverRank(a) - serverRank(b) })
	var rows [][]string
	var wrong []string
	stale := 0
	for _, s := range srv {
		t := tallies[s]
		f, d := t.facts, t.doubt
		rows = append(rows, []string{sname(s), num(t.answers), num(f[results.FactPresent]), num(f[results.FactAbsent]),
			num(f[results.FactContradicted]), num(f[results.FactDisputed]), num(d[results.DoubtYes]), num(d[results.DoubtNo]),
			num(d[results.DoubtDisputed]), num(t.confidentlyWrong)})
		wrong = append(wrong, fmt.Sprintf("%s %d", sname(s), f[results.FactContradicted]))
		stale += t.stale
	}
	tbl(sb, []string{"Server", "Answers", "Facts present", "Facts absent", "Facts contradicted", "Facts disputed", "Doubt yes", "Doubt no",
		"Doubt disputed", "Confidently wrong"}, rows)
	fmt.Fprintf(sb, "Facts wrong (contradicted by both judges): %s. ", strings.Join(wrong, ", "))
	if m := in.Maintainer; m != nil {
		fmt.Fprintf(sb, "**Limit**: on the calibration sheet the maintainer marked %d facts present, %d absent and %d contradicted. "+
			"The calibration therefore checks present against absent, not the judges' detection of a contradiction.\n\n",
			m[results.FactPresent], m[results.FactAbsent], m[results.FactContradicted])
	} else {
		sb.WriteString("**Limit**: no filled calibration sheet was found, so no figure here was checked against a human grade.\n\n")
	}
	sb.WriteString("Confidently wrong: the rubric failed and both judges found no doubt or limit in the answer.")
	if stale > 0 {
		fmt.Fprintf(sb, " %d answers of judge2.json carry an older rubric verdict than their part-A record; the count above uses the record.", stale)
	}
	sb.WriteString("\n\n")

	g := judgeAgreement(j)
	if g.facts > 0 {
		fmt.Fprintf(sb, "Agreement between the judges, on every answer: %s of %s fact labels (Cohen's kappa %s) and %s of %s doubt answers "+
			"(kappa %s).\n\n", num(g.factsAgree), num(g.facts), kappaText(g.factKappa), num(g.doubtAgree), num(g.doubts), kappaText(g.doubtKappa))
	}

	c := in.Calibration
	if c == nil {
		sb.WriteString("Calibration: not run yet. Without it, the judges' measures cannot be published (§7).\n\n")
		return
	}
	fmt.Fprintf(sb, "Calibration: the maintainer graded %d answers blind, %d fact labels. A judge whose fact kappa is under %.1f is not "+
		"published.\n\n", c.Sheet, c.Filled, judge.KappaFloor)
	rows = nil
	for _, m := range j.Judges {
		rows = append(rows, []string{m, kappaPtrText(c.FactKappa[m]), kappaPtrText(c.DoubtKappa[m]), yesNo(c.Published[m])})
	}
	tbl(sb, []string{"Judge", "Fact kappa", "Doubt kappa", "Published"}, rows)
	for _, m := range j.Judges {
		if !c.Published[m] {
			fmt.Fprintf(sb, "%s is under the floor: its measures above are not to be read as results.\n\n", m)
		}
	}
}

func kappaPtrText(k *float64) string {
	if k == nil {
		return "undefined"
	}
	return kappaText(*k)
}

// serverRank orders servers as the registry does.
func serverRank(s string) int {
	i := slices.Index([]string{results.DBA, results.Crystal, results.DBHub, results.Toolbox, results.Reference}, s)
	if i < 0 {
		return 99
	}
	return i
}

func receiveServers(r *results.Receive) []string {
	var out []string
	for _, s := range r.Env.Servers {
		out = append(out, s.Name)
	}
	for _, row := range r.Rows {
		for s := range row.Cells {
			if !slices.Contains(out, s) {
				out = append(out, s)
			}
		}
	}
	slices.SortStableFunc(out, func(a, b string) int { return serverRank(a) - serverRank(b) })
	return out
}

func tokText(n int, estimated bool) string {
	if estimated {
		return num(n) + " (estimated)"
	}
	return num(n)
}

func renderV2PartB(sb *strings.Builder, in V2) {
	sb.WriteString("## 4. Part B: what the model receives\n\n")
	r := in.Receive
	if r == nil {
		notRun(sb, "Part B", in.Runs.B)
		return
	}
	fmt.Fprintf(sb, "The same statements through each server's SQL tool, as `bench_ro`. Token counter: %s; \"estimated\" is ceil(bytes/4), "+
		"not a count, and the protocol says it understates (§5).\n\n", r.Env.TokenCounter)
	srv := receiveServers(r)
	for _, row := range r.Rows {
		fmt.Fprintf(sb, "### %s: `%s`\n\n", row.ID, row.SQL)
		switch {
		case row.TrueCellBytes > 0:
			fmt.Fprintf(sb, "The database returns %s row; the cell is %s bytes.\n\n", num(row.TrueRows), num(row.TrueCellBytes))
		case row.TrueRows > 0:
			fmt.Fprintf(sb, "The database returns %s rows.\n\n", num(row.TrueRows))
		}
		var rows [][]string
		var details []string
		for _, s := range srv {
			c, ok := row.Cells[s]
			if !ok {
				continue
			}
			rowsText, trunc, wrong := "n/a", c.Truncation, "n/a"
			if c.Rows >= 0 {
				rowsText = num(c.Rows)
			}
			if trunc == "" {
				trunc = "n/a"
			}
			if c.Values > 0 {
				wrong = fmt.Sprintf("%d of %d", len(c.Wrong), c.Values)
				d := fmt.Sprintf("- %s: %d of %d values wrong", sname(s), len(c.Wrong), c.Values)
				if len(c.Wrong) > 0 {
					d += " (" + esc(strings.Join(c.Wrong, "; ")) + ")"
				}
				details = append(details, d+fmt.Sprintf(", %d in another notation of the same value.", len(c.Reformatted)))
			}
			if row.ID == "B7" {
				details = append(details, fmt.Sprintf("- %s: names the column %s, gives a hint %s.", sname(s), yesNo(c.NamesColumn), yesNo(c.Hint)))
			}
			if c.Note != "" {
				details = append(details, fmt.Sprintf("- %s: %s", sname(s), c.Note))
			}
			rows = append(rows, []string{sname(s), c.Outcome, num(c.Bytes), tokText(c.Tokens, c.Estimated), rowsText, trunc, wrong,
				yesNo(c.StayedUp), num(int(math.Round(c.MS)))})
		}
		tbl(sb, []string{"Server", "Outcome", "Bytes", "Tokens", "Rows returned", "Truncation", "Wrong values", "Stayed up", "Time (ms)"}, rows)
		if len(details) > 0 {
			sb.WriteString(strings.Join(details, "\n") + "\n\n")
		}
	}
	sb.WriteString("### Tool definitions\n\nThe `tools/list` result of each server, counted like the responses.\n\n")
	var rows [][]string
	for _, t := range r.Tools {
		rows = append(rows, []string{sname(t.Server), num(t.Tools), num(t.Bytes), tokText(t.Tokens, t.Estimated)})
	}
	tbl(sb, []string{"Server", "Tools", "Bytes", "Tokens"}, rows)
	fmt.Fprintf(sb, "Every call was bounded: %s. The container memory cap shapes B3: a server that holds the whole result in memory can "+
		"reach it before it answers. The notes under B3 give how each container ended, with its exit status and whether Docker "+
		"killed it for memory.\n\n", r.Bounds)
}

func renderV2PartC(sb *strings.Builder, in V2) {
	sb.WriteString("## 5. Part C: read-only enforcement\n\n")
	ro := in.ReadOnly
	if ro == nil {
		notRun(sb, "Part C", in.Runs.C)
		return
	}
	fmt.Fprintf(sb, "Every statement goes through each server's SQL tool in its read-only mode, connected as `%s`, a role that can "+
		"write. A snapshot of the database after each statement classifies the outcome. Part C has no score.\n\n", ro.Role)
	if len(ro.Servers) < len(ro.Env.Servers) { // readonly saves after each server
		fmt.Fprintf(sb, "**Incomplete**: %d of the %d servers of this run have a result; the run stopped or is still running.\n\n",
			len(ro.Servers), len(ro.Env.Servers))
	}
	sb.WriteString("Read-only model of each server, as the run records it:\n\n")
	for _, s := range ro.Servers {
		fmt.Fprintf(sb, "- %s: %s.\n", sname(s.Server), s.ReadOnly)
	}
	sb.WriteString("\n")
	for _, name := range []string{results.CorpusOwn, results.CorpusExternal, results.CorpusLegit, results.CorpusAgents} {
		i := slices.IndexFunc(ro.Corpora, func(c results.ReadOnlyCorpus) bool { return c.Name == name })
		if i < 0 {
			fmt.Fprintf(sb, "### Corpus %s\n\nNot in this run.\n\n", name)
			continue
		}
		c := ro.Corpora[i]
		fmt.Fprintf(sb, "### Corpus %s: %s\n\n%s statements from `%s`.\n\n", c.Name, c.Label, num(c.Cases), c.File)
		var rows [][]string
		for _, s := range ro.Servers {
			counts, r := s.Counts(name), []string{sname(s.Server)}
			for _, o := range results.ROOutcomes {
				r = append(r, num(counts[o]))
			}
			rows = append(rows, append(r, num(len(s.Corpora[name]))))
		}
		tbl(sb, append(append([]string{"Server"}, results.ROOutcomes...), "Statements"), rows)
	}
	var rows [][]string
	for _, s := range ro.Servers {
		for _, c := range s.Corpora[results.CorpusExternal] {
			if c.Outcome == results.ROLasting {
				rows = append(rows, []string{sname(s.Server), "`" + c.ID + "`", c.Category, esc(c.Effect), c.Source})
			}
		}
	}
	sb.WriteString("Every statement of the external corpus that was executed with a lasting effect:\n\n")
	if len(rows) == 0 {
		sb.WriteString("None.\n\n")
		return
	}
	tbl(sb, []string{"Server", "Case", "Category", "What the snapshot saw", "Source"}, rows)
}

// n2Trunc maps the truncation of an N2 cell to the words of PROTOCOL.md §6a.
var n2Trunc = map[string]string{results.TruncSignalled: "stated", results.TruncMissing: "silent", results.TruncNone: "none"}

func renderV2PartD(sb *strings.Builder, in V2) {
	sb.WriteString("## 6. Part D: tool by tool, against crystaldba\n\n")
	n2 := in.N2
	if n2 == nil {
		notRun(sb, "Part D", in.Runs.D)
		return
	}
	est := n2.Env.TokenCounter == results.CounterEstimate
	calls := 0
	for _, r := range n2.Rows {
		for _, x := range r.Cells {
			calls = max(calls, len(x.LatencyMS))
		}
	}
	fmt.Fprintf(sb, "The same call to each tool both servers expose, as `bench_ro`, %d times each. Token counter: %s", calls, n2.Env.TokenCounter)
	if est {
		sb.WriteString(", so every token figure is estimated (ceil(bytes/4))")
	}
	sb.WriteString(". Bytes are the largest response of the calls and tokens their median; correct and truncation are graded on the " +
		"first call.\n\n")
	d, c := results.DBA, results.Crystal
	val := func(cell results.N2Cell, ok bool, f func(results.N2Cell) string) string {
		switch {
		case !ok:
			return "n/a"
		case cell.Status != results.StatusOK:
			return cell.Status
		}
		return f(cell)
	}
	bytesOf := func(x results.N2Cell) string { return num(x.Bytes) }
	tokOf := func(x results.N2Cell) string { return tokText(x.Tokens, x.Estimated) }
	latOf := func(x results.N2Cell) string { return fmt.Sprintf("%.1f / %.1f", x.P50MS, x.P95MS) }
	var rows [][]string
	var both, bytesD, bytesC, tokD, tokC, corD, corC, frD, frC int
	for _, r := range n2.Rows {
		a, okA := r.Cells[d]
		b, okB := r.Cells[c]
		correct := func(x results.N2Cell, ok bool) string {
			if !ok {
				return "n/a"
			}
			return yesNo(x.Correct)
		}
		trunc := func(x results.N2Cell, ok bool) string {
			if !ok || x.Status != results.StatusOK {
				return "n/a"
			}
			return n2Trunc[x.Truncation]
		}
		rows = append(rows, []string{r.ID, "`" + r.Tool + "`", val(a, okA, bytesOf), val(b, okB, bytesOf), val(a, okA, tokOf), val(b, okB, tokOf),
			correct(a, okA) + " / " + correct(b, okB), trunc(a, okA) + " / " + trunc(b, okB), val(a, okA, latOf), val(b, okB, latOf)})
		if a.Correct {
			corD++
		}
		if b.Correct {
			corC++
		}
		if a.FalseRefusal {
			frD++
		}
		if b.FalseRefusal {
			frC++
		}
		if a.Status == results.StatusOK && b.Status == results.StatusOK {
			both++
			bytesD, bytesC, tokD, tokC = bytesD+a.Bytes, bytesC+b.Bytes, tokD+a.Tokens, tokC+b.Tokens
		}
	}
	tbl(sb, []string{"Scenario", "Tool", "Bytes, postgres-dba-mcp", "Bytes, crystaldba", "Tokens, postgres-dba-mcp", "Tokens, crystaldba",
		"Correct (dba / crystal)", "Truncation (dba / crystal)", "p50 / p95 ms, postgres-dba-mcp", "p50 / p95 ms, crystaldba"}, rows)
	fmt.Fprintf(sb, "Totals over the %d scenarios: correct, postgres-dba-mcp %d and crystaldba %d; false refusals, %d and %d. "+
		"Over the %d scenarios both servers answered: bytes, %s and %s; tokens, %s and %s%s.\n\n",
		len(n2.Rows), corD, corC, frD, frC, both, num(bytesD), num(bytesC), num(tokD), num(tokC), estMark(est))
	var failed []string
	for _, r := range n2.Rows {
		for _, s := range []string{d, c} {
			if x, ok := r.Cells[s]; ok && x.Status != results.StatusOK {
				f := fmt.Sprintf("- %s, %s: %s", r.ID, sname(s), x.Status)
				if x.Note != "" {
					f += ": " + esc(x.Note)
				}
				failed = append(failed, f)
			}
		}
	}
	if len(failed) > 0 {
		sb.WriteString("Calls that returned no result:\n\n" + strings.Join(failed, "\n") + "\n\n")
	}
}

func renderV2Hypotheses(sb *strings.Builder, in V2) {
	sb.WriteString("## 7. Hypotheses\n\nWritten before the runs (§8). A result against them is published under them.\n\n")
	texts := []string{
		"On the tasks labelled \"none\", postgres-dba-mcp ties: its interval of the gap includes 0.",
		"On A06, A07 and B2 to B4, it uses fewer tokens per solved task and states every truncation.",
		"In part C with `bench_owner`, only the servers with a statement parser refuse the side-effect functions. The reference server lets the transaction escape through.",
		"On A10, A12 and A13, Toolbox may tie or win, because it has dedicated tools for them.",
	}
	for i, h := range hypotheses(in) {
		fmt.Fprintf(sb, "%d. %s\n   **%s**: %s\n", i+1, texts[i], strings.ToUpper(h.Verdict[:1])+h.Verdict[1:], h.Measured)
	}
	sb.WriteString("\n")
}

func renderV2Costs(sb *strings.Builder, in V2) {
	sb.WriteString("## 8. Costs\n\nThe `total_cost_usd` that `claude -p` computes for each call. The runs go through a Claude Code subscription.\n\n")
	var rows [][]string
	var total float64
	if t := in.Tasks; t != nil {
		var sum float64
		var per []string
		for _, s := range partAServers(t) {
			var c float64
			for _, r := range t.Runs {
				if r.Server == s {
					c += r.CostUSD
				}
			}
			sum += c
			per = append(per, sname(s)+" "+usd(c))
		}
		total += sum
		rows = append(rows, []string{"Part A, both arms", fmt.Sprintf("%s runs", num(len(t.Runs))), usd(sum), strings.Join(per, ", ")})
	}
	if j := in.Judge; j != nil {
		var per []string
		costs := judgeCosts(j)
		for _, m := range j.Judges {
			per = append(per, m+" "+usd(costs[m]))
		}
		total += j.CostUSD
		rows = append(rows, []string{"Judge", fmt.Sprintf("%s answers", num(len(j.Answers))), usd(j.CostUSD), strings.Join(per, ", ")})
	}
	if len(rows) == 0 {
		sb.WriteString("No part that records a cost has run yet.\n\n")
	} else {
		tbl(sb, []string{"Item", "Calls", "Cost", "Detail"}, rows)
		fmt.Fprintf(sb, "Total recorded: %s.\n\n", usd(total))
	}
	sb.WriteString("Part C calls no model. receive.json and n2.json record no cost: ")
	var counted []string
	if in.Receive != nil && in.Receive.Env.TokenCounter != results.CounterEstimate {
		counted = append(counted, "B")
	}
	if in.N2 != nil && in.N2.Env.TokenCounter != results.CounterEstimate {
		counted = append(counted, "D")
	}
	if len(counted) == 0 {
		sb.WriteString("with estimated tokens, parts B and D call no model either.\n\n")
		return
	}
	its := "its"
	if len(counted) > 1 {
		its = "their"
	}
	fmt.Fprintf(sb, "part %s counted %s tokens through %s, whose cost the harness logs but does not store; "+
		"[PROTOCOL.md §10](PROTOCOL.md#10-cost) gives it.\n\n", strings.Join(counted, " and part "), its, results.CounterClaude)
}
