//go:build integration

package db

import (
	"context"
	"io"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// flakyProxy forwards TCP connections to target, except that every connection after the first is closed at once
// until heal is called. It returns the address to connect to and heal. Counting on "the audit opens connections 1
// and 2" was racy: any extra dial of the pool got through and let the audit succeed.
func flakyProxy(t *testing.T, target string) (addr string, heal func()) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	var n atomic.Int32
	var healed atomic.Bool
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			if n.Add(1) > 1 && !healed.Load() {
				_ = c.Close()
				continue
			}
			go func() {
				defer func() { _ = c.Close() }()
				up, err := net.Dial("tcp", target)
				if err != nil {
					return
				}
				defer func() { _ = up.Close() }()
				go func() { _, _ = io.Copy(up, c) }()
				_, _ = io.Copy(c, up)
			}()
		}
	}()
	return ln.Addr().String(), func() { healed.Store(true) }
}

// A failed audit goes back to idle, so the next call that gets a connection runs it again and logs its warnings.
func TestRoleAuditRunsAgainAfterFailure(t *testing.T) {
	u, err := url.Parse(testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	// The first connection is the caller's; every connection the audit opens dies until heal.
	var heal func()
	u.Host, heal = flakyProxy(t, u.Host)
	logs := captureLog(t)
	d := open(t, u.String())

	// The call keeps the caller's connection busy until the audit has failed, so the audit has to open its own.
	err = d.ReadOnly(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		deadline := time.Now().Add(10 * time.Second)
		for {
			d.mu.Lock()
			st := d.audit
			d.mu.Unlock()
			if st == auditIdle {
				return nil
			}
			if st == auditComplete || time.Now().After(deadline) {
				t.Errorf("audit state = %d after a failed attempt, want idle", st)
				return nil
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(logs(), "WARNING") {
		t.Fatalf("a failed audit logged %q", logs())
	}

	heal()
	scalar[int](t, d, "SELECT 1")
	waitAudit(t, d)
	if want := "WARNING: role postgres: is a superuser; use a dedicated read-only role\n"; !strings.Contains(logs(), want) {
		t.Errorf("log = %q, want it to contain %q", logs(), want)
	}
}
