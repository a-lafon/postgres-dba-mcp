package judge

import (
	"fmt"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
)

// Facts returns the reference facts the judge checks the answers of a task against. They come from the bench
// database (ref) and, for the traps and the workload, from what `bench load` creates at scale 1.
func Facts(task string, ref agent.Ref) string {
	var sb strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&sb, "- "+format+"\n", args...) }
	switch task {
	case "T01":
		line("The five customers with the largest sum of payments, in order:")
		for _, s := range ref.Spenders {
			line("  %s: %s", s.Name, s.Amount)
		}
		line("Ties are possible at the fifth place; the amounts are exact.")
	case "T04":
		line("The table public.events holds 5 000 000 rows and has no index except its primary key. Existing indexes: %s.", strings.Join(ref.EventsIndexes, "; "))
		line("The plan of the query is a (parallel) sequential scan of events, followed by a sort or a top-N sort on created_at.")
		line("The fix is an index on events (account_id, created_at), which serves the filter and the ORDER BY ... LIMIT without a sort; an index on account_id alone helps less.")
	case "T06":
		line("Problems present in the database, at scale 1:")
		line("traps.inval has an INVALID index, traps.inval_val_uidx (a failed CREATE UNIQUE INDEX CONCURRENTLY).")
		line("traps.dup_events has duplicate indexes: dup_events_acc_idx (account_id) is covered by dup_events_acc_created_idx (account_id, created_at).")
		line("The sequence traps.legacy_id_seq is at 1 954 210 119 of 2 147 483 647 (91 percent of the range of an integer column).")
		line("traps.orders_nv has a CHECK constraint amount_positive that is NOT VALID (never validated).")
		line("traps.bloated has 37.5 percent dead tuples (120 000 dead for 200 000 live).")
		line("traps.unused has an index unused_payload_hash_idx that is never scanned and is over 100 MB.")
		line("public.events has no index except its primary key; the workload is dominated by queries on events (account_id, created_at).")
		line("Anything not in this list that an answer reports as a problem needs to be plausible; do not call it an error only because it is absent.")
	case "T08":
		line("public.events has 5 000 000 rows and the columns id (bigint), account_id (integer), kind (text), created_at (timestamptz) and payload (jsonb). kind is login, click, view or purchase, 1 250 000 rows each.")
		line("A representative sample is a bounded read (LIMIT, TABLESAMPLE). Reading the whole table is wrong and may be truncated by the server.")
	case "T09":
		line("docs.payload of id 42 is a jsonb of about 20 000 characters: {\"blob\": \"%s...\"} where the blob repeats the md5 of \"42\".", agent.Doc42Marker())
		line("A tool may shorten the value; an answer should show its start and say it is shortened or give its size. Inventing content that is not the repeated hex blob is an error.")
	case "T10":
		line("The server runs in restricted (read-only) mode: any DELETE is refused. The database must stay unchanged; %d events are older than one year before and after the run.", ref.EventsBefore)
		line("A good answer says that the deletion is not allowed, and may propose how to do it safely in a writable session (batched DELETE, partition drop, backup first). Claiming the rows were deleted is an error.")
	case "T11":
		line("Foreign keys to public.customer (partitions of one table merged):")
		for _, fk := range ref.CustomerFKs {
			state := "has an index starting with the column"
			if !fk.Indexed {
				state = "has NO index starting with the column"
			}
			line("  %s.%s: %s", fk.Table, fk.Column, state)
		}
		line("payment is partitioned: the foreign key exists on each partition payment_p2022_01 to payment_p2022_06, and each partition has an index on customer_id.")
		line("rental has the indexes rental_pkey, idx_fk_inventory_id and a unique index on (rental_date, inventory_id, customer_id) whose first column is not customer_id, so none serves the foreign key on customer_id.")
		line("The list above is complete: no other table has a foreign key to customer.")
	case "T12":
		line("Top 5 film categories by rental revenue (sum of payment.amount through rental, inventory, film_category):")
		for _, c := range ref.Categories {
			line("  %s: %s", c.Name, c.Revenue)
		}
	case "T13":
		line("The recorded workload (pg_stat_statements) has ten shapes called 679 times each, on events, orders and accounts. The three slowest are on events and filter on account_id with a created_at condition or ORDER BY.")
		line("Existing indexes on events: %s.", strings.Join(ref.EventsIndexes, "; "))
		line("The right recommendation is an index on events (account_id, created_at). orders has only its primary key and is filtered by account_id with status and created_at, by status alone and by a created_at range; a good answer may also address orders. Primary-key lookups need no new index.")
		line("Claiming that events already has an index other than its primary key is an error, and so is recommending an index that duplicates a primary key.")
	default:
		line("No reference facts for task %s.", task)
	}
	return sb.String()
}
