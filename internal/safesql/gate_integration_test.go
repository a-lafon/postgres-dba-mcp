//go:build integration

package safesql

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// TestGate runs the volatility gate against a real catalog: a function the
// name rules leave undecided is refused only when the server does not ship it,
// it is declared VOLATILE, and no review cleared it.
func TestGate(t *testing.T) {
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, testpg.Admin(t))
	if err != nil {
		t.Fatal(err)
	}
	// Registered first, so it runs after the cleanup that needs the connection.
	t.Cleanup(func() { _ = admin.Close(ctx) })

	// gate_path is put on the search path of each test transaction, so its
	// random() shadows a built-in name, while gate_off_path stays out of
	// reach. Nothing is created in public: a fixture object of the same name
	// must never be dropped by this test.
	setup := []string{
		`CREATE SCHEMA IF NOT EXISTS gate_path`,
		`CREATE SCHEMA IF NOT EXISTS gate_off_path`,
		`GRANT USAGE ON SCHEMA gate_path TO PUBLIC`,
		`CREATE FUNCTION gate_path.gate_volatile() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE FUNCTION gate_path.gate_stable() RETURNS int LANGUAGE sql STABLE AS 'SELECT 1'`,
		`CREATE FUNCTION gate_path.random() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
		`CREATE FUNCTION gate_path.random(v int) RETURNS int LANGUAGE sql AS 'SELECT v'`,
		`CREATE FUNCTION gate_off_path.gate_hidden() RETURNS int LANGUAGE sql AS 'SELECT 1'`,
	}
	drop := func() {
		for _, s := range []string{
			`DROP SCHEMA IF EXISTS gate_path CASCADE`,
			`DROP SCHEMA IF EXISTS gate_off_path CASCADE`,
		} {
			if _, err := admin.Exec(ctx, s); err != nil {
				t.Fatalf("%s: %v", s, err)
			}
		}
	}
	drop()
	t.Cleanup(drop)
	for _, s := range setup {
		if _, err := admin.Exec(ctx, s); err != nil {
			t.Fatalf("%s: %v", s, err)
		}
	}

	tests := []struct {
		name, sql string
		allow     []string
		wantErr   string // substring; empty means the gate lets it through
	}{
		{name: "built-in volatile", sql: "SELECT now()"},
		{name: "a shadow of a built-in name is refused", sql: "SELECT random()", wantErr: "random() is VOLATILE"},
		// The built-in takes no int, so the server calls public.random(int):
		// a candidate in pg_catalog must not stand for the whole name.
		{name: "a shadow the built-in does not cover", sql: "SELECT random(1)", wantErr: "random() is VOLATILE"},
		{name: "reviewed extension function", sql: "SELECT hypopg_reset()"},
		{name: "user stable", sql: "SELECT gate_stable()"},
		{name: "unknown name resolves to nothing", sql: "SELECT gate_does_not_exist()"},
		{name: "user volatile", sql: "SELECT gate_volatile()", wantErr: "gate_volatile() is VOLATILE and was not reviewed"},
		{name: "user volatile, qualified", sql: "SELECT gate_path.gate_volatile()", wantErr: "gate_path.gate_volatile() is VOLATILE"},
		{name: "volatile off the search path, qualified", sql: "SELECT gate_off_path.gate_hidden()", wantErr: "gate_off_path.gate_hidden() is VOLATILE"},
		{name: "a shadow reached by its schema", sql: "SELECT gate_path.random()", wantErr: "gate_path.random() is VOLATILE"},
		{name: "user allow wins over the gate", sql: "SELECT gate_volatile()", allow: []string{"gate_volatile"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPolicy(tt.allow, nil)
			if err != nil {
				t.Fatal(err)
			}
			st, err := Check(tt.sql, p)
			if err != nil {
				t.Fatalf("Check: %v", err)
			}
			if !st.Executes() {
				t.Fatalf("Executes() is false for %q", tt.sql)
			}
			conn, err := pgx.Connect(ctx, testpg.RO(t))
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = conn.Close(ctx) }()
			tx, err := conn.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			// The gate resolves names in this transaction, so the shadows of
			// gate_path only count once it is on its search path.
			if _, err := tx.Exec(ctx, "SET LOCAL search_path TO gate_path, public"); err != nil {
				t.Fatal(err)
			}

			err = Gate(ctx, tx, st.Unresolved)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("refused: %v", err)
			case tt.wantErr != "" && err == nil:
				t.Fatalf("let through, want an error naming %q", tt.wantErr)
			case tt.wantErr != "" && !strings.Contains(err.Error(), tt.wantErr):
				t.Fatalf("error %q, want one containing %q", err, tt.wantErr)
			}
		})
	}
}
