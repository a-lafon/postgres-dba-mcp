package tasks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// The lock chain of A13 (PROTOCOL.md §3): a back-office session updates an order and leaves its transaction open, and
// two checkout sessions wait to update the same row. Neither ever commits.
const (
	lockTable   = "orders"
	holderQuery = "UPDATE public.orders SET status = '" + holderMarker + "' WHERE id = 1042"
	waiterQuery = "UPDATE public.orders SET status = 'paid' WHERE id = 1042"
)

// LockChain is a live lock chain on the bench database.
type LockChain struct {
	Holder  int32
	Waiters []int32
	holder  *pgx.Conn
	waiters []*pgx.Conn
	cancel  context.CancelFunc
	done    chan error
}

// StartLockChain opens the chain with dsn, a role that can update public.orders, and returns once both waiters are
// blocked by the holder. Stop ends it without any change to the data.
func StartLockChain(ctx context.Context, dsn string) (lc *LockChain, err error) {
	lc = &LockChain{done: make(chan error, 2)}
	defer func() {
		if err != nil {
			lc.Stop()
		}
	}()
	open := func(app string) (*pgx.Conn, int32, error) {
		cfg, err := pgx.ParseConfig(dsn)
		if err != nil {
			return nil, 0, err
		}
		cfg.RuntimeParams["application_name"] = app
		c, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return nil, 0, fmt.Errorf("lock chain: connect %s: %w", app, err)
		}
		var pid int32
		if err := c.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
			_ = c.Close(ctx)
			return nil, 0, err
		}
		if _, err := c.Exec(ctx, "BEGIN"); err != nil {
			_ = c.Close(ctx)
			return nil, 0, err
		}
		return c, pid, nil
	}
	if lc.holder, lc.Holder, err = open("backoffice"); err != nil {
		return lc, err
	}
	if _, err := lc.holder.Exec(ctx, holderQuery); err != nil {
		return lc, fmt.Errorf("lock chain: holder: %w", err)
	}
	wctx, cancel := context.WithCancel(context.Background())
	lc.cancel = cancel
	for range 2 {
		c, pid, err := open("checkout")
		if err != nil {
			return lc, err
		}
		lc.waiters, lc.Waiters = append(lc.waiters, c), append(lc.Waiters, pid)
		go func() {
			_, err := c.Exec(wctx, waiterQuery)
			lc.done <- err
		}()
	}
	// Both waiters must be blocked before the agent starts: the first by the holder's transaction, the second by the
	// first, whose tuple lock it queues behind. The check runs on its own session, so that the holder's last statement
	// stays the UPDATE.
	mon, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return lc, fmt.Errorf("lock chain: connect: %w", err)
	}
	defer func() { _ = mon.Close(ctx) }()
	deadline := time.Now().Add(15 * time.Second)
	for {
		var n int
		if err := mon.QueryRow(ctx, `SELECT count(*) FROM pg_stat_activity
			WHERE pid = ANY ($1) AND (pid = $2 AND $3 = ANY (pg_blocking_pids(pid)) OR pid = $4 AND cardinality(pg_blocking_pids(pid)) > 0)`,
			lc.Waiters, lc.Waiters[0], lc.Holder, lc.Waiters[1]).Scan(&n); err != nil {
			return lc, fmt.Errorf("lock chain: check: %w", err)
		}
		if n == 2 {
			return lc, nil
		}
		if time.Now().After(deadline) {
			return lc, fmt.Errorf("lock chain: %d of 2 sessions blocked after 15 s", n)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Stop cancels the waiters and rolls the holder back; closing a connection rolls its transaction back too.
func (lc *LockChain) Stop() {
	ctx := context.Background()
	if lc.cancel != nil {
		lc.cancel()
		for range lc.waiters {
			<-lc.done // canceled, or ran if the holder let go: either way its transaction stays open until Close
		}
	}
	for _, c := range lc.waiters {
		_ = c.Close(ctx)
	}
	if lc.holder != nil {
		_, _ = lc.holder.Exec(ctx, "ROLLBACK")
		_ = lc.holder.Close(ctx)
	}
}
