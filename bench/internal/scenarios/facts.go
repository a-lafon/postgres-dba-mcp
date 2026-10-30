// Package scenarios holds the N2 scenarios S01-S21: the call each one makes, the facts its answer must contain
// (computed from the database, rendered the way each server renders them) and the runner.
package scenarios

import (
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

// Fact is a string a server's answer must contain. An empty form means the server is not asked for it.
type Fact struct {
	DBA     string
	Crystal string
}

// For returns the form of f for a server; pgEdge answers in Postgres text like postgres-dba-mcp.
func (f Fact) For(server string) string {
	if server == results.Crystal {
		return f.Crystal
	}
	return f.DBA
}

// Same is a fact that reads the same on every server.
func Same(s string) Fact { return Fact{DBA: s, Crystal: s} }

// Int is an integer cell.
func Int(n int64) Fact { return Same(strconv.FormatInt(n, 10)) }

// Str is a text cell: raw (CSV-quoted when needed) for postgres-dba-mcp, a Python repr for crystaldba.
func Str(s string) Fact { return Fact{DBA: csvField(s), Crystal: pyStr(s)} }

// Num is a numeric cell given as Postgres text: `532.10` and `Decimal('532.10')`.
func Num(text string) Fact { return Fact{DBA: text, Crystal: "Decimal('" + text + "')"} }

// Null is a NULL cell.
func Null() Fact { return Fact{DBA: `\N`, Crystal: "None"} }

// Empty is an empty-string cell.
func Empty() Fact { return Fact{DBA: `""`, Crystal: "''"} }

// IntArray is an int[] cell: `"{1,2}"` and `[1, 2]`.
func IntArray(v []int32) Fact {
	d, c := make([]string, len(v)), make([]string, len(v))
	for i, n := range v {
		d[i], c[i] = strconv.Itoa(int(n)), strconv.Itoa(int(n))
	}
	return Fact{DBA: `"{` + strings.Join(d, ",") + `}"`, Crystal: "[" + strings.Join(c, ", ") + "]"}
}

// Bytea is a bytea cell: postgres-dba-mcp prints the Postgres hex text, crystaldba the Python bytes.
func Bytea(b []byte) Fact {
	return Fact{DBA: `\x` + hex.EncodeToString(b), Crystal: pyBytes(b)}
}

// Time is a timestamptz cell: Postgres text in UTC, and the leading part of Python's datetime repr (what follows is
// the tzinfo, which depends on the psycopg build).
func Time(t time.Time) Fact {
	t = t.UTC()
	return Fact{DBA: t.Format("2006-01-02 15:04:05.999999") + "+00", Crystal: pyDatetimePrefix(t)}
}

// Interval is an interval cell given as its Postgres text and its days and microseconds (no months).
func Interval(text string, days int32, micros int64) Fact {
	return Fact{DBA: text, Crystal: pyTimedelta(days, micros)}
}

// Row is a result row: its cells joined by commas for postgres-dba-mcp, and `'col': value` pairs for crystaldba.
func Row(cols []string, vals ...Fact) Fact {
	d := make([]string, len(vals))
	c := make([]string, len(vals))
	for i, v := range vals {
		d[i] = v.DBA
		c[i] = "'" + cols[i] + "': " + v.Crystal
	}
	return Fact{DBA: "\n" + strings.Join(d, ","), Crystal: strings.Join(c, ", ")}
}

// Missing returns the facts of text that are absent, as the form the server was asked for.
func Missing(server, text string, facts []Fact) []string {
	var miss []string
	for _, f := range facts {
		want := f.For(server)
		if want == "" {
			continue
		}
		if !strings.Contains(text, want) {
			miss = append(miss, strings.TrimPrefix(want, "\n"))
		}
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
