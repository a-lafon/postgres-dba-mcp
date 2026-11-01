package format

import (
	"strings"
	"testing"
	"time"
)

// FuzzRender checks the budget Render promises: the whole text, prefix and footer included, stays within
// maxBytes. Every cell is a value the server sent, so none of it was written here. Rows are padded to the
// column count because a caller never builds a ragged table.
func FuzzRender(f *testing.F) {
	f.Add("a,b\nc,d", "-- top queries", false, 200, 40, 0)
	f.Add(strings.Repeat("é", 300), "", true, 64, 8, 4096)
	f.Add("\xff\xfe,\n,\"", "x", true, 0, 0, 1)
	f.Fuzz(func(t *testing.T, body, prefix string, more bool, maxBytes, maxCell, cellLimit int) {
		if maxBytes < 0 || maxBytes > 1<<16 || maxCell < 0 || maxCell > 1<<10 || cellLimit < 0 || cellLimit > 1<<12 {
			t.Skip()
		}
		cols := []string{"a", "b"}
		var rows [][]Cell
		for _, line := range strings.Split(body, "\n") {
			row := make([]Cell, len(cols))
			for j, field := range strings.SplitN(line, ",", len(cols)) {
				row[j] = Cell(field)
			}
			rows = append(rows, row)
		}
		out, err := Render(Table{Columns: cols, Rows: rows, More: more, Prefix: prefix, CellLimit: cellLimit}, maxBytes, maxCell, time.Second)
		if err != nil {
			return
		}
		if len(out) > maxBytes {
			t.Fatalf("rendered %d bytes over a %d-byte budget:\n%s", len(out), maxBytes, out)
		}
	})
}

// FuzzCut checks what Cut promises: the text it returns is what Shorten would have returned, although it never
// holds the whole value. Both walk code points over byte offsets, so a value that is not valid UTF-8 or that
// ends inside a rune has to come out the same on either side.
func FuzzCut(f *testing.F) {
	f.Add("hello", 3)
	f.Add(strings.Repeat("é", 100), 10)
	f.Add("\xff\xfe"+strings.Repeat("a", 50), 1)
	f.Add(strings.Repeat("😀", 40), 0)
	f.Fuzz(func(t *testing.T, s string, max int) {
		if max < 0 || max > 1<<16 {
			t.Skip()
		}
		got, cut := Cut([]byte(s), max)
		want, wantCut := shorten(s, max)
		if string(got) != want || cut != wantCut {
			t.Fatalf("Cut(%q, %d) = %q, %v; Shorten = %q, %v", s, max, got, cut, want, wantCut)
		}
	})
}

// FuzzSummarizeTSVector walks the escape rules of a tsvector over byte offsets on text the server sent: a
// malformed value must be refused, never read past its end.
func FuzzSummarizeTSVector(f *testing.F) {
	f.Add("'cat':1 'sat':2,3")
	f.Add(`'it''s' 'a\'b'`)
	f.Add("'")
	f.Add(`'a\`)
	f.Fuzz(func(t *testing.T, s string) {
		got, ok := SummarizeTSVector(s)
		switch {
		case ok && !strings.HasPrefix(got, "[tsvector: "):
			t.Fatalf("SummarizeTSVector(%q) = %q, want a summary", s, got)
		case !ok && got != "":
			t.Fatalf("SummarizeTSVector(%q) refused but returned %q", s, got)
		}
	})
}
