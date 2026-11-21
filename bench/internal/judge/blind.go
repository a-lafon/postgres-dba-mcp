package judge

import (
	"encoding/csv"
	"regexp"
	"slices"
	"strings"
)

// ToolNames are the default tools of the five servers of PROTOCOL.md §2, at the pinned versions. Blind removes them
// from an answer: a server's tool set reveals it. Toolbox's are the 29 tools of internal/prebuiltconfigs/tools/
// postgres.yaml at v1.17.0; the others are from the servers' READMEs and tools/list.
var ToolNames = map[string][]string{
	"dba": {"list_schemas", "list_objects", "get_object_details", "execute_sql", "explain_query", "get_top_queries",
		"analyze_db_health", "analyze_query_indexes", "analyze_workload_indexes", "analyze_locks"},
	"crystal": {"list_schemas", "list_objects", "get_object_details", "execute_sql", "explain_query", "get_top_queries",
		"analyze_db_health", "analyze_query_indexes", "analyze_workload_indexes"},
	"dbhub": {"execute_sql", "search_objects"},
	"toolbox": {"execute_sql", "list_tables", "list_active_queries", "list_available_extensions",
		"list_installed_extensions", "long_running_transactions", "list_locks", "replication_stats",
		"list_autovacuum_configurations", "list_memory_configurations", "list_top_bloated_tables",
		"list_replication_slots", "list_invalid_indexes", "get_query_plan", "list_views", "list_schemas",
		"database_overview", "list_triggers", "list_indexes", "list_sequences", "list_query_stats",
		"get_column_cardinality", "list_table_stats", "list_publication_tables", "list_tablespaces", "list_pg_settings",
		"list_database_stats", "list_roles", "list_stored_procedure"},
	"reference": {"query"}, // an English word: Blind removes it only as code or as "the query tool"
}

const (
	toolMark   = "[tool]"
	serverMark = "[server]"
)

var (
	// mcp__<server>__<tool>, as Claude Code names an MCP tool.
	mcpTool = regexp.MustCompile("`?\\bmcp__[A-Za-z0-9_-]+__[A-Za-z0-9_-]+\\b`?")
	// The servers' names. Bare "dba" and "reference" are ordinary words and stay.
	serverName = regexp.MustCompile("(?i)`?(?:@modelcontextprotocol/server-postgres|\\bserver-postgres\\b|\\bpostgres[-_ ]dba[-_ ]mcp\\b|\\bdba[-_]mcp\\b|" +
		"\\bcrystaldba/postgres-mcp\\b|\\bcrystal[ -]?dba\\b|\\bpostgres[-_]mcp\\b|\\bpostgres mcp pro\\b|\\bbytebase\\b|\\bdbhub\\b|" +
		"\\b(?:google(?:'s)? )?(?:genai[- ])?(?:mcp )?toolbox(?: for databases)?\\b|\\breference (?:postgres(?:ql)? )?(?:mcp )?server\\b)`?")
	// The reference server's tool, only where it cannot be the English word.
	queryTool = regexp.MustCompile("(?i)`query`|\\bthe query tool\\b")
	// postgres-dba-mcp's footers and notes: "-- 12 rows, 38 ms", "-- plan, 4 ms", "-- 1 cell shortened ...",
	// "-- 2 schemas not readable by ...", "-- UPDATE 3, 3 ms", "-- no lock wait in progress".
	footer   = regexp.MustCompile(`(?m)^[ \t>]*-- (?:\d+ rows?\b|plan, |\d+ cells? shortened|\d+ \w+ not readable|[A-Z]+(?: \d+)+, [\d.]+ ?[mµn]?s\b|no lock wait in progress).*(?:\r?\n|$)`)
	cellCut  = regexp.MustCompile(`(?:…|\.\.\.)\[\+\d+ chars?\]`)
	tsvector = regexp.MustCompile(`\[tsvector: \d+ lexemes?\]`)
	sqlstate = regexp.MustCompile(` ?\(SQLSTATE [0-9A-Z]{5}\)`)
	errorPre = regexp.MustCompile("(?m)(^[ \\t>]*|`)(?:Error|ERROR): ")
	decimal  = regexp.MustCompile(`Decimal\('([^']*)'\)`) // crystaldba's Python repr of a numeric
	pyLine   = regexp.MustCompile(`(?m)^.*\{'.*$`)        // a line of a Python repr of rows
	pyStr    = regexp.MustCompile(`'([^'\n]*)'`)
	pyNone   = regexp.MustCompile(`([:,\[(]\s*)None\b`)
	fence    = regexp.MustCompile("(?ms)^```([^\\n`]*)\\n(.*?)^```[ \\t]*$")
	ident    = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
	blanks   = regexp.MustCompile(`\n{3,}`)
)

// toolName matches every tool name of ToolNames but "query", as a word, with or without backticks.
var toolName = func() *regexp.Regexp {
	var names []string
	for _, ts := range ToolNames {
		for _, t := range ts {
			if t != "query" && !slices.Contains(names, t) {
				names = append(names, t)
			}
		}
	}
	slices.SortFunc(names, func(a, b string) int { return len(b) - len(a) }) // longest first
	return regexp.MustCompile("`?\\b(?:" + strings.Join(names, "|") + ")\\b`?")
}()

// Blind strips from an answer what reveals the server behind it (PROTOCOL.md §7): MCP tool names, server names, and
// the output formats of the servers (postgres-dba-mcp's CSV blocks, `--` footers, cut cells, tsvector summaries and
// SQLSTATE suffixes; crystaldba's Python reprs; error prefixes). Numbers, names and values stay as they are.
func Blind(answer string) string {
	s := mcpTool.ReplaceAllString(answer, toolMark)
	s = toolName.ReplaceAllString(s, toolMark)
	s = queryTool.ReplaceAllStringFunc(s, func(m string) string {
		if strings.HasPrefix(m, "`") {
			return toolMark
		}
		return "the " + toolMark
	})
	s = serverName.ReplaceAllString(s, serverMark)
	s = footer.ReplaceAllString(s, "")
	s = cellCut.ReplaceAllString(s, "…")
	s = tsvector.ReplaceAllString(s, "(tsvector)")
	s = sqlstate.ReplaceAllString(s, "")
	s = errorPre.ReplaceAllString(s, "${1}")
	s = decimal.ReplaceAllString(s, "$1")
	s = pyLine.ReplaceAllStringFunc(s, func(l string) string {
		return pyNone.ReplaceAllString(pyStr.ReplaceAllString(l, `"$1"`), "${1}null")
	})
	s = fence.ReplaceAllStringFunc(s, neutralTable)
	return strings.TrimSpace(blanks.ReplaceAllString(s, "\n\n"))
}

// neutralTable rewrites a fenced CSV block with a header row of identifiers, postgres-dba-mcp's result format, as
// rows of cells separated by " | ", with \N shown as NULL. Any other block is returned unchanged.
func neutralTable(block string) string {
	m := fence.FindStringSubmatch(block)
	if lang := strings.ToLower(strings.TrimSpace(m[1])); lang != "" && lang != "csv" && lang != "text" {
		return block
	}
	r := csv.NewReader(strings.NewReader(m[2]))
	r.LazyQuotes = true
	recs, err := r.ReadAll() // every record must have the header's field count
	if err != nil || len(recs) < 2 || len(recs[0]) < 2 {
		return block
	}
	for _, h := range recs[0] {
		if !ident.MatchString(h) {
			return block
		}
	}
	var sb strings.Builder
	sb.WriteString("```\n")
	for _, rec := range recs {
		for i, c := range rec {
			if c == `\N` {
				rec[i] = "NULL"
			}
		}
		sb.WriteString(strings.Join(rec, " | "))
		sb.WriteByte('\n')
	}
	sb.WriteString("```")
	return sb.String()
}
