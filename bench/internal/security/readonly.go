package security

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// ResponseLimit is the response cap of parts B and C: far above any answer an agent can use, low enough to bound the
// harness on SELECT * FROM events (5,000,000 rows).
const ResponseLimit = 256 << 20

// Memory bounds each server container, so that a huge result cannot exhaust the Docker VM that also runs the database.
const Memory = "4g"

// Bounded returns spec with the container memory capped at Memory, and without --rm: a container that dies stays
// until the spec's Cleanup removes it, so that its exit code and OOM flag can be read. Only docker specs change.
func Bounded(spec servers.Spec) servers.Spec {
	if len(spec.Argv) > 2 && spec.Argv[0] == "docker" && spec.Argv[1] == "run" {
		argv := []string{"docker", "run", "--memory=" + Memory}
		for _, a := range spec.Argv[2:] {
			if a != "--rm" {
				argv = append(argv, a)
			}
		}
		spec.Argv = argv
	}
	return spec
}

// Target is one server of parts B and C: how to start it and its SQL tool.
type Target struct {
	Spec     servers.Spec
	Tool     string // the SQL tool
	Arg      string // its argument that holds the SQL
	ReadOnly string // the read-only model, from the registry
}

// Answer turns one tool call into what the model receives: the text, whether it is an error, and the transport
// failure when there was no answer. The reference server returns a failed query as a JSON-RPC error, which is an
// answer (its message reaches the model); crystaldba returns one as a normal result whose text starts with "Error".
func Answer(server string, res mcpc.Result, err error) (text string, isError bool, transport error) {
	var wire *jsonrpc.Error
	switch {
	case errors.As(err, &wire):
		return wire.Message, true, nil
	case err != nil:
		return "", false, err
	case server == results.Crystal && strings.HasPrefix(res.Text, "Error"):
		return res.Text, true, nil
	}
	return res.Text, res.IsError, nil
}

// Outcome classifies a part C statement. A lasting effect wins over the answer; otherwise an error is a refusal by
// the server when the server's own check stopped the statement, else a refusal by the database.
func Outcome(server, text string, isError bool, transport error, lasting bool) string {
	switch {
	case lasting:
		return results.ROLasting
	case transport != nil:
		return results.RONoAnswer
	case !isError:
		return results.RONoEffect
	case refusedByServer(server, text):
		return results.ROServer
	}
	return results.RODatabase
}

// refusedByServer reports whether an error text comes from the server's own check rather than from PostgreSQL.
func refusedByServer(server, text string) bool {
	switch server {
	case results.DBA:
		// Every database error carries its SQLSTATE; the restricted-mode checks do not.
		return !strings.Contains(text, "(SQLSTATE ")
	case results.Crystal:
		return strings.HasPrefix(text, "Error: Error validating query") || strings.HasPrefix(text, "Error: Failed to parse SQL statement")
	case results.DBHub:
		// The keyword allow-list of readonly = true (src/utils/allowed-keywords.ts); the database's own
		// "read-only transaction" errors come back as their PostgreSQL message.
		return strings.Contains(text, "READONLY_VIOLATION") || strings.Contains(text, "Read-only mode")
	}
	// Toolbox has no read-only check and the reference server no parser: every error is the database's.
	return false
}

// ReplayReadOnly sends every case of a corpus through the target's SQL tool and classifies each outcome with a
// snapshot of the database. base is the snapshot of the database before the first case; the returned snapshot is the
// one after the last. A lasting effect resets the database and restarts the server before the next case, so no case
// sees the effect of another.
func (r *Replayer) ReplayReadOnly(ctx context.Context, tg Target, cases []Case, base Snapshot) ([]results.ReadOnlyCase, Snapshot, error) {
	var srv *servers.Server
	defer func() {
		if srv != nil {
			srv.Close()
		}
	}()
	var out []results.ReadOnlyCase
	for _, c := range cases {
		if srv == nil {
			var err error
			if srv, err = tg.Spec.StartLimit(ctx, ResponseLimit); err != nil {
				return nil, base, err
			}
		}
		_, _ = fmt.Fprintf(r.Log, "readonly %s %s\n", tg.Spec.Name, c.ID)
		res, callErr := srv.Call(ctx, tg.Tool, map[string]any{tg.Arg: c.SQL})
		text, isError, transport := Answer(tg.Spec.Name, res, callErr)
		if transport != nil {
			text = "transport: " + transport.Error()
		}
		var effect string
		after, err := TakeAfter(ctx, r.admin, r.sentinel, base)
		if err != nil {
			// Something holds a lock the snapshot needs, or the database went away: count it as a lasting effect.
			effect = "snapshot failed: " + err.Error()
		} else if d := Diff(base, after); len(d) > 0 {
			effect = strings.Join(d, "; ")
		}
		out = append(out, results.ReadOnlyCase{ID: c.ID, Category: c.Category, Source: c.Source,
			Outcome: Outcome(tg.Spec.Name, text, isError, transport, effect != ""), Effect: effect, Text: brief(text)})
		switch {
		case effect != "":
			srv.Close()
			srv = nil
			if err := r.reset(ctx); err != nil {
				return nil, base, err
			}
			if base, err = Take(ctx, r.admin, r.sentinel); err != nil {
				return nil, base, fmt.Errorf("snapshot after reset: %w", err)
			}
		case transport != nil:
			srv.Close()
			srv = nil
			base = after
		default:
			base = after
		}
	}
	return out, base, nil
}

// brief returns the start of a response on one line: a JSON answer (DBHub) has nothing useful on its first line.
func brief(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 240 {
		s = s[:240]
	}
	return s
}

// Baseline resets the database and returns its snapshot: what each server starts from.
func (r *Replayer) Baseline(ctx context.Context) (Snapshot, error) {
	if err := r.reset(ctx); err != nil {
		return Snapshot{}, err
	}
	return Take(ctx, r.admin, r.sentinel)
}
