package safesql

import (
	"reflect"
	"strings"
)

// RewriteTypedParams rewrites the typed-literal form that pg_stat_statements produces, `<type> $n`, to the
// valid `$n::<type>` that the literal meant (`interval $1` becomes `$1::interval`, `interval $1 day to second`
// becomes `$1::interval day to second`). `<type> $n` is not SQL, so neither the parser nor Postgres accepts it.
//
// Only lexer tokens are rewritten: a `$1` in a string, a quoted identifier, a dollar quote or a comment is
// untouched. The text is returned as given when it has nothing to rewrite, cannot be scanned, or would not keep
// its parse tree (locations aside). The rewrite only
// adds a cast around a parameter: it adds no function call and no statement, so the policy check must run on
// the returned text, which is the text to execute.
func RewriteTypedParams(sql string) string {
	if !strings.Contains(sql, "$") {
		return sql
	}
	toks, err := scan(sql)
	if err != nil {
		return sql
	}
	var code []scanToken // comments never take part in a match
	for _, t := range toks {
		if t.Kind != "C_COMMENT" && t.Kind != "SQL_COMMENT" {
			code = append(code, t)
		}
	}
	var b strings.Builder
	last := 0
	for i := 0; i < len(code); {
		param, end, ok := matchTyped(sql, code, i)
		if !ok {
			i++
			continue
		}
		// <type> $n <fields>  ->  $n::<type> <fields>, the fields keeping their original spacing.
		b.WriteString(sql[last:code[i].Start])
		b.WriteString(sql[code[param].Start:code[param].End])
		b.WriteString("::")
		b.WriteString(sql[code[i].Start:code[param-1].End])
		b.WriteString(sql[code[param].End:code[end-1].End])
		last, i = code[end-1].End, end
	}
	if last == 0 {
		return sql
	}
	out := b.String() + sql[last:]
	// The parser reads `<type> $n` as a cast of the parameter, so a true match keeps the parse tree. A keyword
	// that legitimately precedes a parameter (`json_object('a' VALUE $1)`, `xmlparse(CONTENT $1)`) changes it:
	// keep the text as given then.
	before, err := parse(sql)
	if err != nil {
		return sql
	}
	after, err := parse(out)
	if err != nil || !reflect.DeepEqual(stripLocations(before), stripLocations(after)) {
		return sql
	}
	return out
}

// stripLocations removes the "location" keys of a parse tree, which are the only thing a rewrite moves.
func stripLocations(n any) any {
	switch v := n.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, x := range v {
			if k != "location" {
				out[k] = stripLocations(x)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = stripLocations(x)
		}
		return out
	}
	return n
}

// noType are the non-reserved keywords that can stand right before a parameter in valid SQL (`ORDER BY $1`,
// `FETCH FIRST $1 ROWS`, `BETWEEN $1 AND $2`, `AT TIME ZONE $1`, `LIKE … ESCAPE $1`, `ROWS $1 PRECEDING`).
// Reserved keywords (LIMIT, WHERE, AND, CASE, …) are excluded by their class.
var noType = map[string]bool{
	"by": true, "first": true, "next": true, "zone": true, "escape": true, "between": true, "rows": true,
	"range": true, "groups": true, "of": true, "only": true,
}

// matchTyped reads `<type> $n [interval fields]` starting at code[i]. It returns the index of the parameter and
// the index after the match. The type is code[i:param].
func matchTyped(sql string, code []scanToken, i int) (param, end int, ok bool) {
	text := func(j int) string {
		if j < 0 || j >= len(code) {
			return ""
		}
		return strings.ToLower(sql[code[j].Start:code[j].End])
	}
	isName := func(j int) bool {
		t := code[j]
		return t.Kind == "IDENT" || (t.Keyword == "UNRESERVED_KEYWORD" || t.Keyword == "COL_NAME_KEYWORD") && !noType[text(j)]
	}
	if !isName(i) || text(i-1) == "." {
		return 0, 0, false
	}
	j := i + 1
	for text(j) == "." && j+1 < len(code) && isName(j+1) { // schema.type
		j += 2
	}
	plain := j == i+1 // a bare name: the only case with multi-word forms
	first := text(i)
	if plain && code[i].Kind != "IDENT" { // multi-word names: double precision, character varying, national char, bit varying
		switch first {
		case "double":
			if text(j) == "precision" {
				j++
			}
		case "character", "char", "nchar", "bit":
			if text(j) == "varying" {
				j++
			}
		case "national":
			if text(j) == "character" || text(j) == "char" {
				j++
				if text(j) == "varying" {
					j++
				}
			}
		}
	}
	if text(j) == "(" { // type modifier: numeric(5,2), interval(3), char(2)
		k := j + 1
		for k < len(code) && (code[k].Kind == "ICONST" || text(k) == ",") {
			k++
		}
		if k == j+1 || text(k) != ")" {
			return 0, 0, false
		}
		j = k + 1
	}
	if plain && (first == "timestamp" || first == "time") && (text(j) == "with" || text(j) == "without") &&
		text(j+1) == "time" && text(j+2) == "zone" {
		j += 3
	}
	if j >= len(code) || code[j].Kind != "PARAM" {
		return 0, 0, false
	}
	end = j + 1
	if plain && first == "interval" {
		end = intervalFields(text, end)
	}
	return j, end, true
}

// intervalFields returns the index after the optional field list that follows `interval $n`: YEAR [TO MONTH],
// MONTH, DAY [TO HOUR|MINUTE|SECOND], HOUR [TO MINUTE|SECOND], MINUTE [TO SECOND], SECOND [(n)]. Postgres
// validates the combination.
func intervalFields(text func(int) string, j int) int {
	field := func(j int) bool {
		switch text(j) {
		case "year", "month", "day", "hour", "minute", "second":
			return true
		}
		return false
	}
	if !field(j) {
		return j
	}
	j++
	if text(j) == "to" && field(j+1) {
		j += 2
	}
	if text(j-1) == "second" && text(j) == "(" && text(j+2) == ")" {
		j += 3
	}
	return j
}
