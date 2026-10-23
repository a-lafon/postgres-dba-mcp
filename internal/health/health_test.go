package health

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func names(cs []check) string {
	var n []string
	for _, c := range cs {
		n = append(n, c.name)
	}
	return strings.Join(n, ",")
}

func TestParseTypes(t *testing.T) {
	all := "index,connection,vacuum,sequence,replication,buffer,constraint"
	tests := []struct {
		in, want string
	}{
		{"all", all},
		{"constraint, Index", "index,constraint"},
		{"INDEX,index", "index"},
		{"vacuum,all", all},
		{" buffer ", "buffer"},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			got, err := parseTypes(tt.in)
			if err != nil {
				t.Fatal(err)
			}
			if names(got) != tt.want {
				t.Errorf("got %s, want %s", names(got), tt.want)
			}
		})
	}
	for _, in := range []string{"index,", "", "nope", "index,,vacuum", "all,x"} {
		t.Run("invalid "+in, func(t *testing.T) {
			got, err := parseTypes(in)
			want := fmt.Sprintf(`invalid health_type "%s": valid values are all, buffer, connection, constraint, index, replication, sequence, vacuum`, in)
			if err == nil || err.Error() != want || got != nil {
				t.Errorf("got %v, %v; want error %q", got, err, want)
			}
		})
	}
}

func TestFailedSection(t *testing.T) {
	s := failedSection("replication", errors.New("first line\nsecond line"))
	if got, want := s.text(), "replication  ! check failed: first line\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func fptr(f float64) *float64 { return &f }

func TestRenderTrapReport(t *testing.T) {
	secs := []section{
		indexResult{
			Invalid: []indexInfo{{Schema: "health", Name: "ix_invalid", Table: "dups"}},
			Duplicates: []dupPair{{
				Index:    indexInfo{Schema: "health", Name: "ix_dup_a", Columns: []string{"account_id"}},
				Covering: indexInfo{Schema: "health", Name: "ix_dup_b", Columns: []string{"account_id", "created_at"}},
			}},
			Unused: []unusedIndex{{Schema: "health", Name: "ix_unused", Size: 1258291}, {Schema: "health", Name: "ix_dup_b", Size: 352256, Scans: 1}},
		}.render(),
		connectionResult{Total: 7}.render(),
		vacuumResult{Dead: []deadRowsTable{{"health", "bloated", 40000, 100000, nil}}}.render(),
		sequenceResult{Readable: 1, Over: []sequenceUse{{Schema: "health", Name: "legacy_id_seq", DataType: "integer",
			OwnerSchema: "health", OwnerTable: "legacy", OwnerCol: "id", Used: 0.91, Left: 193273528}}}.render(),
		replicationResult{Slots: 1, Inactive: []inactiveSlot{{"health_test_slot", "physical", ""}}}.render(),
		bufferResult{Tables: fptr(0.992), Indexes: fptr(0.998)}.render(),
		constraintResult{Invalid: []invalidConstraint{
			{Schema: "health", Table: "orders_nv", Name: "amount_positive"},
			{"health", "child", "child_parent_fk", "health", "parent"},
		}}.render(),
	}
	for i, n := range []string{"index", "connection", "vacuum", "sequence", "replication", "buffer", "constraint"} {
		secs[i].name = n
	}
	want := `index        ! invalid: health.ix_invalid on health.dups
             ! duplicate: health.ix_dup_a (account_id) covered by health.ix_dup_b (account_id, created_at)
             ! rarely used (<= 50 scans): 2 indexes, 1.5 MB: health.ix_unused 1.2 MB 0 scans, health.ix_dup_b 344 kB 1 scan
connection   OK (cluster: 7 total, 0 idle in transaction)
vacuum       ! dead rows: health.bloated 28.6% (40k dead, 100k live), last vacuum never
sequence     ! health.legacy_id_seq (health.legacy.id, integer): 91.0% used, 193M left
replication  ! inactive slot health_test_slot (physical)
buffer       OK (cache hit 99.2% tables, 99.8% indexes)
constraint   ! health.orders_nv.amount_positive is NOT VALID
             ! health.child.child_parent_fk referencing health.parent is NOT VALID
`
	if got := renderReport(secs, 1<<20); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestCaps(t *testing.T) {
	var items []item
	for i := 0; i < 13; i++ {
		items = append(items, item{"wraparound", fmt.Sprintf("wraparound: t%d", i)})
	}
	items = append(items, item{"dead rows", "dead rows: x"})
	got := section{name: "vacuum", items: items}.text()
	lines := strings.Split(strings.TrimSuffix(got, "\n"), "\n")
	if len(lines) != 12 || lines[10] != "             ! … and 3 more wraparound" || lines[11] != "             ! dead rows: x" {
		t.Errorf("got:\n%s", got)
	}
	// Exactly the cap: no overflow item.
	exact := section{name: "x", items: items[:10]}.text()
	if strings.Contains(exact, "more") {
		t.Errorf("unexpected overflow:\n%s", exact)
	}
}

func TestRarelyUsedList(t *testing.T) {
	var u []unusedIndex
	for i := 0; i < 12; i++ {
		u = append(u, unusedIndex{Schema: "s", Name: fmt.Sprintf("i%d", i), Size: 1024})
	}
	got := indexResult{Unused: u}.render().items[0].text
	if !strings.HasPrefix(got, "rarely used (<= 50 scans): 12 indexes, 12 kB: s.i0 1.0 kB 0 scans") || !strings.HasSuffix(got, "s.i9 1.0 kB 0 scans, …") {
		t.Errorf("got %q", got)
	}
}

func TestRenderTruncation(t *testing.T) {
	secs := []section{{name: "index"}, {name: "connection"}, {name: "vacuum"}, {name: "sequence"}}
	if got := renderReport(secs, 1<<20); strings.Contains(got, "truncated") || strings.Count(got, "\n") != 4 {
		t.Errorf("unexpected report %q", got)
	}
	// Each section takes 16 bytes and the footer 29: room for the first section only.
	got := renderReport(secs, 50)
	if want := "index        OK\n-- truncated (50-byte limit)\n"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The four sections fit exactly: no footer.
	if got := renderReport(secs, 64); strings.Contains(got, "truncated") {
		t.Errorf("got %q", got)
	}
}

func TestConnectionThresholds(t *testing.T) {
	tests := []struct {
		name      string
		total, in int64
		want      string
	}{
		{"both", 612, 140, "connection   ! 612 connections (threshold 500)\n             ! 140 idle in transaction (threshold 100)\n"},
		{"healthy", 7, 0, "connection   OK (cluster: 7 total, 0 idle in transaction)\n"},
		{"idle only", 50, 101, "connection   ! 101 idle in transaction (threshold 100)\n"},
		{"at limits", 500, 100, "connection   OK (cluster: 500 total, 100 idle in transaction)\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := connectionResult{tt.total, tt.in}.render()
			s.name = "connection"
			if got := s.text(); got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestReplicationBranches(t *testing.T) {
	tests := []struct {
		name string
		in   replicationResult
		want string
	}{
		{"primary", replicationResult{Replicas: 2, Slots: 3}, "OK (primary, 2 replicas, 3 slots)"},
		{"replica streaming no lag", replicationResult{Replica: true, Streaming: true, Lag: fptr(0)}, "OK (replica, streaming, lag 0)"},
		{"replica streaming with lag", replicationResult{Replica: true, Streaming: true, Lag: fptr(6.1)}, "OK (replica, streaming, lag 6.1 s)"},
		{"replica lag unknown", replicationResult{Replica: true, Streaming: true}, "OK (replica, streaming)"},
		{"replica not streaming", replicationResult{Replica: true}, "! replica is not streaming from its primary"},
		{"logical slot", replicationResult{Inactive: []inactiveSlot{{"s", "logical", "app"}}}, "! inactive slot s (logical), database app"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.in.render()
			s.name = "r"
			got := strings.TrimSpace(strings.TrimPrefix(s.text(), "r"))
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestBufferCases(t *testing.T) {
	tests := []struct {
		name string
		in   bufferResult
		want string
	}{
		{"low tables", bufferResult{Tables: fptr(0.801), Indexes: fptr(0.998)}, "! table cache hit 80.1% (below 95%)"},
		{"both low", bufferResult{Tables: fptr(0.5), Indexes: fptr(0.9)}, "! table cache hit 50.0% (below 95%)\n             ! index cache hit 90.0% (below 95%)"},
		{"null tables", bufferResult{Indexes: fptr(0.99)}, "OK (cache hit 99.0% indexes)"},
		{"null both", bufferResult{}, "OK (no cache statistics yet)"},
		{"one culprit", bufferResult{Tables: fptr(0.338), Indexes: fptr(0.998), Culprits: []culprit{{"public.events", 0.972, 1234}}},
			"! table cache hit 33.8% (below 95%): public.events 97% of disk reads, 1.2k seq scans"},
		{"three culprits, one scan", bufferResult{Tables: fptr(0.5), Culprits: []culprit{{"public.a", 0.6, 1}, {"s.b", 0.25, 0}, {"s.c", 0.1, 999}}},
			"! table cache hit 50.0% (below 95%): public.a 60% of disk reads, 1 seq scan; s.b 25% of disk reads, 0 seq scans; s.c 10% of disk reads, 999 seq scans"},
		{"culprits and low indexes", bufferResult{Tables: fptr(0.5), Indexes: fptr(0.9), Culprits: []culprit{{"public.a", 1, 3}}},
			"! table cache hit 50.0% (below 95%): public.a 100% of disk reads, 3 seq scans\n             ! index cache hit 90.0% (below 95%)"},
		{"share rounds down", bufferResult{Tables: fptr(0.333), Culprits: []culprit{{"public.events", 0.9957, 7828}, {"public.x", 0.29, 2}}},
			"! table cache hit 33.3% (below 95%): public.events 99% of disk reads, 7.8k seq scans; public.x 29% of disk reads, 2 seq scans"},
		{"culprits ignored when rate is fine", bufferResult{Tables: fptr(0.96), Culprits: []culprit{{"public.a", 1, 3}}}, "OK (cache hit 96.0% tables)"},
		{"at threshold", bufferResult{Tables: fptr(0.95), Indexes: fptr(0.95)}, "OK (cache hit 95.0% tables, 95.0% indexes)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := tt.in.render()
			s.name = "buffer"
			got := strings.TrimSuffix(strings.TrimPrefix(s.text(), "buffer       "), "\n")
			if got != tt.want {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestSequenceRender(t *testing.T) {
	s := sequenceResult{}.render()
	s.name = "sequence"
	if got := s.text(); got != "sequence     OK (no sequences)\n" {
		t.Errorf("got %q", got)
	}
	s = sequenceResult{Readable: 2}.render()
	s.name = "sequence"
	if got := s.text(); got != "sequence     OK\n" {
		t.Errorf("got %q", got)
	}
	s = sequenceResult{Readable: 1, Over: []sequenceUse{{Schema: "public", Name: "s", DataType: "bigint", Used: 0.95, Left: 5}}}.render()
	if got := s.items[0].text; got != "public.s (bigint): 95.0% used, 5 left" {
		t.Errorf("got %q", got)
	}
}

func idx(name string, cols ...string) indexInfo {
	return indexInfo{Schema: "s", Table: "t", Name: name, Using: "btree", Columns: cols, Valid: true}
}

func TestDuplicateIndexes(t *testing.T) {
	uniq := idx("u", "a", "b")
	uniq.Unique = true
	pk := idx("pk", "a")
	pk.Primary, pk.Unique = true, true
	invalid := idx("b_full", "a", "b")
	invalid.Valid = false
	hash := idx("h", "a", "b")
	hash.Using = "hash"
	partial := idx("p", "a", "b")
	partial.Pred = "(x > 0)"
	expr := idx("e", "<expr>")
	expr.Exprs = "lower(x)"
	expr2 := idx("e2", "<expr>", "z")
	expr2.Exprs = "other"

	tests := []struct {
		name string
		in   []indexInfo
		want []string // "index>covering"
	}{
		{"prefix", []indexInfo{idx("a1", "a"), idx("a2", "a", "b")}, []string{"a1>a2"}},
		{"not a prefix", []indexInfo{idx("b1", "b"), idx("a2", "a", "b")}, nil},
		{"same columns, greater name is unneeded", []indexInfo{idx("i1", "a"), idx("i2", "a")}, []string{"i2>i1"}},
		{"unique covering with same columns", []indexInfo{idx("i1", "a", "b"), uniq}, []string{"i1>u"}},
		{"unique index is never unneeded", []indexInfo{uniq, idx("z", "a", "b", "c")}, nil},
		{"primary covers", []indexInfo{idx("i1", "a"), pk}, []string{"i1>pk"}},
		{"invalid covering ignored", []indexInfo{idx("a1", "a"), invalid}, nil},
		{"different using", []indexInfo{idx("a1", "a"), hash}, nil},
		{"different predicate", []indexInfo{idx("a1", "a"), partial}, nil},
		{"different expressions", []indexInfo{expr, expr2}, nil},
		{"same expression", []indexInfo{expr, func() indexInfo { e := idx("e3", "<expr>", "z"); e.Exprs = "lower(x)"; return e }()}, []string{"e>e3"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got []string
			for _, d := range duplicateIndexes(tt.in) {
				got = append(got, d.Index.Name+">"+d.Covering.Name)
			}
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// partIx makes the index of partition payment_p<n> on column customer_id, as seen in a partition tree of public.payment.
func partIx(n int, name, cols string) indexInfo {
	return indexInfo{Schema: "public", Table: fmt.Sprintf("payment_p%d", n), Name: fmt.Sprintf(name, n), Columns: []string{cols}, Root: "public.payment"}
}

func TestRenderGroupsPartitionFindings(t *testing.T) {
	var dups []dupPair
	var unused []unusedIndex
	for n := 1; n <= 3; n++ {
		dups = append(dups, dupPair{partIx(n, "payment_p%d_customer_id_idx", "customer_id"), partIx(n, "idx_fk_payment_p%d_customer_id", "customer_id")})
		unused = append(unused, unusedIndex{"public", fmt.Sprintf("payment_p%d_amount_idx", n), 1024, int64(n), fmt.Sprintf("payment_p%d", n), "public.payment"})
	}
	plain := dupPair{indexInfo{Schema: "health", Name: "ix_dup_a", Table: "events_dup", Columns: []string{"account_id"}},
		indexInfo{Schema: "health", Name: "ix_dup_b", Table: "events_dup", Columns: []string{"account_id", "created_at"}}}
	unused = append(unused, unusedIndex{"health", "ix_unused", 2048, 0, "unused_big", ""})
	tests := []struct {
		name string
		res  indexResult
		want []string
	}{
		{"duplicates of several partitions become one line", indexResult{Duplicates: dups},
			[]string{"duplicate on 3 partitions of public.payment: <partition>_customer_id_idx (customer_id) covered by idx_fk_<partition>_customer_id (customer_id); e.g. payment_p1"}},
		{"one partition keeps its own line", indexResult{Duplicates: dups[:1]},
			[]string{"duplicate: public.payment_p1_customer_id_idx (customer_id) covered by public.idx_fk_payment_p1_customer_id (customer_id)"}},
		{"non-partition finding stays verbatim", indexResult{Duplicates: append([]dupPair{plain}, dups...)},
			[]string{"duplicate: health.ix_dup_a (account_id) covered by health.ix_dup_b (account_id, created_at)", "duplicate on 3 partitions of public.payment: "}},
		{"rarely used keeps count and total, groups the partition indexes", indexResult{Unused: unused},
			[]string{"rarely used (<= 50 scans): 4 indexes, 5.0 kB: public.<partition>_amount_idx (3 partitions of public.payment) 3.0 kB 6 scans, health.ix_unused 2.0 kB 0 scans"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			items := tc.res.render().items
			if len(items) != len(tc.want) {
				t.Fatalf("got %d items %v, want %d", len(items), items, len(tc.want))
			}
			for i, w := range tc.want {
				if !strings.Contains(items[i].text, w) {
					t.Errorf("item %d = %q, want it to contain %q", i, items[i].text, w)
				}
			}
		})
	}
}
