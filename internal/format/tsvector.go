package format

import "strings"

// SummarizeTSVector returns "[tsvector: <n> lexemes]" for the text form of a tsvector ("[tsvector: 1 lexeme]" for
// one). Lexemes are single-quoted, a quote inside is doubled, a backslash escapes the next character, and each is
// optionally followed by ":positions"; entries are separated by spaces. ok is false when the text is not a
// well-formed tsvector, so that the caller can keep the original text.
func SummarizeTSVector(s string) (summary string, ok bool) {
	n := 0
	for i := 0; i < len(s); {
		switch s[i] {
		case ' ':
			i++
		case '\'':
			end, closed := lexemeEnd(s, i+1)
			if !closed {
				return "", false
			}
			n++
			i = end
			if i < len(s) && s[i] == ':' { // positions: up to the next space
				if j := strings.IndexByte(s[i:], ' '); j < 0 {
					i = len(s)
				} else {
					i += j
				}
			} else if i < len(s) && s[i] != ' ' {
				return "", false
			}
		default:
			return "", false
		}
	}
	return "[tsvector: " + plural(n, "lexeme") + "]", true
}

// lexemeEnd returns the index after the closing quote of the lexeme whose text starts at i.
func lexemeEnd(s string, i int) (end int, closed bool) {
	for i < len(s) {
		switch s[i] {
		case '\\':
			i += 2
		case '\'':
			if i+1 < len(s) && s[i+1] == '\'' {
				i += 2
				continue
			}
			return i + 1, true
		default:
			i++
		}
	}
	return 0, false
}
