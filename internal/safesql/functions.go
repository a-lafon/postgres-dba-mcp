package safesql

import (
	"sort"
	"strings"
)

// call is one function call found in a statement.
type call struct {
	schema string // second-to-last name part, lowercase, empty if unqualified
	name   string // last name part, lowercase
	full   string // all name parts joined by '.', as parsed
	// rawSchema and rawName are those two parts with the case the parser
	// reported, which is the spelling pg_proc holds for a quoted identifier.
	rawSchema, rawName string
	loc                float64
}

// HasFuncCall reports whether sql calls at least one function, that is
// whether running only part of it could leave anything behind. A statement
// the parser cannot read takes the cautious side.
func HasFuncCall(sql string) bool {
	stmts, err := parseStatements(sql)
	if err != nil {
		return true
	}
	return len(funcCalls(stmts)) > 0
}

// funcCalls returns every FuncCall anywhere under n, in source order. The
// walk is generic on purpose: no clause can be forgotten.
func funcCalls(n any) []call {
	var calls []call
	walk(n, func(key string, body map[string]any) {
		if key != "FuncCall" {
			return
		}
		var parts []string
		names, _ := body["funcname"].([]any)
		for _, p := range names {
			if _, s := node(p); s != nil {
				v, _ := s["sval"].(string)
				parts = append(parts, v)
			}
		}
		if len(parts) == 0 {
			return
		}
		c := call{
			name:    strings.ToLower(parts[len(parts)-1]),
			rawName: parts[len(parts)-1],
			full:    strings.Join(parts, "."),
		}
		if len(parts) > 1 {
			c.schema = strings.ToLower(parts[len(parts)-2])
			c.rawSchema = parts[len(parts)-2]
		}
		c.loc, _ = body["location"].(float64)
		calls = append(calls, c)
	})
	sort.SliceStable(calls, func(i, j int) bool { return calls[i].loc < calls[j].loc })
	return calls
}
