package results

// Part C of protocol v2 (PROTOCOL.md §6): what each server's read-only mode lets through, connected as bench_owner.

// Outcomes of one part C statement. A lasting effect wins over what the server answered.
const (
	ROServer   = "refused by the server"
	RODatabase = "refused by the database"
	RONoEffect = "executed, no lasting effect"
	ROLasting  = "executed, lasting effect"
	// RONoAnswer is a call that got no answer (timeout, or the server exited) and left nothing behind.
	RONoAnswer = "no answer"
)

// ROOutcomes are the outcomes in the order of the report.
var ROOutcomes = []string{ROServer, RODatabase, RONoEffect, ROLasting, RONoAnswer}

// Corpora of part C.
const (
	CorpusOwn      = "own"      // testdata/safesql/attacks.sql
	CorpusExternal = "external" // bench/data/external-attacks.sql
	CorpusLegit    = "legit"    // testdata/safesql/legit.sql
	CorpusAgents   = "agents"   // the SQL the agents sent in part A, when given
)

// ReadOnlyCorpus describes one corpus of a part C run.
type ReadOnlyCorpus struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	File  string `json:"file"`
	Cases int    `json:"cases"`
}

// ReadOnlyCase is the outcome of one statement on one server.
type ReadOnlyCase struct {
	ID       string `json:"id"`
	Category string `json:"category"`
	Source   string `json:"source,omitempty"`
	Outcome  string `json:"outcome"`
	Effect   string `json:"effect,omitempty"` // what the snapshot saw change
	Text     string `json:"text,omitempty"`   // first line of the response
}

// ReadOnlyServer is one server's part C.
type ReadOnlyServer struct {
	Server   string                    `json:"server"`
	ReadOnly string                    `json:"read_only"` // the read-only model, from the registry
	Corpora  map[string][]ReadOnlyCase `json:"corpora"`   // by corpus name
}

// Counts returns the number of cases of a corpus per outcome.
func (s ReadOnlyServer) Counts(corpus string) map[string]int {
	m := map[string]int{}
	for _, c := range s.Corpora[corpus] {
		m[c.Outcome]++
	}
	return m
}

// ReadOnly is readonly.json.
type ReadOnly struct {
	Env     Env              `json:"env"`
	Role    string           `json:"role"`
	Corpora []ReadOnlyCorpus `json:"corpora"`
	Servers []ReadOnlyServer `json:"servers"`
}
