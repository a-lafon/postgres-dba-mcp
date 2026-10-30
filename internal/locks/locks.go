// Package locks implements the analyze_locks tool: one snapshot of the lock waits in progress, read from
// pg_locks and pg_stat_activity, with the transitive root of each blocking chain resolved in Go.
package locks

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

//go:embed locks.sql
var locksSQL string

const toolDescription = "Reports the lock waits in progress: which sessions are blocked, which sessions block them, " +
	"through how many hops, on which lock and for how long. Read-only diagnosis of a single snapshot: it never " +
	"cancels or terminates anything."

const inputSchema = `{"type":"object","properties":{}}`

// readOnlyRunner is the only database capability the tool needs: it never writes, whatever the access mode.
type readOnlyRunner interface {
	ReadOnly(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}

// row is one pair of a session waiting on a lock and a session directly blocking it. A nil pointer is a
// detail of pg_stat_activity the role may not read; rootPID is resolved in Go, not by the statement.
type row struct {
	blockedPID           int32
	waiting              *string
	lock                 string
	blockingPID, rootPID int32
	blockingState, txAge *string
	blockingQuery        *string
	txSeconds            *float64 // age of the blocker's transaction, for ordering only
}

// chain is one blocking chain, named by the session at its head.
type chain struct {
	root      int32
	state     *string
	txAge     *string
	txSeconds *float64
	waiting   int // distinct sessions waiting behind the root
}

// snapshot is the computed result of one call.
type snapshot struct {
	rows     []row
	chains   []chain
	hasStats bool
	role     string
	elapsed  time.Duration
}

// read runs the snapshot statement and resolves the chains. Rows come back ordered by wait duration, longest
// first, falling back to blocked_pid ascending when no duration is readable.
func read(ctx context.Context, run readOnlyRunner) (snapshot, error) {
	var s snapshot
	start := time.Now()
	err := run.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		s.rows = nil
		rs, err := tx.Query(ctx, locksSQL)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var r row
			if err := rs.Scan(&r.blockedPID, &r.waiting, &r.lock, &r.blockingPID, &r.blockingState,
				&r.txAge, &r.blockingQuery, &s.hasStats, &s.role, &r.txSeconds); err != nil {
				return err
			}
			s.rows = append(s.rows, r)
		}
		return rs.Err()
	})
	if err != nil {
		return snapshot{}, fmt.Errorf("analyze locks: %w", err)
	}
	resolveRoots(s.rows)
	s.chains = buildChains(s.rows)
	s.elapsed = time.Since(start)
	return s, nil
}

// resolveRoots fills each row's rootPID by following the blocking chain to a session that is not itself
// waiting. The visited set stops a cycle at the last distinct blocker found, so the row is still reported.
func resolveRoots(rows []row) {
	blockers := map[int32][]int32{}
	for _, r := range rows {
		blockers[r.blockedPID] = append(blockers[r.blockedPID], r.blockingPID)
	}
	for i := range rows {
		r := &rows[i]
		cur := r.blockingPID
		visited := map[int32]bool{r.blockedPID: true, cur: true}
		for {
			next := blockers[cur]
			if len(next) == 0 || visited[next[0]] {
				break
			}
			cur = next[0]
			visited[cur] = true
		}
		r.rootPID = cur
	}
}

// buildChains groups the resolved rows by root, longest-held transaction first. A root's own details are
// read from any row naming it as a direct blocker, wherever that row sits: inside a cycle the root of a
// group is blocked itself, so its details live in another group's row.
func buildChains(rows []row) []chain {
	details := map[int32]*row{}
	for i := range rows {
		r := &rows[i]
		if _, ok := details[r.blockingPID]; !ok {
			details[r.blockingPID] = r
		}
	}
	byRoot := map[int32]*chain{}
	seen := map[[2]int32]bool{} // (root, blocked pid), so two blockers of one session count once
	var order []int32
	for _, r := range rows {
		c, ok := byRoot[r.rootPID]
		if !ok {
			c = &chain{root: r.rootPID}
			if d, found := details[r.rootPID]; found {
				c.state, c.txAge, c.txSeconds = d.blockingState, d.txAge, d.txSeconds
			}
			byRoot[r.rootPID] = c
			order = append(order, r.rootPID)
		}
		if key := [2]int32{r.rootPID, r.blockedPID}; !seen[key] {
			seen[key] = true
			c.waiting++
		}
	}
	out := make([]chain, 0, len(order))
	for _, pid := range order {
		out = append(out, *byRoot[pid])
	}
	// Longest-held first; a root whose transaction age is hidden sorts last, then by pid for a stable report.
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i].txSeconds, out[j].txSeconds
		switch {
		case a != nil && b != nil && *a != *b:
			return *a > *b
		case (a == nil) != (b == nil):
			return a != nil
		default:
			return out[i].root < out[j].root
		}
	})
	return out
}

func tool(cfg *config.Config, run readOnlyRunner) server.Tool {
	return server.Tool{
		Group: "locks",
		Title: "Lock waits",
		Def: &mcp.Tool{
			Name:        "analyze_locks",
			Description: toolDescription,
			InputSchema: json.RawMessage(inputSchema),
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in struct{}
			if err := server.Decode(raw, &in); err != nil {
				return "", err
			}
			s, err := read(ctx, run)
			if err != nil {
				return "", err
			}
			return renderSnapshot(s, cfg)
		},
	}
}

// Register adds the tool analyze_locks to r.
func Register(r *server.Registry) {
	r.Add(tool(r.Config(), r.DB()))
}
