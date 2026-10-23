package safesql

import "sort"

// Use is how a column is used in a condition clause. A smaller value is a stronger use for an index.
type Use int

// The uses, from the strongest to the weakest for an index.
const (
	// UseEq is an equality: =, IN, = ANY, IS NULL or a join equality.
	UseEq Use = iota
	// UseRange is a range: <, <=, >, >=, BETWEEN or LIKE.
	UseRange
	// UseSort is an ORDER BY key.
	UseSort
	// UseOther is any other use: a function argument, <>, IS NOT NULL, a bare boolean.
	UseOther
)

// RelRef is a relation of a FROM clause. A RelRef with an empty Name is opaque: a subquery, a CTE, a function
// or any other FROM item whose columns the catalog cannot tell.
type RelRef struct{ Schema, Name, Alias string }

// ColumnRef is a column reference found in a condition clause.
type ColumnRef struct {
	Column string
	Use    Use
	// Pos is the byte offset of the reference in the SQL text.
	Pos int
	// Rels is the one relation the reference is qualified with, or every relation of its scope when it is
	// unqualified. It is empty when the qualifier matches no relation of the scope.
	Rels []RelRef
}

// Refs returns the column references of the condition clauses of sql, in source order: WHERE, every JOIN
// condition at any nesting depth, HAVING and ORDER BY of each SELECT scope, subqueries and CTEs included.
// The select list and GROUP BY are not collected. A multi-statement text is read as its first statement.
func Refs(sql string) ([]ColumnRef, error) {
	c, err := collect(sql)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(c.refs, func(i, j int) bool { return c.refs[i].Pos < c.refs[j].Pos })
	return c.refs, nil
}

// Relations returns every relation of the FROM clauses of sql, at any depth, in source order. A CTE
// reference, a subquery or a function appears as an opaque RelRef (empty Name).
func Relations(sql string) ([]RelRef, error) {
	c, err := collect(sql)
	if err != nil {
		return nil, err
	}
	return c.rels, nil
}

// refScope is the FROM list of one SELECT, with the scope that encloses it.
type refScope struct {
	rels   []RelRef
	parent *refScope
}

type refCollector struct {
	ctes map[string]bool
	refs []ColumnRef
	rels []RelRef
}

func collect(sql string) (*refCollector, error) {
	tree, err := parse(sql)
	if err != nil {
		return nil, err
	}
	c := &refCollector{ctes: map[string]bool{}}
	root, _ := tree.(map[string]any)
	stmts, _ := root["stmts"].([]any)
	if len(stmts) == 0 {
		return c, nil
	}
	if name, body := statementNode(stmts[0]); name == "SelectStmt" {
		c.selectStmt(body, nil)
	}
	return c, nil
}

// selectStmt collects one SELECT scope. Set operations and CTEs are scopes of their own.
func (c *refCollector) selectStmt(body map[string]any, parent *refScope) {
	if with, _ := body["withClause"].(map[string]any); with != nil {
		ctes, _ := with["ctes"].([]any)
		for _, n := range ctes {
			if _, cte := node(n); cte != nil {
				if name, _ := cte["ctename"].(string); name != "" {
					c.ctes[name] = true
				}
			}
		}
		for _, n := range ctes {
			if _, cte := node(n); cte != nil {
				if q, b := node(cte["ctequery"]); q == "SelectStmt" {
					c.selectStmt(b, nil)
				}
			}
		}
	}
	if op, _ := body["op"].(string); op != "" && op != "SETOP_NONE" {
		for _, side := range []string{"larg", "rarg"} {
			if b, _ := body[side].(map[string]any); b != nil {
				c.selectStmt(b, parent)
			}
		}
		return
	}
	sc := &refScope{parent: parent}
	var quals, subs []any
	from, _ := body["fromClause"].([]any)
	for _, f := range from {
		c.fromItem(f, sc, &quals, &subs)
	}
	for _, q := range quals {
		c.expr(q, UseOther, sc)
	}
	for _, s := range subs {
		c.expr(s, UseOther, sc)
	}
	c.expr(body["whereClause"], UseOther, sc)
	c.expr(body["havingClause"], UseOther, sc)
	sorts, _ := body["sortClause"].([]any)
	for _, s := range sorts {
		if _, sb := node(s); sb != nil {
			c.expr(sb["node"], UseSort, sc)
		}
	}
	targets, _ := body["targetList"].([]any)
	for _, t := range targets {
		c.nested(t, sc)
	}
}

// fromItem adds the relations of one FROM item to sc and queues JOIN conditions and subqueries.
func (c *refCollector) fromItem(n any, sc *refScope, quals, subs *[]any) {
	name, body := node(n)
	switch name {
	case "RangeVar":
		rel := RelRef{Schema: str(body["schemaname"]), Name: str(body["relname"])}
		if a, _ := body["alias"].(map[string]any); a != nil {
			rel.Alias = str(a["aliasname"])
		}
		if rel.Schema == "" && c.ctes[rel.Name] {
			rel = RelRef{Alias: rel.Alias}
			if rel.Alias == "" {
				rel.Alias = str(body["relname"])
			}
		}
		sc.rels = append(sc.rels, rel)
		c.rels = append(c.rels, rel)
	case "JoinExpr":
		c.fromItem(body["larg"], sc, quals, subs)
		c.fromItem(body["rarg"], sc, quals, subs)
		if body["quals"] != nil {
			*quals = append(*quals, body["quals"])
		}
	case "RangeSubselect":
		rel := RelRef{}
		if a, _ := body["alias"].(map[string]any); a != nil {
			rel.Alias = str(a["aliasname"])
		}
		sc.rels = append(sc.rels, rel)
		c.rels = append(c.rels, rel)
		*subs = append(*subs, body["subquery"])
	default:
		sc.rels = append(sc.rels, RelRef{})
		c.rels = append(c.rels, RelRef{})
		if body != nil {
			*subs = append(*subs, n) // a function argument may hold a subquery
		}
	}
}

// nested walks n only to find the SELECT scopes inside it; its column references are not collected.
func (c *refCollector) nested(n any, sc *refScope) {
	walkNodes(n, func(key string, body map[string]any) bool {
		if key == "SelectStmt" {
			c.selectStmt(body, sc)
			return false
		}
		return true
	})
}

// walkNodes calls fn for every node wrapper under n, parents first; fn returns false to skip the subtree.
func walkNodes(n any, fn func(key string, body map[string]any) bool) {
	switch v := n.(type) {
	case map[string]any:
		for k, child := range v {
			if body, ok := child.(map[string]any); ok && fn(k, body) {
				walkNodes(body, fn)
			} else if !ok {
				walkNodes(child, fn)
			}
		}
	case []any:
		for _, child := range v {
			walkNodes(child, fn)
		}
	}
}

// expr collects the column references of a condition expression. use is the use of a ColumnRef that is
// the expression itself.
func (c *refCollector) expr(n any, use Use, sc *refScope) {
	name, body := node(n)
	switch name {
	case "":
		return
	case "ColumnRef":
		c.emit(body, use, sc)
	case "A_Expr":
		u := exprUse(body)
		for _, side := range []string{"lexpr", "rexpr"} {
			if k, _ := node(body[side]); k == "ColumnRef" {
				c.expr(body[side], u, sc)
			} else {
				c.expr(body[side], UseOther, sc)
			}
		}
	case "BoolExpr":
		args, _ := body["args"].([]any)
		for _, a := range args {
			c.expr(a, use, sc)
		}
	case "NullTest":
		u := UseOther
		if str(body["nulltesttype"]) == "IS_NULL" {
			u = UseEq
		}
		c.expr(body["arg"], u, sc)
	case "SubLink":
		u := UseOther
		if str(body["subLinkType"]) == "ANY_SUBLINK" {
			u = UseEq
		}
		c.expr(body["testexpr"], u, sc)
		c.expr(body["subselect"], UseOther, sc)
	case "SelectStmt":
		c.selectStmt(body, sc)
	default:
		for _, child := range body {
			c.exprAny(child, sc)
		}
	}
}

// exprAny descends into a field value: a node wrapper, an unwrapped body or a list.
func (c *refCollector) exprAny(v any, sc *refScope) {
	switch t := v.(type) {
	case map[string]any:
		if len(t) == 1 {
			c.expr(t, UseOther, sc)
			return
		}
		for _, child := range t {
			c.exprAny(child, sc)
		}
	case []any:
		for _, child := range t {
			c.exprAny(child, sc)
		}
	}
}

// emit records a ColumnRef. A reference qualified with an unknown relation is dropped.
func (c *refCollector) emit(body map[string]any, use Use, sc *refScope) {
	var parts []string
	fields, _ := body["fields"].([]any)
	for _, f := range fields {
		k, s := node(f)
		if k != "String" {
			return // a star
		}
		parts = append(parts, str(s["sval"]))
	}
	pos, _ := body["location"].(float64)
	ref := ColumnRef{Column: parts[len(parts)-1], Use: use, Pos: int(pos)}
	switch len(parts) {
	case 1:
		ref.Rels = append([]RelRef(nil), sc.rels...)
	case 2, 3:
		for s := sc; s != nil && ref.Rels == nil; s = s.parent {
			for _, r := range s.rels {
				if matches(r, parts[:len(parts)-1]) {
					ref.Rels = []RelRef{r}
					break
				}
			}
		}
	default:
		return
	}
	c.refs = append(c.refs, ref)
}

// matches reports whether the qualifier (table or schema.table) designates r.
func matches(r RelRef, qual []string) bool {
	if len(qual) == 2 {
		return r.Alias == "" && r.Name != "" && r.Schema == qual[0] && r.Name == qual[1]
	}
	if r.Alias != "" {
		return r.Alias == qual[0]
	}
	return r.Name != "" && r.Name == qual[0]
}

// exprUse classifies an A_Expr by its kind and operator.
func exprUse(body map[string]any) Use {
	op := ""
	if names, _ := body["name"].([]any); len(names) > 0 {
		_, s := node(names[len(names)-1])
		op = str(s["sval"])
	}
	switch str(body["kind"]) {
	case "AEXPR_OP", "AEXPR_OP_ANY":
		switch op {
		case "=":
			return UseEq
		case "<", "<=", ">", ">=":
			return UseRange
		}
	case "AEXPR_IN":
		if op == "=" {
			return UseEq
		}
	case "AEXPR_LIKE", "AEXPR_ILIKE":
		if op == "~~" || op == "~~*" {
			return UseRange
		}
	case "AEXPR_BETWEEN", "AEXPR_BETWEEN_SYM":
		return UseRange
	}
	return UseOther
}

func str(v any) string {
	s, _ := v.(string)
	return s
}
