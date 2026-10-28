//go:build integration

package safesql

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// TestLegitCorpusPassesTheGate carries the "no false refusals" claim of
// BENCHMARK.md through the whole restricted-mode path. TestCorpora stops at
// the name rules, which need no database, so nothing checked that the
// volatility gate lets the legitimate corpus through. Statements that only
// plan are not gated here either, because the tools do not gate them.
func TestLegitCorpusPassesTheGate(t *testing.T) {
	ctx := context.Background()
	legit := loadCorpus(t, "legit.sql", map[string]bool{"legit": true})
	p := defaultPolicy(t)
	conn, err := pgx.Connect(ctx, testpg.RO(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(ctx) })

	gated := 0
	for _, c := range legit {
		st, err := Check(c.SQL, p)
		if err != nil {
			t.Errorf("legit/%s refused by the name rules: %v\n%s", c.ID, err, c.SQL)
			continue
		}
		if !st.Executes() || len(st.Unresolved) == 0 {
			continue
		}
		gated++
		t.Run("legit/"+c.ID, func(t *testing.T) {
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if err := Gate(ctx, tx, st.Unresolved); err != nil {
				t.Errorf("refused by the volatility gate: %v\n%s", err, c.SQL)
			}
		})
	}
	if gated == 0 {
		t.Fatal("no legitimate statement reached the gate: the corpus no longer covers it")
	}
	t.Logf("%d of %d legitimate statements reached the gate", gated, len(legit))
}
