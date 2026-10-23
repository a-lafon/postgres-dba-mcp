package safesql

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPolicy(t *testing.T) {
	tests := []struct {
		name        string
		allow, deny []string
		sql         string
		refused     bool
	}{
		{"allow lifts built-in", []string{"pg_terminate_backend"}, nil, "SELECT pg_terminate_backend(123)", false},
		{"deny beats allow", []string{"myapp.*"}, []string{"myapp.purge_cache"}, "SELECT myapp.purge_cache()", true},
		{"allow covers the rest", []string{"myapp.*"}, []string{"myapp.purge_cache"}, "SELECT myapp.score(1)", false},
		{"qualified deny is schema-specific", nil, []string{"myapp.purge_cache"}, "SELECT other.purge_cache()", false},
		{"unqualified call fails closed", nil, []string{"myapp.purge_cache"}, "SELECT purge_cache()", true},
		{"qualified allow, unqualified call", []string{"pg_catalog.pg_terminate_backend"}, nil, "SELECT pg_terminate_backend(1)", true},
		{"qualified allow, qualified call", []string{"pg_catalog.pg_terminate_backend"}, nil, "SELECT pg_catalog.pg_terminate_backend(1)", false},
		{"qualified allow, other schema", []string{"pg_catalog.pg_terminate_backend"}, nil, "SELECT public.pg_terminate_backend(1)", true},
		{"unqualified deny matches any schema", nil, []string{"purge_*"}, "SELECT myapp.purge_cache()", true},
		{"deny is case-insensitive", nil, []string{"MyApp.Purge_Cache"}, `SELECT "MYAPP"."PURGE_CACHE"()`, true},
		{"user deny of an ordinary function", nil, []string{"now"}, "SELECT now()", true},
		{"wildcard allow", []string{"*"}, nil, "SELECT lo_import('x'), set_config('a', 'b', false)", false},
		{"schema wildcard", []string{"my*.f"}, nil, "SELECT myapp.f(), lo_get(1)", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPolicy(tt.allow, tt.deny)
			if err != nil {
				t.Fatal(err)
			}
			_, err = Check(tt.sql, p)
			if got := err != nil; got != tt.refused {
				t.Fatalf("refused = %v (err %v), want %v", got, err, tt.refused)
			}
		})
	}
}

func TestNewPolicyErrors(t *testing.T) {
	tests := []struct {
		name        string
		allow, deny []string
		want        string
	}{
		{"bad glob", nil, []string{"[abc"}, `invalid pattern "[abc"`},
		{"bad glob in allow", []string{"a.[b"}, nil, `invalid pattern "a.[b"`},
		{"two dots", []string{"a.b.c"}, nil, `invalid pattern "a.b.c"`},
		{"empty", []string{""}, nil, `invalid pattern ""`},
		{"empty schema", nil, []string{".f"}, `invalid pattern ".f"`},
		{"empty name", nil, []string{"s."}, `invalid pattern "s."`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := NewPolicy(tt.allow, tt.deny)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestBuiltinDenylistPatternsValid(t *testing.T) {
	if _, err := parsePatterns(builtinDeny); err != nil {
		t.Fatal(err)
	}
}

func TestBuiltinDenylist(t *testing.T) {
	// One concrete name per pattern family of the spec.
	names := []string{
		"lo_import", "loread", "lowrite", "set_config", "pg_advisory_lock", "pg_try_advisory_xact_lock",
		"pg_notify", "pg_cancel_backend", "pg_terminate_backend", "pg_read_file", "pg_read_binary_file",
		"pg_ls_dir", "pg_stat_file", "pg_reload_conf", "pg_rotate_logfile", "pg_switch_wal",
		"pg_create_restore_point", "pg_backup_start", "pg_start_backup", "pg_stop_backup", "pg_promote",
		"pg_wal_replay_pause", "pg_log_backend_memory_contexts", "pg_import_system_collations",
		"pg_create_physical_replication_slot", "pg_drop_replication_slot", "pg_copy_logical_replication_slot",
		"pg_replication_slot_advance", "pg_logical_slot_peek_changes", "pg_logical_emit_message",
		"pg_replication_origin_create", "pg_sync_replication_slots", "pg_stat_reset", "pg_stat_reset_shared",
		"pg_stat_statements_reset", "brin_summarize_new_values", "brin_summarize_range", "brin_desummarize_range",
		"gin_clean_pending_list", "pg_restore_relation_stats", "pg_restore_attribute_stats", "pg_clear_relation_stats",
		"pg_clear_attribute_stats", "pg_read_file_old", "pg_rotate_logfile_old", "pg_log_standby_snapshot",
		"binary_upgrade_set_next_pg_type_oid", "pg_nextoid", "pg_stop_making_pinned_objects", "pg_extension_config_dump",
		"query_to_xml", "query_to_xml_and_xmlschema", "cursor_to_xml", "ts_stat", "ts_rewrite",
		"crosstab", "crosstab2", "crosstab_hash", "connectby", "xpath_table",
		"dblink", "dblink_exec", "pg_file_write", "pg_file_rename", "pg_file_unlink", "pg_file_sync",
		"pg_logdir_ls", "http", "http_get",
	}
	p := defaultPolicy(t)
	for _, n := range names {
		for _, qn := range []string{n, "public." + n, "x." + strings.ToUpper(n)} {
			t.Run(qn, func(t *testing.T) {
				if _, err := Check("SELECT "+qn+"(1)", p); err == nil {
					t.Fatal("accepted")
				}
			})
		}
	}
}

// loadReviewed reads catalog_volatile.txt: the volatile pg_catalog functions reviewed and allowed, one
// "name reason" line each. Lines starting with '#' are comments.
func loadReviewed(t testing.TB) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", "testdata", "safesql", "catalog_volatile.txt"))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for i, line := range strings.Split(string(b), "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, reason, _ := strings.Cut(line, " ")
		if reason = strings.TrimSpace(reason); reason == "" {
			t.Fatalf("catalog_volatile.txt:%d: %q has no reason", i+1, name)
		}
		if _, dup := out[name]; dup {
			t.Fatalf("catalog_volatile.txt:%d: duplicate %q", i+1, name)
		}
		out[name] = reason
	}
	return out
}

func TestReviewedNotDenied(t *testing.T) {
	p := defaultPolicy(t)
	for name := range loadReviewed(t) {
		if p.refuses(call{name: strings.ToLower(name)}) {
			t.Errorf("%s is reviewed as safe but the built-in denylist refuses it: remove it from catalog_volatile.txt", name)
		}
	}
}

func TestWarnings(t *testing.T) {
	const base = "WARNING: restricted mode: --allow-function=%s re-enables functions denied by default"
	tests := []struct {
		name  string
		allow []string
		want  []string
	}{
		{"none", nil, nil},
		{"unrelated", []string{"myapp.score", "my*", "now"}, nil},
		{"qualified wildcard re-enables by name", []string{"myapp.*"}, []string{strings.Replace(base, "%s", "myapp.*", 1) + " (callers can then change statement_timeout)"}},
		{"set_config", []string{"set_config"}, []string{strings.Replace(base, "%s", "set_config", 1) + " (callers can then change statement_timeout)"}},
		{"exact builtin", []string{"pg_terminate_backend"}, []string{strings.Replace(base, "%s", "pg_terminate_backend", 1)}},
		{"user pattern wider than builtin", []string{"pg_*"}, []string{strings.Replace(base, "%s", "pg_*", 1)}},
		{"user pattern narrower than builtin", []string{"lo_get"}, []string{strings.Replace(base, "%s", "lo_get", 1)}},
		{"qualified overlaps by name", []string{"pg_catalog.pg_notify"}, []string{strings.Replace(base, "%s", "pg_catalog.pg_notify", 1)}},
		{"star covers set_config", []string{"*"}, []string{strings.Replace(base, "%s", "*", 1) + " (callers can then change statement_timeout)"}},
		{"one line per pattern", []string{"pg_notify", "now", "dblink_exec"}, []string{strings.Replace(base, "%s", "pg_notify", 1), strings.Replace(base, "%s", "dblink_exec", 1)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p, err := NewPolicy(tt.allow, nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := p.Warnings(); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}
