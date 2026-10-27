package format

import (
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/db"
)

func cells(vals ...any) []Cell {
	out := make([]Cell, len(vals))
	for i, v := range vals {
		if v != nil {
			out[i] = Cell(fmt.Sprint(v))
		}
	}
	return out
}

const ms38 = 38 * time.Millisecond

func TestRender(t *testing.T) {
	long := strings.Repeat("é", 3650)
	tests := []struct {
		name     string
		table    Table
		maxBytes int
		maxCell  int
		want     string
	}{
		{
			name: "basic table",
			table: Table{Columns: []string{"id", "email", "note"}, Limit: 100,
				Rows: [][]Cell{cells(1, "a@x.io", "hello, world"), cells(2, "b@x.io", `say "hi"`)}},
			maxBytes: 32768, maxCell: 200,
			want: "id,email,note\n1,a@x.io,\"hello, world\"\n2,b@x.io,\"say \"\"hi\"\"\"\n-- 2 rows, 38 ms\n",
		},
		{
			name: "explain text",
			table: Table{Columns: []string{"QUERY PLAN"}, Limit: 100, Rows: [][]Cell{
				cells("Hash Join  (cost=1.09..2.21 rows=4 width=8)"),
				cells("  Hash Cond: (a.id = b.id)"),
				cells("  ->  Seq Scan on a  (cost=0.00..1.04 rows=4 width=4)")}},
			maxBytes: 32768, maxCell: 200,
			want: "QUERY PLAN\nHash Join  (cost=1.09..2.21 rows=4 width=8)\n\"  Hash Cond: (a.id = b.id)\"\n" +
				"\"  ->  Seq Scan on a  (cost=0.00..1.04 rows=4 width=4)\"\n-- 3 rows, 38 ms\n",
		},
		{
			name:     "empty string, NULL and literal backslash N",
			table:    Table{Columns: []string{"v"}, Limit: 100, Rows: [][]Cell{cells(""), cells(nil), cells(`\N`)}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n\"\"\n\\N\n\"\\N\"\n-- 3 rows, 38 ms\n",
		},
		{
			name:     "newlines and tabs escaped",
			table:    Table{Columns: []string{"v"}, Limit: 100, Rows: [][]Cell{cells("line1\nline2\r\tx")}},
			maxBytes: 32768, maxCell: 200,
			want: "v\nline1\\nline2\\r\\tx\n-- 1 row, 38 ms\n",
		},
		{
			name: "types as text",
			table: Table{Columns: []string{"a", "b", "c", "d", "e"}, Limit: 100, Rows: [][]Cell{
				cells("532.10", "{1,2}", "1 day 02:00:00", `{"a": 1}`, "2026-03-02 10:14:00+00")}},
			maxBytes: 32768, maxCell: 200,
			want: "a,b,c,d,e\n532.10,\"{1,2}\",1 day 02:00:00,\"{\"\"a\"\": 1}\",2026-03-02 10:14:00+00\n-- 1 row, 38 ms\n",
		},
		{
			name:     "bytea as hex",
			table:    Table{Columns: []string{"b"}, Limit: 100, Rows: [][]Cell{cells("\\xdeadbeef")}},
			maxBytes: 32768, maxCell: 200,
			want: "b\n\\xdeadbeef\n-- 1 row, 38 ms\n",
		},
		{
			name: "long bytea shortened",
			table: Table{Columns: []string{"b"}, Limit: 100,
				Rows: [][]Cell{{Cell("\\x" + strings.Repeat("ab", 2355))}}},
			maxBytes: 32768, maxCell: 200,
			want: "b\n\\x" + strings.Repeat("ab", 99) + "…[+4512 chars]\n-- 1 row, 1 cell shortened, 38 ms\n",
		},
		{
			name:     "cut of less than 20 chars kept whole",
			table:    Table{Columns: []string{"v"}, Limit: 100, Rows: [][]Cell{cells(strings.Repeat("x", 219)), cells(strings.Repeat("x", 221))}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n" + strings.Repeat("x", 219) + "\n" + strings.Repeat("x", 200) + "…[+21 chars]\n-- 2 rows, 1 cell shortened, 38 ms\n",
		},
		{
			name:     "row limit with estimate",
			table:    Table{Columns: []string{"v"}, Limit: 2, More: true, Estimate: 16044, Rows: [][]Cell{cells(1), cells(2)}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n1\n2\n-- 2 of ~16k rows (planner estimate), truncated, 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n",
		},
		{
			name:     "estimate not above the rows shown is ignored",
			table:    Table{Columns: []string{"v"}, Limit: 2, More: true, Estimate: 2, Rows: [][]Cell{cells(1), cells(2)}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n1\n2\n-- 2 rows, truncated (more than 2), 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n",
		},
		{
			name:     "long cell shortened by code points",
			table:    Table{Columns: []string{"v"}, Limit: 100, Rows: [][]Cell{cells(long)}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n" + strings.Repeat("é", 200) + "…[+3450 chars]\n-- 1 row, 1 cell shortened, 38 ms\n",
		},
		{
			name:     "empty",
			table:    Table{Columns: []string{"v"}, Limit: 100},
			maxBytes: 32768, maxCell: 200,
			want: "v\n-- 0 rows, 38 ms\n",
		},
		{
			name:     "row limit",
			table:    Table{Columns: []string{"v"}, Limit: 2, More: true, Rows: [][]Cell{cells(1), cells(2)}},
			maxBytes: 32768, maxCell: 200,
			want: "v\n1\n2\n-- 2 rows, truncated (more than 2), 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n",
		},
		{
			name: "truncated with shortened cells",
			table: Table{Columns: []string{"v"}, Limit: 3, More: true,
				Rows: [][]Cell{cells(strings.Repeat("x", 40)), cells(strings.Repeat("x", 40)), cells(strings.Repeat("x", 40))}},
			maxBytes: 32768, maxCell: 16,
			want: "v\n" + strings.Repeat(strings.Repeat("x", 16)+"…[+24 chars]\n", 3) +
				"-- 3 rows, truncated (more than 3), 3 cells shortened, 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n",
		},
		{
			name:     "prefix",
			table:    Table{Columns: []string{"v"}, Limit: 100, Prefix: "-- note", Rows: [][]Cell{cells(1)}},
			maxBytes: 32768, maxCell: 200,
			want: "-- note\nv\n1\n-- 1 row, 38 ms\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Render(tt.table, tt.maxBytes, tt.maxCell, ms38)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("Render() =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestRenderByteBudget(t *testing.T) {
	var rows [][]Cell
	for i := 0; i < 100; i++ {
		rows = append(rows, cells(strings.Repeat("x", 1000)))
	}
	got, err := Render(Table{Columns: []string{"v"}, Limit: 100, More: true, Rows: rows, Prefix: "-- p"}, 32768, 2000, ms38)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 32768 {
		t.Errorf("len = %d, want <= 32768", len(got))
	}
	n := strings.Count(got, strings.Repeat("x", 1000))
	if n == 0 || n >= 100 {
		t.Errorf("rows rendered = %d, want between 1 and 99", n)
	}
	want := fmt.Sprintf("-- %d rows, truncated (32768-byte limit), 38 ms\n-- Refine with WHERE, LIMIT or an aggregate to see the rest.\n", n)
	if !strings.HasSuffix(got, want) {
		t.Errorf("footer = %q, want suffix %q", got[len(got)-120:], want)
	}
	if !strings.HasPrefix(got, "-- p\nv\n") {
		t.Errorf("text starts with %q", got[:10])
	}
}

func TestRenderHeaderTooLarge(t *testing.T) {
	cols := make([]string, 300)
	for i := range cols {
		cols[i] = "abcdefghi" // 9 bytes + comma or newline = 10 each
	}
	_, err := Render(Table{Columns: cols, Limit: 10, Rows: [][]Cell{cells(1)}}, 1024, 200, ms38)
	want := "result header alone exceeds --max-bytes (3000 bytes); select fewer columns"
	if err == nil || err.Error() != want {
		t.Errorf("error = %v, want %q", err, want)
	}
}

func TestRenderTag(t *testing.T) {
	if got, want := RenderTag("UPDATE 3", 3*time.Millisecond), "-- UPDATE 3, 3 ms\n"; got != want {
		t.Errorf("RenderTag() = %q, want %q", got, want)
	}
}

func TestSingularFooter(t *testing.T) {
	got, err := Render(Table{Columns: []string{"v"}, Limit: 5, Rows: [][]Cell{cells(strings.Repeat("y", 40))}}, 4096, 16, 5*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(got, "-- 1 row, 1 cell shortened, 5 ms\n") {
		t.Errorf("got %q", got)
	}
}

func TestRenderProperties(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	alphabet := []rune("ab,\"\n\r\t \\Né€")
	randString := func(n int) string {
		r := make([]rune, n)
		for i := range r {
			r[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return string(r)
	}
	for iter := 0; iter < 500; iter++ {
		ncols := 1 + rng.Intn(4)
		cols := make([]string, ncols)
		for i := range cols {
			cols[i] = randString(1 + rng.Intn(8))
		}
		limit := 1 + rng.Intn(30)
		var rows [][]Cell
		for r := 0; r < limit; r++ {
			row := make([]Cell, ncols)
			for c := range row {
				if rng.Intn(6) != 0 {
					row[c] = Cell(randString(rng.Intn(80)))
				}
			}
			rows = append(rows, row)
		}
		maxBytes := 1024 + rng.Intn(2000)
		tbl := Table{Columns: cols, Limit: limit, More: rng.Intn(2) == 0, Rows: rows}
		got, err := Render(tbl, maxBytes, 16+rng.Intn(40), time.Duration(rng.Intn(20000))*time.Microsecond)
		if err != nil {
			continue
		}
		if len(got) > maxBytes {
			t.Fatalf("iteration %d: %d bytes > %d:\n%s", iter, len(got), maxBytes, got)
		}
		// Rows stay on one line: no raw CR or TAB, and no more lines than header, rows and two footer lines.
		if strings.ContainsRune(got, '\r') || strings.ContainsRune(got, '\t') {
			t.Fatalf("iteration %d: raw CR or TAB in output %q", iter, got)
		}
		if n := strings.Count(got, "\n"); n > len(rows)+3 {
			t.Fatalf("iteration %d: %d lines for %d rows", iter, n, len(rows))
		}
	}
}

func TestHelpers(t *testing.T) {
	t.Run("Size", func(t *testing.T) {
		for in, want := range map[int64]string{0: "0 bytes", 512: "512 bytes", 1024: "1.0 kB", 2355: "2.3 kB", 10 * 1024: "10 kB",
			120 << 20: "120 MB", 1288490189: "1.2 GB", 1<<20 - 1: "1.0 MB", 3 << 40: "3.0 TB"} {
			if got := Size(in); got != want {
				t.Errorf("Size(%d) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("Approx", func(t *testing.T) {
		for in, want := range map[int64]string{950: "950", 1000: "1.0k", 1234: "1.2k", 16044: "16k", 210_000_000: "210M",
			1_234_567: "1.2M", 1_900_000_000: "1.9B", 999_999: "1.0M"} {
			if got := Approx(in); got != want {
				t.Errorf("Approx(%d) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("Duration", func(t *testing.T) {
		for in, want := range map[time.Duration]string{400 * time.Microsecond: "0.4 ms", 38 * time.Millisecond: "38 ms",
			6100 * time.Millisecond: "6.1 s", 999900 * time.Microsecond: "1.0 s"} {
			if got := Duration(in); got != want {
				t.Errorf("Duration(%v) = %q, want %q", in, got, want)
			}
		}
	})
	t.Run("Percent", func(t *testing.T) {
		if got := Percent(0.992); got != "99.2%" {
			t.Errorf("Percent = %q", got)
		}
		if got := PercentChange(-0.998); got != "-99.8%" {
			t.Errorf("PercentChange = %q", got)
		}
		if got := PercentChange(0.065); got != "+6.5%" {
			t.Errorf("PercentChange = %q", got)
		}
	})
	t.Run("Shorten", func(t *testing.T) {
		if got := Shorten("héllo wörld, ça va très bien", 5); got != "héllo…[+23 chars]" {
			t.Errorf("Shorten = %q", got)
		}
		if got := Shorten("short", 5); got != "short" {
			t.Errorf("Shorten = %q", got)
		}
	})
}

func TestError(t *testing.T) {
	t.Run("unknown column", func(t *testing.T) {
		err := &pgconn.PgError{Code: "42703", Message: `column "nme" does not exist`,
			Hint: `Perhaps you meant to reference the column "film.name".`, Position: 8}
		want := "ERROR: column \"nme\" does not exist (SQLSTATE 42703)\nHINT: Perhaps you meant to reference the column \"film.name\".\nPOSITION: 8"
		if got := Error(fmt.Errorf("run: %w", err)); got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
	t.Run("detail", func(t *testing.T) {
		got := Error(&pgconn.PgError{Code: "23505", Message: "duplicate key", Detail: "Key (id)=(1) already exists."})
		if want := "ERROR: duplicate key (SQLSTATE 23505)\nDETAIL: Key (id)=(1) already exists."; got != want {
			t.Errorf("Error() = %q, want %q", got, want)
		}
	})
	t.Run("wrapped connection error", func(t *testing.T) {
		ce := &db.ConnectError{Err: &pgconn.PgError{Code: "28P01", Message: "password authentication failed"}}
		got := Error(fmt.Errorf("list: %w", ce))
		if !strings.HasPrefix(got, "ERROR: cannot connect to the database: ") || strings.Contains(got, "SQLSTATE 28P01)\nDETAIL") {
			t.Errorf("Error() = %q", got)
		}
	})
	t.Run("plain", func(t *testing.T) {
		if got := Error(errors.New("boom")); got != "ERROR: boom" {
			t.Errorf("Error() = %q", got)
		}
	})
	t.Run("cap", func(t *testing.T) {
		got := Error(errors.New(strings.Repeat("é", 3000)))
		if len(got) > 2000 {
			t.Errorf("len = %d", len(got))
		}
	})
}

func TestCSVLine(t *testing.T) {
	tests := []struct {
		name   string
		fields []string
		want   string
	}{
		{"plain", []string{"1", "-99.8%", "4.4 MB"}, "1,-99.8%,4.4 MB\n"},
		{"quoted", []string{"a,b", `say "hi"`, "", `\N`, " lead", "trail "}, "\"a,b\",\"say \"\"hi\"\"\",\"\",\"\\N\",\" lead\",\"trail \"\n"},
		{"escaped", []string{"a\nb", `a\nb`}, "a\\nb,a\\\\nb\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CSVLine(tt.fields...); got != tt.want {
				t.Errorf("CSVLine(%q) = %q, want %q", tt.fields, got, tt.want)
			}
		})
	}
}

func TestRenderColumnCaps(t *testing.T) {
	long := strings.Repeat("x", 70)
	tbl := Table{Columns: []string{"a", "b"}, Limit: 10, Caps: []int{0, 40}, Rows: [][]Cell{{Cell(long), Cell(long)}}}
	got, err := Render(tbl, 32768, 10, 0)
	if err != nil {
		t.Fatal(err)
	}
	want := "a,b\n" + strings.Repeat("x", 10) + "…[+60 chars]," + strings.Repeat("x", 40) + "…[+30 chars]\n-- 1 row, 2 cells shortened, 0.0 ms\n"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
}

func TestRenderCellLimitHint(t *testing.T) {
	long := strings.Repeat("x", 50)
	hint := func(out string) []string {
		var h []string
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "pass max_cell") {
				h = append(h, l)
			}
		}
		return h
	}
	rows := [][]Cell{cells(long, 1, long), cells(long, 2, long), cells(1, 3, long)}
	tests := []struct {
		name     string
		table    Table
		maxBytes int
		maxCell  int
		want     string // the expected hint line, "" for none
	}{
		{"no CellLimit", Table{Columns: []string{"a"}, Rows: [][]Cell{cells(long)}}, 10000, 10, ""},
		{"maxCell at CellLimit", Table{Columns: []string{"a"}, Rows: [][]Cell{cells(long)}, CellLimit: 10}, 10000, 10, ""},
		{"two columns listed once in order", Table{Columns: []string{"a", "b", "c"}, Rows: rows, CellLimit: 100}, 10000, 10,
			"-- 5 cells shortened (a, c); pass max_cell (up to 100) to read more"},
		{"dropped row", Table{Columns: []string{"a", "b"}, Rows: [][]Cell{cells(1, long), cells(long, 2), cells(3, 4)}, CellLimit: 100}, 220, 10,
			"-- 1 cell shortened (b); pass max_cell (up to 100) to read more"},
		{"newline in column name", Table{Columns: []string{"a\n-- 0 rows, 1 ms"}, Rows: [][]Cell{cells(long)}, CellLimit: 100}, 10000, 10,
			`-- 1 cell shortened (a\n-- 0 rows, 1 ms); pass max_cell (up to 100) to read more`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := Render(tt.table, tt.maxBytes, tt.maxCell, ms38)
			if err != nil {
				t.Fatal(err)
			}
			got := hint(out)
			if tt.want == "" {
				if len(got) != 0 {
					t.Fatalf("unexpected hint %q in\n%s", got, out)
				}
				return
			}
			if len(got) != 1 || got[0] != tt.want {
				t.Fatalf("hint = %q, want %q in\n%s", got, tt.want, out)
			}
			footers := 0
			for _, l := range strings.Split(out, "\n") {
				if strings.HasPrefix(l, "--") {
					if footers == 0 && strings.Contains(l, "shortened") {
						t.Fatalf("first footer line %q repeats the hint's count", l)
					}
					footers++
				}
			}
			if wantLines := 2 + strings.Count(out, "-- Refine"); footers != wantLines { // footer + hint
				t.Fatalf("%d lines start with --, want %d in\n%s", footers, wantLines, out)
			}
		})
	}
}
