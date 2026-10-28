// Package health implements the analyze_db_health tool: seven independent checks (index, connection, vacuum,
// sequence, replication, buffer, constraint) whose findings are rendered as a compact "problems only" report.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/a-lafon/postgres-dba-mcp/internal/server"
)

// Thresholds, taken from crystaldba/postgres-mcp 0.3.0 except the dead rows ones. They are variables so that
// integration tests can lower them: the bloat and wraparound traps are too costly to build for real.
var (
	bloatMinBytes     int64 = 104857600
	unusedMaxScans    int64 = 50
	maxConnections    int64 = 500
	maxIdleInTx       int64 = 100
	xidMax            int64 = 2146483648
	xidThreshold      int64 = 10000000
	deadMinRows       int64 = 10000
	deadMinRatio            = 0.20
	seqThreshold            = 0.90
	cacheHitThreshold       = 0.95
)

const toolDescription = `Analyzes database health. Here are the available health checks:
- index - checks for invalid, duplicate, bloated and rarely used indexes
- connection - checks the number of connections and their utilization
- vacuum - checks transaction id wraparound and dead rows
- sequence - checks sequences at risk of exceeding their maximum value
- replication - checks replication health including lag and slots
- buffer - checks for buffer cache hit rates for indexes and tables
- constraint - checks for invalid constraints
- all - runs all checks
You can optionally specify a single health check or a comma-separated list of health checks. The default is 'all' checks.`

const inputSchema = `{"type":"object","properties":{"health_type":{"type":"string","default":"all",` +
	`"description":"Optional. Valid values are: all, buffer, connection, constraint, index, replication, sequence, vacuum."}}}`

// readOnlyRunner is the only database capability the checks need: they never write, whatever the access mode.
type readOnlyRunner interface {
	ReadOnly(ctx context.Context, fn func(ctx context.Context, tx pgx.Tx) error) error
}

// result is what one check found; render turns it into a report section.
type result interface {
	render() section
}

// check is one health check. The slice order of allChecks is the order of the report.
type check struct {
	name string
	run  func(ctx context.Context, tx pgx.Tx) (result, error)
}

var allChecks = []check{
	{"index", checkIndex},
	{"connection", checkConnection},
	{"vacuum", checkVacuum},
	{"sequence", checkSequence},
	{"replication", checkReplication},
	{"buffer", checkBuffer},
	{"constraint", checkConstraint},
}

const validTypes = "all, buffer, connection, constraint, index, replication, sequence, vacuum"

// parseTypes returns the checks selected by raw, in report order. Any invalid or empty token rejects the whole input.
func parseTypes(raw string) ([]check, error) {
	picked := map[string]bool{}
	for _, tok := range strings.Split(raw, ",") {
		tok = strings.ToLower(strings.TrimSpace(tok))
		ok := tok == "all"
		for _, c := range allChecks {
			ok = ok || tok == c.name
		}
		if !ok {
			return nil, fmt.Errorf(`invalid health_type "%s": valid values are %s`, raw, validTypes)
		}
		picked[tok] = true
	}
	var sel []check
	for _, c := range allChecks {
		if picked["all"] || picked[c.name] {
			sel = append(sel, c)
		}
	}
	return sel, nil
}

// runChecks runs the checks in one read-only transaction, each under its own savepoint, so that a failing check
// is reported in its own section and does not hide the others.
func runChecks(ctx context.Context, run readOnlyRunner, checks []check) ([]section, error) {
	sections := make([]section, 0, len(checks))
	err := run.ReadOnly(ctx, func(ctx context.Context, tx pgx.Tx) error {
		sections = sections[:0]
		for _, c := range checks {
			res, err := runOne(ctx, tx, c)
			if err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				sections = append(sections, failedSection(c.name, err))
				continue
			}
			s := res.render()
			s.name = c.name
			sections = append(sections, s)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("analyze db health: %w", err)
	}
	return sections, nil
}

// runOne runs c in a savepoint: released on success, rolled back on failure.
func runOne(ctx context.Context, tx pgx.Tx, c check) (result, error) {
	sp, err := tx.Begin(ctx)
	if err != nil {
		return nil, err
	}
	res, err := c.run(ctx, sp)
	if err != nil {
		_ = sp.Rollback(context.WithoutCancel(ctx))
		return nil, err
	}
	if err := sp.Commit(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// failedSection reports err, reduced to its first line, as the only item of the section. A check the server
// does not implement (a managed fork such as Aurora answers 0A000 feature_not_supported) is skipped instead:
// it is no finding of this database, and listing it with the problems makes a false alert.
func failedSection(name string, err error) section {
	msg, code := err.Error(), ""
	var pe *pgconn.PgError
	if errors.As(err, &pe) {
		msg, code = pe.Message, pe.Code
	}
	msg, _, _ = strings.Cut(msg, "\n")
	if code == "0A000" {
		if msg == "" {
			msg = code // an empty skip reason would render the section as a healthy one
		}
		return section{name: name, skip: msg}
	}
	return section{name: name, items: []item{{kind: "check failed", text: "check failed: " + msg}}}
}

type args struct {
	HealthType *string `json:"health_type"`
}

func tool(maxBytes int, run readOnlyRunner) server.Tool {
	return server.Tool{
		Group: "health",
		Title: "Database health",
		Def: &mcp.Tool{
			Name:        "analyze_db_health",
			Description: toolDescription,
			InputSchema: json.RawMessage(inputSchema),
		},
		Handler: func(ctx context.Context, raw json.RawMessage) (string, error) {
			var in args
			if err := server.Decode(raw, &in); err != nil {
				return "", err
			}
			types := "all"
			if in.HealthType != nil {
				types = *in.HealthType
			}
			checks, err := parseTypes(types)
			if err != nil {
				return "", err
			}
			sections, err := runChecks(ctx, run, checks)
			if err != nil {
				return "", err
			}
			return renderReport(sections, maxBytes), nil
		},
	}
}

// Register adds the tool analyze_db_health to r.
func Register(r *server.Registry) {
	r.Add(tool(r.Config().MaxBytes, r.DB()))
}
