package health

import (
	"context"
	_ "embed"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

//go:embed wraparound.sql
var wraparoundSQL string

//go:embed dead_rows.sql
var deadRowsSQL string

type wraparoundTable struct {
	Schema, Table string
	Left          int64
}

type deadRowsTable struct {
	Schema, Table string
	Dead, Live    int64
	LastVacuum    *time.Time // nil when never vacuumed
}

// vacuumResult is what the vacuum check found.
type vacuumResult struct {
	Wraparound []wraparoundTable // fewest transactions left first
	Dead       []deadRowsTable   // most dead rows first
}

func checkVacuum(ctx context.Context, tx pgx.Tx) (result, error) {
	var res vacuumResult
	rows, err := tx.Query(ctx, wraparoundSQL, xidMax, xidThreshold)
	if err != nil {
		return nil, fmt.Errorf("wraparound: %w", err)
	}
	res.Wraparound, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (w wraparoundTable, err error) {
		err = r.Scan(&w.Schema, &w.Table, &w.Left)
		return
	})
	if err != nil {
		return nil, fmt.Errorf("wraparound: %w", err)
	}

	rows, err = tx.Query(ctx, deadRowsSQL, deadMinRows, deadMinRatio)
	if err != nil {
		return nil, fmt.Errorf("dead rows: %w", err)
	}
	res.Dead, err = pgx.CollectRows(rows, func(r pgx.CollectableRow) (d deadRowsTable, err error) {
		err = r.Scan(&d.Schema, &d.Table, &d.Dead, &d.Live, &d.LastVacuum)
		return
	})
	if err != nil {
		return nil, fmt.Errorf("dead rows: %w", err)
	}
	return res, nil
}
