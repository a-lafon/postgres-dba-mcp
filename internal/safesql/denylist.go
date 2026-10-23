package safesql

// builtinDeny lists the function-name patterns that a READ ONLY transaction
// does not stop and that restricted mode therefore refuses, whatever the
// schema. One group per family of the design; patterns use path.Match
// wildcards. Names are lowercase.
var builtinDeny = []string{
	// Large objects: some LO functions write even in read-only.
	"lo_*", "loread", "lowrite",
	// Settings: changes statement_timeout and other settings.
	"set_config",
	// Advisory locks: session locks survive rollback.
	"pg_advisory_*", "pg_try_advisory_*",
	// Notifications.
	"pg_notify",
	// Other sessions.
	"pg_cancel_backend", "pg_terminate_backend",
	// Server files: reads outside the database.
	"pg_read_file*", "pg_read_binary_file", "pg_ls_*", "pg_stat_file",
	// Server administration.
	"pg_reload_conf", "pg_rotate_logfile*", "pg_switch_wal", "pg_log_standby_snapshot",
	"pg_create_restore_point", "pg_backup_*", "pg_start_backup",
	"pg_stop_backup", "pg_promote", "pg_wal_replay_*",
	"pg_log_backend_memory_contexts", "pg_import_system_collations",
	// Replication: consume or alter slots.
	"pg_create_*_replication_slot", "pg_drop_replication_slot",
	"pg_copy_*_replication_slot", "pg_replication_slot_advance",
	"pg_logical_slot_*_changes", "pg_logical_emit_message",
	"pg_replication_origin_*", "pg_sync_replication_slots",
	// pg_upgrade, initdb and extension scripts: no use in a query; pg_nextoid consumes OIDs even in read-only.
	"binary_upgrade_*", "pg_nextoid", "pg_stop_making_pinned_objects", "pg_extension_config_dump",
	// Statistics reset: destroys diagnostics.
	"pg_stat_reset*", "pg_stat_statements_reset",
	// Index maintenance: writes index pages even in read-only, and rollback does not undo it.
	"brin_summarize_*", "brin_desummarize_range", "gin_clean_pending_list",
	// Planner statistics: writes pg_class and pg_statistic even in read-only.
	"pg_restore_*_stats", "pg_clear_*_stats",
	// Dynamic SQL: run a SQL string the parser never sees.
	"query_to_xml*", "cursor_to_xml*", "ts_stat", "ts_rewrite",
	// Dynamic SQL of extensions: tablefunc (crosstab*, connectby) and xml2 (xpath_table).
	"crosstab*", "connectby", "xpath_table",
	// Risky extensions: dblink; adminpack; pgsql-http and pg_net (net.http_*).
	"dblink*",
	"pg_file_write", "pg_file_rename", "pg_file_unlink", "pg_file_sync", "pg_logdir_ls",
	"http", "http_*",
}
