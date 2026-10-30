package security

import (
	"os"
	"reflect"
	"testing"
)

func TestDiff(t *testing.T) {
	base := Snapshot{
		Relations: []string{"public.accounts:r"}, Sequences: []string{"public.s=1"},
		Tables:        map[string]string{"public.accounts": "10/5"},
		SentinelAlive: true,
	}
	with := func(f func(*Snapshot)) Snapshot {
		s := base
		s.Tables = map[string]string{"public.accounts": "10/5"}
		f(&s)
		return s
	}
	tests := []struct {
		name string
		to   Snapshot
		want []string
	}{
		{"unchanged", with(func(*Snapshot) {}), nil},
		{"new table", with(func(s *Snapshot) { s.Relations = []string{"public.accounts:r", "public.pwned:r"} }), []string{"relations +public.pwned:r"}},
		{"large object", with(func(s *Snapshot) { s.LargeObjects = 1 }), []string{"large objects +1"}},
		{"sequence moved", with(func(s *Snapshot) { s.Sequences = []string{"public.s=2"} }), []string{"sequences +public.s=2", "sequences -public.s=1"}},
		{"rows deleted", with(func(s *Snapshot) { s.Tables["public.accounts"] = "0/0" }), []string{"public.accounts content 10/5 -> 0/0"}},
		{"advisory lock", with(func(s *Snapshot) { s.Advisory = []string{"4242/0/1/ExclusiveLock"} }), []string{"advisory locks +4242/0/1/ExclusiveLock"}},
		{"role setting", with(func(s *Snapshot) { s.RoleSettings = []string{"0/16384/{statement_timeout=0}"} }), []string{"role settings +0/16384/{statement_timeout=0}"}},
		{"sentinel killed", with(func(s *Snapshot) { s.SentinelAlive = false }), []string{"sentinel session terminated"}},
		{"notified", with(func(s *Snapshot) { s.Notifications = []string{"chan:payload"} }), []string{"notifications received: chan:payload"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Diff(base, tt.to); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Diff = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCorpus(t *testing.T) {
	cases, err := ParseCorpus("-- case: a write\nDELETE FROM t;\n\n-- case: b multi\nSELECT 1; -- harmless\nDELETE FROM t;\n")
	if err != nil {
		t.Fatal(err)
	}
	want := []Case{{"a", "write", "DELETE FROM t;"}, {"b", "multi", "SELECT 1; -- harmless\nDELETE FROM t;"}}
	if !reflect.DeepEqual(cases, want) {
		t.Errorf("cases = %+v, want %+v", cases, want)
	}
	if _, err := ParseCorpus("SELECT 1;"); err == nil {
		t.Error("corpus without case: want error")
	}
}

func TestRealCorpora(t *testing.T) {
	tests := []struct {
		file string
		min  int
	}{{"attacks.sql", 100}, {"legit.sql", 40}}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := os.ReadFile("../../../testdata/safesql/" + tt.file)
			if err != nil {
				t.Fatal(err)
			}
			cases, err := ParseCorpus(string(b))
			if err != nil {
				t.Fatal(err)
			}
			if len(cases) < tt.min {
				t.Errorf("%d cases, want at least %d", len(cases), tt.min)
			}
			for _, c := range cases {
				if c.SQL == "" {
					t.Errorf("case %s has no SQL", c.ID)
				}
			}
			if tt.file == "attacks.sql" && !reflect.DeepEqual(Channels(cases), []string{"chan", "c"}) {
				t.Errorf("channels = %v", Channels(cases))
			}
		})
	}
}
