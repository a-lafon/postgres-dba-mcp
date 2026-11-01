package safesql

import "testing"

// FuzzSafeSQL runs the whole database-free surface on arbitrary text. Most of what reaches it was written by
// nobody here: indexadv rewrites, checks and walks the statements pg_stat_statements hands back, which come
// from every other client of the server. Nothing may panic, the rewrite must settle in one pass, since a query
// read from get_top_queries can be passed back to explain and rewritten again, and a statement Check accepts
// must be the single allowed statement Classify reads.
func FuzzSafeSQL(f *testing.F) {
	p := defaultPolicy(f)
	for _, c := range loadCorpus(f, "attacks.sql", attackFamilies) {
		f.Add(c.SQL)
	}
	for _, c := range loadCorpus(f, "legit.sql", map[string]bool{"legit": true}) {
		f.Add(c.SQL)
	}
	for _, sql := range []string{
		"SELECT count(*) FROM events WHERE created_at < now() - interval $1",
		"SELECT interval $1 day to second, interval(3) $2",
		"SELECT json_object('a' VALUE $1)",
		"", "$", "$1", "'", "\\", "--", "/*", "SELECT $1::",
	} {
		f.Add(sql)
	}
	f.Fuzz(func(t *testing.T, sql string) {
		out := RewriteTypedParams(sql)
		if again := RewriteTypedParams(out); again != out {
			t.Fatalf("rewrite not settled: %q -> %q -> %q", sql, out, again)
		}
		for _, s := range []string{sql, out} {
			HasParams(s)
			_, _ = Refs(s)
			_, _ = Relations(s)
			st, err := Check(s, p)
			if err != nil {
				continue
			}
			count, kind, err := Classify(s)
			if err != nil || count != 1 || kind != st.Kind {
				t.Fatalf("Check accepted %q as kind %d, Classify says count=%d kind=%d err=%v", s, st.Kind, count, kind, err)
			}
		}
	})
}
