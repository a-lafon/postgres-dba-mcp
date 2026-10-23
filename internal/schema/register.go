package schema

import "github.com/a-lafon/postgres-dba-mcp/internal/server"

// Register adds the tools of group schema to r. This is a shared edit point: add-schema-tools adds its tools here.
func Register(r *server.Registry) {
	r.Add(listSchemasTool(r.Config(), r.DB()))
	r.Add(listObjectsTool(r.Config(), r.DB()))
	r.Add(objectDetailsTool(r.Config(), r.DB()))
}
