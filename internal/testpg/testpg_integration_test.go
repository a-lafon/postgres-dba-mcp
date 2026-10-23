//go:build integration

package testpg

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTempDB(t *testing.T) {
	var name string
	t.Run("use", func(t *testing.T) {
		name = TempDB(t)
		ctx := context.Background()
		conn, err := pgx.Connect(ctx, DSN(RO(t), name))
		if err != nil {
			t.Fatalf("connect to the temporary database as pgdba_ro: %v", err)
		}
		defer func() { _ = conn.Close(ctx) }()
		var exts []string
		rows, err := conn.Query(ctx, "SELECT extname::text FROM pg_extension")
		if err != nil {
			t.Fatal(err)
		}
		exts, err = pgx.CollectRows(rows, pgx.RowTo[string])
		if err != nil {
			t.Fatal(err)
		}
		if len(exts) != 1 || exts[0] != "plpgsql" {
			t.Errorf("extensions = %v, want [plpgsql]", exts)
		}
		rw, err := pgx.Connect(ctx, DSN(RW(t), name))
		if err != nil {
			t.Fatalf("connect as pgdba_rw: %v", err)
		}
		_ = rw.Close(ctx)
	})

	ctx := context.Background()
	admin, err := pgx.Connect(ctx, Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var exists bool
	if err := admin.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM pg_database WHERE datname = $1)", name).Scan(&exists); err != nil {
		t.Fatal(err)
	}
	if exists {
		t.Errorf("database %s still exists after the test", name)
	}
}

func TestDSN(t *testing.T) {
	got := DSN("postgres://u:p@h:5432/pgdba?sslmode=disable", "other")
	if got != "postgres://u:p@h:5432/other?sslmode=disable" {
		t.Errorf("DSN() = %q", got)
	}
}

// recorder is a testing.TB whose Fatal methods record the message instead of failing the test.
type recorder struct {
	testing.TB
	msg string
}

func (r *recorder) Fatal(args ...any)                 { r.msg = "fatal" }
func (r *recorder) Fatalf(format string, args ...any) { r.msg = format }
func (r *recorder) FailNow()                          {}

func TestFixtureDown(t *testing.T) {
	t.Setenv(envRO, "postgres://pgdba_ro:pgdba@localhost:1/pgdba?sslmode=disable")
	rec := &recorder{TB: t}
	RO(rec)
	if !strings.Contains(rec.msg, "docker compose up -d --wait") {
		t.Errorf("failure message = %q, want the docker compose hint", rec.msg)
	}
}
