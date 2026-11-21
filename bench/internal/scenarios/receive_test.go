package scenarios

import (
	"reflect"
	"strings"
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

func TestCountRows(t *testing.T) {
	tests := []struct {
		name, text, key string
		want            int
	}{
		{"csv with footer", "id,name\n1,a\n2,b\n-- 2 rows, truncated (more than 2), 1 ms\n-- Refine with WHERE\n", "id", 2},
		{"json array", `[{"id":1},{"id":2},{"id":3}]`, "id", 3},
		{"json empty", `[]`, "id", 0},
		{"json object with rows", `{"columns":[{"name":"id"}],"rows":[{"id":1},{"id":2}],"count":2}`, "id", 2},
		// DBHub 2.0: the statements are an array of objects too; the rows are the array whose objects have the key.
		{"dbhub", `{"success":true,"data":{"statements":[{"sql":"SELECT 1","rows":[{"id":1},{"id":2},{"id":3}],"count":3}]}}`, "id", 3},
		{"json lines", "{\"id\":1}\n{\"id\":2}\n", "id", 2},
		{"python repr", "[{'id': 1, 'x': None}, {'id': 2, 'x': None}]", "id", 2},
		{"aligned table", "id | name\n1 | a\n", "id", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CountRows(tt.text, tt.key); got != tt.want {
				t.Errorf("CountRows = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTruncation(t *testing.T) {
	tests := []struct {
		name     string
		complete bool
		text     string
		want     string
	}{
		{"complete", true, "id\n1\n-- 1 row", results.TruncNone},
		{"complete even if it says truncated", true, "-- 1 row, truncated", results.TruncNone},
		{"dba row bound", false, "id\n1\n-- 100 rows, truncated (more than 100), 4 ms", results.TruncStated},
		{"dba cell", false, `42,"{""blob"": ""a1d0…[+19812 chars]"`, results.TruncStated},
		{"silent cut", false, `[{"id":1},{"id":2}]`, results.TruncSilent},
		{"other wording", false, "Showing first 1000 rows", results.TruncStated},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Truncation(tt.complete, tt.text); got != tt.want {
				t.Errorf("Truncation = %q, want %q", got, tt.want)
			}
		})
	}
}

var precisionCols = []string{"id", "big", "num", "ts", "iv", "m"}

// precisionValues is the text of public.precision as the comments of bench/data/40_fixtures.sql give it.
var precisionValues = [][]string{
	{"1", "9007199254740993", "12345678901234567890.1234567891", "2026-03-29 00:59:59.999999+00", "1 year 2 mons 3 days 04:05:06.789012", "$1,234.56"},
	{"2", "9223372036854775807", "0.0000000001", "2026-03-29 01:00:00+00", "-1 days +02:00:00", "-$0.01"},
	{"3", "-9007199254740993", "-99999999999999999999.9999999999", "2026-10-25 00:59:59.000001+00", "36:00:00", "$92,233,720,368,547,758.07"},
}

func TestComparePrecision(t *testing.T) {
	exact := "id,big,num,ts,iv,m\n" +
		"1,9007199254740993,12345678901234567890.1234567891,2026-03-29 00:59:59.999999+00,1 year 2 mons 3 days 04:05:06.789012,\"$1,234.56\"\n" +
		"2,9223372036854775807,0.0000000001,2026-03-29 01:00:00+00,-1 days +02:00:00,-$0.01\n" +
		"3,-9007199254740993,-99999999999999999999.9999999999,2026-10-25 00:59:59.000001+00,36:00:00,\"$92,233,720,368,547,758.07\"\n"
	n, wrong, reformatted := ComparePrecision(exact, precisionCols, precisionValues)
	if n != 15 || wrong != nil || reformatted != nil {
		t.Fatalf("exact text: %d values, wrong %q, reformatted %q; want 15, none, none", n, wrong, reformatted)
	}
	// A JavaScript number loses the last digit above 2^53 (and -9007199254740993 must not hide it); a JavaScript time
	// with exact milliseconds is another notation of the same instant, one that drops microseconds is wrong.
	lossy := strings.Replace(exact, "9007199254740993,", "9007199254740992,", 1)
	lossy = strings.Replace(lossy, "2026-03-29 01:00:00+00", "2026-03-29T01:00:00.000Z", 1)
	lossy = strings.Replace(lossy, "2026-10-25 00:59:59.000001+00", "2026-10-25T00:59:59.000Z", 1)
	_, wrong, reformatted = ComparePrecision(lossy, precisionCols, precisionValues)
	if want := []string{"row 1 big: want 9007199254740993", "row 3 ts: want 2026-10-25 00:59:59.000001+00"}; !reflect.DeepEqual(wrong, want) {
		t.Errorf("wrong = %q, want %q", wrong, want)
	}
	if want := []string{"row 2 ts: want 2026-03-29 01:00:00+00"}; !reflect.DeepEqual(reformatted, want) {
		t.Errorf("reformatted = %q, want %q", reformatted, want)
	}
}

// TestComparePrecisionServers grades the B6 answers the servers gave on 2026-11-20, white space and all.
func TestComparePrecisionServers(t *testing.T) {
	tests := []struct {
		name, text         string
		wrong, reformatted int
	}{
		// crystaldba: Python reprs; the intervals with months or hours turned into days are wrong.
		{"crystal", `[{'id': 1, 'big': 9007199254740993, 'num': Decimal('12345678901234567890.1234567891'), 'ts': datetime.datetime(2026, 3, 29, 0, 59, 59, 999999, tzinfo=zoneinfo.ZoneInfo(key='Etc/UTC')), 'iv': datetime.timedelta(days=428, seconds=14706, microseconds=789012), 'm': '$1,234.56'}, {'id': 2, 'big': 9223372036854775807, 'num': Decimal('1E-10'), 'ts': datetime.datetime(2026, 3, 29, 1, 0, tzinfo=zoneinfo.ZoneInfo(key='Etc/UTC')), 'iv': datetime.timedelta(days=-1, seconds=7200), 'm': '-$0.01'}, {'id': 3, 'big': -9007199254740993, 'num': Decimal('-99999999999999999999.9999999999'), 'ts': datetime.datetime(2026, 10, 25, 0, 59, 59, 1, tzinfo=zoneinfo.ZoneInfo(key='Etc/UTC')), 'iv': datetime.timedelta(days=1, seconds=43200), 'm': '$92,233,720,368,547,758.07'}]`, 2, 5},
		// Toolbox: RFC 3339 times and pgx interval objects, all the same values.
		{"toolbox", `{"id":1,"big":9007199254740993,"num":12345678901234567890.1234567891,"ts":"2026-03-29T00:59:59.999999Z","iv":{"Microseconds":14706789012,"Days":3,"Months":14,"Valid":true},"m":"$1,234.56"}{"id":2,"big":9223372036854775807,"num":0.0000000001,"ts":"2026-03-29T01:00:00Z","iv":{"Microseconds":7200000000,"Days":-1,"Months":0,"Valid":true},"m":"-$0.01"}{"id":3,"big":-9007199254740993,"num":-99999999999999999999.9999999999,"ts":"2026-10-25T00:59:59.000001Z","iv":{"Microseconds":129600000000,"Days":0,"Months":0,"Valid":true},"m":"$92,233,720,368,547,758.07"}`, 0, 6},
		// The reference server: JavaScript times keep milliseconds only.
		{"reference", "[\n  {\n    \"id\": 1,\n    \"big\": \"9007199254740993\",\n    \"num\": \"12345678901234567890.1234567891\",\n    \"ts\": \"2026-03-29T00:59:59.999Z\",\n    \"iv\": {\n      \"years\": 1,\n      \"months\": 2,\n      \"days\": 3,\n      \"hours\": 4,\n      \"minutes\": 5,\n      \"seconds\": 6,\n      \"milliseconds\": 789.012\n    },\n    \"m\": \"$1,234.56\"\n  },\n  {\"id\": 2, \"big\": \"9223372036854775807\", \"num\": \"0.0000000001\", \"ts\": \"2026-03-29T01:00:00.000Z\", \"iv\": {\"days\": -1, \"hours\": 2}, \"m\": \"-$0.01\"},\n  {\"id\": 3, \"big\": \"-9007199254740993\", \"num\": \"-99999999999999999999.9999999999\", \"ts\": \"2026-10-25T00:59:59.000Z\", \"iv\": {\"hours\": 36}, \"m\": \"$92,233,720,368,547,758.07\"}\n]", 2, 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			n, wrong, reformatted := ComparePrecision(tt.text, precisionCols, precisionValues)
			if n != 15 || len(wrong) != tt.wrong || len(reformatted) != tt.reformatted {
				t.Errorf("%d values, wrong %q, reformatted %q; want 15, %d wrong, %d reformatted", n, wrong, reformatted, tt.wrong, tt.reformatted)
			}
		})
	}
}

func TestGradeB7(t *testing.T) {
	var cell results.ReceiveCell
	grade(&cell, ReceiveStatement{ID: "B7"}, truth{}, `ERROR: column "nme" does not exist (SQLSTATE 42703)`, true)
	if !cell.NamesColumn || cell.Hint {
		t.Errorf("dba B7: names %v hint %v, want true false", cell.NamesColumn, cell.Hint)
	}
	grade(&cell, ReceiveStatement{ID: "B7"}, truth{}, "column \"nme\" does not exist\nHINT: Perhaps you meant to reference the column \"film.name\".", true)
	if !cell.Hint {
		t.Error("a Postgres HINT must count as a hint")
	}
}
