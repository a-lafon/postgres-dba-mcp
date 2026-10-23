package hypo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Size returns the estimated size in bytes of the hypothetical index idx.
func Size(ctx context.Context, tx pgx.Tx, idx Index) (size int64, err error) {
	d, err := ddl(idx)
	if err != nil {
		return 0, err
	}
	err = session(ctx, tx, true, func(ctx context.Context, tx pgx.Tx) error {
		if err := tx.QueryRow(ctx, "SELECT hypopg_relation_size(indexrelid) FROM hypopg_create_index($1)", d).Scan(&size); err != nil {
			return fmt.Errorf("estimate index size: %w", err)
		}
		return nil
	})
	return size, err
}
