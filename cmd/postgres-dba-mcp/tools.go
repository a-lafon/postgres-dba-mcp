package main

import (
	"github.com/a-lafon/postgres-dba-mcp/internal/explain"
	"github.com/a-lafon/postgres-dba-mcp/internal/health"
	"github.com/a-lafon/postgres-dba-mcp/internal/indexadv"
	"github.com/a-lafon/postgres-dba-mcp/internal/locks"
	"github.com/a-lafon/postgres-dba-mcp/internal/query"
	"github.com/a-lafon/postgres-dba-mcp/internal/schema"
	"github.com/a-lafon/postgres-dba-mcp/internal/server"
	"github.com/a-lafon/postgres-dba-mcp/internal/stats"
)

// registerTools is the shared edit point of the tool packages: one Register line per package.
func registerTools(r *server.Registry) {
	query.Register(r)
	schema.Register(r)
	stats.Register(r)
	explain.Register(r)
	health.Register(r)
	indexadv.Register(r)
	locks.Register(r)
}
