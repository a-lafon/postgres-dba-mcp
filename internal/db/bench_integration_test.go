//go:build integration

package db

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// BenchmarkCall measures one ReadOnly call (acquire, begin, settings, SELECT 1, rollback, release hook), and
// BenchmarkRelease the release hook alone (hypopg_reset when installed, then DISCARD ALL).
func BenchmarkCall(b *testing.B) {
	d, err := New(testCfg(b, testpg.RO(b)))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(d.Close)
	ctx := context.Background()
	fn := func(ctx context.Context, tx pgx.Tx) error {
		var n int
		return tx.QueryRow(ctx, "SELECT 1").Scan(&n)
	}
	if err := d.ReadOnly(ctx, fn); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	for b.Loop() {
		if err := d.ReadOnly(ctx, fn); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkRelease(b *testing.B) {
	d, err := New(testCfg(b, testpg.RO(b)))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(d.Close)
	if _, err := d.Info(context.Background()); err != nil {
		b.Fatal(err)
	}
	c, err := d.pool.Acquire(context.Background())
	if err != nil {
		b.Fatal(err)
	}
	defer c.Release()
	b.ResetTimer()
	for b.Loop() {
		if !d.afterRelease(c.Conn()) {
			b.Fatal("release hook failed")
		}
	}
}
