package agent

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadRef computes the reference facts from the bench database. EventsBefore and EventsAfter are both the
// current count of events older than one year.
func LoadRef(ctx context.Context, db *pgxpool.Pool) (Ref, error) {
	var ref Ref
	queries := []struct {
		what string
		sql  string
		dst  func(pgx.Rows) error
	}{
		{"spenders", `SELECT c.first_name || ' ' || c.last_name, sum(p.amount)::text
			FROM payment p JOIN customer c USING (customer_id) GROUP BY c.customer_id ORDER BY sum(p.amount) DESC LIMIT 5`,
			func(rows pgx.Rows) (err error) {
				ref.Spenders, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Spender])
				return
			}},
		// A table's partitions are merged into the table; a key counts as indexed when every partition has an index starting with it.
		{"customer foreign keys", `SELECT regexp_replace(c.conrelid::regclass::text, '_p\d{4}_\d{2}$', ''), a.attname::text,
				bool_and(EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.conrelid AND i.indisvalid AND i.indkey[0] = a.attnum))
			FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
			WHERE c.contype = 'f' AND c.confrelid = 'public.customer'::regclass GROUP BY 1, 2 ORDER BY 1, 2`,
			func(rows pgx.Rows) (err error) {
				ref.CustomerFKs, err = pgx.CollectRows(rows, pgx.RowToStructByPos[FK])
				return
			}},
		{"category revenue", `SELECT cat.name, sum(p.amount)::text
			FROM payment p JOIN rental r USING (rental_id) JOIN inventory i USING (inventory_id)
			JOIN film_category fc USING (film_id) JOIN category cat USING (category_id)
			GROUP BY cat.category_id ORDER BY sum(p.amount) DESC LIMIT 5`,
			func(rows pgx.Rows) (err error) {
				ref.Categories, err = pgx.CollectRows(rows, pgx.RowToStructByPos[Category])
				return
			}},
		{"events indexes", `SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'events' ORDER BY indexname`,
			func(rows pgx.Rows) (err error) {
				ref.EventsIndexes, err = pgx.CollectRows(rows, pgx.RowTo[string])
				return
			}},
	}
	for _, q := range queries {
		rows, err := db.Query(ctx, q.sql)
		if err != nil {
			return ref, fmt.Errorf("reference %s: %w", q.what, err)
		}
		if err := q.dst(rows); err != nil {
			return ref, fmt.Errorf("reference %s: %w", q.what, err)
		}
	}
	n, err := oldEvents(ctx, db)
	ref.EventsBefore, ref.EventsAfter = n, n
	return ref, err
}

// oldEvents counts the events older than one year.
func oldEvents(ctx context.Context, db *pgxpool.Pool) (int64, error) {
	var n int64
	if err := db.QueryRow(ctx, "SELECT count(*) FROM events WHERE created_at < now() - interval '1 year'").Scan(&n); err != nil {
		return 0, fmt.Errorf("count old events: %w", err)
	}
	return n, nil
}
