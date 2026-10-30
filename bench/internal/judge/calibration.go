package judge

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// CalibrationSeed fixes which pairs go on the calibration sheet and which answer is shown as A.
const CalibrationSeed = 20261022

// KeyPair says, for one pair of the sheet, which server is A and B and what the judge decided.
type KeyPair struct {
	N       int    `json:"n"`
	Task    string `json:"task"`
	Run     int    `json:"run"`
	A       string `json:"a"`     // server shown as A
	B       string `json:"b"`     // server shown as B
	Outcome string `json:"judge"` // "A", "B" or "tie", the judge's verdict on the pair
}

// Key is calibration-key.json.
type Key struct {
	Seed  uint64    `json:"seed"`
	Pairs []KeyPair `json:"pairs"`
}

// Sheet builds the blind calibration sheet for the human: up to n pairs spread over the tasks (one per task in
// turn, in a seeded order), each with the question, the facts and both answers as A and B, and a line for the
// verdict. It carries neither server names nor the judge's verdict; those go to the key.
func Sheet(cases []Case, pairs []results.JudgePair, n int) (string, Key) {
	rng := rand.New(rand.NewPCG(CalibrationSeed, CalibrationSeed))
	var tasks []string
	byTask := map[string][]int{}
	for i, c := range cases {
		if _, ok := byTask[c.Task]; !ok {
			tasks = append(tasks, c.Task)
		}
		byTask[c.Task] = append(byTask[c.Task], i)
	}
	for _, t := range tasks {
		rng.Shuffle(len(byTask[t]), func(a, b int) { byTask[t][a], byTask[t][b] = byTask[t][b], byTask[t][a] })
	}
	var picked []int
	for round := 0; len(picked) < n; round++ {
		before := len(picked)
		for _, t := range tasks {
			if round < len(byTask[t]) && len(picked) < n {
				picked = append(picked, byTask[t][round])
			}
		}
		if len(picked) == before {
			break
		}
	}
	key := Key{Seed: CalibrationSeed}
	var sb strings.Builder
	sb.WriteString("# Calibration sheet\n\nFor each pair, read the question, the reference facts and both answers, then write your verdict on the `Verdict:` line: A, B or tie, with a note if you like. " +
		"The answers are shown as A and B at random.\n")
	for k, i := range picked {
		c := cases[i]
		a, b := results.DBA, results.Crystal
		if rng.IntN(2) == 1 {
			a, b = b, a
		}
		label := Tie
		switch pairs[i].Outcome {
		case a:
			label = "A"
		case b:
			label = "B"
		}
		key.Pairs = append(key.Pairs, KeyPair{N: k + 1, Task: c.Task, Run: c.Run, A: a, B: b, Outcome: label})
		fmt.Fprintf(&sb, "\n## Pair %d\n\n**Question:** %s\n\n**Reference facts:**\n\n%s\n### Answer A\n\n~~~~~~\n%s\n~~~~~~\n\n### Answer B\n\n~~~~~~\n%s\n~~~~~~\n\nVerdict (A / B / tie): \n\nNote: \n",
			k+1, c.Question, c.Facts, c.Answers[a], c.Answers[b])
	}
	return sb.String(), key
}

// Agreement is how the human's verdicts compare with the judge's.
type Agreement struct {
	Filled, Agree int
	Lines         []string // one per pair of the key: the human's verdict, the judge's, and whether they match
}

var (
	pairHeading = regexp.MustCompile(`(?m)^## Pair (\d+)\s*$`)
	verdictLine = regexp.MustCompile(`(?m)^Verdict[^:\n]*:[ \t]*(.*)$`)
)

// Compare reads a filled sheet and its key. A pair whose verdict line is empty counts as not filled; a verdict
// that is not A, B or tie (any case) is an error.
func Compare(sheet string, key Key) (Agreement, error) {
	heads := pairHeading.FindAllStringSubmatchIndex(sheet, -1)
	if len(heads) == 0 {
		return Agreement{}, errors.New("no pair in the sheet")
	}
	human := map[int]string{}
	for i, h := range heads {
		n, _ := strconv.Atoi(sheet[h[2]:h[3]])
		end := len(sheet)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		// The verdict line follows the answers, so the last one of the section is the human's.
		m := verdictLine.FindAllStringSubmatch(sheet[h[1]:end], -1)
		if len(m) == 0 {
			continue
		}
		v := strings.TrimSpace(m[len(m)-1][1])
		if v == "" {
			continue
		}
		switch strings.ToLower(v) {
		case "a":
			v = "A"
		case "b":
			v = "B"
		case "tie":
			v = Tie
		default:
			return Agreement{}, fmt.Errorf("pair %d: verdict %q is not A, B or tie", n, v)
		}
		human[n] = v
	}
	var ag Agreement
	for _, p := range slices.SortedFunc(slices.Values(key.Pairs), func(x, y KeyPair) int { return x.N - y.N }) {
		h, ok := human[p.N]
		if !ok {
			ag.Lines = append(ag.Lines, fmt.Sprintf("pair %d (%s run %d): not filled, judge %s", p.N, p.Task, p.Run, p.Outcome))
			continue
		}
		ag.Filled++
		same := h == p.Outcome
		if same {
			ag.Agree++
		}
		ag.Lines = append(ag.Lines, fmt.Sprintf("pair %d (%s run %d): human %s, judge %s, %s", p.N, p.Task, p.Run, h, p.Outcome, map[bool]string{true: "agree", false: "differ"}[same]))
	}
	return ag, nil
}

// ReadKey decodes calibration-key.json.
func ReadKey(b []byte) (Key, error) {
	var k Key
	if err := json.Unmarshal(b, &k); err != nil {
		return k, fmt.Errorf("decode calibration key: %w", err)
	}
	return k, nil
}
