package safesql

import "strings"

// HasParams reports whether sql holds a `$n` parameter reference. It reads the lexer tokens, so a `$n` inside a
// string, a quoted identifier, a dollar quote or a comment does not count. A text the lexer cannot scan reports
// false: Postgres refuses it anyway.
func HasParams(sql string) bool {
	if !strings.Contains(sql, "$") {
		return false
	}
	toks, err := scan(sql)
	if err != nil {
		return false
	}
	for _, t := range toks {
		if t.Kind == "PARAM" {
			return true
		}
	}
	return false
}
