package stats

import "github.com/a-lafon/postgres-dba-mcp/internal/server"

// Register adds the tools of group stats to r.
func Register(r *server.Registry) {
	r.Add(topQueriesTool(r.Config(), r.DB()))
}
