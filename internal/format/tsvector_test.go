package format

import "testing"

func TestSummarizeTSVector(t *testing.T) {
	for _, tt := range []struct {
		name, in, want string
		ok             bool
	}{
		{"empty", ``, "[tsvector: 0 lexemes]", true},
		{"one", `'a'`, "[tsvector: 1 lexeme]", true},
		{"positions", `'amaz':4 'boat':20 'boy':16`, "[tsvector: 3 lexemes]", true},
		{"weights and several positions", `'a':1,3A,5B 'b':2C,4D`, "[tsvector: 2 lexemes]", true},
		{"no positions", `'a' 'b' 'c'`, "[tsvector: 3 lexemes]", true},
		{"doubled quote", `'it''s':1,3A 'a b' 'c\\d':2 'e\'f'`, "[tsvector: 4 lexemes]", true},
		{"quote only", `''''`, "[tsvector: 1 lexeme]", true},
		{"colon and space inside lexeme", `'a:1 b' 'c'`, "[tsvector: 2 lexemes]", true},
		{"trailing backslash escapes the quote", `'a\'`, "", false},
		{"unterminated", `'abc`, "", false},
		{"unquoted", `abc:1`, "", false},
		{"junk after lexeme", `'a'b`, "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := SummarizeTSVector(tt.in)
			if got != tt.want || ok != tt.ok {
				t.Errorf("SummarizeTSVector(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
			}
		})
	}
}
