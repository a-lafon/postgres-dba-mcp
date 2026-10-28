package safesql

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

//go:embed gate.sql
var gateSQL string

// FuncRef is a function call that the name rules left undecided.
type FuncRef struct {
	// Schema is the qualifier as written, empty when the call is unqualified.
	Schema string
	// Name is the function name as written.
	Name string
	// Full is every name part joined by '.', as the client wrote them.
	Full string
}

// Gate refuses the statement when one of its undecided calls reaches a
// VOLATILE function that PostgreSQL does not ship and that no review cleared.
// A READ ONLY transaction refuses SQL writes, but not what an extension does
// in C, in another session or outside the database, so a function that may
// have side effects and that nobody classified fails closed.
//
// The query runs in tx, so the names resolve against the same search_path as
// the statement itself.
func Gate(ctx context.Context, tx pgx.Tx, refs []FuncRef) error {
	if len(refs) == 0 {
		return nil
	}
	schemas, names := make([]string, len(refs)), make([]string, len(refs))
	for i, r := range refs {
		schemas[i], names[i] = r.Schema, r.Name
	}
	rows, err := tx.Query(ctx, gateSQL, schemas, names)
	if err != nil {
		return unclassified(err)
	}
	defer rows.Close()
	refused := map[int]bool{}
	for rows.Next() {
		var ord int
		var extension string
		if err := rows.Scan(&ord, &extension); err != nil {
			return unclassified(err)
		}
		if ord < 1 || ord > len(refs) {
			continue
		}
		if !reviewedSafe(extension, refs[ord-1].Name) {
			refused[ord] = true
		}
	}
	if err := rows.Err(); err != nil {
		return unclassified(err)
	}
	if len(refused) == 0 {
		return nil
	}
	var listed []string
	var first FuncRef
	for i, r := range refs {
		if !refused[i+1] {
			continue
		}
		if len(listed) == 0 {
			first = r
		}
		listed = append(listed, r.Full+"()")
	}
	return fmt.Errorf(
		"restricted mode: %s is VOLATILE and was not reviewed, so a read-only transaction cannot undo what it may do; "+
			"declare it STABLE if it only reads, or pass --allow-function %s",
		strings.Join(listed, ", "), first.Full)
}

// unclassified reports that the catalog query itself failed: the gate cannot
// tell what the call reaches, so the statement does not run.
func unclassified(err error) error {
	return fmt.Errorf("restricted mode: cannot classify the functions of the query: %w", err)
}
