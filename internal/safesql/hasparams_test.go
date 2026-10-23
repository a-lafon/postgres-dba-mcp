package safesql

import "testing"

func TestHasParams(t *testing.T) {
	tests := []struct {
		sql  string
		want bool
	}{
		{"SELECT * FROM t WHERE a = $1 LIMIT $2", true},
		{"SELECT $1::int", true},
		{"SELECT now() - interval $1", true},
		{"SELECT 1 /* $1 */, $2", true},
		{"SELECT '$1'", false},
		{"SELECT 'it''s $1'", false},
		{"SELECT $$text $1$$", false},
		{"SELECT $q$ $1 $q$ || $$a$$", false},
		{`SELECT 1 AS "$1"`, false},
		{"SELECT 1 -- $1", false},
		{"SELECT 1 /* $1 */", false},
		{"SELECT $a$ unterminated", false},
		{"SELECT 1", false},
		{"", false},
	}
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			if got := HasParams(tt.sql); got != tt.want {
				t.Errorf("HasParams(%q) = %v, want %v", tt.sql, got, tt.want)
			}
		})
	}
}
