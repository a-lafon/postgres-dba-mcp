package scenarios

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Trunc is the truncation a scenario is expected to trigger in postgres-dba-mcp.
type Trunc int

// Truncation expectations.
const (
	TruncNone  Trunc = iota // no truncation expected
	TruncRows               // the row or byte bound must apply and be signalled
	TruncCells              // a cell must be shortened and signalled
)

// Step is a tool call made before the measured one, on the same server process.
type Step struct {
	Tool string
	Args map[string]any
}

// Outcome is what a check found in one response.
type Outcome struct {
	Missing  []string        // expected facts that are absent
	Detected []string        // trap ids found (S17, S18)
	Advice   *results.Advice // what an index advisor recommended (S19, S20)
}

// Call is a prepared scenario: the arguments, the setup steps and the check of the answer.
type Call struct {
	Args  map[string]any
	Setup []Step
	Check func(server, text string) Outcome
}

// Scenario is one row of the N2 table.
type Scenario struct {
	ID      string
	Tool    string
	Pagila  bool  // needs the Pagila tables
	Trunc   Trunc // truncation expected from postgres-dba-mcp
	Prepare func(ctx context.Context, db *pgxpool.Pool) (Call, error)
}

// Trap names a trap of schema traps and the pattern that proves a report found it.
type Trap struct {
	ID    string
	Match *regexp.Regexp
}

// Traps are T1-T6 of bench/data/20_traps.sql.
var Traps = []Trap{
	{"T1", regexp.MustCompile(`inval_val_uidx`)}, {"T2", regexp.MustCompile(`dup_events_acc_idx`)},
	// T3 is dead tuples: an index bloat finding on the table's primary key is not that.
	{"T3", regexp.MustCompile(`dead rows: traps\.bloated`)},
	{"T4", regexp.MustCompile(`legacy_id_seq`)}, {"T5", regexp.MustCompile(`amount_positive`)},
	{"T6", regexp.MustCompile(`unused_payload_hash_idx`)},
}

// ExpectedTraps returns the traps a health scenario must find: every trap for S17 (`all`), the index traps for S18.
func ExpectedTraps(id string) []string {
	if id == "S18" {
		return []string{"T1", "T2"}
	}
	ids := make([]string, len(Traps))
	for i, t := range Traps {
		ids[i] = t.ID
	}
	return ids
}

// CrystalTraps are the traps crystaldba can detect: its vacuum check covers wraparound, not dead tuples (T3).
var CrystalTraps = []string{"T1", "T2", "T4", "T5", "T6"}

const (
	querySeq = "SELECT * FROM events WHERE account_id = 42 ORDER BY created_at DESC LIMIT 20"
	queryHyp = "SELECT * FROM events WHERE account_id = 42 ORDER BY created_at DESC LIMIT 20"
)

var hypoEvents = []map[string]any{{"table": "events", "columns": []string{"account_id", "created_at"}}}

// All returns S01-S21 in order.
func All() []Scenario {
	sql := func(id, q string, p bool, t Trunc, facts func(context.Context, *pgxpool.Pool) ([]Fact, error)) Scenario {
		return Scenario{ID: id, Tool: "execute_sql", Pagila: p, Trunc: t, Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
			var fs []Fact
			if facts != nil {
				var err error
				if fs, err = facts(ctx, db); err != nil {
					return Call{}, err
				}
			}
			return factCall(map[string]any{"sql": q}, fs), nil
		}}
	}
	return []Scenario{
		sql("S01", "SELECT * FROM film LIMIT 10", true, TruncNone, func(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
			titles, err := strs(ctx, db, "SELECT title FROM film LIMIT 10")
			return mapFacts(titles, Str), err
		}),
		sql("S02", "SELECT * FROM rental", true, TruncRows, func(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
			ids, err := ints(ctx, db, "SELECT rental_id FROM rental LIMIT 3")
			return rowIDs("rental_id", ids), err
		}),
		sql("S03", "SELECT * FROM events", false, TruncRows, nil),
		sql("S04", "SELECT id, payload FROM docs LIMIT 20", false, TruncCells, func(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
			ids, err := ints(ctx, db, "SELECT id FROM docs LIMIT 20")
			return rowIDs("id", ids), err
		}),
		sql("S05", "SELECT customer_id, sum(amount) FROM payment GROUP BY 1 ORDER BY 2 DESC LIMIT 10", true, TruncNone, sums),
		sql("S06", "SELECT * FROM types", false, TruncNone, typesFacts),
		sql("S07", "SELECT nme FROM film", true, TruncNone, badColumn),
		listSchemas(),
		listObjects(),
		details("S10", "rental", true, rentalFacts),
		details("S11", "wide60", false, wideFacts),
		explain("S12", nil),
		explain("S13", hypoEvents),
		explainAnalyze(),
		topQueries("S15", "total_time"),
		topQueries("S16", "resources"),
		health("S17", "all"),
		health("S18", "index"),
		advice("S19"),
		advice("S20"),
		leak(),
	}
}

func factCall(args map[string]any, facts []Fact) Call {
	return Call{Args: args, Check: func(server, text string) Outcome {
		return Outcome{Missing: Missing(server, text, facts)}
	}}
}

func mapFacts(v []string, f func(string) Fact) []Fact {
	out := make([]Fact, len(v))
	for i, s := range v {
		out[i] = f(s)
	}
	return out
}

func rowIDs(col string, ids []int64) []Fact {
	out := make([]Fact, len(ids))
	for i, id := range ids {
		out[i] = Row([]string{col}, Int(id))
	}
	return out
}

func strs(ctx context.Context, db *pgxpool.Pool, q string, args ...any) ([]string, error) {
	rows, err := db.Query(ctx, q, args...)
	if err != nil {
		return nil, fmt.Errorf("reference query %q: %w", q, err)
	}
	v, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("reference query %q: %w", q, err)
	}
	return v, nil
}

func ints(ctx context.Context, db *pgxpool.Pool, q string) ([]int64, error) {
	rows, err := db.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("reference query %q: %w", q, err)
	}
	v, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("reference query %q: %w", q, err)
	}
	return v, nil
}

func sums(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
	rows, err := db.Query(ctx, "SELECT customer_id::bigint, sum(amount)::text FROM payment GROUP BY 1 ORDER BY sum(amount) DESC LIMIT 10")
	if err != nil {
		return nil, fmt.Errorf("reference sums: %w", err)
	}
	type r struct {
		ID  int64
		Sum string
	}
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[r])
	if err != nil {
		return nil, fmt.Errorf("reference sums: %w", err)
	}
	var out []Fact
	for _, x := range got {
		out = append(out, Row([]string{"customer_id", "sum"}, Int(x.ID), Num(x.Sum)))
	}
	return out, nil
}

func typesFacts(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
	var (
		txt, empty *string
		num, ivTxt string
		arr        []int32
		iv         pgtype.Interval
		ts         time.Time
		bin        []byte
	)
	err := db.QueryRow(ctx, "SELECT txt, empty, num::text, arr, iv::text, iv, ts, bin FROM types WHERE id = 1").
		Scan(&txt, &empty, &num, &arr, &ivTxt, &iv, &ts, &bin)
	if err != nil {
		return nil, fmt.Errorf("reference types row: %w", err)
	}
	if txt == nil || empty == nil || *empty != "" {
		return nil, errors.New("reference types row 1 is not the loaded one")
	}
	return []Fact{Str(*txt), Empty(), Num(num), IntArray(arr), Interval(ivTxt, iv.Days, iv.Microseconds), Time(ts), Bytea(bin), Null()}, nil
}

func badColumn(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
	_, err := db.Exec(ctx, "SELECT nme FROM film")
	var pe *pgconn.PgError
	if !errors.As(err, &pe) {
		return nil, fmt.Errorf("reference query SELECT nme FROM film did not fail with a Postgres error: %v", err)
	}
	facts := []Fact{{DBA: "(SQLSTATE " + pe.Code + ")"}, Same(pe.Message)}
	if pe.Hint != "" {
		facts = append(facts, Same(pe.Hint))
	}
	return facts, nil
}

func listSchemas() Scenario {
	return Scenario{ID: "S08", Tool: "list_schemas", Prepare: func(context.Context, *pgxpool.Pool) (Call, error) {
		return factCall(map[string]any{}, []Fact{Same("public"), Same("traps")}), nil
	}}
}

func listObjects() Scenario {
	return Scenario{ID: "S09", Tool: "list_objects", Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		names, err := strs(ctx, db, "SELECT c.relname::text FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE n.nspname = 'public' AND c.relkind IN ('r', 'p') AND NOT c.relispartition ORDER BY 1")
		if err != nil {
			return Call{}, err
		}
		return factCall(map[string]any{"schema_name": "public", "object_type": "table"}, mapFacts(names, Same)), nil
	}}
}

func details(id, table string, pagila bool, facts func(context.Context, *pgxpool.Pool) ([]Fact, error)) Scenario {
	return Scenario{ID: id, Tool: "get_object_details", Pagila: pagila, Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		fs, err := facts(ctx, db)
		if err != nil {
			return Call{}, err
		}
		return factCall(map[string]any{"schema_name": "public", "object_name": table, "object_type": "table"}, fs), nil
	}}
}

// rentalFacts are the columns, the primary key and index names, and the foreign keys of public.rental. A foreign
// key reads `-> table(column)` in postgres-dba-mcp and is its constraint name in crystaldba.
func rentalFacts(ctx context.Context, db *pgxpool.Pool) ([]Fact, error) {
	var facts []Fact
	for _, q := range []string{
		"SELECT column_name FROM information_schema.columns WHERE table_schema = 'public' AND table_name = 'rental'",
		"SELECT conname FROM pg_constraint WHERE conrelid = 'public.rental'::regclass AND contype = 'p'",
		"SELECT indexname FROM pg_indexes WHERE schemaname = 'public' AND tablename = 'rental'",
	} {
		v, err := strs(ctx, db, q)
		if err != nil {
			return nil, err
		}
		facts = append(facts, mapFacts(v, Same)...)
	}
	rows, err := db.Query(ctx, `SELECT conname, confrelid::regclass::text, (SELECT attname FROM pg_attribute WHERE attrelid = confrelid AND attnum = confkey[1])
		FROM pg_constraint WHERE conrelid = 'public.rental'::regclass AND contype = 'f' ORDER BY conname`)
	if err != nil {
		return nil, fmt.Errorf("reference foreign keys: %w", err)
	}
	type fk struct{ Name, Table, Column string }
	fks, err := pgx.CollectRows(rows, pgx.RowToStructByPos[fk])
	if err != nil {
		return nil, fmt.Errorf("reference foreign keys: %w", err)
	}
	for _, f := range fks {
		facts = append(facts, Fact{DBA: fmt.Sprintf("→ %s(%s)", f.Table, f.Column), Crystal: f.Name})
	}
	return facts, nil
}

func wideFacts(context.Context, *pgxpool.Pool) ([]Fact, error) {
	out := make([]Fact, 60)
	for i := range out {
		out[i] = Same(fmt.Sprintf("c%02d", i+1))
	}
	return out, nil
}

// planCost returns the total cost of the plain plan of q.
func planCost(ctx context.Context, db *pgxpool.Pool, q string) (float64, error) {
	var raw []byte
	if err := db.QueryRow(ctx, "EXPLAIN (FORMAT JSON) "+q).Scan(&raw); err != nil {
		return 0, fmt.Errorf("reference plan: %w", err)
	}
	var plan []struct {
		Plan struct {
			Total float64 `json:"Total Cost"`
		}
	}
	if err := json.Unmarshal(raw, &plan); err != nil || len(plan) == 0 {
		return 0, fmt.Errorf("decode reference plan: %v", err)
	}
	return plan[0].Plan.Total, nil
}

var reIndexScan = regexp.MustCompile(`Index (Only )?Scan`)

func explain(id string, hypo []map[string]any) Scenario {
	return Scenario{ID: id, Tool: "explain_query", Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		base, err := planCost(ctx, db, querySeq)
		if err != nil {
			return Call{}, err
		}
		args := map[string]any{"sql": querySeq}
		if hypo != nil {
			args["hypothetical_indexes"] = hypo
		}
		return Call{Args: args, Check: func(server, text string) Outcome {
			if hypo == nil {
				return Outcome{Missing: Missing(server, text, []Fact{Same("Seq Scan"), Same("events")})}
			}
			var miss []string
			if !reIndexScan.MatchString(text) {
				miss = append(miss, "Index Scan")
			}
			if c, ok := RootCost(server, text); !ok || c >= base {
				miss = append(miss, fmt.Sprintf("total cost below %.2f", base))
			}
			return Outcome{Missing: miss}
		}}, nil
	}}
}

func explainAnalyze() Scenario {
	const q = "SELECT count(*) FROM orders WHERE status = 'pending'"
	return Scenario{ID: "S14", Tool: "explain_query", Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		var n int64
		if err := db.QueryRow(ctx, q).Scan(&n); err != nil {
			return Call{}, fmt.Errorf("reference count: %w", err)
		}
		return Call{Args: map[string]any{"sql": q, "analyze": true}, Check: func(server, text string) Outcome {
			// postgres-dba-mcp prints counts with Approx (2.5k), crystaldba prints them exactly.
			facts := []Fact{{DBA: "actual rows ", Crystal: fmt.Sprintf("Rows: %d, Loops", n)}, {DBA: "execution ", Crystal: "Execution Time"},
				{DBA: "(actual ", Crystal: "[Actual:"}, Same("Seq Scan")}
			return Outcome{Missing: Missing(server, text, facts)}
		}}, nil
	}}
}

const statementsFilter = `dbid = (SELECT oid FROM pg_database WHERE datname = current_database())`

func topQueries(id, sortBy string) Scenario {
	return Scenario{ID: id, Tool: "get_top_queries", Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		limit := "10"
		if sortBy == "resources" {
			limit = "1"
		}
		q, err := strs(ctx, db, "SELECT query FROM pg_stat_statements WHERE "+statementsFilter+" ORDER BY total_exec_time DESC LIMIT "+limit)
		if err != nil {
			return Call{}, err
		}
		if len(q) == 0 {
			return Call{}, errors.New("pg_stat_statements is empty: run load first")
		}
		args := map[string]any{"sort_by": sortBy}
		if sortBy != "resources" {
			args["limit"] = 10
		}
		prefixes := make([]string, len(q))
		for i, s := range q {
			n := NormalizeQuery(s)
			prefixes[i] = n[:min(len(n), 50)]
		}
		return Call{Args: args, Check: func(_, text string) Outcome {
			norm := NormalizeQuery(text)
			var miss []string
			for _, p := range prefixes {
				if !strings.Contains(norm, p) {
					miss = append(miss, p)
				}
			}
			return Outcome{Missing: miss}
		}}, nil
	}}
}

func health(id, kind string) Scenario {
	return Scenario{ID: id, Tool: "analyze_db_health", Prepare: func(context.Context, *pgxpool.Pool) (Call, error) {
		want := ExpectedTraps(id)
		return Call{Args: map[string]any{"health_type": kind}, Check: func(server, text string) Outcome {
			var o Outcome
			for _, t := range Traps {
				if t.Match.MatchString(text) {
					o.Detected = append(o.Detected, t.ID)
				}
			}
			expect := want
			if server == results.Crystal && kind == "all" {
				expect = CrystalTraps
			}
			for _, w := range expect {
				if !contains(o.Detected, w) {
					o.Missing = append(o.Missing, w)
				}
			}
			return o
		}}, nil
	}}
}

func contains(v []string, s string) bool {
	for _, x := range v {
		if x == s {
			return true
		}
	}
	return false
}

func advice(id string) Scenario {
	return Scenario{ID: id, Tool: "analyze_query_indexes", Prepare: func(ctx context.Context, db *pgxpool.Pool) (Call, error) {
		queries := []string{"SELECT * FROM accounts WHERE id = 1"}
		if id == "S19" {
			var err error
			queries, err = strs(ctx, db, `SELECT query FROM pg_stat_statements
				WHERE `+statementsFilter+` AND calls >= 50 AND query ~* '\m(events|orders|accounts)\M'
				  AND query !~* 'pg_stat|pg_catalog|pg_class|hypopg|explain|^(set|show) '
				ORDER BY mean_exec_time DESC LIMIT 3`)
			if err != nil {
				return Call{}, err
			}
			if len(queries) < 3 {
				return Call{}, fmt.Errorf("workload has %d slow queries, want 3: run load first", len(queries))
			}
		}
		return Call{Args: map[string]any{"queries": queries}, Check: func(server, text string) Outcome {
			a := ParseAdvice(server, text)
			o := Outcome{Advice: &a}
			switch {
			case id == "S20" && !a.None:
				o.Missing = []string{"no recommendation"}
			case id == "S19" && !contains(a.Indexes, "events(account_id,created_at)"):
				o.Missing = []string{"index on events(account_id, created_at)"}
			}
			return o
		}}, nil
	}}
}

func leak() Scenario {
	return Scenario{ID: "S21", Tool: "explain_query", Prepare: func(context.Context, *pgxpool.Pool) (Call, error) {
		return Call{
			Setup: []Step{{Tool: "explain_query", Args: map[string]any{"sql": queryHyp, "hypothetical_indexes": hypoEvents}}},
			Args:  map[string]any{"sql": querySeq},
			Check: func(_, text string) Outcome {
				var miss []string
				if !strings.Contains(text, "Seq Scan") {
					miss = append(miss, "Seq Scan")
				}
				if reIndexScan.MatchString(text) || strings.Contains(strings.ToLower(text), "hypothetical") {
					miss = append(miss, "no hypothetical index in the plan")
				}
				return Outcome{Missing: miss}
			},
		}, nil
	}}
}
