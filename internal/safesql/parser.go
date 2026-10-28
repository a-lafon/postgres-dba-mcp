// Package safesql decides whether one SQL statement may run in restricted
// mode. It parses the statement with libpg_query and checks the statement
// type and every function call against a Policy, without a database. Gate is
// the one exception: the calls no name rule settles are resolved against
// pg_proc, inside the transaction that is about to run the statement.
package safesql

import (
	"encoding/json"
	"fmt"
	"reflect"
	"runtime/debug"
	"sync"

	pgquery "github.com/wasilibs/go-pgquery"
)

var (
	warmOnce  sync.Once
	warmReady = make(chan struct{})
)

// Warm starts the parser initialization (the WASM module compilation) in the
// background and returns at once. It is idempotent and safe for concurrent
// use; Check and Classify wait for the initialization instead of starting
// another one.
func Warm() {
	warmOnce.Do(func() {
		go func() {
			defer close(warmReady)
			_, _ = pgquery.ParseToJSON("SELECT 1")
			// Compiling the WASM module peaks at ~250 MB of Go heap that is garbage
			// afterwards; hand it back to the OS once.
			debug.FreeOSMemory()
		}()
	})
}

// parseError is a syntax error reported by the parser. Pos is the 1-based
// character position, or 0 when the parser reports none.
type parseError struct {
	Msg string
	Pos int
}

func (e *parseError) Error() string {
	if e.Pos == 0 {
		return "syntax error: " + e.Msg
	}
	return fmt.Sprintf("syntax error: %s (position %d)", e.Msg, e.Pos)
}

// parse returns the libpg_query tree of sql, decoded as map[string]any and
// []any. Parser failures come back as *parseError.
func parse(sql string) (any, error) {
	Warm()
	<-warmReady
	raw, err := pgquery.ParseToJSON(sql)
	if err != nil {
		return nil, &parseError{Msg: err.Error(), Pos: cursorPos(err)}
	}
	var tree any
	if err := json.Unmarshal([]byte(raw), &tree); err != nil {
		return nil, fmt.Errorf("decode parse tree: %w", err)
	}
	return tree, nil
}

// scanToken is one lexical token of a SQL text. Start and End are byte offsets; Kind is the lexer token name
// (IDENT, PARAM, ICONST, SCONST, ASCII_40 for "(", C_COMMENT, ...); Keyword is the keyword class of the token
// (NO_KEYWORD, UNRESERVED_KEYWORD, COL_NAME_KEYWORD, TYPE_FUNC_NAME_KEYWORD or RESERVED_KEYWORD).
type scanToken struct {
	Start, End    int
	Kind, Keyword string
}

// scan runs the lexer of the parser on sql. Strings, quoted identifiers, dollar quotes and comments are single
// tokens, so no caller has to re-implement the lexical rules.
func scan(sql string) ([]scanToken, error) {
	Warm()
	<-warmReady
	res, err := pgquery.Scan(sql)
	if err != nil {
		return nil, fmt.Errorf("scan: %w", err)
	}
	toks := make([]scanToken, len(res.Tokens))
	for i, t := range res.Tokens {
		toks[i] = scanToken{int(t.Start), int(t.End), t.Token.String(), t.KeywordKind.String()}
	}
	return toks, nil
}

// cursorPos reads the Cursorpos field of the parser's error. Its type lives
// in an internal package of the parser, so reflection is the only way to
// reach it without importing it.
func cursorPos(err error) int {
	v := reflect.ValueOf(err)
	if v.Kind() == reflect.Pointer && !v.IsNil() {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return 0
	}
	if f := v.FieldByName("Cursorpos"); f.IsValid() && f.CanInt() {
		return int(f.Int())
	}
	return 0
}
