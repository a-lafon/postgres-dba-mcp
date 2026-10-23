// Package schema holds the schema inspection tools. Register is the shared edit point that later changes extend.
package schema

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

//go:embed list_schemas.sql
var listSchemasSQL string

// readOnlyRunner is the only database capability the schema tools need: they never write, whatever the access mode.
type readOnlyRunner interface {
	ReadOnly(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}

// schemaRow is one line of the list_schemas result.
type schemaRow struct {
	Name, Owner, Type string
	Tables            *int64 // nil for system schemas
}

// schemaList is the computed result of list_schemas.
type schemaList struct {
	Rows    []schemaRow
	More    bool
	Elapsed time.Duration
}

func listSchemasTool(cfg *config.Config, run readOnlyRunner) server.Tool {
	return server.Tool{
		Group: "schema",
		Title: "List schemas",
		Def: &mcp.Tool{
			Name:        "list_schemas",
			Description: "List all schemas in the database",
			InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct{}
			if err := server.Decode(args, &in); err != nil {
				return "", err
			}
			res, err := listSchemas(ctx, run, cfg.MaxRows)
			if err != nil {
				return "", err
			}
			return renderSchemas(res, cfg)
		},
	}
}

// listSchemas fetches maxRows+1 rows to learn whether more exist.
func listSchemas(ctx context.Context, run readOnlyRunner, maxRows int) (schemaList, error) {
	var res schemaList
	start := time.Now()
	err := run.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		rows, err := tx.Query(ctx, listSchemasSQL, maxRows+1)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r schemaRow
			if err := rows.Scan(&r.Name, &r.Owner, &r.Type, &r.Tables); err != nil {
				return err
			}
			res.Rows = append(res.Rows, r)
		}
		return rows.Err()
	})
	if err != nil {
		return schemaList{}, fmt.Errorf("list schemas: %w", err)
	}
	if len(res.Rows) > maxRows {
		res.Rows, res.More = res.Rows[:maxRows], true
	}
	res.Elapsed = time.Since(start)
	return res, nil
}
