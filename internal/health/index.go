package health

import (
	"context"
	_ "embed"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed indexes.sql
var indexesSQL string

//go:embed index_bloat.sql
var indexBloatSQL string

//go:embed unused_indexes.sql
var unusedIndexesSQL string

// indexInfo is one index of a user schema, as listed by indexes.sql.
type indexInfo struct {
	Schema, Table, Name, Using string
	Columns, KeyOpts           []string // KeyOpts: per key column, operator class/collation/options
	Unique, Primary, Valid     bool
	Exprs, Pred                string
	Root                       string // schema.name of the partition tree root, empty outside a partition tree
}

// dupPair is an index that another index of the same table makes unnecessary.
type dupPair struct{ Index, Covering indexInfo }

type bloatedIndex struct {
	Schema, Table, Name string
	Wasted, Size        int64
}

type unusedIndex struct {
	Schema, Name string
	Size, Scans  int64
	Table, Root  string // Root as in indexInfo
}

// indexResult is what the index check found.
type indexResult struct {
	Invalid    []indexInfo
	Duplicates []dupPair
	Bloated    []bloatedIndex
	Unused     []unusedIndex // largest first
}

func checkIndex(ctx context.Context, tx pgx.Tx) (result, error) {
	var res indexResult
	indexes, err := listIndexes(ctx, tx)
	if err != nil {
		return nil, fmt.Errorf("list indexes: %w", err)
	}
	for _, ix := range indexes {
		if !ix.Valid {
			res.Invalid = append(res.Invalid, ix)
		}
	}
	res.Duplicates = duplicateIndexes(indexes)

	rows, err := tx.Query(ctx, indexBloatSQL, bloatMinBytes)
	if err != nil {
		return nil, fmt.Errorf("index bloat: %w", err)
	}
	res.Bloated, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (b bloatedIndex, err error) {
		err = r.Scan(&b.Schema, &b.Table, &b.Name, &b.Wasted, &b.Size)
		return
	})
	if err != nil {
		return nil, fmt.Errorf("index bloat: %w", err)
	}

	rows, err = tx.Query(ctx, unusedIndexesSQL, unusedMaxScans)
	if err != nil {
		return nil, fmt.Errorf("unused indexes: %w", err)
	}
	res.Unused, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (u unusedIndex, err error) {
		err = r.Scan(&u.Schema, &u.Name, &u.Size, &u.Scans, &u.Table, &u.Root)
		return
	})
	if err != nil {
		return nil, fmt.Errorf("unused indexes: %w", err)
	}
	return res, nil
}

func listIndexes(ctx context.Context, tx pgx.Tx) ([]indexInfo, error) {
	rows, err := tx.Query(ctx, indexesSQL)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, func(r pgx.CollectableRow) (ix indexInfo, err error) {
		err = r.Scan(&ix.Schema, &ix.Table, &ix.Name, &ix.Using, &ix.Columns, &ix.KeyOpts, &ix.Unique, &ix.Primary, &ix.Valid, &ix.Exprs, &ix.Pred, &ix.Root)
		return
	})
}

// duplicateIndexes applies crystaldba's algorithm: a valid, non-unique, non-primary index is unneeded when another
// valid index of the same table has the same access method, expressions and predicate and starts with the same
// columns, operator classes, collations and sort options. When both have the same columns, the one with the
// greater name is the unneeded one, unless the other is unique or primary.
func duplicateIndexes(indexes []indexInfo) []dupPair {
	type tableKey struct{ schema, table string }
	byTable := map[tableKey][]indexInfo{}
	for _, ix := range indexes {
		k := tableKey{ix.Schema, ix.Table}
		byTable[k] = append(byTable[k], ix)
	}
	var out []dupPair
	for _, ix := range indexes {
		if !ix.Valid || ix.Primary || ix.Unique {
			continue
		}
		for _, cov := range byTable[tableKey{ix.Schema, ix.Table}] {
			if !cov.Valid || cov.Name == ix.Name || cov.Using != ix.Using || cov.Exprs != ix.Exprs || cov.Pred != ix.Pred {
				continue
			}
			if len(cov.Columns) < len(ix.Columns) || !slices.Equal(cov.Columns[:len(ix.Columns)], ix.Columns) ||
				len(cov.KeyOpts) < len(ix.KeyOpts) || !slices.Equal(cov.KeyOpts[:len(ix.KeyOpts)], ix.KeyOpts) {
				continue
			}
			if len(cov.Columns) != len(ix.Columns) || ix.Name > cov.Name || cov.Primary || cov.Unique {
				out = append(out, dupPair{ix, cov})
				break
			}
		}
	}
	slices.SortStableFunc(out, func(a, b dupPair) int {
		return strings.Compare(a.Index.Schema+"\x00"+a.Index.Table+"\x00"+strings.Join(a.Index.Columns, "\x00")+"\x00"+a.Index.Name,
			b.Index.Schema+"\x00"+b.Index.Table+"\x00"+strings.Join(b.Index.Columns, "\x00")+"\x00"+b.Index.Name)
	})
	return out
}
