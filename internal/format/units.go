// Package format renders tool results as compact, bounded text: CSV tables with a footer line, unit helpers
// and error text. Every tool renderer uses only this package to write its output.
package format

import (
	"fmt"
	"math"
	"time"
	"unicode/utf8"
)

// scale divides v by base until it is below base, then rounds it to one decimal below 10 units and to an
// integer above, moving to the next unit when the rounding reaches base. It returns the text and the unit index.
func scale(v, base float64, maxUnit int) (string, int) {
	i := 0
	for v >= base && i < maxUnit {
		v /= base
		i++
	}
	r := math.Round(v)
	if v < 10 {
		r = math.Round(v*10) / 10
	}
	if r >= base && i < maxUnit {
		return scaleText(r/base, i+1)
	}
	return scaleText(r, i)
}

func scaleText(r float64, i int) (string, int) {
	if r < 10 {
		return fmt.Sprintf("%.1f", r), i
	}
	return fmt.Sprintf("%.0f", r), i
}

// Size formats a byte count, 1024-based, in the style of pg_size_pretty: 512 bytes, 2.3 kB, 120 MB, 1.4 GB.
func Size(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d bytes", bytes)
	}
	units := []string{"bytes", "kB", "MB", "GB", "TB"}
	s, i := scale(float64(bytes), 1024, len(units)-1)
	return s + " " + units[i]
}

// Approx formats a count: exact below 1 000, then 1.2k, 16k, 210M, 1.9B.
func Approx(n int64) string {
	if n > -1000 && n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	units := []string{"", "k", "M", "B"}
	neg := ""
	v := float64(n)
	if n < 0 {
		neg, v = "-", -v
	}
	s, i := scale(v, 1000, len(units)-1)
	return neg + s + units[i]
}

// Duration formats a duration: 0.4 ms below 1 ms, 38 ms up to 1 s, then 6.1 s.
func Duration(d time.Duration) string {
	ms := float64(d) / float64(time.Millisecond)
	switch {
	case ms < 1:
		return fmt.Sprintf("%.1f ms", ms)
	case math.Round(ms) < 1000:
		return fmt.Sprintf("%.0f ms", ms)
	default:
		return fmt.Sprintf("%.1f s", d.Seconds())
	}
}

// Percent formats a ratio given as a 0-1 fraction: 0.992 gives 99.2%.
func Percent(frac float64) string {
	return fmt.Sprintf("%.1f%%", frac*100)
}

// PercentChange formats a change given as a fraction, always signed: -0.998 gives -99.8%, 0.065 gives +6.5%.
func PercentChange(frac float64) string {
	p := math.Round(frac*1000) / 10
	if p == 0 {
		return "+0.0%"
	}
	return fmt.Sprintf("%+.1f%%", p)
}

// minCut is the least number of code points a cut must remove: the "…[+N chars]" marker is about as long as a
// small cut, so a value at most max+minCut long is left whole.
const minCut = 20

// Shorten cuts s to its first max code points followed by "…[+N chars]", N being the number of code points removed.
// A value that would lose fewer than 20 code points is returned unchanged.
func Shorten(s string, max int) string {
	out, _ := shorten(s, max)
	return out
}

func shorten(s string, max int) (string, bool) {
	// A string of at most max+minCut bytes has at most that many code points.
	if len(s) <= max+minCut {
		return s, false
	}
	n := utf8.RuneCountInString(s)
	if n <= max+minCut {
		return s, false
	}
	return cut(s, max, n), true
}

// Cut is Shorten on a cell read from the server: it copies only the code points it keeps, so a reader can hold
// a bounded result without ever holding the whole value. The text it returns is what Shorten would have
// returned, and Render leaves it alone when it renders with the same max.
func Cut(c []byte, max int) (Cell, bool) {
	if len(c) <= max+minCut {
		return append(Cell{}, c...), false
	}
	n := utf8.RuneCount(c)
	if n <= max+minCut {
		return append(Cell{}, c...), false
	}
	// The first max code points take at most 4 bytes each, so copying that many is enough to cut.
	head := c
	if len(head) > 4*max {
		head = head[:4*max]
	}
	return Cell(cut(string(head), max, n)), true
}

// cut returns the first max code points of s followed by the marker, n being the code points of the whole value
// s is the start of. s must hold at least max code points.
func cut(s string, max, n int) string {
	i := 0
	for j := 0; j < max; j++ {
		_, w := utf8.DecodeRuneInString(s[i:])
		i += w
	}
	return fmt.Sprintf("%s…[+%d chars]", s[:i], n-max)
}
