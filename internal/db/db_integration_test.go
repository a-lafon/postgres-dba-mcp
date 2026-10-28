//go:build integration

package db

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// captureLog replaces the log writer for the test and returns what was written.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	logMu.Lock()
	old := logWriter
	logWriter = writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	})
	logMu.Unlock()
	t.Cleanup(func() {
		logMu.Lock()
		logWriter = old
		logMu.Unlock()
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// open builds a DB for dsn, closed at cleanup. The role audit is not awaited.
func open(t *testing.T, dsn string, flags ...string) *DB {
	t.Helper()
	d, err := New(testCfg(t, dsn, flags...))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(d.Close)
	return d
}

func admin(t *testing.T) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	c, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close(ctx) })
	return c
}

func sqlstate(t *testing.T, err error) string {
	t.Helper()
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		t.Fatalf("error %v holds no Postgres error", err)
	}
	return pe.Code
}

func scalar[T any](t *testing.T, d *DB, sql string, args ...any) T {
	t.Helper()
	var v T
	err := d.ReadOnly(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		return tx.QueryRow(ctx, sql, args...).Scan(&v)
	})
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func relationExists(t *testing.T, a *pgx.Conn, name string) bool {
	t.Helper()
	var ok bool
	if err := a.QueryRow(context.Background(), "SELECT to_regclass($1) IS NOT NULL", name).Scan(&ok); err != nil {
		t.Fatal(err)
	}
	return ok
}

// dropAtCleanup drops the given tables as postgres when the test ends, so a local rerun does not fail with 42P07.
func dropAtCleanup(t *testing.T, tables ...string) {
	t.Helper()
	a := admin(t)
	t.Cleanup(func() {
		for _, tbl := range tables {
			_, _ = a.Exec(context.Background(), "DROP TABLE IF EXISTS "+tbl)
		}
	})
}

func TestSingleStatement(t *testing.T) {
	ctx := context.Background()
	t.Run("multi-statement query rejected", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			rows, err := tx.Query(ctx, "SELECT 1; SELECT 2")
			if err == nil {
				rows.Close()
				err = rows.Err()
			}
			return err
		})
		if got := sqlstate(t, err); got != "42601" {
			t.Errorf("SQLSTATE = %s, want 42601", got)
		}
	})

	t.Run("Exec without arguments stays single-statement", func(t *testing.T) {
		dropAtCleanup(t, "app_rw.m1", "app_rw.m2")
		d := open(t, testpg.RW(t), "--access-mode=unrestricted")
		err := d.ReadWrite(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE app_rw.m1(x int); CREATE TABLE app_rw.m2(x int)")
			return err
		})
		if got := sqlstate(t, err); got != "42601" {
			t.Errorf("SQLSTATE = %s, want 42601", got)
		}
		a := admin(t)
		if relationExists(t, a, "app_rw.m1") || relationExists(t, a, "app_rw.m2") {
			t.Error("a table exists after the rejected call")
		}
	})

	t.Run("Exec inside a savepoint stays single-statement", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			defer func() { _ = sp.Rollback(ctx) }()
			_, err = sp.Exec(ctx, "SELECT 1; SELECT 2")
			return err
		})
		if got := sqlstate(t, err); got != "42601" {
			t.Errorf("SQLSTATE = %s, want 42601", got)
		}
	})

	t.Run("Exec returns the command tag", func(t *testing.T) {
		dropAtCleanup(t, "app_rw.m3")
		d := open(t, testpg.RW(t), "--access-mode=unrestricted")
		var tag string
		err := d.ReadWrite(ctx, func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, "CREATE TABLE app_rw.m3(x int)")
			tag = ct.String()
			return err
		})
		if err != nil || tag != "CREATE TABLE" {
			t.Errorf("tag = %q, err = %v", tag, err)
		}
	})
}

func TestSessionSettings(t *testing.T) {
	ctx := context.Background()
	t.Run("statement timeout applies", func(t *testing.T) {
		d := open(t, testpg.RO(t), "--statement-timeout=1s")
		start := time.Now()
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
			return err
		})
		if got := sqlstate(t, err); got != "57014" {
			t.Errorf("SQLSTATE = %s, want 57014", got)
		}
		if el := time.Since(start); el > 2*time.Second {
			t.Errorf("took %v, want under 2s", el)
		}
	})

	t.Run("other settings", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		for sql, want := range map[string]string{
			"SELECT timestamptz '2026-03-02 11:14:00+01'::text":             "2026-03-02 10:14:00+00",
			"SELECT interval '1 day 02:00'::text":                           "1 day 02:00:00",
			"SELECT current_setting('lock_timeout')":                        "10s",
			"SELECT current_setting('idle_in_transaction_session_timeout')": "1min",
			"SELECT current_setting('bytea_output')":                        "hex",
			"SELECT current_setting('statement_timeout')":                   "30s",
			"SELECT current_setting('application_name')":                    "postgres-dba-mcp",
		} {
			if got := scalar[string](t, d, sql); got != want {
				t.Errorf("%s = %q, want %q", sql, got, want)
			}
		}
	})

	t.Run("settings are transaction-local", func(t *testing.T) {
		d := open(t, testpg.RO(t), "--statement-timeout=1s")
		scalar[int](t, d, "SELECT 1")
		// Wait for the release cleanup, then read the setting outside any runner.
		time.Sleep(300 * time.Millisecond)
		c, err := d.pool.Acquire(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Release()
		var got string
		if err := c.QueryRow(ctx, "SELECT current_setting('statement_timeout')").Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got == "1s" {
			t.Errorf("statement_timeout = %q outside a runner, want the server default", got)
		}
	})

	t.Run("behind PgBouncer in transaction mode", func(t *testing.T) {
		d := open(t, testpg.Bouncer(t), "--statement-timeout=1s")
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "SELECT pg_sleep(5)")
			return err
		})
		if got := sqlstate(t, err); got != "57014" {
			t.Errorf("first call SQLSTATE = %s, want 57014 (err %v)", got, err)
		}
		err = d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE t(x int)")
			return err
		})
		if got := sqlstate(t, err); got != "25006" {
			t.Errorf("second call SQLSTATE = %s, want 25006 (err %v)", got, err)
		}
	})
}

func TestRunners(t *testing.T) {
	ctx := context.Background()

	t.Run("write refused in the read-only runner", func(t *testing.T) {
		dropAtCleanup(t, "app_rw.ro_t", "public.ro_t")
		d := open(t, testpg.RW(t))
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE app_rw.ro_t(x int)")
			return err
		})
		if got := sqlstate(t, err); got != "25006" {
			t.Errorf("SQLSTATE = %s, want 25006", got)
		}
		if relationExists(t, admin(t), "app_rw.ro_t") {
			t.Error("table exists after the refused write")
		}
	})

	t.Run("read-only runner rolls back, read-write runner commits", func(t *testing.T) {
		// A notification is delivered only when its transaction commits.
		a := admin(t)
		if _, err := a.Exec(ctx, "LISTEN pgdba_runner_test"); err != nil {
			t.Fatal(err)
		}
		notified := func() bool {
			wctx, cancel := context.WithTimeout(ctx, 700*time.Millisecond)
			defer cancel()
			_, err := a.WaitForNotification(wctx)
			return err == nil
		}
		notify := func(run func(context.Context, func(context.Context, pgx.Tx) error) error) {
			err := run(ctx, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT pg_notify('pgdba_runner_test', 'x')")
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
		}
		ro := open(t, testpg.RW(t))
		notify(ro.ReadOnly)
		if notified() {
			t.Error("ReadOnly committed its transaction")
		}
		rw := open(t, testpg.RW(t), "--access-mode=unrestricted")
		notify(rw.ReadWrite)
		if !notified() {
			t.Error("ReadWrite did not commit its transaction")
		}
	})

	t.Run("savepoints inside the read-only runner", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			sp, err := tx.Begin(ctx)
			if err != nil {
				return err
			}
			if _, err := sp.Exec(ctx, "SELECT * FROM no_such_table"); err == nil {
				return errors.New("expected an error from the missing table")
			}
			if err := sp.Rollback(ctx); err != nil {
				return err
			}
			var n int
			return tx.QueryRow(ctx, "SELECT 1").Scan(&n)
		})
		if err != nil {
			t.Errorf("second statement after the savepoint rollback: %v", err)
		}
	})

	t.Run("write committed", func(t *testing.T) {
		dropAtCleanup(t, "app_rw.w_t")
		d := open(t, testpg.RW(t), "--access-mode=unrestricted")
		err := d.ReadWrite(ctx, func(ctx context.Context, tx pgx.Tx) error {
			_, err := tx.Exec(ctx, "CREATE TABLE app_rw.w_t(x int)")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		if !relationExists(t, admin(t), "app_rw.w_t") {
			t.Error("table missing after ReadWrite")
		}
	})

	t.Run("error rolled back", func(t *testing.T) {
		dropAtCleanup(t, "app_rw.e_t")
		a := admin(t)
		if _, err := a.Exec(ctx, "CREATE TABLE app_rw.e_t(x int); GRANT ALL ON app_rw.e_t TO pgdba_rw"); err != nil {
			t.Fatal(err)
		}
		d := open(t, testpg.RW(t), "--access-mode=unrestricted")
		boom := errors.New("boom")
		err := d.ReadWrite(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, "INSERT INTO app_rw.e_t VALUES (1)"); err != nil {
				return err
			}
			return boom
		})
		if !errors.Is(err, boom) {
			t.Fatalf("error = %v, want boom", err)
		}
		var n int
		if err := a.QueryRow(ctx, "SELECT count(*) FROM app_rw.e_t").Scan(&n); err != nil || n != 0 {
			t.Errorf("rows = %d, err = %v, want 0", n, err)
		}
	})
}

// samePid runs check on the backend of the first call. The pool hands out the most recently released
// connection, so as many concurrent calls as the pool allows are used to reach every connection.
func samePid(t *testing.T, d *DB, first int32, check func(ctx context.Context, tx pgx.Tx)) {
	t.Helper()
	const conc = maxConns
	for round := 0; round < 5; round++ {
		var started sync.WaitGroup
		started.Add(conc)
		var done sync.WaitGroup
		var ran atomic.Bool
		for i := 0; i < conc; i++ {
			done.Add(1)
			go func() {
				defer done.Done()
				err := d.ReadOnly(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
					var pid int32
					if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
						return err
					}
					started.Done()
					started.Wait() // hold the connection until every call has one
					if pid == first {
						ran.Store(true)
						check(ctx, tx)
					}
					return nil
				})
				if err != nil {
					t.Error(err)
				}
			}()
		}
		done.Wait()
		if ran.Load() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("never got the first backend back from the pool")
}

func TestConnectionCleanup(t *testing.T) {
	ctx := context.Background()

	t.Run("advisory lock does not leak", func(t *testing.T) {
		d := open(t, testpg.RO(t), "--access-mode=unrestricted")
		var pid int32
		err := d.ReadWrite(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, "SELECT pg_advisory_lock(42)")
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		samePid(t, d, pid, func(ctx context.Context, tx pgx.Tx) {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND pid = pg_backend_pid()").Scan(&n); err != nil || n != 0 {
				t.Errorf("advisory locks on the reused backend = %d, err = %v, want 0", n, err)
			}
		})
		a := admin(t)
		var n int
		if err := a.QueryRow(ctx, "SELECT count(*) FROM pg_locks WHERE locktype = 'advisory'").Scan(&n); err != nil || n != 0 {
			t.Errorf("advisory locks in the cluster = %d, err = %v, want 0", n, err)
		}
	})

	t.Run("hypothetical index does not leak", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		var pid int32
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			var n int
			return tx.QueryRow(ctx, "SELECT count(*) FROM hypopg_create_index('CREATE INDEX ON public.events (account_id)')").Scan(&n)
		})
		if err != nil {
			t.Fatal(err)
		}
		samePid(t, d, pid, func(ctx context.Context, tx pgx.Tx) {
			var n int
			if err := tx.QueryRow(ctx, "SELECT count(*) FROM hypopg_list_indexes").Scan(&n); err != nil || n != 0 {
				t.Errorf("hypothetical indexes on the reused backend = %d, err = %v, want 0", n, err)
			}
		})
	})
}

func TestPoolBound(t *testing.T) {
	ctx := context.Background()
	// A temporary database keeps PgBouncer's server connections, which carry the same application_name, out of the count.
	dbname := testpg.TempDB(t)
	d := open(t, testpg.DSN(testpg.RO(t), dbname))
	a := admin(t)

	stop := make(chan struct{})
	var max int
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		sampler, err := pgx.Connect(ctx, testpg.Admin(t))
		if err != nil {
			return
		}
		defer func() { _ = sampler.Close(ctx) }()
		for {
			select {
			case <-stop:
				return
			default:
			}
			var n int
			if err := sampler.QueryRow(ctx, "SELECT count(*) FROM pg_stat_activity WHERE application_name = 'postgres-dba-mcp' AND datname = $1", dbname).Scan(&n); err == nil && n > max {
				max = n
			}
		}
	}()
	_ = a

	var failures atomic.Int32
	var calls sync.WaitGroup
	for i := 0; i < 20; i++ {
		calls.Add(1)
		go func() {
			defer calls.Done()
			err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, "SELECT pg_sleep(0.2)")
				return err
			})
			if err != nil {
				failures.Add(1)
				t.Errorf("call failed: %v", err)
			}
		}()
	}
	calls.Wait()
	close(stop)
	wg.Wait()
	if failures.Load() != 0 {
		t.Fatalf("%d calls failed", failures.Load())
	}
	if max < 2 || max > 5 {
		t.Errorf("max sessions = %d, want between 2 and 5", max)
	}
}

func TestRetry(t *testing.T) {
	ctx := context.Background()

	t.Run("backend terminated between two calls", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		a := admin(t)
		first := scalar[int32](t, d, "SELECT pg_backend_pid()")
		// Wait until the release cleanup is done and the backend idles in the pool.
		deadline := time.Now().Add(3 * time.Second)
		for {
			var ready bool
			err := a.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_stat_activity WHERE pid = $1 AND state = 'idle' AND query = 'DISCARD ALL')", first).Scan(&ready)
			if err != nil {
				t.Fatal(err)
			}
			if ready {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("backend never went idle after DISCARD ALL")
			}
			time.Sleep(10 * time.Millisecond)
		}
		if _, err := a.Exec(ctx, "SELECT pg_terminate_backend($1)", first); err != nil {
			t.Fatal(err)
		}
		second, err := func() (int32, error) {
			var pid int32
			err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
				return tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid)
			})
			return pid, err
		}()
		if err != nil {
			t.Fatalf("second call: %v", err)
		}
		if second == first {
			t.Errorf("second call ran on the terminated backend %d", first)
		}
	})

	t.Run("no retry once the function started", func(t *testing.T) {
		d := open(t, testpg.RO(t))
		a := admin(t)
		var runs int
		err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
			runs++
			var pid int32
			if err := tx.QueryRow(ctx, "SELECT pg_backend_pid()").Scan(&pid); err != nil {
				return err
			}
			if _, err := a.Exec(ctx, "SELECT pg_terminate_backend($1)", pid); err != nil {
				return err
			}
			time.Sleep(100 * time.Millisecond)
			_, err := tx.Exec(ctx, "SELECT 1")
			return err
		})
		if err == nil || !isConnError(err) {
			t.Errorf("error = %v, want a connection error", err)
		}
		if runs != 1 {
			t.Errorf("function ran %d times, want 1", runs)
		}
	})
}

func TestLazyConnection(t *testing.T) {
	// A temporary database keeps PgBouncer's server connections, which carry the same application_name, out of the count.
	dbname := testpg.TempDB(t)
	d := open(t, testpg.DSN(testpg.RO(t), dbname))
	a := admin(t)
	count := func() int {
		var n int
		if err := a.QueryRow(context.Background(), "SELECT count(*) FROM pg_stat_activity WHERE application_name = 'postgres-dba-mcp' AND datname = $1", dbname).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := count(); n != 0 {
		t.Fatalf("sessions before the first call = %d, want 0", n)
	}
	scalar[int](t, d, "SELECT 1")
	if n := count(); n == 0 {
		t.Error("no session after the first call")
	}
}

func TestInfo(t *testing.T) {
	ctx := context.Background()
	d := open(t, testpg.RO(t))
	info, err := d.Info(ctx)
	if err != nil {
		t.Fatal(err)
	}
	want := scalar[string](t, d, "SHOW server_version_num")
	if fmt.Sprint(info.VersionNum) != want {
		t.Errorf("VersionNum = %d, want %s", info.VersionNum, want)
	}
	for _, ext := range []string{"hypopg", "pg_stat_statements"} {
		if info.Extensions[ext] == "" {
			t.Errorf("extension %s missing from %v", ext, info.Extensions)
		}
	}

	t.Run("failure not cached", func(t *testing.T) {
		bad := open(t, "postgres://pgdba_ro:pgdba@127.0.0.1:1/pgdba?sslmode=disable")
		if _, err := bad.Info(ctx); err == nil {
			t.Fatal("Info succeeded against a closed port")
		}
		if bad.info != nil {
			t.Error("a failed fetch was cached")
		}
	})
}

func waitAudit(t *testing.T, d *DB) {
	t.Helper()
	select {
	case <-d.auditDone:
	case <-time.After(5 * time.Second):
		t.Fatal("role audit did not complete")
	}
}

func TestRoleAudit(t *testing.T) {
	t.Run("read-only role passes silently", func(t *testing.T) {
		logs := captureLog(t)
		d := open(t, testpg.RO(t))
		scalar[int](t, d, "SELECT 1")
		waitAudit(t, d)
		if strings.Contains(logs(), "WARNING: role") {
			t.Errorf("log = %q", logs())
		}
	})

	t.Run("superuser warned", func(t *testing.T) {
		logs := captureLog(t)
		d := open(t, testpg.Admin(t))
		scalar[int](t, d, "SELECT 1")
		waitAudit(t, d)
		if want := "WARNING: role postgres: is a superuser; use a dedicated read-only role\n"; !strings.Contains(logs(), want) {
			t.Errorf("log = %q, want it to contain %q", logs(), want)
		}
	})

	t.Run("skipped in unrestricted mode", func(t *testing.T) {
		logs := captureLog(t)
		d := open(t, testpg.Admin(t), "--access-mode=unrestricted")
		scalar[int](t, d, "SELECT 1")
		select {
		case <-d.auditDone:
			t.Error("audit ran in unrestricted mode")
		case <-time.After(300 * time.Millisecond):
		}
		if logs() != "" {
			t.Errorf("log = %q", logs())
		}
	})

	t.Run("runs once", func(t *testing.T) {
		logs := captureLog(t)
		d := open(t, testpg.Admin(t))
		for i := 0; i < 3; i++ {
			scalar[int](t, d, "SELECT 1")
		}
		waitAudit(t, d)
		time.Sleep(200 * time.Millisecond)
		if n := strings.Count(logs(), "is a superuser"); n != 1 {
			t.Errorf("superuser warning logged %d times, want 1", n)
		}
	})
}

func TestConnectionErrors(t *testing.T) {
	ctx := context.Background()
	run := func(d *DB) error {
		return d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error { return nil })
	}

	t.Run("wrong password", func(t *testing.T) {
		d := open(t, DSNWithPassword(t, "wrongpw-xyz"))
		err := run(d)
		var ce *ConnectError
		if !errors.As(err, &ce) || !strings.HasPrefix(err.Error(), "cannot connect to the database:") {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(err.Error(), "wrongpw-xyz") {
			t.Errorf("error leaks the password: %v", err)
		}
		if got := sqlstate(t, err); got != "28P01" {
			t.Errorf("SQLSTATE = %s, want 28P01", got)
		}
	})

	t.Run("database down", func(t *testing.T) {
		d := open(t, "postgres://pgdba_ro:pgdba@127.0.0.1:1/pgdba?sslmode=disable")
		var ce *ConnectError
		if err := run(d); !errors.As(err, &ce) {
			t.Errorf("error = %v, want a ConnectError", err)
		}
	})

	t.Run("unreachable host", func(t *testing.T) {
		d := open(t, "postgres://pgdba_ro:pgdba@10.255.255.1:5432/pgdba?sslmode=disable")
		start := time.Now()
		err := run(d)
		var ce *ConnectError
		if !errors.As(err, &ce) {
			t.Errorf("error = %v, want a ConnectError", err)
		}
		if el := time.Since(start); el > 12*time.Second {
			t.Errorf("took %v, want under 12s", el)
		}
	})
}

// DSNWithPassword returns the pgdba_ro DSN of the fixture with another password.
func DSNWithPassword(t *testing.T, password string) string {
	t.Helper()
	cfg := testCfg(t, testpg.RO(t))
	pc, err := pgconn.ParseConfig(cfg.DSN)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("postgres://pgdba_ro:%s@%s:%d/pgdba?sslmode=disable", password, pc.Host, pc.Port)
}

// TestSearchPathFromTheURI checks that a search_path the user put in the connection URI reaches the
// statement, directly and behind PgBouncer. It travels per transaction, not in the startup packet,
// which PgBouncer before 1.21 refuses.
func TestSearchPathFromTheURI(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close(ctx) })
	drop := func() {
		if _, err := admin.Exec(ctx, "DROP SCHEMA IF EXISTS sp_uri CASCADE"); err != nil {
			t.Fatal(err)
		}
	}
	drop()
	t.Cleanup(drop)
	if _, err := admin.Exec(ctx, `CREATE SCHEMA sp_uri;
		GRANT USAGE ON SCHEMA sp_uri TO PUBLIC;
		CREATE TABLE sp_uri.only_here (x int);
		INSERT INTO sp_uri.only_here VALUES (1), (2);
		GRANT SELECT ON sp_uri.only_here TO PUBLIC`); err != nil {
		t.Fatal(err)
	}
	withParam := func(dsn, kv string) string {
		if strings.Contains(dsn, "?") {
			return dsn + "&" + kv
		}
		return dsn + "?" + kv
	}

	for _, tt := range []struct{ name, dsn string }{
		{"direct", testpg.RO(t)},
		{"through pgbouncer", testpg.Bouncer(t)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d := open(t, withParam(tt.dsn, "search_path=sp_uri"))
			var path string
			var rows int
			if err := d.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
				if err := tx.QueryRow(ctx, "SELECT current_setting('search_path')").Scan(&path); err != nil {
					return err
				}
				// The point of the setting: an unqualified name resolves in that schema.
				return tx.QueryRow(ctx, "SELECT count(*) FROM only_here").Scan(&rows)
			}); err != nil {
				t.Fatal(err)
			}
			if path != "sp_uri" {
				t.Errorf("search_path = %q, want %q", path, "sp_uri")
			}
			if rows != 2 {
				t.Errorf("rows = %d, want 2", rows)
			}
		})
	}
}
