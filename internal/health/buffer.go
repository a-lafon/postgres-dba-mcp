package health

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed buffer.sql
var bufferSQL string

// bufferResult holds the cache hit rates as 0-1 fractions (nil when nothing was read yet) and the tables that
// account for most heap reads.
type bufferResult struct {
	Tables, Indexes *float64
	Culprits        []culprit
}

// culprit is a table (or the root of a partition tree) with its share of all heap reads, as a 0-1 fraction.
type culprit struct {
	Table    string  `json:"table"`
	Share    float64 `json:"share"`
	SeqScans int64   `json:"seq_scans"`
}

func checkBuffer(ctx context.Context, tx pgx.Tx) (result, error) {
	var res bufferResult
	if err := tx.QueryRow(ctx, bufferSQL).Scan(&res.Tables, &res.Indexes, &res.Culprits); err != nil {
		return nil, fmt.Errorf("cache hit rates: %w", err)
	}
	return res, nil
}
