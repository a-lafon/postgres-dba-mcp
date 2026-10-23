package health

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed replication.sql
var replicationSQL string

type inactiveSlot struct {
	Name, Type, Database string // Database is empty for physical slots
}

// replicationResult is what the replication check found.
type replicationResult struct {
	Replica   bool
	Streaming bool     // replica only: the WAL receiver is streaming
	Lag       *float64 // replica only, seconds; nil when unknown
	Replicas  int64
	Slots     int64
	Inactive  []inactiveSlot
}

func checkReplication(ctx context.Context, tx pgx.Tx) (result, error) {
	rows, err := tx.Query(ctx, replicationSQL)
	if err != nil {
		return nil, fmt.Errorf("replication state: %w", err)
	}
	defer rows.Close()
	var res replicationResult
	for rows.Next() {
		var name, typ, database *string
		if err := rows.Scan(&res.Replica, &res.Replicas, &res.Slots, &res.Streaming, &res.Lag, &name, &typ, &database); err != nil {
			return nil, fmt.Errorf("replication state: %w", err)
		}
		if name != nil {
			s := inactiveSlot{Name: *name}
			if typ != nil {
				s.Type = *typ
			}
			if database != nil {
				s.Database = *database
			}
			res.Inactive = append(res.Inactive, s)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("replication state: %w", err)
	}
	return res, nil
}
