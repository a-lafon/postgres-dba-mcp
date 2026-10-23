package db

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
)

const auditTimeout = 30 * time.Second

// auditRoleSQL reads the attributes of the current role and its (transitive) memberships of interest.
const auditRoleSQL = `WITH RECURSIVE m(roleid) AS (
  SELECT oid FROM pg_roles WHERE rolname = current_user
  UNION
  SELECT am.roleid FROM pg_auth_members am JOIN m ON am.member = m.roleid
)
SELECT r.rolname, r.rolsuper, r.rolbypassrls,
  coalesce((SELECT array_agg(g.rolname::text) FROM m JOIN pg_roles g ON g.oid = m.roleid
            WHERE g.rolname IN ('pg_read_server_files', 'pg_write_server_files', 'pg_execute_server_program',
                                'pg_signal_backend', 'rds_superuser', 'azure_pg_admin')), '{}'),
  EXISTS (SELECT 1 FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
          WHERE c.relowner = r.oid AND c.relkind IN ('r', 'p')
            AND n.nspname NOT IN ('pg_catalog', 'information_schema'))
FROM pg_roles r WHERE r.rolname = current_user`

// auditMemberships maps a privileged membership to the text of its warning.
var auditMemberships = []struct{ role, text string }{
	{"pg_read_server_files", "can read server files (pg_read_server_files)"},
	{"pg_write_server_files", "can write server files (pg_write_server_files)"},
	{"pg_execute_server_program", "can run server programs (pg_execute_server_program)"},
	{"pg_signal_backend", "can cancel or terminate other sessions (pg_signal_backend)"},
	{"rds_superuser", "has cloud admin rights (rds_superuser)"},
	{"azure_pg_admin", "has cloud admin rights (azure_pg_admin)"},
}

// startAudit launches the role audit after a successful connection, unless it ran already, is running,
// or the access mode is unrestricted. A failed audit goes back to idle so the next connection runs it again.
func (d *DB) startAudit() {
	if d.cfg.AccessMode == config.Unrestricted {
		return
	}
	d.mu.Lock()
	if d.audit != auditIdle {
		d.mu.Unlock()
		return
	}
	d.audit = auditRunning
	d.mu.Unlock()
	go d.runAudit()
}

func (d *DB) runAudit() {
	ctx, cancel := context.WithTimeout(context.Background(), auditTimeout)
	defer cancel()
	var warnings []string
	err := d.run(ctx, true, false, func(ctx context.Context, tx pgx.Tx) error {
		warnings = warnings[:0]
		var name string
		var super, bypass, owns bool
		var roles []string
		if err := tx.QueryRow(ctx, auditRoleSQL).Scan(&name, &super, &bypass, &roles, &owns); err != nil {
			return err
		}
		prefix := "WARNING: role " + name + ": "
		if super {
			warnings = append(warnings, prefix+"is a superuser; use a dedicated read-only role")
		}
		if bypass {
			warnings = append(warnings, prefix+"bypasses row-level security")
		}
		for _, m := range auditMemberships {
			for _, r := range roles {
				if r == m.role {
					warnings = append(warnings, prefix+m.text)
				}
			}
		}
		if owns {
			warnings = append(warnings, prefix+"owns tables; prefer a role with SELECT only")
		}
		return nil
	})

	d.mu.Lock()
	defer d.mu.Unlock()
	if err != nil {
		d.audit = auditIdle
		return
	}
	for _, w := range warnings {
		logf("%s", w)
	}
	d.audit = auditComplete
	close(d.auditDone)
}
