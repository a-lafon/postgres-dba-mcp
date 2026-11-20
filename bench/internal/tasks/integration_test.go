//go:build integration

package tasks

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// TestReferenceAndLockChain needs the loaded bench database (`up`, `load`). It computes the reference, checks that two
// states taken in a row do not differ, and that the lock chain blocks two sessions and leaves no change behind.
func TestReferenceAndLockChain(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	admin, err := pgx.Connect(ctx, servers.BenchAdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	ref, err := LoadRef(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if len(ref.Spenders) != 5 || len(ref.Categories) != 5 || len(ref.TopQueries) != 3 || len(ref.Tickets) != 5 ||
		ref.May2022 == "" || len(ref.CustomerFKs) == 0 || len(ref.FKBackers) == 0 || len(ref.PKIndexes) == 0 {
		t.Fatalf("incomplete reference: %+v", ref)
	}
	t.Logf("A01 %v\nA03 %s wrong %v\nA04 %d/%d\nA09 %v\nA10 %q\nA15 first %d", ref.Spenders, ref.May2022, ref.May2022Wrong,
		ref.ActiveInt, ref.ActiveBool, ref.WorkloadCols, ref.TopQueries, ref.Tickets[0].ID)
	before, err := TakeState(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	lc, err := StartLockChain(ctx, servers.BenchOwnerDSN)
	if err != nil {
		t.Fatal(err)
	}
	var state, query string
	if err := admin.QueryRow(ctx, `SELECT state, query FROM pg_stat_activity WHERE pid = $1`, lc.Holder).Scan(&state, &query); err != nil {
		t.Fatal(err)
	}
	if state != "idle in transaction" || query != holderQuery {
		t.Errorf("holder: state %q, query %q", state, query)
	}
	lc.Stop()
	after, err := TakeState(ctx, admin)
	if err != nil {
		t.Fatal(err)
	}
	if d := Changes(before, after); len(d) > 0 {
		t.Errorf("the lock chain changed the database: %v", d)
	}
}
