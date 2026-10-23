package schema

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

var (
	//go:embed details_relation.sql
	detailsRelationSQL string
	//go:embed details_columns.sql
	detailsColumnsSQL string
	//go:embed details_constraints.sql
	detailsConstraintsSQL string
	//go:embed details_indexes.sql
	detailsIndexesSQL string
	//go:embed details_referenced_by.sql
	detailsReferencedBySQL string
	//go:embed details_triggers.sql
	detailsTriggersSQL string
	//go:embed details_partitions.sql
	detailsPartitionsSQL string
	//go:embed details_sequence.sql
	detailsSequenceSQL string
	//go:embed details_extension.sql
	detailsExtensionSQL string
)

// relation is the pg_class row of the object, with its sizes and, for views, its definition.
type relation struct {
	OID                      uint32
	Kind                     string // relkind
	Rows                     *int64 // nil when never analyzed
	TableSize, IndexSize     int64
	Comment, PartitionKey    *string
	ParentSchema, ParentName *string
	Bound, Definition        *string
}

type column struct {
	Name, Type          string
	NotNull             bool
	Identity, Generated string // attidentity and attgenerated; empty when not applicable
	Default, Comment    *string
}

type constraint struct {
	Name, Type, Def     string // Type is the contype: p, f or c
	Columns             []string
	RefSchema, RefTable *string
	RefColumns          []string
	OnDelete, OnUpdate  string
}

type index struct {
	Name, Def              string
	Primary, Unique, Valid bool
}

type reference struct {
	Schema, Table string
	Columns       []string
}

type trigger struct {
	Name string
	Type int32 // pg_trigger.tgtype bit mask
}

type partition struct{ Schema, Name, Bound string }

type sequenceInfo struct {
	DataType                             string
	Start, Increment, Min, Max           int64
	Cycle                                bool
	LastValue                            *int64
	OwnerSchema, OwnerTable, OwnerColumn *string
}

type extensionInfo struct {
	Version, Schema string
	Relocatable     bool
	Comment         *string
}

// objectDetails is the computed result of get_object_details. Type selects which parts are filled.
type objectDetails struct {
	Type, Schema, Name string
	Relation           relation
	Columns            []column
	Constraints        []constraint
	Indexes            []index
	ReferencedBy       []reference
	Triggers           []trigger
	Partitions         []partition
	Sequence           sequenceInfo
	Extension          extensionInfo
}

func objectDetailsTool(cfg *config.Config, run readOnlyRunner) server.Tool {
	return server.Tool{
		Group: "schema",
		Title: "Object details",
		Def: &mcp.Tool{
			Name:        "get_object_details",
			Description: "Show detailed information about a database object",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"schema_name":{"type":"string","description":"Schema name"},` +
				`"object_name":{"type":"string","description":"Object name"},"object_type":` + objectTypeSchema +
				`},"required":["schema_name","object_name"]}`),
		},
		Handler: func(ctx context.Context, args json.RawMessage) (string, error) {
			var in struct {
				SchemaName string `json:"schema_name"`
				ObjectName string `json:"object_name"`
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
			if in.ObjectName == "" || (in.SchemaName == "" && in.ObjectType != "extension") {
				return "", inputErrorf("schema_name and object_name are required")
			}
			d, err := objectDetail(ctx, run, in.SchemaName, in.ObjectName, in.ObjectType)
			if err != nil {
				return "", err
			}
			return renderDetails(d, cfg), nil
		},
	}
}

// relkindNames names the relkinds in "is a <kind>, not a <object_type>" errors.
var relkindNames = map[string]string{
	"r": "table", "p": "partitioned table", "v": "view", "m": "materialized view",
	"S": "sequence", "i": "index", "I": "index", "f": "foreign table", "c": "composite type", "t": "TOAST table",
}

// relkindsOf lists the relkinds each object_type accepts.
var relkindsOf = map[string]string{"table": "rp", "view": "vm", "sequence": "S"}

// objectDetail resolves the object by exact name and reads everything its details need. objectType is already validated.
func objectDetail(ctx context.Context, run readOnlyRunner, schemaName, name, objectType string) (objectDetails, error) {
	d := objectDetails{Type: objectType, Schema: schemaName, Name: name}
	err := run.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		if objectType == "extension" {
			rows, err := queryRows[extensionInfo](ctx, tx, detailsExtensionSQL, name)
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				return inputErrorf("extension %q not installed", name)
			}
			d.Extension = rows[0]
			return nil
		}
		if err := requireSchema(ctx, tx, schemaName); err != nil {
			return err
		}
		rels, err := queryRows[relation](ctx, tx, detailsRelationSQL, schemaName, name)
		if err != nil {
			return err
		}
		if len(rels) == 0 {
			return inputErrorf("%s \"%s.%s\" not found", objectType, schemaName, name)
		}
		d.Relation = rels[0]
		if !strings.Contains(relkindsOf[objectType], d.Relation.Kind) {
			kind, ok := relkindNames[d.Relation.Kind]
			if !ok {
				kind = "relation of kind " + d.Relation.Kind
			}
			return inputErrorf("\"%s.%s\" is %s, not %s", schemaName, name, article(kind), article(objectType))
		}
		return readParts(ctx, tx, &d)
	})
	if err != nil {
		var ie *inputError
		if errors.As(err, &ie) {
			return objectDetails{}, err
		}
		return objectDetails{}, fmt.Errorf("object details: %w", err)
	}
	return d, nil
}

// readParts fills what depends on the object type once the relation is resolved.
func readParts(ctx context.Context, tx pgx.Tx, d *objectDetails) error {
	oid := d.Relation.OID
	var err error
	if d.Type == "sequence" {
		var seqs []sequenceInfo
		if seqs, err = queryRows[sequenceInfo](ctx, tx, detailsSequenceSQL, d.Schema, d.Name, oid); err != nil {
			return err
		}
		if len(seqs) == 0 {
			return inputErrorf("sequence \"%s.%s\" not found", d.Schema, d.Name)
		}
		d.Sequence = seqs[0]
		return nil
	}
	if d.Columns, err = queryRows[column](ctx, tx, detailsColumnsSQL, oid); err != nil {
		return err
	}
	if d.Type == "view" {
		return nil
	}
	if d.Constraints, err = queryRows[constraint](ctx, tx, detailsConstraintsSQL, oid); err != nil {
		return err
	}
	if d.Indexes, err = queryRows[index](ctx, tx, detailsIndexesSQL, oid); err != nil {
		return err
	}
	if d.ReferencedBy, err = queryRows[reference](ctx, tx, detailsReferencedBySQL, oid); err != nil {
		return err
	}
	if d.Triggers, err = queryRows[trigger](ctx, tx, detailsTriggersSQL, oid); err != nil {
		return err
	}
	d.Partitions, err = queryRows[partition](ctx, tx, detailsPartitionsSQL, oid)
	return err
}
