package health

import (
	"context"
	_ "embed"
	"fmt"

	"github.com/jackc/pgx/v5"
)

//go:embed connection.sql
var connectionSQL string

// connectionResult holds the cluster-wide session counts.
type connectionResult struct{ Total, IdleInTx int64 }

func checkConnection(ctx context.Context, tx pgx.Tx) (result, error) {
	var res connectionResult
	if err := tx.QueryRow(ctx, connectionSQL).Scan(&res.Total, &res.IdleInTx); err != nil {
		return nil, fmt.Errorf("count connections: %w", err)
	}
	return res, nil
}
