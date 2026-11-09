package session

import (
	"testing"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/servers"
)

// Every intent must be answerable by a server that has execute_sql and nothing else, or the measure would compare
// a server's answer with another server's silence.
func TestEveryIntentHasAFallback(t *testing.T) {
	seen := map[string]bool{}
	for _, it := range Intents() {
		if it.SQL == "" || it.ID == "" || it.Want == "" {
			t.Errorf("intent %q is incomplete: %+v", it.ID, it)
		}
		if seen[it.ID] {
			t.Errorf("intent %q appears twice", it.ID)
		}
		seen[it.ID] = true
	}
	if len(seen) != 10 {
		t.Errorf("%d intents, want the ten of the investigation", len(seen))
	}
}

func TestIntentCall(t *testing.T) {
	rich := servers.Spec{Name: "dba"}
	poor := servers.Spec{Name: "dbhub", Tools: map[string]string{"execute_sql": "execute_sql"}}
	renamed := servers.Spec{Name: "dbhub-4tools", Tools: map[string]string{"execute_sql": "execute_sql", "explain_query": "explain_sql"}}
	none := servers.Spec{Name: "toolless", Tools: map[string]string{}}
	plan := Intents()[4]
	if plan.ID != "plan" {
		t.Fatalf("intent 5 is %q, want plan", plan.ID)
	}
	for _, tt := range []struct {
		name     string
		spec     servers.Spec
		wantTool string
		fallback bool
		ok       bool
	}{
		{"its own tool", rich, "explain_query", false, true},
		{"renamed tool", renamed, "explain_sql", false, true},
		{"falls back to SQL", poor, "execute_sql", true, true},
		{"cannot answer", none, "", true, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			name, args, fallback, ok := plan.call(tt.spec)
			if name != tt.wantTool || fallback != tt.fallback || ok != tt.ok {
				t.Fatalf("call = %q, %v, %v; want %q, %v, %v", name, fallback, ok, tt.wantTool, tt.fallback, tt.ok)
			}
			if ok && fallback && args["sql"] != plan.SQL {
				t.Errorf("fallback SQL = %v, want the one every falling-back server runs", args["sql"])
			}
		})
	}
}

// Every server that falls back must run the very same statement, or the answers are not comparable.
func TestFallbackSQLIsTheSameForEveryServer(t *testing.T) {
	a := servers.Spec{Name: "a", Tools: map[string]string{"execute_sql": "execute_sql"}}
	b := servers.Spec{Name: "b", Tools: map[string]string{"execute_sql": "query"}}
	for _, it := range Intents() {
		_, argsA, _, _ := it.call(a)
		_, argsB, _, _ := it.call(b)
		if argsA["sql"] != argsB["sql"] {
			t.Errorf("%s: %q vs %q", it.ID, argsA["sql"], argsB["sql"])
		}
	}
}
