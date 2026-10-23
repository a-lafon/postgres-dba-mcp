//go:build integration

package hypo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

var eventsIdx = []Index{{Table: "events", Columns: []string{"account_id"}}}

const eventsQuery = "SELECT * FROM events WHERE account_id = 42"

// inTx runs fn inside db.ReadOnly on the fixture.
func inTx(t *testing.T, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	inTxOn(t, testpg.RO(t), fn)
}

// inTxOn is inTx on the database at dsn.
func inTxOn(t *testing.T, dsn string, fn func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	cfg, err := config.Parse([]string{dsn}, func(string) string { return "" })
	if err != nil {
		t.Fatal(err)
	}
	d, err := db.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	ctx := context.Background()
	if err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		fn(ctx, tx)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func scalar(t *testing.T, ctx context.Context, tx pgx.Tx, sql string) int {
	t.Helper()
	var n int
	if err := tx.QueryRow(ctx, sql).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func find(n *Node, pred func(*Node) bool) *Node {
	if pred(n) {
		return n
	}
	for _, c := range n.Plans {
		if f := find(c, pred); f != nil {
			return f
		}
	}
	return nil
}

func TestExplainSeqScan(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		n, tot, err := ExplainWithTotals(ctx, tx, eventsQuery, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		s := find(n, func(n *Node) bool { return n.NodeType == "Seq Scan" })
		if s == nil || s.Schema != "public" || s.Relation != "events" || s.Filter == "" {
			t.Fatalf("no seq scan on public.events: %+v", n)
		}
		if tot.ExecutionMs != nil || len(tot.HypoIndexNames) != 0 {
			t.Fatalf("totals = %+v", tot)
		}
	})
}

func TestExplainHypotheticalInReadOnly(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		n, tot, err := ExplainWithTotals(ctx, tx, eventsQuery, eventsIdx, false)
		if err != nil {
			t.Fatal(err)
		}
		if len(tot.HypoIndexNames) != 1 || find(n, func(n *Node) bool { return n.Index == tot.HypoIndexNames[0] }) == nil {
			t.Fatalf("hypothetical index %v not used: %+v", tot.HypoIndexNames, n)
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM hypopg_list_indexes"); c != 0 {
			t.Fatalf("%d hypothetical indexes left", c)
		}
		text, err := ExplainText(ctx, tx, eventsQuery, eventsIdx, false)
		if err != nil || !strings.Contains(text, "<") || strings.HasPrefix(text, "[") {
			t.Fatalf("text plan = %q, %v", text, err)
		}
	})
}

// S21: no leak to the next call.
func TestNoLeak(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		_, tot, err := ExplainWithTotals(ctx, tx, eventsQuery, eventsIdx, false)
		if err != nil {
			t.Fatal(err)
		}
		n, err := Explain(ctx, tx, eventsQuery, nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if find(n, func(n *Node) bool { return n.Index == tot.HypoIndexNames[0] }) != nil {
			t.Fatal("hypothetical index leaked into the next plan")
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM hypopg_list_indexes"); c != 0 {
			t.Fatalf("%d hypothetical indexes left", c)
		}
	})
}

// A call whose context is canceled after the hypothetical index exists must still reset hypopg on its own
// connection: behind a transaction pooler the pool's release hook may run on another server connection.
func TestResetOnCanceledContext(t *testing.T) {
	for name, dsn := range map[string]string{"direct": testpg.RO(t), "pgbouncer": testpg.Bouncer(t)} {
		t.Run(name, func(t *testing.T) {
			inTxOn(t, dsn, func(ctx context.Context, tx pgx.Tx) {
				cctx, cancel := context.WithCancel(ctx)
				defer cancel()
				err := session(cctx, tx, true, func(ctx context.Context, tx pgx.Tx) error {
					if _, err := createAll(ctx, tx, []string{`CREATE INDEX ON "public"."events" USING btree ("account_id")`}); err != nil {
						return err
					}
					cancel()
					return ctx.Err()
				})
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("session error = %v, want context.Canceled", err)
				}
				if c := scalar(t, ctx, tx, "SELECT count(*) FROM hypopg_list_indexes"); c != 0 {
					t.Fatalf("%d hypothetical indexes left after a canceled call", c)
				}
			})
		})
	}
}

// A `$1` in a literal is not a parameter: no generic plan, and ANALYZE is not refused.
func TestDollarInLiteralIsNotAParameter(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		for _, q := range []string{"SELECT '$1' FROM events LIMIT 1", "SELECT $$a $1$$ FROM events LIMIT 1"} {
			if _, _, err := ExplainWithTotals(ctx, tx, q, nil, true); err != nil {
				t.Errorf("%s: %v", q, err)
			}
		}
	})
}

func TestResetOnError(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		if _, err := Explain(ctx, tx, "SELEC 1", eventsIdx, false); err == nil {
			t.Fatal("expected a syntax error")
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM hypopg_list_indexes"); c != 0 {
			t.Fatalf("%d hypothetical indexes left", c)
		}
		if _, err := Explain(ctx, tx, eventsQuery, eventsIdx, false); err != nil {
			t.Fatalf("next Explain in the same transaction failed: %v", err)
		}
		// A failing plain call must not abort the transaction either.
		if _, err := Explain(ctx, tx, "SELECT nme FROM events", nil, false); err == nil {
			t.Fatal("expected an error")
		}
		if _, err := Explain(ctx, tx, eventsQuery, nil, false); err != nil {
			t.Fatalf("transaction unusable after a plain failure: %v", err)
		}
	})
}

func TestRefusals(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		tests := []struct {
			name    string
			query   string
			idxs    []Index
			analyze bool
			want    string
		}{
			{"analyze with indexes", eventsQuery, eventsIdx, true, "analyze cannot be combined with hypothetical indexes"},
			{"analyze with params", "SELECT * FROM events WHERE id = $1", nil, true, "analyze is not possible with $n parameters; replace them with values"},
			{"invalid index", eventsQuery, []Index{{Table: "events", Columns: []string{"lower(x)"}}}, false, "invalid hypothetical index"},
		}
		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				_, err := Explain(ctx, tx, tt.query, tt.idxs, tt.analyze)
				if err == nil || !strings.Contains(err.Error(), tt.want) {
					t.Fatalf("err = %v, want %q", err, tt.want)
				}
			})
		}
	})
}

func TestAnalyze(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		n, tot, err := ExplainWithTotals(ctx, tx, "SELECT account_id, count(*) FROM events GROUP BY account_id", nil, true)
		if err != nil {
			t.Fatal(err)
		}
		if n.ActualRows == nil || n.ActualTotalMs == nil || n.Loops == nil || tot.ExecutionMs == nil {
			t.Fatalf("no actual figures: %+v %+v", n, tot)
		}
	})
}

// A $n query is planned through PREPARE on every version and leaves no prepared statement.
func TestGenericPlan(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		q := "SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT $2"
		n, err := Explain(ctx, tx, q, nil, false)
		if err != nil || n == nil {
			t.Fatalf("Explain: %v", err)
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM pg_prepared_statements WHERE name = 'pgdba_explain'"); c != 0 {
			t.Fatal("prepared statement left behind")
		}
		// With hypothetical indexes too, and after a failing query.
		if _, err := Explain(ctx, tx, "SELECT nme FROM events WHERE account_id = $1", eventsIdx, false); err == nil {
			t.Fatal("expected an error")
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM pg_prepared_statements WHERE name = 'pgdba_explain'"); c != 0 {
			t.Fatal("prepared statement left behind after an error")
		}
		if _, err := Explain(ctx, tx, "SELECT * FROM events WHERE account_id = $1", eventsIdx, false); err != nil {
			t.Fatal(err)
		}
	})
}

// Bound NULLs are folded by the planner under EXPLAIN (GENERIC_PLAN), which turned the plan into a one-time-false
// Result of cost ~0 on PG 16+: the plan must read events and cost something.
func TestGenericPlanIsReal(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		n, err := Explain(ctx, tx, "SELECT * FROM events WHERE account_id = $1", nil, false)
		if err != nil {
			t.Fatal(err)
		}
		if find(n, func(x *Node) bool { return x.Relation == "events" }) == nil || n.TotalCost <= 1 {
			t.Errorf("plan does not read events or costs ~0: %+v", n)
		}
	})
}

func TestSize(t *testing.T) {
	inTx(t, func(ctx context.Context, tx pgx.Tx) {
		sz, err := Size(ctx, tx, Index{Table: "public.events", Columns: []string{"account_id"}})
		if err != nil || sz <= 0 {
			t.Fatalf("Size = %d, %v", sz, err)
		}
		if c := scalar(t, ctx, tx, "SELECT count(*) FROM hypopg_list_indexes"); c != 0 {
			t.Fatal("index left behind")
		}
		if _, err := Size(ctx, tx, Index{Table: "events"}); err == nil {
			t.Fatal("expected a validation error")
		}
	})
}
