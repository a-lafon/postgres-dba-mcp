package tasks

import (
	"context"
	"fmt"
	"slices"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/security"
)

// State is what the "database changed" check compares: part C's snapshot (relations, sequences, role settings,
// advisory locks, large objects, the content of events, orders and accounts), the content of every other table, and
// the definitions a DDL statement changes without touching a row (columns, constraints and their validation, index
// validity, comments).
type State = security.Snapshot

// TakeState reads the state through a superuser connection.
func TakeState(ctx context.Context, admin *pgx.Conn) (State, error) {
	snap, err := security.Take(ctx, admin, nil)
	if err != nil {
		return snap, err
	}
	tables, err := collect(ctx, admin, pgx.RowTo[string], `SELECT format('%I.%I', n.nspname, c.relname) FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind = 'r' AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'
		  AND NOT EXISTS (SELECT 1 FROM pg_depend d WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid AND d.deptype = 'e')
		ORDER BY 1`)
	if err != nil {
		return snap, fmt.Errorf("list tables: %w", err)
	}
	for _, t := range tables {
		if slices.Contains(security.CheckedTables, t) {
			continue
		}
		var n, sum int64
		if err := admin.QueryRow(ctx, fmt.Sprintf("SELECT count(*), coalesce(sum(hashtext(t::text)), 0) FROM %s t", t)).Scan(&n, &sum); err != nil {
			return snap, fmt.Errorf("checksum %s: %w", t, err)
		}
		snap.Tables[t] = fmt.Sprintf("%d/%d", n, sum)
	}
	var ddl string
	if err := admin.QueryRow(ctx, `SELECT md5(concat_ws('|',
			(SELECT string_agg(format('%s.%s:%s:%s:%s', attrelid::regclass, attname, atttypid::regtype, attnotnull, atthasdef), ',' ORDER BY 1)
			   FROM pg_attribute a JOIN pg_class c ON c.oid = a.attrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			  WHERE a.attnum > 0 AND NOT a.attisdropped AND n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'),
			(SELECT string_agg(format('%s:%s:%s:%s', conrelid::regclass, conname, contype, convalidated), ',' ORDER BY 1)
			   FROM pg_constraint c JOIN pg_namespace n ON n.oid = c.connamespace
			  WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'),
			(SELECT string_agg(format('%s:%s:%s', indexrelid::regclass, indisvalid, indisready), ',' ORDER BY 1) FROM pg_index i
			   JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
			  WHERE n.nspname NOT IN ('pg_catalog', 'information_schema') AND n.nspname NOT LIKE 'pg\_%'),
			(SELECT string_agg(format('%s:%s:%s', classoid, objoid, md5(description)), ',' ORDER BY 1) FROM pg_description WHERE objoid >= 16384)))`).Scan(&ddl); err != nil {
		return snap, fmt.Errorf("checksum definitions: %w", err)
	}
	snap.Tables["(definitions)"] = ddl
	return snap, nil
}

// Changes lists what differs between two states; empty when the database is as it was.
func Changes(before, after State) []string { return security.Diff(before, after) }
