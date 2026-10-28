package safesql

import (
	"strings"
	"testing"
)

// TestUnresolved checks what Check hands to the volatility gate: the calls no
// name rule settled, once each, in source order and with the case the client
// wrote, because that is the spelling pg_proc holds.
func TestUnresolved(t *testing.T) {
	tests := []struct {
		name, sql   string
		allow       []string
		want        []string // "schema.name", schema empty when unqualified
		wantErrLike string
	}{
		{name: "builtins are undecided by name", sql: "SELECT now(), count(*) FROM t", want: []string{".now", ".count"}},
		{name: "qualified call keeps its schema", sql: "SELECT utl_file.fopen('/tmp', 'x', 'w')", want: []string{"utl_file.fopen"}},
		{name: "repeats collapse", sql: "SELECT lower(a), lower(b) FROM t", want: []string{".lower"}},
		{name: "source order", sql: "SELECT b(), a() FROM t WHERE c()", want: []string{".b", ".a", ".c"}},
		{name: "quoted name keeps its case", sql: `SELECT "MyFunc"()`, want: []string{".MyFunc"}},
		{name: "allowed by the user is settled", sql: "SELECT score(x) FROM t", allow: []string{"score"}, want: nil},
		{name: "denied never reaches the gate", sql: "SELECT pg_read_file('x')", wantErrLike: "pg_read_file"},
		{name: "same name in two schemas stays apart", sql: "SELECT a.f(), b.f()", want: []string{"a.f", "b.f"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPolicy(tt.allow, nil)
			if err != nil {
				t.Fatal(err)
			}
			st, err := Check(tt.sql, p)
			if tt.wantErrLike != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErrLike) {
					t.Fatalf("error %v, want one naming %q", err, tt.wantErrLike)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var got []string
			for _, r := range st.Unresolved {
				got = append(got, r.Schema+"."+r.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Fatalf("unresolved %v, want %v", got, tt.want)
			}
		})
	}
}

// TestExecutes pins which statements evaluate their functions, because the
// gate only runs for those: a plain EXPLAIN plans without folding a volatile
// function, so it evaluates none.
func TestExecutes(t *testing.T) {
	tests := []struct {
		sql  string
		want bool
	}{
		{"SELECT f()", true},
		{"EXPLAIN ANALYZE SELECT f()", true},
		{"EXPLAIN SELECT f()", false},
		{"SHOW server_version", false},
	}
	p := defaultPolicy(t)
	for _, tt := range tests {
		t.Run(tt.sql, func(t *testing.T) {
			st, err := Check(tt.sql, p)
			if err != nil {
				t.Fatal(err)
			}
			if st.Executes() != tt.want {
				t.Fatalf("Executes() = %v, want %v", st.Executes(), tt.want)
			}
		})
	}
}

func TestReviewedSafe(t *testing.T) {
	tests := []struct {
		extension, name string
		want            bool
	}{
		{"hypopg", "hypopg_reset", true},
		{"hypopg", "HypoPG_Reset", true}, // the catalog name decides, not its case
		{"pgcrypto", "gen_salt", true},
		// The built-in of PostgreSQL 13 and later never reaches Gate, but an
		// older pgcrypto owning the name must stay usable all the same.
		{"pgcrypto", "gen_random_uuid", true},
		// Neither hypopg_reset() nor DISCARD ALL unhides, so a hidden index
		// would follow the pooled connection into the next request.
		{"hypopg", "hypopg_hide_index", false},
		{"", "hypopg_reset", false},       // a hand-made look-alike owns no extension
		{"orafce", "hypopg_reset", false}, // reviewed for hypopg only
		{"pg_background", "pg_background_run", false},
		{"orafce", "fopen", false},
	}
	for _, tt := range tests {
		t.Run(tt.extension+"."+tt.name, func(t *testing.T) {
			if got := reviewedSafe(tt.extension, tt.name); got != tt.want {
				t.Fatalf("reviewedSafe(%q, %q) = %v, want %v", tt.extension, tt.name, got, tt.want)
			}
		})
	}
}

// TestReviewedVolatileNotDenied is TestReviewedNotDenied for the extension
// list: a name the denylist refuses never reaches Gate, so reviewing it
// clears nothing and only reads as an allowance it is not.
func TestReviewedVolatileNotDenied(t *testing.T) {
	p := defaultPolicy(t)
	for ext, names := range reviewedVolatile {
		for _, n := range names {
			if p.refuses(call{name: strings.ToLower(n)}) {
				t.Errorf("%s.%s is reviewed as safe but the built-in denylist refuses it: remove it from reviewedVolatile", ext, n)
			}
		}
	}
}
