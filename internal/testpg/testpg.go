// Package testpg gives integration tests the DSNs of the Docker fixture and throwaway databases.
// Its helpers fail the test, never skip it, when the fixture is not running.
package testpg

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	envRO      = "PGDBA_TEST_RO"
	envRW      = "PGDBA_TEST_RW"
	envAdmin   = "PGDBA_TEST_ADMIN"
	envBouncer = "PGDBA_TEST_BOUNCER"

	defaultRO      = "postgres://pgdba_ro:pgdba@localhost:55432/pgdba?sslmode=disable" //nolint:gosec // password of the local Docker fixture
	defaultRW      = "postgres://pgdba_rw:pgdba@localhost:55432/pgdba?sslmode=disable" //nolint:gosec // password of the local Docker fixture
	defaultAdmin   = "postgres://postgres:pgdba@localhost:55432/pgdba?sslmode=disable" //nolint:gosec // password of the local Docker fixture
	defaultBouncer = "postgres://pgdba_ro:pgdba@localhost:56432/pgdba?sslmode=disable" //nolint:gosec // password of the local Docker fixture
)

// RO returns the DSN of role pgdba_ro.
func RO(t testing.TB) string {
	t.Helper()
	return dsn(t, envRO, defaultRO)
}

// RW returns the DSN of role pgdba_rw.
func RW(t testing.TB) string {
	t.Helper()
	return dsn(t, envRW, defaultRW)
}

// Admin returns the DSN of the superuser postgres.
func Admin(t testing.TB) string {
	t.Helper()
	return dsn(t, envAdmin, defaultAdmin)
}

// Bouncer returns the DSN of role pgdba_ro through PgBouncer in transaction mode.
func Bouncer(t testing.TB) string {
	t.Helper()
	return dsn(t, envBouncer, defaultBouncer)
}

// dsn returns the DSN from the environment or the fixture default, after checking that it answers.
func dsn(t testing.TB, env, def string) string {
	t.Helper()
	d := os.Getenv(env)
	if d == "" {
		d = def
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, d)
	if err != nil {
		t.Fatalf("test database unreachable (%s): %v; start the fixture with `docker compose up -d --wait`", env, err)
		return d
	}
	_ = conn.Close(ctx)
	return d
}

// DSN returns base with its database replaced by dbname.
func DSN(base, dbname string) string {
	u, err := url.Parse(base)
	if err != nil {
		panic(fmt.Sprintf("testpg: parse DSN: %v", err))
	}
	u.Path = "/" + dbname
	return u.String()
}

// TempDB creates an empty database with a unique name as postgres, lets pgdba_ro and pgdba_rw connect to it,
// drops it when the test ends, and returns its name.
func TempDB(t testing.TB) string {
	t.Helper()
	admin := Admin(t)
	var suffix [6]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatalf("random database name: %v", err)
		return ""
	}
	name := "pgdba_tmp_" + hex.EncodeToString(suffix[:])

	ctx := context.Background()
	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect as postgres: %v", err)
		return ""
	}
	defer func() { _ = conn.Close(ctx) }()
	q := pgx.Identifier{name}.Sanitize()
	for _, stmt := range []string{
		"CREATE DATABASE " + q,
		"GRANT CONNECT ON DATABASE " + q + " TO pgdba_ro, pgdba_rw",
	} {
		if _, err := conn.Exec(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
			return ""
		}
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), admin)
		if err != nil {
			t.Errorf("drop %s: connect: %v", name, err)
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		if _, err := c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+q+" WITH (FORCE)"); err != nil {
			t.Errorf("drop %s: %v", name, err)
		}
	})
	return name
}
