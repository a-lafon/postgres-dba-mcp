package security

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/mcpc"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

func TestAnswer(t *testing.T) {
	// The reference server: a failed query is a JSON-RPC error, which mcpc wraps.
	rpc := fmt.Errorf("call query: %w", &jsonrpc.Error{Code: -32603, Message: "cannot execute DELETE in a read-only transaction"})
	text, isError, transport := Answer(results.Reference, mcpc.Result{}, rpc)
	if text != "cannot execute DELETE in a read-only transaction" || !isError || transport != nil {
		t.Errorf("JSON-RPC error: %q %v %v", text, isError, transport)
	}
	// crystaldba: an error is a normal result whose text starts with "Error".
	if _, isError, _ := Answer(results.Crystal, mcpc.Result{Text: "Error: Error validating query"}, nil); !isError {
		t.Error("crystaldba Error: text must be an error")
	}
	if _, isError, _ := Answer(results.DBA, mcpc.Result{Text: "Error: a value"}, nil); isError {
		t.Error("only crystaldba's Error: text is an error")
	}
	timeout := fmt.Errorf("call execute_sql: %w", mcpc.ErrTimeout)
	if _, _, transport := Answer(results.DBA, mcpc.Result{}, timeout); !errors.Is(transport, mcpc.ErrTimeout) {
		t.Errorf("timeout: transport = %v", transport)
	}
}

func TestOutcome(t *testing.T) {
	transport := errors.New("broken pipe")
	tests := []struct {
		name, server, text string
		isError            bool
		transport          error
		lasting            bool
		want               string
	}{
		{"dba parser", results.DBA, "restricted mode: statement DeleteStmt is not allowed", true, nil, false, results.ROServer},
		{"dba database", results.DBA, "ERROR: cannot execute DELETE in a read-only transaction (SQLSTATE 25006)", true, nil, false, results.RODatabase},
		{"dba executed", results.DBA, "id\n1\n-- 1 row", false, nil, false, results.RONoEffect},
		{"crystal parser", results.Crystal, "Error: Error validating query: DELETE FROM t", true, nil, false, results.ROServer},
		{"crystal unparsable", results.Crystal, "Error: Failed to parse SQL statement", true, nil, false, results.ROServer},
		{"crystal database", results.Crystal, "Error: cannot execute nextval() in a read-only transaction", true, nil, false, results.RODatabase},
		{"dbhub allow-list", results.DBHub, "Read-only mode is enabled. Only the following SQL operations are allowed: select, with, explain, show", true, nil, false, results.ROServer},
		{"dbhub database", results.DBHub, "cannot execute DELETE in a read-only transaction", true, nil, false, results.RODatabase},
		{"toolbox database", results.Toolbox, "unable to execute query: ERROR: permission denied (SQLSTATE 42501)", true, nil, false, results.RODatabase},
		{"reference JSON-RPC", results.Reference, "cannot execute DELETE in a read-only transaction", true, nil, false, results.RODatabase},
		{"reference COMMIT escape", results.Reference, "[]", false, nil, true, results.ROLasting},
		{"lasting despite an error", results.Reference, "ROLLBACK failed", true, nil, true, results.ROLasting},
		{"no answer", results.Toolbox, "transport: broken pipe", false, transport, false, results.RONoAnswer},
		{"no answer but lasting", results.Toolbox, "transport: broken pipe", false, transport, true, results.ROLasting},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Outcome(tt.server, tt.text, tt.isError, tt.transport, tt.lasting); got != tt.want {
				t.Errorf("Outcome = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExternalCorpus(t *testing.T) {
	b, err := os.ReadFile("../../data/external-attacks.sql")
	if err != nil {
		t.Fatal(err)
	}
	cases, err := ParseCorpus(string(b))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, c := range cases {
		if !strings.HasPrefix(c.Source, "https://") {
			t.Errorf("case %s has no source URL: %q", c.ID, c.Source)
		}
		if c.SQL == "" || strings.Contains(c.SQL, "-- source:") {
			t.Errorf("case %s: SQL %q", c.ID, c.SQL)
		}
		if seen[c.ID] {
			t.Errorf("case %s given twice", c.ID)
		}
		seen[c.ID] = true
	}
	if got := Channels(cases); len(got) != 1 || got[0] != "ext" {
		t.Errorf("channels = %v, want [ext]", got)
	}
}

func TestBounded(t *testing.T) {
	spec := servers.Spec{Argv: []string{"docker", "run", "-i", "--rm", "img"}}
	got := Bounded(spec).Argv
	want := []string{"docker", "run", "--memory=" + Memory, "-i", "img"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Bounded = %q, want %q", got, want)
	}
	if !reflect.DeepEqual(spec.Argv, []string{"docker", "run", "-i", "--rm", "img"}) {
		t.Errorf("Bounded changed its argument: %q", spec.Argv)
	}
	native := servers.Spec{Argv: []string{"/bin/server"}}
	if got := Bounded(native).Argv; !reflect.DeepEqual(got, native.Argv) {
		t.Errorf("Bounded changed a native spec: %q", got)
	}
}
