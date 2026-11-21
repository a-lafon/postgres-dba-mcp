package results

// Part B of protocol v2 (PROTOCOL.md §5): what each server's SQL tool returns for the same statements, as bench_ro.

// Truncation states of a part B response. TruncNone ("none") is shared with N2.
const (
	TruncStated = "stated" // the response holds less than the result and says so
	TruncSilent = "silent" // the response holds less than the result and does not say so
)

// Outcomes of one part B call.
const (
	ReceiveOK        = "ok"        // the server answered with a result
	ReceiveError     = "error"     // the server answered with an error: a tool error, crystaldba's "Error:" text, a JSON-RPC error
	ReceiveTimeout   = "timeout"   // no answer within the call timeout
	ReceiveOversized = "oversized" // the answer exceeded the client's size cap
	ReceiveNoAnswer  = "no answer" // the connection failed: the server exited or broke the protocol
)

// ReceiveCell is what one server returned for one part B statement.
type ReceiveCell struct {
	Outcome   string  `json:"outcome"`
	Bytes     int     `json:"bytes"`
	Tokens    int     `json:"tokens"`
	Estimated bool    `json:"tokens_estimated,omitempty"` // Tokens is ceil(bytes/4), not a count
	MS        float64 `json:"ms"`                         // time to the response
	// Rows is the number of result rows found in the response, -1 when it holds no row to count.
	Rows int `json:"rows"`
	// Truncation is stated, silent or none for a statement that has a result to cut, empty otherwise (B7, or no answer).
	Truncation string `json:"truncation,omitempty"`
	// Values, Wrong and Reformatted are B6: the number of values compared, every value the response does not hold, and
	// every value it holds in another notation of exactly the same value (an RFC 3339 time, an interval object) rather
	// than the database's text, each as "row <id> <column>: want <text>". The other values are exact.
	Values      int      `json:"values,omitempty"`
	Wrong       []string `json:"wrong,omitempty"`
	Reformatted []string `json:"reformatted,omitempty"`
	// NamesColumn and Hint are B7: the error names the misspelled column, and it gives a hint (a suggestion or the
	// columns that exist).
	NamesColumn bool `json:"names_column,omitempty"`
	Hint        bool `json:"hint,omitempty"`
	// StayedUp is true when the server still answered a SELECT 1 after the call, or, when the connection was lost,
	// its container was still running.
	StayedUp bool   `json:"stayed_up"`
	Note     string `json:"note,omitempty"` // first line of an error
	File     string `json:"file,omitempty"` // the response as received, under receive/
}

// ReceiveRow is one part B statement with the truth read from the database and one cell per server.
type ReceiveRow struct {
	ID            string                 `json:"id"`
	SQL           string                 `json:"sql"`
	TrueRows      int                    `json:"true_rows"`
	TrueCellBytes int                    `json:"true_cell_bytes,omitempty"` // B4: length of the payload text
	Cells         map[string]ReceiveCell `json:"cells"`
}

// ReceiveTools is the size of one server's tool definitions (tools/list), counted like the responses.
type ReceiveTools struct {
	Server    string `json:"server"`
	Tools     int    `json:"tools"`
	Bytes     int    `json:"bytes"` // the JSON of the tools/list result
	Tokens    int    `json:"tokens"`
	Estimated bool   `json:"tokens_estimated,omitempty"`
}

// Receive is receive.json.
type Receive struct {
	Env Env `json:"env"`
	// Bounds says how every call was bounded: call timeout, response cap, container memory.
	Bounds string         `json:"bounds"`
	Tools  []ReceiveTools `json:"tools"`
	Rows   []ReceiveRow   `json:"rows"`
}
