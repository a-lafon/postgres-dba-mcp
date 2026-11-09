package servers

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// DBHubImage is bytebase/dbhub 1.6.0, pinned by digest.
const DBHubImage = "bytebase/dbhub@sha256:ccb6afc0bc1f05c143e15c3c927eb11e8103d49b06947900c33e337f2453f5e3"

// DBHub variants measured by the bench. 1.6.0 dropped --readonly and --max-rows: read-only mode and the row cap are
// per-tool fields of a dbhub.toml, and a source with no [[tools]] block gets the two default tools (execute_sql and
// search_objects) with writes allowed and no cap.
const (
	// DBHubRef is the reference configuration of the goal: readonly = true, max_rows = 1000, the 2 default tools.
	DBHubRef = "dbhub"
	// DBHubRows100 is DBHubRef with max_rows = 100.
	DBHubRows100 = "dbhub-rows100"
	// DBHub4Tools is DBHubRef plus the two built-in tools a [[tools]] block can add: explain_sql and health_check.
	DBHub4Tools = "dbhub-4tools"
	// DBHubDefault is the untuned default: --dsn alone, no dbhub.toml, so no read-only mode and no row cap.
	DBHubDefault = "dbhub-default"
)

func init() {
	Register(DBHubRef, func(t Target) Spec { return dbhub(t, 1000, false) })
	Register(DBHubRows100, func(t Target) Spec { return dbhub(t, 100, false) })
	Register(DBHub4Tools, func(t Target) Spec { return dbhub(t, 1000, true) })
	Register(DBHubDefault, dbhubDefault)
}

// dbhubTools maps the canonical bench tools DBHub has an equivalent of. search_objects is left out on purpose: it
// covers list_schemas, list_objects and get_object_details, but under its own argument names (object_type, pattern,
// schema, table, detail_level), which the harness cannot send. health_check is left out too: it reports connection
// pool and buffer cache metrics, not the trap detection analyze_db_health is graded on.
func dbhubTools(explain bool) map[string]string {
	m := map[string]string{"execute_sql": "execute_sql"}
	if explain {
		m["explain_query"] = "explain_sql"
	}
	return m
}

// dbhubTOML is the configuration of one DBHub source: execute_sql with its read-only flag and row cap, search_objects,
// and with extra the two other built-in tools.
func dbhubTOML(uri string, maxRows int, extra bool) string {
	var b strings.Builder
	fmt.Fprintf(&b, "[[sources]]\nid = \"default\"\ndsn = %q\n\n", dbhubDSN(uri))
	fmt.Fprintf(&b, "[[tools]]\nname = \"execute_sql\"\nsource = \"default\"\nreadonly = true\nmax_rows = %d\n\n", maxRows)
	b.WriteString("[[tools]]\nname = \"search_objects\"\nsource = \"default\"\n")
	if extra {
		b.WriteString("\n[[tools]]\nname = \"explain_sql\"\nsource = \"default\"\n")
		b.WriteString("\n[[tools]]\nname = \"health_check\"\nsource = \"default\"\n")
	}
	return b.String()
}

// dbhubDSN adds sslmode=disable, which the bench databases need and DBHub reads from the URI.
func dbhubDSN(uri string) string {
	if strings.Contains(uri, "?") {
		return uri
	}
	return uri + "?sslmode=disable"
}

// dbhubRun returns the `docker run` argv of a DBHub container on the bench network, with cfg as its dbhub.toml. The
// file is written inside the container from an environment variable, so the harness needs no file on the host.
func dbhubRun(cfg string) (argv []string, name string) {
	name = fmt.Sprintf("bench-dbhub-%d-%d", os.Getpid(), time.Now().UnixNano()%1_000_000)
	argv = []string{"docker", "run", "-i", "--rm", "--name", name, "--network", Network}
	if cfg == "" {
		return argv, name
	}
	const script = `printf '%s' "$DBHUB_TOML" > /tmp/dbhub.toml && exec node dist/index.js --config=/tmp/dbhub.toml`
	return append(argv, "-e", "DBHUB_TOML="+cfg, "--entrypoint", "sh", DBHubImage, "-c", script), name
}

func dbhub(t Target, maxRows int, extra bool) Spec {
	argv, name := dbhubRun(dbhubTOML(t.URI, maxRows, extra))
	return dbhubSpec(argv, name, dbhubTools(extra))
}

func dbhubDefault(t Target) Spec {
	argv, name := dbhubRun("")
	argv = append(argv, DBHubImage, "--dsn="+dbhubDSN(t.URI))
	return dbhubSpec(argv, name, dbhubTools(false))
}

func dbhubSpec(argv []string, name string, tools map[string]string) Spec {
	return Spec{
		Argv:      argv,
		Cleanup:   func() { _ = exec.Command("docker", "rm", "-f", name).Run() },
		Container: name,
		Image:     DBHubImage,
		Tools:     tools,
		Classify:  ClassifyDBHub,
		Truncated: TruncatedDBHub,
	}
}

// reDBHubTruncated matches the field DBHub sets in its result JSON when max_rows cut the result.
var reDBHubTruncated = regexp.MustCompile(`"truncated"\s*:\s*true`)

// TruncatedDBHub reports whether a DBHub answer says it cut the result. DBHub signals it as a `"truncated": true`
// field of the JSON it returns, not as a footer line, so the default reading never sees it.
func TruncatedDBHub(text string) bool { return reDBHubTruncated.MatchString(text) }

// ClassifyDBHub classifies a DBHub execute_sql answer. Every failure is the indented JSON of its error helper, with a
// `code` field: READONLY_VIOLATION is the one the read-only gate raises before touching the database, every other
// code means the statement reached Postgres (EXECUTION_ERROR) or the connection did.
func ClassifyDBHub(isError bool, text string) string {
	if !isError {
		return results.ClassExecuted
	}
	if strings.Contains(text, `"READONLY_VIOLATION"`) {
		return results.ClassRefused
	}
	return results.ClassFailed
}
