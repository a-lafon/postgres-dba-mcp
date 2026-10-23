package health

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed constraints.sql
var constraintsSQL string

type invalidConstraint struct {
	Schema, Table, Name string
	RefSchema, RefTable string // empty unless a foreign key
}

// constraintResult lists the constraints that are NOT VALID.
type constraintResult struct{ Invalid []invalidConstraint }

func checkConstraint(ctx context.Context, tx pgx.Tx) (result, error) {
	rows, err := tx.Query(ctx, constraintsSQL)
	if err != nil {
		return nil, fmt.Errorf("list constraints: %w", err)
	}
	list, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (c invalidConstraint, err error) {
		var rs, rt *string
		if err = r.Scan(&c.Schema, &c.Table, &c.Name, &rs, &rt); err != nil {
			return
		}
		if rs != nil && rt != nil {
			c.RefSchema, c.RefTable = *rs, *rt
		}
		return
	})
	if err != nil {
		return nil, fmt.Errorf("list constraints: %w", err)
	}
	return constraintResult{Invalid: list}, nil
}
