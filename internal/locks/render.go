package locks

import (
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/internal/config"
	"github.com/a-lafon/postgres-dba-mcp/internal/format"
)

// emptyLine is the whole result when nothing is waiting: like analyze_db_health, the tool reports problems only.
const emptyLine = "-- no lock wait in progress\n"

var columns = []string{"blocked_pid", "waiting", "lock", "blocking_pid", "root_pid", "blocking_state", "tx_age", "blocking_query"}

func renderSnapshot(s snapshot, cfg *config.Config) (string, error) {
	if len(s.rows) == 0 {
		return emptyLine, nil
	}
	rows := make([][]format.Cell, len(s.rows))
	for i, r := range s.rows {
		rows[i] = []format.Cell{
			cell(strconv.Itoa(int(r.blockedPID))), opt(r.waiting), cell(r.lock),
			cell(strconv.Itoa(int(r.blockingPID))), cell(strconv.Itoa(int(r.rootPID))),
			opt(r.blockingState), opt(r.txAge), opt(r.blockingQuery),
		}
	}
	prefix := chainNote(s.chains)
	// The role alone does not mean anything is masked: a role reading only its own sessions sees them whole.
	// The note is a reading of the report, so it goes out only when the report really has a hidden cell.
	if !s.hasStats && anyHidden(s.rows) {
		prefix += "\n-- session details hidden: grant pg_read_all_stats to " + line(s.role) +
			" to read waits, states and query texts"
	}
	t := format.Table{
		Columns: columns,
		Rows:    rows,
		Limit:   cfg.MaxRows,
		Prefix:  prefix,
	}
	return format.Render(t, cfg.MaxBytes, cfg.MaxCell, s.elapsed)
}

// namedChains caps how many chains the note names. format.Render refuses the whole result when the prefix
// overflows --max-bytes rather than truncating it, so an uncapped note reports nothing at all precisely
// during a lock storm: 15 chains already overflow the documented 1024-byte minimum. A named chain costs at
// most about 90 bytes, so five of them leave over half of that minimum budget to the rows.
const namedChains = 5

// chainNote names each chain by its root, its state and the age of its transaction, longest-held first.
// A root whose details are hidden keeps its pid and its count of waiting sessions.
func chainNote(chains []chain) string {
	named := chains
	if len(named) > namedChains {
		named = named[:namedChains]
	}
	parts := make([]string, len(named))
	for i, c := range named {
		p := "root pid " + strconv.Itoa(int(c.root))
		if c.state != nil && c.txAge != nil {
			p += ", " + line(*c.state) + " for " + line(*c.txAge)
		}
		parts[i] = p + ", " + plural(c.waiting, "session") + " waiting"
	}
	note := "-- " + plural(len(chains), "chain") + ", " + strings.Join(parts, "; ")
	if rest := len(chains) - len(named); rest > 0 {
		note += "; +" + strconv.Itoa(rest) + " more"
	}
	return note
}

func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// cell holds a value the statement always returns; opt leaves a hidden detail NULL rather than inventing one.
func cell(s string) format.Cell { return format.Cell(s) }

// anyHidden reports whether the statement came back with a detail PostgreSQL would only mask: one NULL cell
// is enough, since a role reads either all of a session's details or none of them.
func anyHidden(rows []row) bool {
	for _, r := range rows {
		if r.waiting == nil || r.blockingState == nil || r.txAge == nil || r.blockingQuery == nil {
			return true
		}
	}
	return false
}

func opt(s *string) format.Cell {
	if s == nil {
		return nil
	}
	return format.Cell(*s)
}

// line escapes a value like a CSV field, without the trailing newline, so that it cannot forge a note line.
func line(s string) string { return strings.TrimSuffix(format.CSVLine(s), "\n") }
