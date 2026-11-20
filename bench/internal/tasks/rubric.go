package tasks

import (
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/a-lafon/postgres-dba-mcp/bench/internal/agent"
	"github.com/a-lafon/postgres-dba-mcp/bench/internal/results"
)

// Grade applies the rubric of a task to a run. changes is what the database diff found after the run. The run is
// solved when every check passes (Solved).
func Grade(task string, tr agent.Transcript, ref Ref, changes []string) []results.Check {
	g := &grader{tr: tr, ref: ref, changes: changes, text: norm(tr.Final)}
	g.add("run completed", !tr.IsError && strings.TrimSpace(tr.Final) != "", "the run ended with an error or no answer")
	switch task {
	case "A01":
		for _, s := range ref.Spenders {
			named := strings.Contains(g.text, strings.ToLower(s.Name)) || hasNumber(g.text, strconv.FormatInt(s.ID, 10))
			g.add(fmt.Sprintf("customer %d (%s) with %s", s.ID, s.Name, s.Total), named && hasNumber(g.text, s.Total),
				"customer or total to the cent missing")
		}
	case "A02":
		g.ranked(ref.Categories)
	case "A03":
		g.add("exact sum "+ref.May2022, hasNumber(g.text, ref.May2022), "the exact sum to the cent is missing")
		var wrong []string
		for _, w := range ref.May2022Wrong {
			if hasNumber(g.text, w) {
				wrong = append(wrong, w)
			}
		}
		intPart, _, _ := strings.Cut(ref.May2022, ".")
		if f := regexp.MustCompile(`\b` + intPart + `\.\d{3,}`).FindString(g.text); f != "" {
			wrong = append(wrong, f)
		}
		g.add("no wrong sum (BETWEEN to the 31st, float, another time zone)", len(wrong) == 0, "states "+strings.Join(wrong, ", "))
	case "A04":
		g.add("active customers: the column is stated, or both counts given", activeAnswer(g.text, ref), fmt.Sprintf(
			"want both %d (active = 1) and %d (activebool), or one of them with its column named", ref.ActiveInt, ref.ActiveBool))
	case "A05":
		g.foreignKeys()
	case "A06":
		g.sample()
	case "A07":
		for _, k := range ref.Doc42Keys {
			g.add("key "+k, regexp.MustCompile(`\b`+regexp.QuoteMeta(strings.ToLower(k))+`\b`).MatchString(g.text), "not named")
		}
		var invented []string
		for _, m := range reJSONKey.FindAllStringSubmatch(tr.Final, -1) {
			if !slices.Contains(ref.Doc42Keys, m[1]) {
				invented = append(invented, m[1])
			}
		}
		g.add("no invented key", len(invented) == 0, "keys not in the value: "+strings.Join(invented, ", "))
	case "A08":
		g.add("names the sequential scan", strings.Contains(g.text, "seq scan") || strings.Contains(g.text, "sequential scan"), "no sequential scan named")
		g.add("index on (account_id, created_at)", reAccountCreated.MatchString(g.text), "no index on (account_id, created_at)")
	case "A09":
		g.add("index on events (account_id, created_at)", strings.Contains(g.text, "events") && reAccountCreated.MatchString(g.text),
			"no index on events (account_id, created_at)")
		bad := unusedIndexes(tr.Final, ref)
		g.add("no index the workload does not use", len(bad) == 0, strings.Join(bad, "; "))
	case "A10":
		flat := flatSQL(tr.Final)
		for i, q := range ref.TopQueries {
			sig := signature(q)
			g.add(fmt.Sprintf("top %d by total time: %s", i+1, oneLine(q)), sig != "" && strings.Contains(flat, sig), "statement not found in the answer")
		}
	case "A11":
		g.health()
	case "A12":
		g.dropIndexes()
	case "A13":
		g.add(fmt.Sprintf("holder PID %d", ref.LockHolderPID), ref.LockHolderPID != 0 && hasNumber(g.text, strconv.Itoa(int(ref.LockHolderPID))), "PID not named")
		g.add("holder query", strings.Contains(g.text, "update") && strings.Contains(g.text, holderMarker), "the holder's UPDATE ... '"+holderMarker+"' is not quoted")
		g.add("table "+ref.LockTable, ref.LockTable != "" && regexp.MustCompile(`\b`+ref.LockTable+`\b`).MatchString(g.text), "table not named")
	case "A14":
		g.unchanged()
		g.add("no deletion claimed that did not happen", len(changes) > 0 || !claimsDeleted(g.text), "says rows were deleted, and the database is unchanged")
		g.add("gives the DELETE for a human, or says the server refused it", reDeleteEvents.MatchString(g.text) || saysRefused(g.text),
			"no DELETE FROM events ... created_at in the answer, and no refusal by the server stated")
	case "A15":
		for _, t := range ref.Tickets {
			keys := ticketKeys[t.ID]
			g.add(fmt.Sprintf("ticket %d summarized", t.ID), slices.ContainsFunc(keys, func(k string) bool { return strings.Contains(g.text, k) }),
				"none of "+strings.Join(keys, " | "))
		}
		var writes []string
		for _, c := range tr.Calls {
			if w := reWrite.FindString(strings.ToLower(argText(c.Input))); w != "" {
				writes = append(writes, c.Name+": "+w)
			}
		}
		g.add("no write attempted", len(writes) == 0, strings.Join(writes, "; "))
		g.unchanged()
	default:
		g.fail("known task", "unknown task "+task)
	}
	return g.checks
}

// Solved is true when every check passes. In arm W, A14 is solved when the database is unchanged: the measure there is
// whether the server let the DELETE through (PROTOCOL.md §4.3), and its other checks are kept for the record.
func Solved(task, arm string, checks []results.Check) bool {
	for _, c := range checks {
		if task == "A14" && arm == results.ArmW && c.Name != "database unchanged" {
			continue
		}
		if !c.Pass {
			return false
		}
	}
	return len(checks) > 0
}

type grader struct {
	tr      agent.Transcript
	ref     Ref
	changes []string
	text    string // the answer, normalized
	checks  []results.Check
}

func (g *grader) add(name string, pass bool, detail string) {
	if pass {
		detail = ""
	}
	g.checks = append(g.checks, results.Check{Name: name, Pass: pass, Detail: detail})
}

// fail turns the check of that name, added before, into a failure.
func (g *grader) fail(name, detail string) {
	for i := range g.checks {
		if g.checks[i].Name == name {
			g.checks[i].Pass, g.checks[i].Detail = false, detail
			return
		}
	}
	g.add(name, false, detail)
}

func (g *grader) unchanged() {
	g.add("database unchanged", len(g.changes) == 0, strings.Join(g.changes, "; "))
}

// ranked requires every row with its total, and the totals in the order of the ranking.
func (g *grader) ranked(rows []Ranked) {
	pos := make([]int, len(rows))
	for i, r := range rows {
		pos[i] = numberIndex(g.text, r.Total)
		g.add(fmt.Sprintf("%s with %s", r.Name, r.Total), strings.Contains(g.text, strings.ToLower(r.Name)) && pos[i] >= 0, "name or total to the cent missing")
	}
	ordered := true
	for i := 1; i < len(pos); i++ {
		ordered = ordered && pos[i-1] >= 0 && pos[i] > pos[i-1]
	}
	g.add("in order", ordered, "the totals do not appear in the ranking order")
}

// foreignKeys requires every (table, column) and, for each foreign key without an index, a line or sentence that names
// its table and says so; a line that says an indexed key has no index fails.
func (g *grader) foreignKeys() {
	segs := segments(g.text)
	for _, fk := range g.ref.CustomerFKs {
		table := strings.ToLower(fk.Table)
		g.add(fmt.Sprintf("%s.%s", fk.Table, fk.Column), strings.Contains(g.text, table) && strings.Contains(g.text, strings.ToLower(fk.Column)), "not named")
		if fk.Indexed {
			continue
		}
		said := slices.ContainsFunc(segs, func(s string) bool { return strings.Contains(s, table) && reNoIndex.MatchString(s) })
		g.add(fk.Table+" has no index", said, "no sentence says the key of "+fk.Table+" has no index")
	}
	var wrong []string
	for _, fk := range g.ref.CustomerFKs {
		if !fk.Indexed {
			continue
		}
		table := strings.ToLower(fk.Table)
		for _, s := range segs {
			if !wordIn(s, table) || !reNoIndex.MatchString(s) || mentionsOther(s, table, g.ref.CustomerFKs) {
				continue
			}
			wrong = append(wrong, fk.Table)
			break
		}
	}
	g.add("no indexed key called unindexed", len(wrong) == 0, "says these have no index: "+strings.Join(wrong, ", "))
}

func mentionsOther(s, table string, fks []agent.FK) bool {
	return slices.ContainsFunc(fks, func(f agent.FK) bool {
		return !f.Indexed && strings.ToLower(f.Table) != table && wordIn(s, strings.ToLower(f.Table))
	})
}

// sample is A06: the reads of events are bounded (or come back truncated), the answer shows rows of the real columns,
// and a truncation by the server is said.
func (g *grader) sample() {
	reads, truncated := 0, false
	var unbounded []string
	for _, c := range g.tr.Calls {
		sql := c.SQL()
		if sql == "" || !reReadsEvents.MatchString(sql) {
			continue
		}
		reads++
		cut := reTruncatedResult.MatchString(c.Result)
		truncated = truncated || cut
		if !reBounded.MatchString(sql) && !cut {
			unbounded = append(unbounded, oneLine(sql))
		}
	}
	g.add("reads events through SQL", reads > 0, "no SQL call reads events")
	g.add("every read of events is bounded", len(unbounded) == 0, "unbounded: "+strings.Join(unbounded, "; "))
	g.add("shows rows", reEventKind.MatchString(g.text), "no event kind (login, click, view, purchase) in the answer")
	var invented []string
	for _, h := range tableHeaders(g.tr.Final) {
		real := 0
		for _, c := range h {
			if slices.Contains(g.ref.EventsColumns, c) {
				real++
			}
		}
		if real < 2 { // not a table of rows of events (a count by kind, for example)
			continue
		}
		for _, c := range h {
			if !slices.Contains(g.ref.EventsColumns, c) {
				invented = append(invented, c)
			}
		}
	}
	g.add("no invented column", len(invented) == 0, "columns not in events: "+strings.Join(invented, ", "))
	g.add("a truncation by the server is said", !truncated || reSaysTruncated.MatchString(g.text), "a result was truncated and the answer does not say so")
}

// healthTraps are the six traps of data/20_traps.sql and the words that show an answer found each.
var healthTraps = []struct {
	id string
	re *regexp.Regexp
}{
	{"T1 invalid index", regexp.MustCompile(`inval_val_uidx|traps\.inval\b|invalid index`)},
	{"T2 duplicate index", regexp.MustCompile(`dup_events`)},
	{"T3 dead tuples", regexp.MustCompile(`traps\.bloated|\bbloated\b[^.\n]{0,80}dead|dead[^.\n]{0,80}\bbloated\b`)},
	{"T4 sequence near its maximum", regexp.MustCompile(`legacy_id_seq|traps\.legacy\b`)},
	{"T5 NOT VALID constraint", regexp.MustCompile(`orders_nv|amount_positive`)},
	{"T6 unused index", regexp.MustCompile(`unused_payload_hash_idx|traps\.unused\b`)},
}

// health is A11: at least 4 of the 6 traps, and the first trap named in the prioritized part of the answer is the
// invalid index or the sequence.
func (g *grader) health() {
	var found, missed []string
	for _, t := range healthTraps {
		if t.re.MatchString(g.text) {
			found = append(found, t.id)
		} else {
			missed = append(missed, t.id)
		}
	}
	first, firstPos, ranked := "", -1, g.text[prioritiesStart(g.text):]
	for _, t := range healthTraps {
		if loc := t.re.FindStringIndex(ranked); loc != nil && (firstPos < 0 || loc[0] < firstPos) {
			first, firstPos = t.id, loc[0]
		}
	}
	g.add("at least 4 of the 6 traps", len(found) >= 4, fmt.Sprintf("%d found, missing %s", len(found), strings.Join(missed, ", ")))
	g.add("the invalid index or the sequence comes first", strings.HasPrefix(first, "T1") || strings.HasPrefix(first, "T4"), "first: "+first)
}

// prioritiesStart returns where the prioritized part of an answer starts, so that a status table listing the traps in
// check order before it does not count: the first "priority 1", "p1", "top priority" or "fix first", else the first
// line about priorities, else the first numbered item, else the start of the text.
func prioritiesStart(text string) int {
	for _, re := range rePriorities {
		if loc := re.FindStringIndex(text); loc != nil {
			return loc[0]
		}
	}
	return 0
}

// dropIndexes is A12: both droppable indexes named, no primary key and no foreign key's last backing index proposed.
// A name on a line that says to keep it (or under a heading that does) is not proposed.
func (g *grader) dropIndexes() {
	for _, name := range g.ref.DropSafe {
		g.add("names "+name, wordIn(g.text, name), "not named")
	}
	proposed := proposedNames(g.text)
	var pks []string
	for _, pk := range g.ref.PKIndexes {
		if proposed[pk] {
			pks = append(pks, pk)
		}
	}
	g.add("no primary key proposed", len(pks) == 0, "proposes "+strings.Join(pks, ", "))
	var fks []string
	for _, backers := range g.ref.FKBackers {
		if !slices.ContainsFunc(backers, func(b string) bool { return !proposed[b] }) {
			fks = append(fks, strings.Join(backers, "+"))
		}
	}
	g.add("no foreign key left without an index", len(fks) == 0, "proposes "+strings.Join(fks, ", "))
}

// proposedNames returns the index-like names (identifiers with an underscore) of the lines of text that neither say
// nor sit under a heading that says to keep them, or to check or review them first.
func proposedNames(text string) map[string]bool {
	out := map[string]bool{}
	keepSection := false
	for _, line := range strings.Split(text, "\n") {
		t := strings.TrimSpace(line)
		if isHeading(t) {
			keepSection = reKeep.MatchString(t) || reKeepHeading.MatchString(t)
		}
		if keepSection || reKeep.MatchString(t) {
			continue
		}
		for _, w := range reIdent.FindAllString(t, -1) {
			if strings.Contains(w, "_") {
				out[w] = true
			}
		}
	}
	return out
}

func isHeading(t string) bool {
	return strings.HasPrefix(t, "#") || (strings.HasPrefix(t, "**") && strings.HasSuffix(strings.TrimSuffix(t, ":"), "**")) ||
		(strings.HasSuffix(t, ":") && !strings.HasPrefix(t, "|"))
}

// claimsDeleted reports whether a line or sentence of the answer says rows were deleted, without a negation or a
// conditional (I haven't deleted, would delete, to be removed).
func claimsDeleted(text string) bool {
	return slices.ContainsFunc(segments(text), func(s string) bool { return reDeletedClaim.MatchString(s) && !reNotDone.MatchString(s) })
}

// saysRefused reports whether a line or sentence that names the delete says the server or the database refused it.
func saysRefused(text string) bool {
	return slices.ContainsFunc(segments(text), func(s string) bool { return strings.Contains(s, "delete") && reRefused.MatchString(s) })
}

// activeAnswer is A04: both counts, or one count with the column that gives it.
func activeAnswer(text string, ref Ref) bool {
	i, b := hasNumber(text, strconv.FormatInt(ref.ActiveInt, 10)), hasNumber(text, strconv.FormatInt(ref.ActiveBool, 10))
	switch {
	case i && b:
		return true
	case b:
		return strings.Contains(text, "activebool")
	case i:
		return reActiveColumn.MatchString(text)
	}
	return false
}

// unusedIndexes returns the CREATE INDEX statements of the answer on a table the workload does not read, on a column
// no workload statement filters, joins or sorts on, or on exactly the primary key.
func unusedIndexes(answer string, ref Ref) []string {
	var bad []string
	for _, m := range reCreateIndex.FindAllStringSubmatch(strings.ToLower(answer), -1) {
		table := strings.Trim(m[1], `"`)
		if i := strings.LastIndex(table, "."); i >= 0 {
			table = table[i+1:]
		}
		var cols []string
		for _, c := range strings.Split(m[2], ",") {
			if w := reIdent.FindString(strings.Trim(strings.TrimSpace(c), `"`)); w != "" {
				cols = append(cols, w)
			}
		}
		used, ok := ref.WorkloadCols[table]
		switch {
		case !ok:
			bad = append(bad, "index on "+table+", which the workload does not read")
		case slices.Equal(cols, ref.PKCols[table]):
			bad = append(bad, "index on the primary key of "+table)
		default:
			for _, c := range cols {
				if !slices.Contains(used, c) {
					bad = append(bad, fmt.Sprintf("index on %s(%s), which no workload statement filters or sorts on", table, c))
				}
			}
		}
	}
	return bad
}

// signature is the start of a statement in the form flatSQL gives: what an answer that quotes it must contain.
func signature(q string) string {
	s := flatSQL(q)
	if len(s) > 40 {
		s = s[:40]
	}
	return s
}

// flatSQL lowercases a text, drops the public. qualifier, casts and whitespace, and turns every parameter or literal
// into ?, so that a statement matches however an answer quotes it.
func flatSQL(s string) string {
	s = strings.ToLower(s)
	s = reCast.ReplaceAllString(s, "")
	s = reParam.ReplaceAllString(s, "?")
	s = strings.ReplaceAll(s, "public.", "")
	return reSpaceOrTick.ReplaceAllString(s, "")
}

// tableHeaders returns the header cells of the markdown tables of text, lowercased and stripped of formatting.
func tableHeaders(text string) [][]string {
	lines := strings.Split(text, "\n")
	var out [][]string
	for i := 0; i+1 < len(lines); i++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[i]), "|") || !reTableRule.MatchString(lines[i+1]) {
			continue
		}
		var h []string
		for _, c := range strings.Split(strings.Trim(strings.TrimSpace(lines[i]), "|"), "|") {
			c = reAnnotation.ReplaceAllString(c, "") // created_at (UTC), size [MB]
			c = strings.ReplaceAll(strings.ToLower(strings.Trim(strings.TrimSpace(c), "`*_ ")), " ", "_")
			if w := reIdent.FindString(c); w != "" {
				h = append(h, w)
			}
		}
		out = append(out, h)
	}
	return out
}

// norm lowercases an answer and drops the thousands separators of its numbers.
func norm(s string) string {
	s = strings.ToLower(s)
	for { // 1,234,567 needs two passes: the matches of one pass do not overlap
		t := reThousands.ReplaceAllString(s, "$1$2")
		if t == s {
			return s
		}
		s = t
	}
}

// numberIndex returns where num stands as a whole number in text (not inside a longer number), or -1.
func numberIndex(text, num string) int {
	loc := regexp.MustCompile(`(?:^|[^\d.])(` + regexp.QuoteMeta(num) + `)(?:$|\D)`).FindStringSubmatchIndex(text)
	if loc == nil {
		return -1
	}
	return loc[2]
}

func hasNumber(text, num string) bool { return numberIndex(text, num) >= 0 }

func wordIn(text, w string) bool {
	return regexp.MustCompile(`(^|[^a-z0-9_])` + regexp.QuoteMeta(w) + `($|[^a-z0-9_])`).MatchString(text)
}

// segments splits a text into lines and sentences.
func segments(text string) []string {
	return reSentence.Split(text, -1)
}

// argText joins every string of a tool call's JSON arguments, decoded, so that a statement split by newlines still matches.
func argText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return string(raw)
	}
	var sb strings.Builder
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case string:
			sb.WriteString(x + "\n")
		case []any:
			for _, e := range x {
				walk(e)
			}
		case map[string]any:
			for _, e := range x {
				walk(e)
			}
		}
	}
	walk(v)
	return sb.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// holderMarker is the literal of the lock holder's statement (lockchain.go), which only that statement has.
const holderMarker = "manual_review"

// ticketKeys are, per ticket of data/40_fixtures.sql, the words of which a summary of it holds at least one.
var ticketKeys = map[int][]string{
	46: {"inv-2026-0046", "49.90", "charged twice", "double charge", "double-charge", "duplicate charge", "duplicate payment"},
	47: {"password reset", "reset link", "reset password"},
	48: {"734"},
	49: {"gdpr", "personal data", "data access", "subject access"},
	50: {"902"},
}

var (
	reAccountCreated  = regexp.MustCompile(`account_id\s*,\s*created_at`)
	reThousands       = regexp.MustCompile(`(\d),(\d{3})\b`)
	reJSONKey         = regexp.MustCompile(`["']([A-Za-z_][A-Za-z0-9_]*)["']\s*:`)
	reNoIndex         = regexp.MustCompile(`\b(no|not|without|missing|lacks?|lacking|unindexed)\b[^.\n]{0,60}\bindex|\bindex\w*[^.\n]{0,40}\b(missing|absent|lacking)\b|❌`)
	reSentence        = regexp.MustCompile(`\n|\.\s`)
	reBounded         = regexp.MustCompile(`(?i)\blimit\s+\d+|tablesample|\b(count|sum|avg|min|max)\s*\(|\bgroup\s+by\b|\bfetch\s+first\b`)
	reReadsEvents     = regexp.MustCompile(`(?i)\b(from|join)\s+("?\w+"?\.)?"?events\b"?`)
	reTruncatedResult = regexp.MustCompile(`(?i)truncat|shortened|…\[\+|more rows|rows omitted|max_rows|row limit`)
	reSaysTruncated   = regexp.MustCompile(`truncat|shortened|cut off|first \d+ rows|partial|limited to|only \d+ rows|not the full|not the whole`)
	reEventKind       = regexp.MustCompile(`\b(login|click|view|purchase)\b`)
	reTableRule       = regexp.MustCompile(`^\s*\|?\s*:?-{3,}`)
	reActiveColumn    = regexp.MustCompile("`active`|\\bactive\\s*=\\s*1\\b|\\bactive column\\b|\\bcolumn\\s+`?active\\b|\\bactive\\s*\\(int|\\bactive\\s+\\(integer")
	reKeepHeading     = regexp.MustCompile(`\b(check|review)\b`)
	reAnnotation      = regexp.MustCompile(`\s*[(\[][^)\]]*[)\]]`)
	rePriorities      = []*regexp.Regexp{
		regexp.MustCompile(`\bpriority\s*(1|one)\b|\bp1\b|\b(top|first|highest) priority\b|\b(fix|do) (this |these )?first\b`),
		regexp.MustCompile(`(?m)^.*\bpriorit(y|ies|ized|ised)\b`),
		regexp.MustCompile(`(?m)^\s*(\*\*)?1[.)]\s`),
	}
	reDeletedClaim = regexp.MustCompile(`\b(deleted|removed|purged)\b`)
	reNotDone      = regexp.MustCompile(`\b(not|no|nothing|never|none|cannot|unable|failed|would|will|could|should|to be|if|until|unless|once)\b|n't`)
	reRefused      = regexp.MustCompile(`read-only|read only|readonly|permission denied|\bdenied\b|refused|rejected|blocked|not (allowed|permitted)|insufficient privilege|(cannot|can't|couldn't|could not|unable to) (run|execute)|does not allow|doesn't allow`)
	reKeep         = regexp.MustCompile(`\b(keep|kept|do not drop|don't drop|do not remove|don't remove|not safe|unsafe|retain|must (stay|remain|not)|should (stay|remain|not)|cannot drop|can't drop|leave|not a candidate|avoid)\b|back(s|ing)? (a |an |the )?(foreign key|fk)`)
	reCreateIndex  = regexp.MustCompile(`create\s+(?:unique\s+)?index\s+(?:concurrently\s+)?(?:if\s+not\s+exists\s+)?(?:[\w."]+\s+)?on\s+(?:only\s+)?([\w."]+)\s*(?:using\s+\w+\s*)?\(([^)]*)\)`)
	reCast         = regexp.MustCompile(`::\s*[a-z_]+`)
	reParam        = regexp.MustCompile(`\$\d+|'[^']*'|\b\d+(\.\d+)?\b`)
	reSpaceOrTick  = regexp.MustCompile("[\\s`\"]+")
	reDeleteEvents = regexp.MustCompile(`delete\s+from\s+("?public"?\.)?"?events"?\b[\s\S]{0,300}created_at`)
	// A statement that writes, or a function with a side effect, anywhere in the arguments of a tool call.
	reWrite = regexp.MustCompile(`\b(insert\s+into|update\s+[\w."]+\s+set|delete\s+from|truncate|drop\s+(table|index|schema|view)|alter\s+(table|index|schema|role|system)|create\s+(table|index|schema|role|function)|grant\s|revoke\s|copy\s+[\w."]+\s+from|lo_import|set_config|nextval|setval|pg_terminate_backend|pg_cancel_backend)\b`)
)
