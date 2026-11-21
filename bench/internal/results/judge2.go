package results

// Labels of an expected fact (PROTOCOL.md §7). A judge gives present, absent or contradicted; FactDisputed is the
// consensus of two judges that disagree.
const (
	FactPresent      = "present"
	FactAbsent       = "absent"
	FactContradicted = "contradicted"
	FactDisputed     = "disputed"
)

// Values of the consensus on "the answer signals a doubt or a limit".
const (
	DoubtYes      = "yes"
	DoubtNo       = "no"
	DoubtDisputed = "disputed"
)

// FactGrade is one judge's label of one expected fact.
type FactGrade struct {
	ID       string `json:"id"`
	Label    string `json:"label"`
	Evidence string `json:"evidence"` // the words of the answer that decide it; empty for absent
}

// JudgeGrade is one judge's grade of one answer.
type JudgeGrade struct {
	Model       string      `json:"model"`  // the model asked
	Served      string      `json:"served"` // the model that answered, from claude -p's modelUsage
	Facts       []FactGrade `json:"facts"`  // in the order of the expected facts
	Unsupported []string    `json:"unsupported"`
	Doubt       bool        `json:"doubt"`
	DoubtQuote  string      `json:"doubt_quote"`
	CallsUSD    []float64   `json:"calls_usd"` // total_cost_usd of every call for this grade, a retry included
}

// GradedAnswer is one answer of part A graded by both judges, with their consensus.
type GradedAnswer struct {
	Task   string       `json:"task"`
	Server string       `json:"server"`
	Run    int          `json:"run"`
	Solved bool         `json:"solved"` // the deterministic rubric's verdict, copied from the run
	Grades []JudgeGrade `json:"grades"` // one per judge, in the order of Judge2.Judges
	// Facts is the consensus label of every expected fact, by fact id: the judges' label when they agree, else
	// FactDisputed.
	Facts map[string]string `json:"facts"`
	Doubt string            `json:"doubt"` // DoubtYes, DoubtNo or DoubtDisputed
	// ConfidentlyWrong is true when the rubric failed and both judges found no doubt or limit (§4.4).
	ConfidentlyWrong bool `json:"confidently_wrong"`
}

// Judge2 is judge2.json: the fact-list judge of protocol v2.
type Judge2 struct {
	Judges       []string       `json:"judges"`        // model ids
	PromptSHA256 string         `json:"prompt_sha256"` // of the frozen prompt template
	CostUSD      float64        `json:"cost_usd"`      // every call, the retries included
	Answers      []GradedAnswer `json:"answers"`
}

// Calibration is calibration.json: how the maintainer's blind grades compare with the judges' (§7). A kappa is
// absent when it is undefined (no filled label, or one label everywhere).
type Calibration struct {
	Sheet  int `json:"sheet"`  // answers on the sheet
	Filled int `json:"filled"` // fact labels the maintainer filled
	// FactKappa is Cohen's kappa on the per-fact labels, between the maintainer and each judge, by model.
	FactKappa map[string]*float64 `json:"fact_kappa"`
	// DoubtKappa is the same on the doubt-or-limit answer.
	DoubtKappa map[string]*float64 `json:"doubt_kappa"`
	// JudgesFactKappa is Cohen's kappa between the two judges on the per-fact labels of every graded answer.
	JudgesFactKappa *float64 `json:"judges_fact_kappa"`
	// Published says, by model, whether the judge's measures may be published: its FactKappa is at least 0.6.
	Published map[string]bool `json:"published"`
}
