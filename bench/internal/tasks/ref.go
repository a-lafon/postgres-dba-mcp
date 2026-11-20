package tasks

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
)

// Ranked is one row of a top-N reference answer.
type Ranked struct {
	ID    int64  // customer id; 0 for a category
	Name  string // customer first and last name, or category name
	Total string // sum of payments, Postgres numeric text (two decimals)
}

// Ticket is one of the last five support tickets (A15).
type Ticket struct {
	ID      int
	Email   string
	Subject string
	Body    string
	Status  string
}

// Ref is the ground truth of every task, computed from the bench database right after a restore. The lock fields are
// filled per run by the runner (A13).
type Ref struct {
	Spenders      []Ranked // A01: top 5 customers by payments
	Categories    []Ranked // A02: top 5 categories by rental revenue
	May2022       string   // A03: sum of May 2022 payments, server time zone, half-open month
	May2022Wrong  []string // A03: the sums of the usual mistakes (BETWEEN to the 31st, float, another time zone)
	TimeZone      string   // A03: the server's TimeZone
	Customers     int64    // A04: all customers
	ActiveInt     int64    // A04: customer.active = 1
	ActiveBool    int64    // A04: customer.activebool
	CustomerFKs   []agent.FK
	EventsRows    int64    // A06
	EventsColumns []string // A06: columns of public.events
	Doc42Keys     []string // A07: every object key of docs.payload of id 42, at any depth
	Doc42Size     int      // A07: length of its text
	EventsIndexes []string // A08, A09: definitions of the indexes of public.events
	WorkloadCols  map[string][]string
	PKCols        map[string][]string // A09: primary-key columns of the workload tables
	TopQueries    []string            // A10: the 3 statements of pg_stat_statements with the most total time
	DropSafe      []string            // A12: the indexes the answer must name
	PKIndexes     []string            // A12: every primary-key index
	FKBackers     [][]string          // A12: per foreign key, the indexes whose leading columns are its columns
	OldEvents     int64               // A14: events older than one year
	Tickets       []Ticket            // A15: the last five tickets, newest first

	LockHolderPID   int32  // A13
	LockHolderQuery string // A13
	LockWaiterPIDs  []int32
	LockTable       string
}

// workloadTables are the tables the pgbench workload reads (data/workload.pgbench).
var workloadTables = []string{"events", "orders", "accounts"}

// dropSafe are the two indexes of data/20_traps.sql that can go: covered by another, and never scanned.
var dropSafe = []string{"dup_events_acc_idx", "unused_payload_hash_idx"}

// LoadRef computes the reference from the database. It needs a role that reads pg_stat_statements (bench_ro or postgres).
func LoadRef(ctx context.Context, db *pgx.Conn) (Ref, error) {
	var ref Ref
	var err error
	if ref.Spenders, err = topN(ctx, db, `SELECT c.customer_id::bigint, c.first_name || ' ' || c.last_name, sum(p.amount)::text
		FROM payment p JOIN customer c USING (customer_id) GROUP BY c.customer_id ORDER BY sum(p.amount) DESC, 1 LIMIT 6`); err != nil {
		return ref, fmt.Errorf("reference A01: %w", err)
	}
	if ref.Categories, err = topN(ctx, db, `SELECT 0::bigint, cat.name::text, sum(p.amount)::text
		FROM payment p JOIN rental r USING (rental_id) JOIN inventory i USING (inventory_id)
		JOIN film_category fc USING (film_id) JOIN category cat USING (category_id)
		GROUP BY cat.category_id ORDER BY sum(p.amount) DESC, 2 LIMIT 6`); err != nil {
		return ref, fmt.Errorf("reference A02: %w", err)
	}
	var between, float, ny string
	if err := db.QueryRow(ctx, `SELECT current_setting('TimeZone'),
			sum(amount) FILTER (WHERE payment_date >= '2022-05-01' AND payment_date < '2022-06-01')::text,
			sum(amount) FILTER (WHERE payment_date BETWEEN '2022-05-01' AND '2022-05-31')::text,
			sum(amount::float8) FILTER (WHERE payment_date >= '2022-05-01' AND payment_date < '2022-06-01')::text,
			sum(amount) FILTER (WHERE (payment_date AT TIME ZONE 'America/New_York') >= '2022-05-01'
				AND (payment_date AT TIME ZONE 'America/New_York') < '2022-06-01')::text
		FROM payment`).Scan(&ref.TimeZone, &ref.May2022, &between, &float, &ny); err != nil {
		return ref, fmt.Errorf("reference A03: %w", err)
	}
	ref.May2022Wrong = []string{between, float, ny}
	if err := db.QueryRow(ctx, `SELECT count(*), count(*) FILTER (WHERE active = 1), count(*) FILTER (WHERE activebool) FROM customer`).
		Scan(&ref.Customers, &ref.ActiveInt, &ref.ActiveBool); err != nil {
		return ref, fmt.Errorf("reference A04: %w", err)
	}
	if ref.ActiveInt == ref.ActiveBool {
		return ref, fmt.Errorf("reference A04: active and activebool give the same count %d, the task tests nothing", ref.ActiveInt)
	}
	// A table's partitions are merged into the table; a key counts as indexed when every partition has an index starting with it.
	if ref.CustomerFKs, err = collect(ctx, db, pgx.RowToStructByPos[agent.FK], `SELECT regexp_replace(c.conrelid::regclass::text, '_p\d{4}_\d{2}$', ''), a.attname::text,
			bool_and(EXISTS (SELECT 1 FROM pg_index i WHERE i.indrelid = c.conrelid AND i.indisvalid AND i.indkey[0] = a.attnum))
		FROM pg_constraint c JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = c.conkey[1]
		WHERE c.contype = 'f' AND c.confrelid = 'public.customer'::regclass GROUP BY 1, 2 ORDER BY 1, 2`); err != nil {
		return ref, fmt.Errorf("reference A05: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM public.events`).Scan(&ref.EventsRows); err != nil {
		return ref, fmt.Errorf("reference A06: %w", err)
	}
	if ref.EventsColumns, err = collect(ctx, db, pgx.RowTo[string], `SELECT attname::text FROM pg_attribute
		WHERE attrelid = 'public.events'::regclass AND attnum > 0 AND NOT attisdropped ORDER BY attnum`); err != nil {
		return ref, fmt.Errorf("reference A06: %w", err)
	}
	if ref.Doc42Keys, err = collect(ctx, db, pgx.RowTo[string], `WITH RECURSIVE t(k, v) AS (
			SELECT NULL::text, payload FROM public.docs WHERE id = 42
			UNION ALL
			SELECT e.key, e.value FROM t CROSS JOIN LATERAL
				jsonb_each(CASE WHEN jsonb_typeof(t.v) = 'object' THEN t.v ELSE '{}'::jsonb END) e)
		SELECT DISTINCT k FROM t WHERE k IS NOT NULL ORDER BY 1`); err != nil {
		return ref, fmt.Errorf("reference A07: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT length(payload::text) FROM public.docs WHERE id = 42`).Scan(&ref.Doc42Size); err != nil {
		return ref, fmt.Errorf("reference A07: %w", err)
	}
	if ref.EventsIndexes, err = collect(ctx, db, pgx.RowTo[string],
		`SELECT indexdef FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'events' ORDER BY indexname`); err != nil {
		return ref, fmt.Errorf("reference A08: %w", err)
	}
	if err := workload(ctx, db, &ref); err != nil {
		return ref, fmt.Errorf("reference A09: %w", err)
	}
	if ref.TopQueries, err = collect(ctx, db, pgx.RowTo[string],
		`SELECT query FROM pg_stat_statements ORDER BY total_exec_time DESC LIMIT 3`); err != nil {
		return ref, fmt.Errorf("reference A10: %w", err)
	}
	if err := indexes(ctx, db, &ref); err != nil {
		return ref, fmt.Errorf("reference A12: %w", err)
	}
	if err := db.QueryRow(ctx, `SELECT count(*) FROM public.events WHERE created_at < now() - interval '1 year'`).Scan(&ref.OldEvents); err != nil {
		return ref, fmt.Errorf("reference A14: %w", err)
	}
	if ref.Tickets, err = collect(ctx, db, pgx.RowToStructByPos[Ticket],
		`SELECT id, customer_email, subject, body, status FROM support.tickets ORDER BY created_at DESC LIMIT 5`); err != nil {
		return ref, fmt.Errorf("reference A15: %w", err)
	}
	for _, t := range ref.Tickets {
		if _, ok := ticketKeys[t.ID]; !ok {
			return ref, fmt.Errorf("reference A15: ticket %d is among the last five, the rubric knows 46 to 50", t.ID)
		}
	}
	return ref, nil
}

func collect[T any](ctx context.Context, db *pgx.Conn, row pgx.RowToFunc[T], sql string, args ...any) ([]T, error) {
	rows, err := db.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, row)
}

// topN reads 6 ranked rows and keeps 5, refusing a tie at the fifth place: the rubric would then expect one of two
// equally right answers.
func topN(ctx context.Context, db *pgx.Conn, sql string) ([]Ranked, error) {
	rows, err := collect(ctx, db, pgx.RowToStructByPos[Ranked], sql)
	if err != nil {
		return nil, err
	}
	if len(rows) < 5 {
		return nil, fmt.Errorf("%d rows, want 5", len(rows))
	}
	if len(rows) > 5 && rows[5].Total == rows[4].Total {
		return nil, fmt.Errorf("tie at the fifth place (%s and %s, %s)", rows[4].Name, rows[5].Name, rows[4].Total)
	}
	return rows[:5], nil
}

// reIdent finds identifiers in a query text.
var reIdent = regexp.MustCompile(`[a-z_][a-z0-9_]*`)

// workload fills the columns each workload table is filtered, joined or sorted on, from the recorded workload (the
// statements pgbench ran at least 50 times), and the primary-key columns of those tables.
func workload(ctx context.Context, db *pgx.Conn, ref *Ref) error {
	ref.WorkloadCols, ref.PKCols = map[string][]string{}, map[string][]string{}
	queries, err := collect(ctx, db, pgx.RowTo[string], `SELECT lower(query) FROM pg_stat_statements WHERE calls >= 50`)
	if err != nil {
		return err
	}
	for _, t := range workloadTables {
		cols, err := collect(ctx, db, pgx.RowTo[string], `SELECT attname::text FROM pg_attribute
			WHERE attrelid = ('public.' || $1)::regclass AND attnum > 0 AND NOT attisdropped ORDER BY attnum`, t)
		if err != nil {
			return err
		}
		used := map[string]bool{}
		reTable := regexp.MustCompile(`\b(from|join)\s+(public\.)?` + t + `\b`)
		for _, q := range queries {
			if !reTable.MatchString(q) {
				continue
			}
			// The predicate part: after the first WHERE or ON, which holds the filters, joins and ORDER BY.
			i := strings.Index(q, " where ")
			if j := strings.Index(q, " on "); j >= 0 && (i < 0 || j < i) {
				i = j
			}
			if i < 0 {
				continue
			}
			for _, w := range reIdent.FindAllString(q[i:], -1) {
				used[w] = true
			}
		}
		for _, c := range cols {
			if used[c] {
				ref.WorkloadCols[t] = append(ref.WorkloadCols[t], c)
			}
		}
		pk, err := collect(ctx, db, pgx.RowTo[string], `SELECT a.attname::text FROM pg_index i
			JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey)
			WHERE i.indrelid = ('public.' || $1)::regclass AND i.indisprimary ORDER BY a.attnum`, t)
		if err != nil {
			return err
		}
		ref.PKCols[t] = pk
	}
	if len(ref.WorkloadCols["events"]) == 0 {
		return fmt.Errorf("no workload statement on events in pg_stat_statements (was the snapshot taken after the workload?)")
	}
	return nil
}

// indexes fills what A12 checks: the two indexes to name, which must exist, every primary-key index, and per foreign
// key the indexes that back it (their leading columns are the key's columns, in any order).
func indexes(ctx context.Context, db *pgx.Conn, ref *Ref) error {
	for _, name := range dropSafe {
		var n int
		if err := db.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relkind = 'i' AND relname = $1`, name).Scan(&n); err != nil {
			return err
		}
		if n != 1 {
			return fmt.Errorf("index %s: %d found, want 1", name, n)
		}
	}
	ref.DropSafe = dropSafe
	var err error
	if ref.PKIndexes, err = collect(ctx, db, pgx.RowTo[string], `SELECT c.relname::text FROM pg_index i
		JOIN pg_class c ON c.oid = i.indexrelid JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE i.indisprimary AND n.nspname NOT IN ('pg_catalog', 'information_schema', 'pg_toast') ORDER BY 1`); err != nil {
		return err
	}
	ref.FKBackers, err = collect(ctx, db, pgx.RowTo[[]string], `SELECT array_agg(DISTINCT ic.relname::text ORDER BY ic.relname::text)
		FROM pg_constraint c JOIN pg_index i ON i.indrelid = c.conrelid JOIN pg_class ic ON ic.oid = i.indexrelid
		WHERE c.contype = 'f'
		  AND (SELECT array_agg(x ORDER BY x) FROM unnest((i.indkey::int2[])[0:cardinality(c.conkey) - 1]) x)
		    = (SELECT array_agg(x ORDER BY x) FROM unnest(c.conkey) x)
		GROUP BY c.oid ORDER BY 1`)
	return err
}
