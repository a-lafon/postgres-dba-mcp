package hypo

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
)

const maxColumns = 32

var plainIdent = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_$]*`)

// splitName parses an identifier or a dot-separated chain of them, each plain or double-quoted.
// Unquoted parts are folded to lower case, as Postgres does.
func splitName(s string) ([]string, bool) {
	var parts []string
	for {
		var part string
		if strings.HasPrefix(s, `"`) {
			var b strings.Builder
			i := 1
			for ; i < len(s); i++ {
				if s[i] != '"' {
					b.WriteByte(s[i])
				} else if i+1 < len(s) && s[i+1] == '"' {
					b.WriteByte('"')
					i++
				} else {
					break
				}
			}
			if i >= len(s) || b.Len() == 0 {
				return nil, false
			}
			part, s = b.String(), s[i+1:]
		} else {
			m := plainIdent.FindString(s)
			if m == "" {
				return nil, false
			}
			part, s = strings.ToLower(m), s[len(m):]
		}
		parts = append(parts, part)
		if s == "" {
			return parts, true
		}
		if s[0] != '.' {
			return nil, false
		}
		s = s[1:]
	}
}

// Validate checks the definition of idx without touching the database.
func Validate(idx Index) error {
	_, err := ddl(idx)
	return err
}

// ddl validates idx and returns its CREATE INDEX text, with every identifier quoted.
func ddl(idx Index) (string, error) {
	invalid := func(format string, args ...any) (string, error) {
		return "", fmt.Errorf("invalid hypothetical index: "+format, args...)
	}
	table, ok := splitName(idx.Table)
	if !ok || len(table) > 2 {
		return invalid("table %q is not a valid [schema.]name", idx.Table)
	}
	switch n := len(idx.Columns); {
	case n == 0:
		return invalid("columns must not be empty")
	case n > maxColumns:
		return invalid("too many columns (max %d)", maxColumns)
	}
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		p, ok := splitName(c)
		if !ok || len(p) != 1 {
			return invalid("column %q is not a valid identifier", c)
		}
		cols[i] = pgx.Identifier{p[0]}.Sanitize()
	}
	using := "btree"
	if idx.Using != "" {
		if plainIdent.FindString(idx.Using) != idx.Using {
			return invalid("using %q is not a valid access method name", idx.Using)
		}
		using = strings.ToLower(idx.Using)
	}
	return fmt.Sprintf("CREATE INDEX ON %s USING %s (%s)", pgx.Identifier(table).Sanitize(), using, strings.Join(cols, ", ")), nil
}
