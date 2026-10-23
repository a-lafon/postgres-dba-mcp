package format

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestShortenThreshold(t *testing.T) {
	for _, c := range []struct {
		n    int
		want string
	}{{5, "xxxxx"}, {25, strings.Repeat("x", 25)}, {26, "xxxxx…[+21 chars]"}} {
		t.Run(strconv.Itoa(c.n), func(t *testing.T) {
			if got := Shorten(strings.Repeat("x", c.n), 5); got != c.want {
				t.Errorf("Shorten(%d x) = %q, want %q", c.n, got, c.want)
			}
		})
	}
}

func TestErrorContext(t *testing.T) {
	long := strings.Repeat("a", 100) + " nme " + strings.Repeat("b", 100)
	for _, c := range []struct {
		name string
		sql  string
		pos  int32
		want string
	}{
		{"single line", "SELECT nme FROM film", 8, "LINE 1: SELECT nme FROM film\n               ^"},
		{"second line", "SELECT id,\n       nme\nFROM film", 19, "LINE 2:        nme\n               ^"},
		{"long line cut around the position", long, 102,
			"LINE 1: ..." + strings.Repeat("a", 39) + " nme " + strings.Repeat("b", 36) + "...\n" +
				strings.Repeat(" ", len("LINE 1: ...")+40) + "^"},
		{"end of input", "SELECT", 7, "LINE 1: SELECT\n              ^"},
		{"multi-byte characters count once", "SELECT 'é', nme", 13, "LINE 1: SELECT 'é', nme\n                    ^"},
	} {
		t.Run(c.name, func(t *testing.T) {
			err := WithSQL(&pgconn.PgError{Message: "boom", Code: "42703", Position: c.pos}, c.sql)
			want := "ERROR: boom (SQLSTATE 42703)\n" + c.want + "\nPOSITION: " + strconv.Itoa(int(c.pos))
			if got := Error(err); got != want {
				t.Errorf("Error =\n%s\nwant\n%s", got, want)
			}
		})
	}
	t.Run("no statement attached", func(t *testing.T) {
		got := Error(&pgconn.PgError{Message: "boom", Code: "42703", Position: 3})
		if got != "ERROR: boom (SQLSTATE 42703)\nPOSITION: 3" {
			t.Errorf("Error = %q", got)
		}
	})
}
