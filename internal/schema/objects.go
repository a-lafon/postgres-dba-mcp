package schema

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// inputError is a problem with the arguments or a missing object. Its text reaches the client unchanged.
type inputError struct{ msg string }

func (e *inputError) Error() string { return e.msg }

func inputErrorf(f string, a ...any) error { return &inputError{fmt.Sprintf(f, a...)} }

// objectTypes are the accepted object_type values, in the order of the input schema enum.
var objectTypes = []string{"table", "view", "sequence", "extension"}

// objectTypeSchema is the input schema property shared by list_objects and get_object_details.
const objectTypeSchema = `{"type":"string","description":"Object type: 'table', 'view', 'sequence', or 'extension'","default":"table","enum":["table","view","sequence","extension"]}`

// checkObjectType rejects an object_type outside objectTypes.
func checkObjectType(t string) error {
	if slices.Contains(objectTypes, t) {
		return nil
	}
	return inputErrorf("unsupported object_type %q (valid: table, view, sequence, extension)", t)
}

//go:embed schema_exists.sql
var schemaExistsSQL string

// requireSchema fails with an inputError when the schema does not exist.
func requireSchema(ctx context.Context, tx pgx.Tx, name string) error {
	var one int
	err := tx.QueryRow(ctx, schemaExistsSQL, name).Scan(&one)
	if errors.Is(err, pgx.ErrNoRows) {
		return inputErrorf("schema %q not found", name)
	}
	return err
}

// queryRows runs sql and scans every row into a T by column position.
func queryRows[T any](ctx context.Context, tx pgx.Tx, sql string, args ...any) ([]T, error) {
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	return pgx.CollectRows(rows, pgx.RowToStructByPos[T])
}

// sequenceUsed returns the share of the sequence range already consumed, e.g. "0.0%". An ascending sequence
// counts from min toward max, a descending one (negative increment) from max toward min. It is false when
// last_value is NULL or the range is empty.
func sequenceUsed(last *int64, minValue, maxValue, increment int64) (string, bool) {
	if last == nil || maxValue <= minValue {
		return "", false
	}
	// float64 differences: int64 ones overflow on a range wider than 2^63.
	consumed := float64(*last) - float64(minValue)
	if increment < 0 {
		consumed = float64(maxValue) - float64(*last)
	}
	return format.Percent(consumed / (float64(maxValue) - float64(minValue))), true
}

// article prefixes a relkind name with "a" or "an".
func article(name string) string {
	if strings.ContainsRune("aeiou", rune(name[0])) {
		return "an " + name
	}
	return "a " + name
}
