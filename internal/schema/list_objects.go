package schema

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

var (
	//go:embed list_objects_tables.sql
	listTablesSQL string
	//go:embed list_objects_views.sql
	listViewsSQL string
	//go:embed list_objects_sequences.sql
	listSequencesSQL string
	//go:embed list_objects_extensions.sql
	listExtensionsSQL string
)

// tableRow is one table of list_objects. Rows is nil when the table was never analyzed.
type tableRow struct {
	Name, Kind string
	Rows       *int64
	Size       int64
}

type viewRow struct{ Name, Kind string }

type sequenceRow struct {
	Name, DataType     string
	LastValue          *int64
	MinValue, MaxValue int64
	Increment          int64
}

type extensionRow struct {
	Name, Version, Schema string
	Relocatable           bool
}

// objectList is the computed result of list_objects: only the slice of its Type is filled.
type objectList struct {
	Type       string
	Tables     []tableRow
	Views      []viewRow
	Sequences  []sequenceRow
	Extensions []extensionRow
	More       bool
	Elapsed    time.Duration
}

func listObjectsTool(cfg *config.Config, run readOnlyRunner) server.Tool {
	return server.Tool{
		Group: "schema",
		Title: "List objects",
		Def: &mcp.Tool{
			Name:        "list_objects",
			Description: "List objects in a schema",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"schema_name":{"type":"string","description":"Schema name"},"object_type":` +
				objectTypeSchema + `},"required":["schema_name"]}`),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				SchemaName string `json:"schema_name"`
				ObjectType string `json:"object_type"`
			}
			if err := server.Decode(args, &in); err != nil {
				return "", err
			}
			if in.ObjectType == "" {
				in.ObjectType = "table"
			}
			if err := checkObjectType(in.ObjectType); err != nil {
				return "", err
			}
			if in.SchemaName == "" && in.ObjectType != "extension" {
				return "", inputErrorf("schema_name is required")
			}
			res, err := listObjects(ctx, run, in.SchemaName, in.ObjectType, cfg.MaxRows)
			if err != nil {
				return "", err
			}
			return renderObjects(res, cfg)
		},
	}
}

// listObjects fetches maxRows+1 rows to learn whether more exist. objectType is already validated.
func listObjects(ctx context.Context, run readOnlyRunner, schemaName, objectType string, maxRows int) (objectList, error) {
	res := objectList{Type: objectType}
	start := time.Now()
	err := run.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if objectType != "extension" {
			if err := requireSchema(ctx, tx, schemaName); err != nil {
				return err
			}
		}
		var err error
		switch objectType {
		case "table":
			res.Tables, res.More, err = fetch[tableRow](ctx, tx, maxRows, listTablesSQL, schemaName)
		case "view":
			res.Views, res.More, err = fetch[viewRow](ctx, tx, maxRows, listViewsSQL, schemaName)
		case "sequence":
			res.Sequences, res.More, err = fetch[sequenceRow](ctx, tx, maxRows, listSequencesSQL, schemaName)
		case "extension":
			res.Extensions, res.More, err = fetch[extensionRow](ctx, tx, maxRows, listExtensionsSQL)
		}
		return err
	})
	if err != nil {
		var ie *inputError
		if errors.As(err, &ie) {
			return objectList{}, err
		}
		return objectList{}, fmt.Errorf("list %ss: %w", objectType, err)
	}
	res.Elapsed = time.Since(start)
	return res, nil
}

// fetch runs sql with maxRows+1 appended to args as its row limit, and reports whether the extra row came back.
func fetch[T any](ctx context.Context, tx pgx.Tx, maxRows int, sql string, args ...any) (rows []T, more bool, err error) {
	rows, err = queryRows[T](ctx, tx, sql, append(args, maxRows+1)...)
	if len(rows) > maxRows {
		rows, more = rows[:maxRows], true
	}
	return rows, more, err
}
