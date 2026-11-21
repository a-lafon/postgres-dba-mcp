package judge

import (
	"strings"
	"testing"
)

// The samples are written in the style of the agents' final answers of the 2026-10-22 run and of the servers' output
// formats (README of postgres-dba-mcp, crystaldba's Python repr, DBHub's JSON), with the facts a judge needs.
func TestBlind(t *testing.T) {
	tests := []struct {
		name     string
		in       string
		keep     []string // facts that must survive
		drop     []string // traces that must go
		wantSame bool     // nothing to strip
	}{
		{
			name: "dba CSV block and footer",
			in:   "Using `mcp__pg__execute_sql` I ran the query. The top 5 customers:\n\n```\ncustomer_id,name,total\n526,KARL SEAL,221.55\n148,ELEANOR HUNT,216.54\n144,\"CLARA SHAW, JR\",195.58\n```\n-- 3 rows, 12 ms\n\nKARL SEAL (526) spent the most.",
			keep: []string{"526 | KARL SEAL | 221.55", "148 | ELEANOR HUNT | 216.54", "CLARA SHAW, JR | 195.58", "KARL SEAL (526) spent the most."},
			drop: []string{"mcp__pg__", "execute_sql", "-- 3 rows", "customer_id,name,total"},
		},
		{
			name: "truncation footer, cut cell, tsvector, shortened note",
			in:   "The payload starts with {\"blob\": \"a1d0c6e83f027327d8461063f4ac58a6a1d0…[+19950 chars]\n-- 1 cell shortened (payload); pass max_cell (up to 10000) to read more\n-- 1 row, truncated (32768-byte limit), 4 ms\nThe search column is [tsvector: 12 lexemes]. The value is about 20 000 characters.",
			keep: []string{"a1d0c6e83f027327d8461063f4ac58a6a1d0…", "about 20 000 characters", "(tsvector)"},
			drop: []string{"[+19950 chars]", "-- 1 cell shortened", "-- 1 row", "lexemes"},
		},
		{
			name: "dba error with SQLSTATE",
			in:   "The tool answered:\n\nERROR: permission denied for table events (SQLSTATE 42501)\nHINT: the server is read-only.\n\nNo row was deleted; 1 873 004 events are older than one year.",
			keep: []string{"permission denied for table events", "HINT: the server is read-only.", "1 873 004 events"},
			drop: []string{"SQLSTATE", "ERROR: "},
		},
		{
			name: "crystaldba Python repr and error prefix",
			in:   "Result: `[{'name': 'Sports', 'revenue': Decimal('5314.21'), 'note': None}]`\n\nError: cannot execute DELETE in a read-only transaction",
			keep: []string{`"name": "Sports"`, `"revenue": 5314.21`, `"note": null`, "cannot execute DELETE in a read-only transaction"},
			drop: []string{"Decimal(", "None", "Error: "},
		},
		{
			name: "server names and the reference server's tool",
			in:   "postgres-dba-mcp (via crystaldba/postgres-mcp, DBHub by Bytebase, MCP Toolbox for Databases and @modelcontextprotocol/server-postgres) all expose SQL. The `query` tool of the reference server ran it; the query tool returned 16,044 rows. Crystal DBA agrees. The query was slow.",
			keep: []string{"16,044 rows", "The query was slow."},
			drop: []string{"postgres-dba-mcp", "crystaldba", "DBHub", "Bytebase", "Toolbox", "server-postgres", "`query`", "query tool", "reference server", "Crystal DBA"},
		},
		{
			name: "Toolbox tool names",
			in:   "I called list_locks and long_running_transactions: pid 4242 holds a RowExclusiveLock on public.orders, query `UPDATE orders SET status = 'paid' WHERE id = 7`.",
			keep: []string{"pid 4242", "public.orders", "UPDATE orders SET status = 'paid' WHERE id = 7", "RowExclusiveLock"},
			drop: []string{"list_locks", "long_running_transactions"},
		},
		{
			name:     "SQL with comments and commas stays",
			in:       "Run this as a human:\n\n```sql\n-- delete in batches\nDELETE FROM events WHERE created_at < now() - interval '1 year' LIMIT 10000;\nCREATE INDEX ON events (account_id, created_at);\n```",
			wantSame: true,
		},
		{
			name:     "prose with None, Error and a markdown table stays",
			in:       "None of the indexes is invalid.\n\n| table | column |\n|---|---|\n| rental | customer_id |\n\nThe DBA should reference the docs. An error: none.",
			wantSame: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Blind(tt.in)
			if tt.wantSame && got != strings.TrimSpace(tt.in) {
				t.Fatalf("changed an answer with nothing to strip:\n%s", got)
			}
			for _, k := range tt.keep {
				if !strings.Contains(got, k) {
					t.Errorf("lost %q:\n%s", k, got)
				}
			}
			for _, d := range tt.drop {
				if strings.Contains(got, d) {
					t.Errorf("kept %q:\n%s", d, got)
				}
			}
		})
	}
}

func TestToolNamesCount(t *testing.T) {
	// PROTOCOL.md §2: default tool counts 10, 9, 2, 29, 1.
	want := map[string]int{"dba": 10, "crystal": 9, "dbhub": 2, "toolbox": 29, "reference": 1}
	for s, n := range want {
		if len(ToolNames[s]) != n {
			t.Errorf("%s: %d tools, want %d", s, len(ToolNames[s]), n)
		}
	}
}
