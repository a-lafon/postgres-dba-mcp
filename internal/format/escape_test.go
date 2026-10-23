package format

import (
	"encoding/csv"
	"strings"
	"testing"
)

// decodeField reverses the rendering of one CSV field: `\N` bare is NULL (ok false), otherwise
// `\\` is `\`, `\n` `\r` `\t` are the control characters and any other `\` is literal.
func decodeField(f string, quoted bool) (string, bool) {
	if f == `\N` && !quoted {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(f); i++ {
		if f[i] == '\\' && i+1 < len(f) {
			switch f[i+1] {
			case '\\':
				b.WriteByte('\\')
				i++
				continue
			case 'n':
				b.WriteByte('\n')
				i++
				continue
			case 'r':
				b.WriteByte('\r')
				i++
				continue
			case 't':
				b.WriteByte('\t')
				i++
				continue
			}
		}
		b.WriteByte(f[i])
	}
	return b.String(), true
}

func TestEscape(t *testing.T) {
	tests := []struct {
		name string
		in   string // nil-free: NULL is tested separately
		want string // the rendered field
	}{
		{"newline", "a\nb", `a\nb`},
		{"carriage return", "a\rb", `a\rb`},
		{"tab", "a\tb", `a\tb`},
		{"backslash then n", `a\nb`, `a\\nb`},
		{"backslash then r", `a\rb`, `a\\rb`},
		{"backslash then t", `a\tb`, `a\\tb`},
		{"double backslash", `a\\b`, `a\\\b`},
		{"backslash then newline", "a\\\nb", `a\\\nb`},
		{"backslash alone", `\`, `\`},
		{"trailing backslash", `a\`, `a\`},
		{"backslash x text", `\x41`, `\x41`},
		{"bytea", `\xdeadbeef`, `\xdeadbeef`},
		{"windows path", `C:\temp\new\table`, `C:\\temp\\new\\table`},
		{"windows path without escapes", `C:\Users\bob`, `C:\Users\bob`},
		{"literal N text", `\N`, `"\N"`},
		{"empty", ``, `""`},
		{"quotes", `say "hi"`, `"say ""hi"""`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := csvField(escape(tt.in))
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestRenderRoundTrip(t *testing.T) {
	vals := []string{
		"line1\nline2", "a\rb", "a\tb", `a\nb`, `a\\b`, `\`, `a\`, `\x41`, `\xdeadbeef`, `C:\temp\new\table`,
		"a\\\nb", "a\\\r\tb", `\\`, `\\\`, `\N`, `\\N`, ``, ` pad `, `x,"y"`, `\n\r\t\\`, "é\\né",
	}
	rows := [][]Cell{{nil}}
	for _, v := range vals {
		rows = append(rows, []Cell{Cell(v)})
	}
	out, err := Render(Table{Columns: []string{"v"}, Rows: rows, Limit: 1000}, 1<<20, 1000, ms38)
	if err != nil {
		t.Fatal(err)
	}
	r := csv.NewReader(strings.NewReader(strings.TrimSuffix(out[:strings.Index(out, "-- ")], "\n")))
	r.FieldsPerRecord = -1
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// encoding/csv drops the quoting information, so detect a quoted `\N` from the raw line.
	lines := strings.Split(strings.TrimSuffix(out[:strings.Index(out, "-- ")], "\n"), "\n")
	if len(recs) != len(rows)+1 || len(lines) != len(rows)+1 {
		t.Fatalf("got %d records and %d lines for %d rows:\n%s", len(recs), len(lines), len(rows), out)
	}
	for i, row := range rows {
		got, ok := decodeField(recs[i+1][0], lines[i+1] == `"\N"`)
		switch {
		case row[0] == nil && ok:
			t.Errorf("row %d: NULL decoded as %q", i, got)
		case row[0] != nil && (!ok || got != string(row[0])):
			t.Errorf("row %d: %q rendered %q decoded %q (ok=%v)", i, row[0], lines[i+1], got, ok)
		}
	}
}
