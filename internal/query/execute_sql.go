// Package query holds the execute_sql tool: one SQL statement per call, checked in restricted mode,
// read through a server-side cursor so that results stay bounded.
package query

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

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
	res, err := x.execute(ctx, in.SQL, maxRows)
	if err != nil {
		return "", format.WithSQL(err, in.SQL)
	}
	return render(res, x.cfg, maxRows, maxCell)
}

// execute validates sql for the access mode, then runs it in the matching transaction runner.
func (x *executor) execute(ctx context.Context, sql string, maxRows int) (result, error) {
	if x.cfg.AccessMode == config.Restricted {
		st, err := safesql.Check(sql, x.policy)
		if err != nil {
			return result{}, err
		}
		return runStatement(ctx, x.run.ReadOnly, sql, st.Kind == safesql.Select, maxRows)
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
	return runStatement(ctx, x.run.ReadWrite, sql, kind == safesql.Select, maxRows)
}
