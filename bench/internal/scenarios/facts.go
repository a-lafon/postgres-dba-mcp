// Package scenarios holds the N2 scenarios S01-S22: the call each one makes, the facts its answer must contain
// (computed from the database, rendered the way each server renders them) and the runner.
package scenarios

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Fact is a value a server's answer must contain, in the shape that server renders it. DBA and Crystal are literal
// substrings; JSON is a regular expression, because a value in JSON is rendered by the server's own driver and the
// two JSON servers under test do not agree on arrays, intervals or bytea. An empty form means the server is not
// asked for that fact, so a fact none of the three forms can express is never counted against anyone.
type Fact struct {
	DBA     string
	Crystal string
	JSON    string
}

// For returns the form of f for a server; pgEdge answers in Postgres text like postgres-dba-mcp.
func (f Fact) For(server string) string {
	switch server {
	case results.Crystal:
		return f.Crystal
	case results.DBA, results.PgEdge:
		return f.DBA
	}
	return f.JSON
}

// has reports whether text holds f in the form server renders values in. It grades the content, not the rendering:
// a JSON server answers neither in the postgres-dba-mcp CSV nor in the crystaldba Python repr, so its fact is the
// JSON one and only that one, exactly as crystaldba is graded on its own form and not on the CSV.
func (f Fact) has(server, text string) bool {
	want := f.For(server)
	if want == "" {
		return true
	}
	if server == results.Crystal || server == results.DBA || server == results.PgEdge {
		return strings.Contains(text, want)
	}
	return regexp.MustCompile(want).MatchString(text)
}

// Same is a fact that reads the same on every server: a name, a message, a plan node.
func Same(s string) Fact { return Fact{DBA: s, Crystal: s, JSON: jsonLiteral(s)} }

// Int is an integer cell. Its JSON form is bounded, so that 1 does not match the 1 of 15.
func Int(n int64) Fact {
	d := strconv.FormatInt(n, 10)
	return Fact{DBA: d, Crystal: d, JSON: `\b` + d + `\b`}
}

// Str is a text cell: raw (CSV-quoted when needed) for postgres-dba-mcp, a Python repr for crystaldba, a quoted
// JSON string elsewhere.
func Str(s string) Fact {
	return Fact{DBA: csvField(s), Crystal: pyStr(s), JSON: `"` + jsonLiteral(s) + `"`}
}

// Num is a numeric cell given as Postgres text: `532.10`, `Decimal('532.10')`, and in JSON either the bare number
// (Toolbox) or the string Postgres sent (DBHub, whose driver keeps numeric as text).
func Num(text string) Fact {
	return Fact{DBA: text, Crystal: "Decimal('" + text + "')", JSON: `"?` + regexp.QuoteMeta(text) + `\b"?`}
}

// Null is a NULL cell.
func Null() Fact { return Fact{DBA: `\N`, Crystal: "None", JSON: `\bnull\b`} }

// Empty is an empty-string cell.
func Empty() Fact { return Fact{DBA: `""`, Crystal: "''", JSON: `""`} }

// IntArray is an int[] cell: `"{1,2}"`, `[1, 2]`, and a JSON array whichever way the server indents it.
func IntArray(v []int32) Fact {
	d, c, j := make([]string, len(v)), make([]string, len(v)), make([]string, len(v))
	for i, n := range v {
		d[i], c[i], j[i] = strconv.Itoa(int(n)), strconv.Itoa(int(n)), strconv.Itoa(int(n))
	}
	return Fact{DBA: `"{` + strings.Join(d, ",") + `}"`, Crystal: "[" + strings.Join(c, ", ") + "]",
		JSON: `\[\s*` + strings.Join(j, `,\s*`) + `\s*\]`}
}

// Bytea is a bytea cell: postgres-dba-mcp prints the Postgres hex text, crystaldba the Python bytes, and a JSON
// server either base64 (Toolbox) or the byte list of a node Buffer (DBHub).
func Bytea(b []byte) Fact {
	dec := make([]string, len(b))
	for i, c := range b {
		dec[i] = strconv.Itoa(int(c))
	}
	return Fact{DBA: `\x` + hex.EncodeToString(b), Crystal: pyBytes(b),
		JSON: `(?:` + regexp.QuoteMeta(base64.StdEncoding.EncodeToString(b)) + `|` + strings.Join(dec, `,\s*`) + `)`}
}

// Time is a timestamptz cell: Postgres text in UTC, the leading part of Python's datetime repr (what follows is
// the tzinfo, which depends on the psycopg build), and in JSON either Postgres text or RFC 3339.
func Time(t time.Time) Fact {
	t = t.UTC()
	return Fact{DBA: t.Format("2006-01-02 15:04:05.999999") + "+00", Crystal: pyDatetimePrefix(t),
		JSON: t.Format("2006-01-02") + `[ T]` + t.Format("15:04:05") + `(?:\.\d+)?(?:Z|\+00(?::00)?)`}
}

// Interval is an interval cell given as its Postgres text and its days and microseconds (no months). In JSON the
// two servers break it down differently, so the fact asks for the day count and the rest of the value, in either
// order: `{"days":1,"hours":2}` (DBHub) and `{"Microseconds":7200000000,"Days":1,...}` (Toolbox).
func Interval(text string, days int32, micros int64) Fact {
	parts := []string{fmt.Sprintf(`(?i)"days"\s*:\s*%d\b`, days)}
	rest := []string{fmt.Sprintf(`(?i)"microseconds"\s*:\s*%d\b`, micros)}
	if micros%3_600_000_000 == 0 {
		rest = append(rest, fmt.Sprintf(`(?i)"hours"\s*:\s*%d\b`, micros/3_600_000_000))
	}
	parts = append(parts, `(?:`+strings.Join(rest, "|")+`)`)
	return Fact{DBA: text, Crystal: pyTimedelta(days, micros), JSON: eitherOrder(parts[0], parts[1])}
}

// eitherOrder matches a and b in the same JSON object, whichever order the server writes them in.
func eitherOrder(a, b string) string {
	return `(?:` + a + `[^{}]*` + b + `|` + b + `[^{}]*` + a + `)`
}

// Row is a result row: its cells joined by commas for postgres-dba-mcp, `'col': value` pairs for crystaldba, and a
// JSON object holding every column with its value. The JSON form keeps the columns in query order, which is the
// order both JSON servers write them in, and `[^{}]` between two columns keeps the match inside one row object:
// the point of a row is that the values belong together, and matching them anywhere in the answer would not prove
// that. The row is not anchored on the braces themselves, because a jsonb column puts braces inside the row.
func Row(cols []string, vals ...Fact) Fact {
	d := make([]string, len(vals))
	c := make([]string, len(vals))
	j := make([]string, len(vals))
	for i, v := range vals {
		d[i] = v.DBA
		c[i] = "'" + cols[i] + "': " + v.Crystal
		j[i] = `"` + regexp.QuoteMeta(cols[i]) + `"\s*:\s*` + v.JSON
	}
	return Fact{DBA: "\n" + strings.Join(d, ","), Crystal: strings.Join(c, ", "),
		JSON: strings.Join(j, `[^{}]*`)}
}

// jsonLiteral is s escaped for a regular expression, with every quote allowed to carry the backslash a JSON
// string gives it: the same Postgres message reaches the harness escaped inside DBHub's JSON and bare in Toolbox's
// plain-text error. It handles quotes only, which is the one escape the measured messages contain.
func jsonLiteral(s string) string {
	return strings.ReplaceAll(regexp.QuoteMeta(s), `"`, `\\?"`)
}

// Missing returns the facts of text that are absent, as the form the server was asked for.
func Missing(server, text string, facts []Fact) []string {
	var miss []string
	for _, f := range facts {
		if f.has(server, text) {
			continue
		}
		miss = append(miss, strings.TrimPrefix(f.For(server), "\n"))
	}
	return miss
}

var csvNeedsQuote = regexp.MustCompile(`[,"\r\n\t]|^ | $|^$|^\\N$`)

func csvField(s string) string {
	s = strings.NewReplacer("\r", `\r`, "\n", `\n`, "\t", `\t`).Replace(s)
	if !csvNeedsQuote.MatchString(s) {
		return s
	}
	return `"` + strings.ReplaceAll(s, `"`, `""`) + `"`
}

func pyStr(s string) string {
	q := "'"
	if strings.Contains(s, "'") && !strings.Contains(s, `"`) {
		q = `"`
	}
	s = strings.NewReplacer(`\`, `\\`, "\n", `\n`, "\r", `\r`, "\t", `\t`).Replace(s)
	if q == "'" {
		s = strings.ReplaceAll(s, "'", `\'`)
	}
	return q + s + q
}

func pyBytes(b []byte) string {
	var sb strings.Builder
	sb.WriteString("b'")
	for _, c := range b {
		switch {
		case c == '\\':
			sb.WriteString(`\\`)
		case c == '\'':
			sb.WriteString(`\'`)
		case c == '\n':
			sb.WriteString(`\n`)
		case c == '\r':
			sb.WriteString(`\r`)
		case c == '\t':
			sb.WriteString(`\t`)
		case c >= 0x20 && c < 0x7f:
			sb.WriteByte(c)
		default:
			fmt.Fprintf(&sb, `\x%02x`, c)
		}
	}
	sb.WriteString("'")
	return sb.String()
}

// pyDatetimePrefix is repr(datetime) up to, not including, the tzinfo: seconds appear only when non-zero, and
// microseconds only when non-zero, as Python prints them.
func pyDatetimePrefix(t time.Time) string {
	s := fmt.Sprintf("datetime.datetime(%d, %d, %d, %d, %d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute())
	us := t.Nanosecond() / 1000
	if t.Second() != 0 || us != 0 {
		s += fmt.Sprintf(", %d", t.Second())
	}
	if us != 0 {
		s += fmt.Sprintf(", %d", us)
	}
	return s
}

func pyTimedelta(days int32, micros int64) string {
	d := int64(days) + micros/(86400*1_000_000)
	rest := micros % (86400 * 1_000_000)
	secs, us := rest/1_000_000, rest%1_000_000
	var parts []string
	if d != 0 {
		parts = append(parts, fmt.Sprintf("days=%d", d))
	}
	if secs != 0 {
		parts = append(parts, fmt.Sprintf("seconds=%d", secs))
	}
	if us != 0 {
		parts = append(parts, fmt.Sprintf("microseconds=%d", us))
	}
	if len(parts) == 0 {
		return "datetime.timedelta(0)"
	}
	return "datetime.timedelta(" + strings.Join(parts, ", ") + ")"
}

// Percentile returns the nearest-rank p-th percentile (0-100) of v, or 0 for an empty slice.
func Percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	rank := int(math.Ceil(p / 100 * float64(len(s))))
	return s[max(rank, 1)-1]
}

// Sorted returns the keys of m in order.
func Sorted[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Median returns the middle value of v (the mean of the two middle ones for an even count).
func Median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

var (
	reDBATotal    = regexp.MustCompile(`Total cost ([\d.]+)`)
	reDBACost     = regexp.MustCompile(`total cost ([\d.]+) -> ([\d.]+)`)
	reCrystalCost = regexp.MustCompile(`\(Cost: [\d.]+\.\.([\d.]+)\)`)
	reDBAWorkload = regexp.MustCompile(`Workload cost ([\d.]+[kMB]?) → ([\d.]+[kMB]?)`)
	reCrystalSum  = regexp.MustCompile(`'base_cost': '([\d.]+)', 'new_cost': '([\d.]+)'`)
	reDDL         = regexp.MustCompile(`(?i)create index(?: \S+)? on (?:\w+\.)?(\w+) (?:using \w+ )?\(([^)]*)\)`)
	// reNotRecommended matches the postgres-dba-mcp lines naming indexes it leaves out, whose DDL is no advice.
	reNotRecommended = regexp.MustCompile(`(?m)^Not recommended.*$`)
)

// RootCost returns the total cost of a plan answer: the cost with hypothetical indexes when postgres-dba-mcp prints
// both, else the root cost. For crystaldba it is the cost of the first node.
func RootCost(server, text string) (cost float64, ok bool) {
	var m []string
	switch server {
	case results.Crystal:
		m = reCrystalCost.FindStringSubmatch(text)
	default:
		if m = reDBACost.FindStringSubmatch(text); m != nil {
			m = m[1:] // the cost after the hypothetical indexes
		} else {
			m = reDBATotal.FindStringSubmatch(text)
		}
	}
	if m == nil {
		return 0, false
	}
	v, err := strconv.ParseFloat(m[len(m)-1], 64)
	return v, err == nil
}

// ParseAdvice reads what an index advisor recommends: the set of "table(col,col)" indexes, the workload cost change
// as a fraction, and whether it recommends nothing.
func ParseAdvice(server, text string) results.Advice {
	var a results.Advice
	seen := map[string]bool{}
	for _, m := range reDDL.FindAllStringSubmatch(reNotRecommended.ReplaceAllString(text, ""), -1) {
		cols := strings.ReplaceAll(strings.ReplaceAll(m[2], " ", ""), `"`, "")
		ix := strings.ToLower(m[1]) + "(" + strings.ToLower(cols) + ")"
		if !seen[ix] {
			seen[ix] = true
			a.Indexes = append(a.Indexes, ix)
		}
	}
	sort.Strings(a.Indexes)
	re := reDBAWorkload
	if server == results.Crystal {
		re = reCrystalSum
	}
	if m := re.FindStringSubmatch(text); m != nil {
		base, after := parseApprox(m[1]), parseApprox(m[2])
		if base > 0 {
			a.Change = (after - base) / base
		}
	}
	a.None = strings.Contains(text, "No index recommendation")
	return a
}

// parseApprox reads a number that format.Approx may have shortened (`604M`) or a plain one (`4812.0`).
func parseApprox(s string) float64 {
	mult := map[byte]float64{'k': 1e3, 'M': 1e6, 'B': 1e9}[s[len(s)-1]]
	if mult == 0 {
		mult = 1
	} else {
		s = s[:len(s)-1]
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v * mult
}

var reSpace = regexp.MustCompile(`(?:\s|\\n|\\t)+`)

// NormalizeQuery collapses whitespace and the escaped newlines of a Python repr, for query-text matching.
func NormalizeQuery(s string) string { return strings.TrimSpace(reSpace.ReplaceAllString(s, " ")) }
