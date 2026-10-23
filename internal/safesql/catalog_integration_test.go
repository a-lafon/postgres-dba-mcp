//go:build integration

package safesql

import (
	"context"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/testpg"
)

// TestCatalogVolatileClassified fails when a volatile function of pg_catalog is neither denied nor reviewed, for
// instance one added by a new PostgreSQL major. A denylist holds only if every built-in function with side effects is
// known; stable and immutable functions cannot write, so only volatile ones need a decision.
func TestCatalogVolatileClassified(t *testing.T) {
	reviewed := loadReviewed(t)
	p := defaultPolicy(t)
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, testpg.RO(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	// A function with an internal argument cannot be called from SQL.
	rows, err := conn.Query(ctx, `SELECT DISTINCT proname::text FROM pg_proc
		WHERE pronamespace = 'pg_catalog'::regnamespace AND prokind <> 'p' AND provolatile = 'v'
		  AND NOT 'internal'::regtype = ANY (proargtypes::oid[])
		ORDER BY 1`)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	var missing []string
	for _, n := range names {
		if !p.refuses(call{name: strings.ToLower(n)}) && reviewed[n] == "" {
			missing = append(missing, n)
		}
	}
	if len(missing) > 0 {
		t.Errorf("volatile pg_catalog functions neither denied nor reviewed: %s\n"+
			"add each one to builtinDeny, or to testdata/safesql/catalog_volatile.txt with the reason it is safe",
			strings.Join(missing, " "))
	}
}
