package safesql

import (
	"reflect"
	"strings"
	"testing"
)

func TestRewriteTypedParams(t *testing.T) {
	tests := []struct {
		name, sql, want string // want "" means unchanged
	}{
		{"interval", "SELECT count(*) FROM events WHERE created_at < now() - interval $1",
			"SELECT count(*) FROM events WHERE created_at < now() - $1::interval"},
		{"interval year", "SELECT 1 WHERE now() - interval $1 year > x", "SELECT 1 WHERE now() - $1::interval year > x"},
		{"interval day to second", "SELECT interval $1 day to second", "SELECT $1::interval day to second"},
		{"interval second precision", "SELECT interval $1 second(3)", "SELECT $1::interval second(3)"},
		{"interval precision", "SELECT interval(3) $1", "SELECT $1::interval(3)"},
		{"upper case", "SELECT INTERVAL $1 DAY", "SELECT $1::INTERVAL DAY"},
		{"two params", "SELECT * FROM t WHERE a BETWEEN timestamptz $1 AND timestamptz $2",
			"SELECT * FROM t WHERE a BETWEEN $1::timestamptz AND $2::timestamptz"},
		{"timestamp with time zone", "SELECT timestamp with time zone $1", "SELECT $1::timestamp with time zone"},
		{"timestamp without time zone", "SELECT timestamp without time zone $1", "SELECT $1::timestamp without time zone"},
		{"timestamp precision tz", "SELECT timestamp(3) with time zone $1", "SELECT $1::timestamp(3) with time zone"},
		{"time with time zone", "SELECT time with time zone $1", "SELECT $1::time with time zone"},
		{"date", "SELECT date $1", "SELECT $1::date"},
		{"numeric", "SELECT numeric $1 + numeric(5,2) $2", "SELECT $1::numeric + $2::numeric(5,2)"},
		{"int4 negated in parentheses", "SELECT id = (int4 $1) OR id = - int4 $2", "SELECT id = ($1::int4) OR id = - $2::int4"},
		{"double precision", "SELECT double precision $1", "SELECT $1::double precision"},
		{"character varying", "SELECT character varying $1, national character varying $2, bit varying $3",
			"SELECT $1::character varying, $2::national character varying, $3::bit varying"},
		{"char modifier", "SELECT char(2) $1", "SELECT $1::char(2)"},
		{"schema qualified", "SELECT pg_catalog.int4 $1, public.mytype $2", "SELECT $1::pg_catalog.int4, $2::public.mytype"},
		{"quoted type", `SELECT "my type" $1`, `SELECT $1::"my type"`},
		{"mixed with valid params", "SELECT * FROM t WHERE a = $1 AND b < now() - interval $2 LIMIT $3",
			"SELECT * FROM t WHERE a = $1 AND b < now() - $2::interval LIMIT $3"},
		{"multi-line spacing kept", "SELECT 1\n  WHERE x < interval $1\n    day", "SELECT 1\n  WHERE x < $1::interval\n    day"},

		{"no parameter", "SELECT interval '1 day'", ""},
		{"valid cast", "SELECT $1::interval", ""},
		{"inside string", "SELECT 'interval $1', E'date $1', $q$ interval $1 $q$", ""},
		{"inside quoted identifier", `SELECT "interval $1" FROM t`, ""},
		{"inside line comment", "SELECT 1 -- interval $1\n", ""},
		{"inside block comment", "SELECT 1 /* interval $1 */", ""},
		{"LIMIT OFFSET", "SELECT * FROM t LIMIT $1 OFFSET $2", ""},
		{"WHERE AND OR NOT", "SELECT 1 WHERE $1 AND NOT $2 OR $3", ""},
		{"ORDER BY", "SELECT * FROM t ORDER BY $1", ""},
		{"FETCH FIRST", "SELECT * FROM t FETCH FIRST $1 ROWS ONLY", ""},
		{"BETWEEN", "SELECT 1 WHERE x BETWEEN $1 AND $2", ""},
		{"AT TIME ZONE", "SELECT now() AT TIME ZONE $1", ""},
		{"ESCAPE", "SELECT 1 WHERE a LIKE $1 ESCAPE $2", ""},
		{"LIKE", "SELECT 1 WHERE a LIKE $1 OR a ILIKE $2", ""},
		{"frame", "SELECT sum(x) OVER (ORDER BY y ROWS $1 PRECEDING) FROM t", ""},
		{"CASE", "SELECT CASE $1 WHEN 1 THEN 2 END", ""},
		{"function call", "SELECT now() - interval($1)", ""},
		{"json VALUE", "SELECT json_object('a' VALUE $1)", ""},
		{"json PASSING", "SELECT json_query('{}', '$.a' PASSING $1 AS x)", ""},
		{"xml CONTENT", "SELECT xmlparse(content $1)", ""},
		{"xml DOCUMENT", "SELECT xmlserialize(document $1 AS text)", ""},
		{"qualified name is a type", "SELECT t.interval $1", "SELECT $1::t.interval"},
		{"name after a dot is not a start", "SELECT a. $1", ""},
		{"unscannable", "SELECT 'unterminated interval $1", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.sql
			}
			if got := RewriteTypedParams(tc.sql); got != want {
				t.Errorf("RewriteTypedParams(%q)\n got %q\nwant %q", tc.sql, got, want)
			}
		})
	}
}

// TestRewriteTypedParamsSameTree checks that the rewritten text has the parse tree of the original, locations
// aside: the parser reads `<type> $n` as a cast of the parameter, so the rewrite adds no node, no function and
// no statement. Postgres itself rejects the original; the integration tests run the rewritten text.
func TestRewriteTypedParamsSameTree(t *testing.T) {
	for _, sql := range []string{
		"SELECT count(*) FROM events WHERE created_at < now() - interval $1",
		"SELECT interval $1 year, interval $2 day to second, interval $3 second(3), interval(3) $4",
		"SELECT timestamp with time zone $1, timestamp(3) without time zone $2, time with time zone $3",
		"SELECT numeric(5,2) $1, double precision $2, character varying $3, national character varying $4, bit varying $5",
		"SELECT pg_catalog.int4 $1, \"text\" $2, date $3, uuid $4, t.mytype $5",
		"SELECT * FROM t WHERE a = $1 AND b BETWEEN timestamptz $2 AND timestamptz $3 ORDER BY $4 LIMIT $5",
	} {
		t.Run(sql, func(t *testing.T) {
			out := RewriteTypedParams(sql)
			if out == sql {
				t.Fatalf("nothing was rewritten")
			}
			before, err := parse(sql)
			if err != nil {
				t.Fatalf("parse original: %v", err)
			}
			after, err := parse(out)
			if err != nil {
				t.Fatalf("parse %q: %v", out, err)
			}
			if !reflect.DeepEqual(stripLocations(before), stripLocations(after)) {
				t.Errorf("parse tree changed by the rewrite to %q", out)
			}
		})
	}
}

// TestRewriteTypedParamsKeepsSafety checks that the policy check on the rewritten text refuses what it refused.
func TestRewriteTypedParamsKeepsSafety(t *testing.T) {
	p := defaultPolicy(t)
	tests := []struct {
		name, sql, wantErr string // wantErr "" means accepted
	}{
		{"typed literal accepted", "SELECT * FROM events WHERE created_at < now() - interval $1", ""},
		{"denied function", "SELECT pg_read_file(text $1)", "pg_read_file"},
		{"denied function in a cast argument", "SELECT 1 WHERE interval $1 > (SELECT pg_read_file('x'))::interval", "pg_read_file"},
		{"denied function after the type", "SELECT interval $1 day, pg_terminate_backend(1)", "pg_terminate_backend"},
		{"second statement", "SELECT interval $1; DROP TABLE events", "only one statement"},
		{"write statement", "DELETE FROM events WHERE created_at < now() - interval $1", "DeleteStmt"},
		{"select into", "SELECT interval $1 INTO t", "INTO"},
		{"function hidden in a string stays a string", "SELECT interval $1, 'pg_read_file(date $1)'", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Check(RewriteTypedParams(tc.sql), p)
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("refused: %v", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("error = %v, want one containing %q", err, tc.wantErr)
			}
		})
	}
}
