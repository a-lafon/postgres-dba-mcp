package health

import (
	"context"
	_ "embed"
	"fmt"
	"math"

	"github.com/jackc/pgx/v5"
)

//go:embed sequences.sql
var sequencesSQL string

type sequenceUse struct {
	Schema, Name, DataType            string
	OwnerSchema, OwnerTable, OwnerCol string  // empty when the sequence has no owner
	Used                              float64 // 0-1 fraction of the range
	Left                              int64
}

// sequenceResult is what the sequence check found.
type sequenceResult struct {
	Readable int           // sequences the role can read
	Over     []sequenceUse // above seqThreshold, fewest values left first
}

func checkSequence(ctx context.Context, tx pgx.Tx) (result, error) {
	rows, err := tx.Query(ctx, sequencesSQL)
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	all, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (s sequenceUse, err error) {
		var left float64
		err = r.Scan(&s.Schema, &s.Name, &s.DataType, &s.OwnerSchema, &s.OwnerTable, &s.OwnerCol, &s.Used, &left)
		s.Left = int64(math.Min(math.Max(left, 0), math.MaxInt64))
		return
	})
	if err != nil {
		return nil, fmt.Errorf("list sequences: %w", err)
	}
	res := sequenceResult{Readable: len(all)}
	for _, s := range all {
		if s.Used > seqThreshold {
			res.Over = append(res.Over, s)
		}
	}
	return res, nil
}
