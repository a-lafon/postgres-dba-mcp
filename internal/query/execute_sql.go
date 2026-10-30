// Package query holds the execute_sql tool: one SQL statement per call, checked in restricted mode,
// read through a server-side cursor so that results stay bounded.
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/db"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
	"github.com/a-lafon/postgres-dba-mcp/internal/safesql"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

// Register adds execute_sql to r. In restricted mode it also logs the warnings of the function policy.
func Register(r *server.Registry) {
	cfg := r.Config()
	safesql.Warm()
	policy, err := safesql.NewPolicy(cfg.AllowFunctions, cfg.DenyFunctions)
	if err != nil {
		// config.Parse validates the patterns, so this is a programming error.
		panic(fmt.Sprintf("query: build function policy: %v", err))
	}
	if cfg.AccessMode == config.Restricted {
		for _, w := range policy.Warnings() {
			r.Warn(w)
		}
	}
	r.Add(executeSQLTool(cfg, r.DB(), policy))
}

func executeSQLTool(cfg *config.Config, run *db.DB, policy *safesql.Policy) server.Tool {
	restricted := cfg.AccessMode == config.Restricted
	desc := "Execute any SQL query"
	const cells = ". Long cells are shortened; pass max_cell to read more."
	if restricted {
		desc = "Execute a read-only SQL query"
	}
	desc += cells
	desc += fmt.Sprintf(" The result is also capped at %d bytes, whatever max_rows says; the footer says so when it is cut.", cfg.MaxBytes)
	schema := fmt.Sprintf(`{"type":"object","properties":{`+
		`"sql":{"type":"string","description":"SQL to run"},`+
		`"max_rows":{"type":"integer","minimum":1,"description":"Maximum rows to return (default %d, at most %d)"},`+
		`"max_cell":{"type":"integer","minimum":1,"description":"Maximum characters per cell before it is shortened (default %d, at most %d)"}},`+
		`"required":["sql"]}`, cfg.MaxRows, cfg.MaxRowsLimit, cfg.MaxCell, cfg.MaxCellLimit)
	x := &executor{cfg: cfg, run: run, policy: policy}
	return server.Tool{
		Group:  "query",
		Title:  "Execute SQL",
		Writes: !restricted,
		Def: &mcp.Tool{
			Name:        "execute_sql",
			Description: desc,
			InputSchema: json.RawMessage(schema),
		},
		Handler: x.handle,
	}
}

type executor struct {
	cfg    *config.Config
	run    *db.DB
	policy *safesql.Policy
}

func (x *executor) handle(ctx context.Context, args json.RawMessage) (string, error) {
	var in struct {
		SQL     string `json:"sql"`
		MaxRows *int   `json:"max_rows"`
		MaxCell *int   `json:"max_cell"`
	}
	if err := server.Decode(args, &in); err != nil {
		return "", err
	}
	if strings.TrimSpace(in.SQL) == "" {
		return "", errors.New("invalid arguments: sql is required")
	}
	maxRows := x.cfg.MaxRows
	if in.MaxRows != nil {
		maxRows = *in.MaxRows
	}
	switch {
	case maxRows < 1:
		return "", errors.New("invalid arguments: max_rows must be at least 1")
	case maxRows > x.cfg.MaxRowsLimit:
		return "", fmt.Errorf("invalid arguments: max_rows %d exceeds the server limit %d", maxRows, x.cfg.MaxRowsLimit)
	}
	maxCell := x.cfg.MaxCell
	if in.MaxCell != nil {
		maxCell = *in.MaxCell
	}
	switch {
	case maxCell < 1:
		return "", errors.New("invalid arguments: max_cell must be at least 1")
	case maxCell > x.cfg.MaxCellLimit:
		return "", fmt.Errorf("invalid arguments: max_cell %d exceeds the server limit %d", maxCell, x.cfg.MaxCellLimit)
	}
	// Nothing ever binds values here, so a $n reaches Postgres as a bind error naming a prepared statement
	// the caller never wrote. Checked with the other argument bounds, before the access mode: it is the
	// contract of the tool, and a caller who fixes it still meets the refusal the mode owes them.
	if safesql.HasParams(in.SQL) {
		return "", errors.New("execute_sql takes no parameters: inline the values in the SQL (explain_query accepts $n)")
	}
	res, err := x.execute(ctx, in.SQL, limits{rows: maxRows, cell: maxCell, bytes: x.cfg.MaxBytes})
	if err != nil {
		return "", format.WithSQL(x.timeoutHint(err), in.SQL)
	}
	return render(res, x.cfg, maxRows, maxCell)
}

// timeoutHint adds the limit and what to do to a statement timeout, which says neither. It sets the Postgres
// HINT line, which format.Error already renders. 57014 is also a cancel by a DBA or a standby conflict, so the
// message must say statement timeout too. It returns a copy: the PgError belongs to the chain pgx built.
func (x *executor) timeoutHint(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "57014" && strings.Contains(pe.Message, "statement timeout") && pe.Hint == "" {
		c := *pe
		c.Hint = fmt.Sprintf("the statement timeout is %s; bound the query with a WHERE on an indexed column or a LIMIT, or look at its plan first; a cold cache can cause this, so a retry may succeed", x.cfg.StatementTimeout)
		return &c
	}
	return err
}

// unrestrictedHint names the flag when unrestricted mode would have run sql, which is the whole
// difference for someone whose previous server ran in that mode by default. Parsing to exactly one
// statement is the condition, since that is all unrestricted mode asks of a statement. A denied
// function is the exception: those are denied because a read-only transaction does not stop them, so
// naming the flag that drops that transaction would advise undoing the protection just refused.
func unrestrictedHint(sql string, err error) error {
	var denied *safesql.DeniedFuncError
	if errors.As(err, &denied) {
		return err
	}
	if count, _, cErr := safesql.Classify(sql); cErr != nil || count != 1 {
		return err
	}
	return fmt.Errorf("%w; start the server with --access-mode=unrestricted to run it", err)
}

// execute validates sql for the access mode, then runs it in the matching transaction runner.
func (x *executor) execute(ctx context.Context, sql string, lim limits) (result, error) {
	if x.cfg.AccessMode == config.Restricted {
		st, err := safesql.Check(sql, x.policy)
		if err != nil {
			return result{}, unrestrictedHint(sql, err)
		}
		run := x.run.ReadOnly
		if st.Executes() {
			run = withVolatilityGate(run, st.Unresolved)
		}
		// Read only and always rolled back: bounding the work the server does is the point.
		return runStatement(ctx, run, sql, st.Kind == safesql.Select, false /* drain */, lim)
	}
	count, kind, err := safesql.Classify(sql)
	switch {
	case err != nil:
		return result{}, err
	case count == 0:
		return result{}, errors.New("empty query")
	case count > 1:
		return result{}, errors.New("only one statement per call is allowed; send each statement in its own call")
	}
	// This transaction commits, so a statement whose rows have side effects must run to its end, truncated
	// output or not. Without a function call there is nothing to leave half done, and draining would make
	// every truncated SELECT pay for the rows nobody reads.
	// A call this text does not name is caught by fetchCursor, which asks the server what the statement has
	// already written.
	// shortcut: a hidden call that writes only past the first batch is named nowhere and has written nothing
	// to find, so such a statement is not drained; upgrade by resolving views, policies, operators and casts
	// in the catalog, the way the restricted-mode gate would have to.
	cursor := kind == safesql.Select
	return runStatement(ctx, x.run.ReadWrite, sql, cursor, cursor && safesql.HasFuncCall(sql), lim)
}
