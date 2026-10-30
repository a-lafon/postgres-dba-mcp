package locks

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/health"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

func ptr[T any](v T) *T { return &v }

// listTools registers the locks and health tools under the given --tools value and lists them. The pool is
// lazy, so no database is needed.
func listTools(t *testing.T, args ...string) []*mcp.Tool {
	t.Helper()
	cfg, err := config.Parse(append(args, "postgres://u@localhost:1/d"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	reg := server.New(cfg, d, "test")
	Register(reg)
	health.Register(reg)

	ctx := context.Background()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := reg.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	res, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	return res.Tools
}

func names(tools []*mcp.Tool) []string {
	out := make([]string, len(tools))
	for i, tool := range tools {
		out[i] = tool.Name
	}
	return out
}

func TestRegisterGroups(t *testing.T) {
	// Every group but locks: --tools takes the enabled ones, so locks is simply left out of the list.
	without := strings.Join(slices.DeleteFunc(slices.Clone(config.Groups), func(g string) bool { return g == "locks" }), ",")
	for _, tt := range []struct {
		name            string
		args            []string
		locks, dbHealth bool
	}{
		{"all groups", nil, true, true},
		{"locks off", []string{"--tools=" + without}, false, true},
		{"locks only", []string{"--tools=locks"}, true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := names(listTools(t, tt.args...))
			if slices.Contains(got, "analyze_locks") != tt.locks {
				t.Errorf("analyze_locks in %v, want %v", got, tt.locks)
			}
			if slices.Contains(got, "analyze_db_health") != tt.dbHealth {
				t.Errorf("analyze_db_health in %v, want %v", got, tt.dbHealth)
			}
		})
	}
}

func TestInputSchemaHasNoProperty(t *testing.T) {
	tools := listTools(t, "--tools=locks")
	if len(tools) != 1 {
		t.Fatalf("tools = %v", names(tools))
	}
	raw, err := json.Marshal(tools[0].InputSchema)
	if err != nil {
		t.Fatal(err)
	}
	var schema struct {
		Type       string         `json:"type"`
		Properties map[string]any `json:"properties"`
	}
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	if schema.Type != "object" || len(schema.Properties) != 0 {
		t.Errorf("input schema = %s", raw)
	}
}

// pair builds a row of the snapshot, before the root is resolved.
func pair(blocked, blocking int32) row {
	return row{blockedPID: blocked, blockingPID: blocking}
}

func TestResolveRoots(t *testing.T) {
	for _, tt := range []struct {
		name string
		rows []row
		want []int32
	}{
		{"one hop", []row{pair(4821, 4790)}, []int32{4790}},
		{"three hops", []row{pair(4835, 4821), pair(4821, 4790)}, []int32{4790, 4790}},
		{"two chains", []row{pair(2, 1), pair(4, 3)}, []int32{1, 3}},
		{"two direct blockers", []row{pair(3, 1), pair(3, 2), pair(2, 1)}, []int32{1, 1, 1}},
		// 1 waits on 2 and 2 waits on 1: the walk stops at the last distinct blocker instead of looping.
		{"cycle", []row{pair(1, 2), pair(2, 1)}, []int32{2, 1}},
		{"self cycle", []row{pair(1, 1)}, []int32{1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rows := slices.Clone(tt.rows)
			resolveRoots(rows)
			for i, r := range rows {
				if r.rootPID != tt.want[i] {
					t.Errorf("row %d: root %d, want %d", i, r.rootPID, tt.want[i])
				}
			}
		})
	}
}

func TestBuildChains(t *testing.T) {
	// 4835 and 4821 both end up behind 4790; 9002 is a shorter-held chain of its own.
	rows := []row{
		{blockedPID: 4821, blockingPID: 4790, blockingState: ptr("idle in transaction"), txAge: ptr("00:05:03"), txSeconds: ptr(303.0)},
		{blockedPID: 4835, blockingPID: 4821},
		{blockedPID: 9001, blockingPID: 9002, blockingState: ptr("active"), txAge: ptr("00:00:12"), txSeconds: ptr(12.0)},
	}
	resolveRoots(rows)
	got := buildChains(rows)
	if len(got) != 2 {
		t.Fatalf("chains = %+v", got)
	}
	if got[0].root != 4790 || got[0].waiting != 2 || *got[0].txAge != "00:05:03" {
		t.Errorf("chain 0 = %+v", got[0])
	}
	if got[1].root != 9002 || got[1].waiting != 1 {
		t.Errorf("chain 1 = %+v", got[1])
	}

	single := []row{{blockedPID: 2, blockingPID: 1}}
	resolveRoots(single)
	if c := buildChains(single); len(c) != 1 || c[0].root != 1 || c[0].waiting != 1 {
		t.Errorf("single chain = %+v", c)
	}
}

// TestBuildChainsCycleKeepsDetails pins the case where a root is blocked itself: with 1, 2 and 3 waiting on
// each other, no group holds a row naming its own root as a direct blocker, so the details have to be read
// from another group's row.
func TestBuildChainsCycleKeepsDetails(t *testing.T) {
	rows := []row{
		{blockedPID: 1, blockingPID: 2, blockingState: ptr("active"), txAge: ptr("00:05:00"), txSeconds: ptr(300.0)},
		{blockedPID: 2, blockingPID: 3, blockingState: ptr("active"), txAge: ptr("00:04:00"), txSeconds: ptr(240.0)},
		{blockedPID: 3, blockingPID: 1, blockingState: ptr("active"), txAge: ptr("00:03:00"), txSeconds: ptr(180.0)},
	}
	resolveRoots(rows)
	for _, c := range buildChains(rows) {
		if c.state == nil || c.txAge == nil {
			t.Errorf("chain root %d lost its details: %+v", c.root, c)
		}
	}
}

// TestBuildChainsCountsSessionOnce covers a session held up by two direct blockers: two rows, one waiter.
func TestBuildChainsCountsSessionOnce(t *testing.T) {
	rows := []row{pair(3, 1), pair(3, 2), pair(2, 1)}
	resolveRoots(rows)
	got := buildChains(rows)
	if len(got) != 1 || got[0].root != 1 || got[0].waiting != 2 {
		t.Fatalf("chains = %+v, want one chain rooted at 1 with 2 waiting sessions", got)
	}
	s := snapshot{rows: rows, chains: got, hasStats: true, elapsed: time.Millisecond}
	out, err := renderSnapshot(s, cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "-- 1 chain, root pid 1, 2 sessions waiting\n") || !strings.Contains(out, "-- 3 rows,") {
		t.Errorf("got:\n%s", out)
	}
}

// TestRenderCapsTheChainNote is the lock storm: format.Render refuses a prefix it cannot fit instead of
// truncating it, so an uncapped note would turn the worst incident into an error and no report at all.
func TestRenderCapsTheChainNote(t *testing.T) {
	var rows []row
	for i := int32(0); i < 1000; i++ {
		p := 10000 + i*2
		rows = append(rows, row{blockedPID: p + 1, blockingPID: p, lock: "transactionid ShareLock",
			waiting: ptr("00:01:00"), blockingState: ptr("idle in transaction"), txAge: ptr("00:05:03"), txSeconds: ptr(303.0)})
	}
	resolveRoots(rows)
	s := snapshot{rows: rows, chains: buildChains(rows), hasStats: true, elapsed: time.Millisecond}
	got, err := renderSnapshot(s, cfg(t, "--max-bytes=1024"))
	if err != nil {
		t.Fatalf("1000 chains must still report: %v", err)
	}
	if len(got) > 1024 {
		t.Errorf("%d bytes over the 1024 budget", len(got))
	}
	if !strings.HasPrefix(got, "-- 1000 chains, root pid 10000,") || !strings.Contains(got, "; +995 more\n") {
		t.Errorf("note not capped:\n%s", got)
	}
}

// TestRenderNoteOnlyWhenHidden: lacking the role is not the same as losing data. A role that owns every
// session reads them whole, and must not be told its report is incomplete.
func TestRenderNoteOnlyWhenHidden(t *testing.T) {
	s := specSnapshot()
	s.hasStats = false
	got, err := renderSnapshot(s, cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "session details hidden") {
		t.Errorf("note on a report with no hidden cell:\n%s", got)
	}
	s.rows[0].blockingQuery = nil
	if got, err = renderSnapshot(s, cfg(t)); err != nil {
		t.Fatal(err)
	} else if !strings.Contains(got, "session details hidden") {
		t.Errorf("no note on a report with a hidden cell:\n%s", got)
	}
}

func cfg(t *testing.T, args ...string) *config.Config {
	t.Helper()
	c, err := config.Parse(append(args, "postgres://u@localhost:1/d"), func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// specSnapshot is the "Two sessions waiting behind one holder" scenario of the spec.
func specSnapshot() snapshot {
	q := "UPDATE orders SET status = $1 WHERE id = $2"
	rows := []row{
		{blockedPID: 4821, waiting: ptr("00:02:14"), lock: "transactionid ShareLock", blockingPID: 4790,
			blockingState: ptr("idle in transaction"), txAge: ptr("00:05:03"), blockingQuery: &q, txSeconds: ptr(303.0)},
		{blockedPID: 4835, waiting: ptr("00:01:58"), lock: "relation AccessExclusiveLock on public.orders", blockingPID: 4790,
			blockingState: ptr("idle in transaction"), txAge: ptr("00:05:03"), blockingQuery: &q, txSeconds: ptr(303.0)},
	}
	resolveRoots(rows)
	return snapshot{rows: rows, chains: buildChains(rows), hasStats: true, role: "pgdba_ro", elapsed: 12 * time.Millisecond}
}

func TestRenderSpecScenario(t *testing.T) {
	got, err := renderSnapshot(specSnapshot(), cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "-- 1 chain, root pid 4790, idle in transaction for 00:05:03, 2 sessions waiting\n" +
		"blocked_pid,waiting,lock,blocking_pid,root_pid,blocking_state,tx_age,blocking_query\n" +
		"4821,00:02:14,transactionid ShareLock,4790,4790,idle in transaction,00:05:03,UPDATE orders SET status = $1 WHERE id = $2\n" +
		"4835,00:01:58,relation AccessExclusiveLock on public.orders,4790,4790,idle in transaction,00:05:03,UPDATE orders SET status = $1 WHERE id = $2\n" +
		"-- 2 rows, 12 ms\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRenderEmpty(t *testing.T) {
	got, err := renderSnapshot(snapshot{}, cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	if got != "-- no lock wait in progress\n" {
		t.Errorf("got %q", got)
	}
}

func TestRenderDegraded(t *testing.T) {
	s := specSnapshot()
	s.hasStats = false
	for i := range s.rows {
		s.rows[i].waiting, s.rows[i].blockingState, s.rows[i].txAge, s.rows[i].blockingQuery, s.rows[i].txSeconds = nil, nil, nil, nil, nil
	}
	s.chains = buildChains(s.rows)
	got, err := renderSnapshot(s, cfg(t))
	if err != nil {
		t.Fatal(err)
	}
	want := "-- 1 chain, root pid 4790, 2 sessions waiting\n" +
		"-- session details hidden: grant pg_read_all_stats to pgdba_ro to read waits, states and query texts\n" +
		"blocked_pid,waiting,lock,blocking_pid,root_pid,blocking_state,tx_age,blocking_query\n" +
		`4821,\N,transactionid ShareLock,4790,4790,\N,\N,\N` + "\n" +
		`4835,\N,relation AccessExclusiveLock on public.orders,4790,4790,\N,\N,\N` + "\n" +
		"-- 2 rows, 12 ms\n"
	if got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestChainNoteTwoChains(t *testing.T) {
	rows := []row{
		{blockedPID: 2, blockingPID: 1, blockingState: ptr("idle in transaction"), txAge: ptr("00:05:03"), txSeconds: ptr(303.0)},
		{blockedPID: 4, blockingPID: 3, blockingState: ptr("active"), txAge: ptr("00:00:12"), txSeconds: ptr(12.0)},
	}
	resolveRoots(rows)
	got := chainNote(buildChains(rows))
	want := "-- 2 chains, root pid 1, idle in transaction for 00:05:03, 1 session waiting; " +
		"root pid 3, active for 00:00:12, 1 session waiting"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderRespectsMaxRows(t *testing.T) {
	s := specSnapshot()
	got, err := renderSnapshot(s, cfg(t, "--max-rows=1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "4821,00:02:14") || strings.Contains(got, "4835") {
		t.Errorf("the longest wait was not kept:\n%s", got)
	}
	if !strings.Contains(got, "-- 1 row, truncated (more than 1), 12 ms\n") {
		t.Errorf("footer:\n%s", got)
	}
}

func TestRenderRespectsMaxBytes(t *testing.T) {
	s := specSnapshot()
	for i := int32(0); i < 50; i++ { // one spec row is about 110 bytes, so 50 of them overflow the budget
		r := s.rows[1]
		r.blockedPID = 5000 + i
		s.rows = append(s.rows, r)
	}
	resolveRoots(s.rows)
	s.chains = buildChains(s.rows)
	got, err := renderSnapshot(s, cfg(t, "--max-bytes=1024"))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) > 1024 || !strings.Contains(got, "truncated (1024-byte limit)") {
		t.Errorf("%d bytes:\n%s", len(got), got)
	}
}

func TestRenderShortensLongQuery(t *testing.T) {
	s := specSnapshot()
	long := "UPDATE orders SET status = $1 WHERE id = $2 /* " + strings.Repeat("x", 100) + " */"
	s.rows[0].blockingQuery = &long
	got, err := renderSnapshot(s, cfg(t, "--max-cell=16"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "…[+") || !strings.Contains(got, " chars]") {
		t.Errorf("no shortening marker:\n%s", got)
	}
}
