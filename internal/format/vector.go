package format

// SummarizeVector returns "[vector: <n> dims]" for the text form of a pgvector value, "[0.1,-0.2,3e-05]". ok is
// false when the text is not a well-formed one, so that the caller can keep the original text. pgvector is an
// extension, so its type has no fixed OID and only the text form tells its values apart; the caller must already
// have ruled out the built-in types, several of which (json, an int array cast to text) render the same way.
func SummarizeVector(s string) (summary string, ok bool) {
	if len(s) < 2 || s[0] != '[' || s[len(s)-1] != ']' {
		return "", false
	}
	n, elem := 1, 0 // a vector has at least one dimension, so there is always one element to read
	for i := 1; i < len(s)-1; i++ {
		switch c := s[i]; {
		case c == ',':
			if elem == 0 {
				return "", false
			}
			n, elem = n+1, 0
		case c >= '0' && c <= '9', c == '-', c == '+', c == '.', c == 'e', c == 'E':
			elem++
		default:
			return "", false
		}
	}
	if elem == 0 {
		return "", false
	}
	return "[vector: " + plural(n, "dim") + "]", true
}
