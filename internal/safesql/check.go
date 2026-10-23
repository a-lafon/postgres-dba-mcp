package safesql

import (
	"errors"
	"fmt"
	"strings"
)

// Kind is the class of an accepted statement.
type Kind int

// The statement kinds. The zero Kind means "no single statement".
const (
	// Select is a SelectStmt (SELECT, VALUES, TABLE, set operations, WITH)
	// that can run inside DECLARE CURSOR.
	Select Kind = iota + 1
	// Explain is an EXPLAIN statement.
	Explain
	// Show is a SHOW statement.
	Show
	// Other is any other statement, or a SELECT with an INTO clause or a
	// data-modifying CTE. Only Classify returns it.
	Other
)

// Statement describes an accepted statement.
type Statement struct {
	Kind Kind
	// Analyze is true when Kind is Explain and the ANALYZE option is on.
	Analyze bool
}

const (
	errEmpty      = "restricted mode: empty query"
	errMulti      = "restricted mode: only one statement per call is allowed"
	errExplain    = "restricted mode: EXPLAIN is only allowed on SELECT, VALUES, TABLE or WITH … SELECT"
	errInto       = "restricted mode: SELECT … INTO is not allowed"
	errLocking    = "restricted mode: row locking (FOR UPDATE/SHARE) is not allowed"
	errDataWith   = "restricted mode: data-modifying WITH (INSERT/UPDATE/DELETE/MERGE) is not allowed"
	errNotAllowed = "restricted mode: %s statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW)"
)

// Check accepts sql only when it is exactly one SELECT-like statement (see
// the sql-safety spec) whose every function call p allows. The error text is
// the message returned to the client.
func Check(sql string, p *Policy) (Statement, error) {
	if p == nil {
		return Statement{}, errors.New("safesql: nil policy")
	}
	stmts, err := parseStatements(sql)
	if err != nil {
		return Statement{}, err
	}
	switch len(stmts) {
	case 0:
		return Statement{}, errors.New(errEmpty)
	case 1:
	default:
		return Statement{}, errors.New(errMulti)
	}
	name, body := statementNode(stmts[0])
	var st Statement
	switch name {
	case "SelectStmt":
		if err := selectRules(body); err != nil {
			return Statement{}, err
		}
		st.Kind = Select
	case "ExplainStmt":
		innerName, inner := node(body["query"])
		if innerName != "SelectStmt" {
			return Statement{}, errors.New(errExplain)
		}
		if err := selectRules(inner); err != nil {
			return Statement{}, err
		}
		st = Statement{Kind: Explain, Analyze: explainAnalyze(body)}
	case "VariableShowStmt":
		st.Kind = Show
	default:
		return Statement{}, fmt.Errorf(errNotAllowed, name)
	}
	for _, c := range funcCalls(stmts[0]) {
		if p.refuses(c) {
			return Statement{}, fmt.Errorf("restricted mode: function %s() is not allowed", c.full)
		}
	}
	return st, nil
}

// Classify parses sql and reports how many statements it holds and, when
// there is exactly one, its Kind (zero otherwise). It applies no statement or
// function rule: it fails only on syntax errors.
func Classify(sql string) (count int, kind Kind, err error) {
	stmts, err := parseStatements(sql)
	if err != nil {
		return 0, 0, err
	}
	if len(stmts) != 1 {
		return len(stmts), 0, nil
	}
	name, body := statementNode(stmts[0])
	switch name {
	case "SelectStmt":
		if f := scanSelect(body); f.into || f.dml {
			return 1, Other, nil
		}
		return 1, Select, nil
	case "ExplainStmt":
		return 1, Explain, nil
	case "VariableShowStmt":
		return 1, Show, nil
	}
	return 1, Other, nil
}

// parseStatements parses sql and returns the raw statements.
func parseStatements(sql string) ([]any, error) {
	tree, err := parse(sql)
	if err != nil {
		return nil, err
	}
	root, _ := tree.(map[string]any)
	stmts, _ := root["stmts"].([]any)
	return stmts, nil
}

// statementNode returns the node name and body of a RawStmt entry.
func statementNode(raw any) (string, map[string]any) {
	m, _ := raw.(map[string]any)
	return node(m["stmt"])
}

// node unpacks a {"NodeName": {...}} wrapper.
func node(n any) (string, map[string]any) {
	m, ok := n.(map[string]any)
	if !ok {
		return "", nil
	}
	for k, v := range m {
		body, _ := v.(map[string]any)
		return k, body
	}
	return "", nil
}

// walk calls fn for every value of the tree that is an object, with the key
// that holds it: a node wrapper key ("FuncCall") or a field name.
func walk(n any, fn func(key string, body map[string]any)) {
	switch v := n.(type) {
	case map[string]any:
		for k, c := range v {
			if body, ok := c.(map[string]any); ok {
				fn(k, body)
			}
			walk(c, fn)
		}
	case []any:
		for _, c := range v {
			walk(c, fn)
		}
	}
}

type selectFindings struct{ into, lock, dml bool }

// scanSelect looks, at every nesting level under n, for INTO clauses,
// locking clauses and data-modifying CTEs.
func scanSelect(n map[string]any) selectFindings {
	var f selectFindings
	check := func(key string, body map[string]any) {
		switch key {
		case "SelectStmt":
			f.into = f.into || body["intoClause"] != nil
			if l, _ := body["lockingClause"].([]any); len(l) > 0 {
				f.lock = true
			}
		case "CommonTableExpr":
			switch q, _ := node(body["ctequery"]); q {
			case "InsertStmt", "UpdateStmt", "DeleteStmt", "MergeStmt":
				f.dml = true
			}
		}
	}
	// n itself is a SelectStmt body: check it as a node, then its subtree.
	check("SelectStmt", n)
	walk(n, check)
	return f
}

// selectRules applies the SELECT-form rules to a SelectStmt body.
func selectRules(body map[string]any) error {
	f := scanSelect(body)
	switch {
	case f.into:
		return errors.New(errInto)
	case f.lock:
		return errors.New(errLocking)
	case f.dml:
		return errors.New(errDataWith)
	}
	return nil
}

// explainAnalyze reads the boolean value of the ANALYZE option of an
// ExplainStmt body. Unreadable values count as true: it is the cautious side.
func explainAnalyze(body map[string]any) bool {
	opts, _ := body["options"].([]any)
	on := false
	for _, o := range opts {
		name, def := node(o)
		if name != "DefElem" {
			continue
		}
		if n, _ := def["defname"].(string); n != "analyze" && n != "analyse" {
			continue
		}
		on = optionBool(def["arg"])
	}
	return on
}

// optionBool mirrors Postgres' boolean option parsing; no argument means on.
func optionBool(arg any) bool {
	kind, v := node(arg)
	switch kind {
	case "":
		return true
	case "Integer":
		i, _ := v["ival"].(float64)
		return i != 0
	case "String":
		s, _ := v["sval"].(string)
		s = strings.ToLower(s)
		switch {
		case s == "":
			return true
		case strings.HasPrefix("false", s), strings.HasPrefix("no", s), s == "off", s == "of", s == "0":
			return false
		}
		return true
	}
	return true
}
