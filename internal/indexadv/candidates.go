package indexadv

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/hypo"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
)

//go:embed columns.sql
var columnsSQL string

//go:embed existing.sql
var existingSQL string

// maxWidth is the widest candidate index (crystaldba max_index_width).
const maxWidth = 3

// table is a relation resolved against the catalog.
type table struct {
	OID      int64
	Schema   string
	Name     string
	Quoted   string // schema.name, quoted as needed
	Kind     string // pg_class.relkind
	Size     int64  // pg_total_relation_size
	Analyzed bool
	Cols     map[string]*column
}

type column struct {
	Name, Quoted string
	Long         bool // a long text column, never indexed
}

// indexable reports whether the table can get a candidate index: a regular, partitioned or materialized table
// outside the system schemas.
func (t *table) indexable() bool {
	return t.plain() && !t.system()
}

// plain reports whether the relation is a regular, partitioned or materialized table.
func (t *table) plain() bool { return t.Kind == "r" || t.Kind == "p" || t.Kind == "m" }

func (t *table) system() bool {
	return t.Schema == "pg_catalog" || t.Schema == "information_schema" || t.Schema == "pg_toast"
}

// relKey identifies a relation as the parser names it.
type relKey struct{ schema, name string }

// resolve looks up every named relation of the queries in the catalog, with its columns. A relation that does
// not exist is absent from the result.
func resolve(ctx context.Context, tx pgx.Tx, qs []*query) (map[relKey]*table, error) {
	var keys []relKey
	seen := map[relKey]bool{}
	for _, q := range qs {
		for _, r := range q.rels {
			if k := (relKey{r.Schema, r.Name}); r.Name != "" && !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		}
	}
	byKey := map[relKey]*table{}
	if len(keys) == 0 {
		return byKey, nil
	}
	schemas, names := make([]string, len(keys)), make([]string, len(keys))
	for i, k := range keys {
		schemas[i], names[i] = k.schema, k.name
	}
	rows, err := tx.Query(ctx, columnsSQL, schemas, names)
	if err != nil {
		return nil, fmt.Errorf("resolve relations: %w", err)
	}
	defer rows.Close()
	byOID := map[int64]*table{}
	for rows.Next() {
		var ord int
		var oid int64
		var attname, qattname string
		var long bool
		t := &table{}
		if err := rows.Scan(&ord, &oid, &t.Kind, &t.Schema, &t.Name, &t.Quoted, &t.Size, &t.Analyzed, &attname, &qattname, &long); err != nil {
			return nil, fmt.Errorf("resolve relations: %w", err)
		}
		t.OID = oid
		if byOID[oid] == nil {
			t.Cols = map[string]*column{}
			byOID[oid] = t
		}
		byOID[oid].Cols[attname] = &column{Name: attname, Quoted: qattname, Long: long}
		byKey[keys[ord-1]] = byOID[oid]
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("resolve relations: %w", err)
	}
	return byKey, nil
}

// bind attaches to each query its resolved tables and whether they tell the whole story, and drops the
// queries that only read system catalogs.
func bind(qs []*query, tables map[relKey]*table) (kept []*query) {
	for _, q := range qs {
		q.exact = true
		catalogOnly := true
		for _, r := range q.rels {
			t := tables[relKey{r.Schema, r.Name}]
			switch {
			case r.Name == "" || t == nil:
				q.exact, catalogOnly = false, false
			case !t.plain():
				q.exact = false
				catalogOnly = catalogOnly && t.system()
			default:
				catalogOnly = catalogOnly && t.system()
			}
			if t != nil && !q.hasTable(t) {
				q.tables = append(q.tables, t)
			}
		}
		if !catalogOnly {
			kept = append(kept, q)
		}
	}
	for i, q := range kept {
		q.idx = i
	}
	return kept
}

// candidate is a btree index on one table.
type candidate struct {
	Table *table
	Cols  []*column
	key   string
	// together is true when one query of the workload uses every column of the candidate, so that the index
	// can serve all its conditions at once.
	together bool
}

// index is the hypopg description of the candidate.
func (c candidate) index() hypo.Index {
	names := make([]string, len(c.Cols))
	for i, col := range c.Cols {
		names[i] = col.Quoted // hypo parses the columns as SQL identifiers
	}
	return hypo.Index{Table: pgx.Identifier{c.Table.Schema, c.Table.Name}.Sanitize(), Columns: names}
}

// ddl is the CREATE INDEX statement of the candidate.
func (c candidate) ddl() string {
	names := make([]string, len(c.Cols))
	for i, col := range c.Cols {
		names[i] = col.Quoted
	}
	return "CREATE INDEX ON " + c.Table.Quoted + " (" + strings.Join(names, ", ") + ");"
}

// usage is the best use of a column over the workload and where it first appears.
type usage struct {
	use     safesql.Use
	qi, pos int
	qs      map[int]bool // indexes of the queries that use the column
}

func (u usage) before(o usage) bool {
	if u.qi != o.qi {
		return u.qi < o.qi
	}
	return u.pos < o.pos
}

// columnUse maps tables to the columns of their condition clauses.
type columnUse map[*table]map[string]*usage

// owner returns the one indexable table of the reference's scope that has the column, or nil. A subquery, CTE
// or function of the scope (an opaque RelRef) is skipped: the query is valid, so an unqualified column that a
// table of the scope has cannot also belong to another FROM item without being ambiguous.
func owner(ref safesql.ColumnRef, tables map[relKey]*table) *table {
	var found *table
	for _, r := range ref.Rels {
		t := tables[relKey{r.Schema, r.Name}]
		if t == nil || t.Cols[ref.Column] == nil || t == found {
			continue
		}
		if found != nil {
			return nil // ambiguous
		}
		found = t
	}
	if found == nil || !found.indexable() {
		return nil
	}
	return found
}

// collectUse reads the condition columns of the queries and resolves them.
func collectUse(qs []*query, tables map[relKey]*table) (columnUse, error) {
	use := columnUse{}
	for _, q := range qs {
		refs, err := safesql.Refs(q.SQL)
		if err != nil {
			return nil, fmt.Errorf("read columns of %s: %w", q.Label, err)
		}
		for _, ref := range refs {
			t := owner(ref, tables)
			if t == nil || t.Cols[ref.Column].Long {
				continue
			}
			u := usage{use: ref.Use, qi: q.idx, pos: ref.Pos, qs: map[int]bool{q.idx: true}}
			m := use[t]
			if m == nil {
				m = map[string]*usage{}
				use[t] = m
			}
			if cur := m[ref.Column]; cur == nil {
				m[ref.Column] = &u
			} else {
				cur.qs[q.idx] = true
				if u.use < cur.use {
					cur.use = u.use
				}
				if u.before(*cur) {
					cur.qi, cur.pos = u.qi, u.pos
				}
			}
		}
	}
	return use, nil
}

// existingIndexes returns, per table OID, the column lists of its valid plain btree indexes.
func existingIndexes(ctx context.Context, tx pgx.Tx, use columnUse) (map[int64][][]string, error) {
	oids := make([]int64, 0, len(use))
	for t := range use {
		oids = append(oids, t.OID)
	}
	out := map[int64][][]string{}
	if len(oids) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, existingSQL, oids)
	if err != nil {
		return nil, fmt.Errorf("read existing indexes: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var oid int64
		var cols []string
		if err := rows.Scan(&oid, &cols); err != nil {
			return nil, fmt.Errorf("read existing indexes: %w", err)
		}
		out[oid] = append(out[oid], cols)
	}
	return out, rows.Err()
}

// candidates builds the candidate indexes of 1 to maxWidth columns per table, with one column order per column
// set: best use, then first position in the workload, then name. They come in evaluation order: table, width,
// column list. Candidates identical to an existing index are left out.
func candidates(use columnUse, existing map[int64][][]string) []candidate {
	var out []candidate
	for t, cols := range use {
		names := make([]string, 0, len(cols))
		for n := range cols {
			names = append(names, n)
		}
		slices.SortFunc(names, func(a, b string) int {
			ua, ub := cols[a], cols[b]
			switch {
			case ua.use != ub.use:
				return int(ua.use) - int(ub.use)
			case ua.before(*ub):
				return -1
			case ub.before(*ua):
				return 1
			}
			return strings.Compare(a, b)
		})
		combos(len(names), maxWidth, func(pick []int) {
			c := candidate{Table: t}
			keys := make([]string, len(pick))
			for i, p := range pick {
				c.Cols = append(c.Cols, t.Cols[names[p]])
				keys[i] = names[p]
			}
			c.together = sharedQuery(cols, keys)
			if slices.ContainsFunc(existing[t.OID], func(e []string) bool { return slices.Equal(e, keys) }) {
				return
			}
			c.key = t.Quoted + "(" + strings.Join(keys, ",") + ")"
			out = append(out, c)
		})
	}
	slices.SortFunc(out, func(a, b candidate) int {
		if c := strings.Compare(a.Table.Schema, b.Table.Schema); c != 0 {
			return c
		}
		if c := strings.Compare(a.Table.Name, b.Table.Name); c != 0 {
			return c
		}
		if len(a.Cols) != len(b.Cols) {
			return len(a.Cols) - len(b.Cols)
		}
		for i := range a.Cols {
			if c := strings.Compare(a.Cols[i].Name, b.Cols[i].Name); c != 0 {
				return c
			}
		}
		return 0
	})
	return out
}

// sharedQuery reports whether one query uses all the named columns.
func sharedQuery(cols map[string]*usage, names []string) bool {
	for qi := range cols[names[0]].qs {
		if !slices.ContainsFunc(names, func(n string) bool { return !cols[n].qs[qi] }) {
			return true
		}
	}
	return false
}

// combos calls fn with every combination of 1 to width indexes among 0..n-1, each in increasing order.
func combos(n, width int, fn func(pick []int)) {
	var rec func(start int, pick []int)
	rec = func(start int, pick []int) {
		if len(pick) > 0 {
			fn(pick)
		}
		if len(pick) == width {
			return
		}
		for i := start; i < n; i++ {
			rec(i+1, append(pick, i))
		}
	}
	rec(0, make([]int, 0, width))
}
