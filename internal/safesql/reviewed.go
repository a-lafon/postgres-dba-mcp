package safesql

import "strings"

// reviewedVolatile lists, per extension, the VOLATILE functions that were run
// inside a READ ONLY transaction and left nothing behind once it rolled back:
// no committed change, no file, no effect on another session or backend, no
// background worker, no statistics reset, no state the connection pool fails
// to clear. Gate lets only these through.
//
// A function is keyed by the extension that owns it, so a look-alike created
// by hand in another schema stays refused. CONTRIBUTING.md describes how to
// review a new one.
//
// The denylist decides before Gate, so a name it matches never reaches this
// list: listing one would be dead and would read as an allowance it is not.
// TestReviewedNotDenied refuses the contradiction.
//
// Not here on purpose: hypopg_hide_index, because neither hypopg_reset() nor
// DISCARD ALL unhides, so a hidden index would change the plans of later
// requests on the same pooled connection.
var reviewedVolatile = map[string][]string{
	"amcheck": {
		"bt_index_check", "bt_index_parent_check", "verify_heapam",
	},
	"credcheck": {
		"pg_banned_role", "pg_password_history",
	},
	"file_fdw": {
		"file_fdw_validator",
	},
	"hypopg": {
		"hypopg", "hypopg_create_index", "hypopg_drop_index", "hypopg_get_indexdef",
		"hypopg_hidden_indexes", "hypopg_relation_size", "hypopg_reset", "hypopg_reset_index",
		"hypopg_unhide_all_indexes", "hypopg_unhide_index",
	},
	"mimeo": {
		"concurrent_lock_check", "sequence_max_value",
	},
	"mysql_fdw": {
		"mysql_fdw_display_pushdown_list", "mysql_fdw_validator", "mysql_fdw_version",
	},
	"orafce": {
		"__list_pipes", "add_bizdays", "bizdays_between", "days_inmonth", "dump", "format_call_stack",
		"get_time", "including_start", "is_open", "isbizday", "isleapyear", "last_row_count",
		"nearest_bizday", "next_bizday", "normal", "prev_bizday", "random", "string", "subst",
		"sys_guid", "translate_oracle_modifiers", "unique_session_name", "unistr", "using_easter",
		"using_great_friday", "value", "version",
	},
	"pageinspect": {
		"brin_metapage_info", "brin_page_items", "brin_page_type", "brin_revmap_data", "bt_metap",
		"bt_multi_page_stats", "bt_page_items", "bt_page_stats", "fsm_page_contents", "get_raw_page",
		"gin_leafpage_items", "gin_metapage_info", "gin_page_opaque_info", "gist_page_items",
		"gist_page_items_bytea", "gist_page_opaque_info", "hash_bitmap_info", "hash_metapage_info",
		"hash_page_items", "hash_page_stats", "hash_page_type", "heap_page_item_attrs",
		"heap_page_items", "heap_tuple_infomask_flags", "page_checksum", "page_header",
		"tuple_data_split",
	},
	"periods": {
		"_serialize", "validate_foreign_key_new_row", "validate_foreign_key_old_row",
	},
	"pg_buffercache": {
		"pg_buffercache_pages", "pg_buffercache_summary", "pg_buffercache_usage_counts",
	},
	"pg_dirtyread": {
		"pg_dirtyread",
	},
	"pg_freespacemap": {
		"pg_freespace",
	},
	"pg_partman": {
		"check_subpartition_limits", "partman_safe_obj_name", "uuid7_time_decoder", "uuid7_time_encoder",
	},
	"pg_permissions": {
		"permission_diffs",
	},
	"pg_qualstats": {
		"pg_qualstats", "pg_qualstats_deparse_qual", "pg_qualstats_example_queries",
		"pg_qualstats_example_query", "pg_qualstats_get_idx_col", "pg_qualstats_get_qualnode_rel",
		"pg_qualstats_index_advisor", "pg_qualstats_names",
	},
	"pg_squeeze": {
		"get_active_workers", "get_heap_fillfactor", "get_heap_freespace", "pgstattuple_approx",
	},
	"pg_stat_kcache": {
		"pg_stat_kcache",
	},
	"pg_stat_statements": {
		"pg_stat_statements", "pg_stat_statements_info",
	},
	"pg_track_settings": {
		"pg_track_db_role_settings", "pg_track_db_role_settings_diff", "pg_track_db_role_settings_log",
		"pg_track_reboot_log", "pg_track_settings", "pg_track_settings_diff", "pg_track_settings_log",
		"pg_track_settings_rds_src", "pg_track_settings_reboot_src", "pg_track_settings_settings_src",
	},
	"pg_trgm": {
		"set_limit",
	},
	"pg_visibility": {
		"pg_check_frozen", "pg_check_visible", "pg_visibility", "pg_visibility_map",
		"pg_visibility_map_summary",
	},
	"pg_wait_sampling": {
		"pg_wait_sampling_get_current", "pg_wait_sampling_get_history", "pg_wait_sampling_get_profile",
	},
	"pg_walinspect": {
		"pg_get_wal_block_info", "pg_get_wal_record_info", "pg_get_wal_records_info", "pg_get_wal_stats",
	},
	"pgcrypto": {
		"gen_random_bytes", "gen_random_uuid", "gen_salt", "pgp_pub_encrypt", "pgp_pub_encrypt_bytea",
		"pgp_sym_encrypt", "pgp_sym_encrypt_bytea",
	},
	"pgfincore": {
		"pgfincore", "pgfincore_drawer", "pgsysconf", "pgsysconf_pretty",
	},
	"pgl_ddl_deploy": {
		"current_query", "deployment_check", "deployment_check_count", "deployment_check_wrapper",
		"is_subscriber", "provider_node_name", "raise_message", "sql_command_tags",
	},
	"pglogical": {
		"pglogical_max_proto_version", "pglogical_min_proto_version", "pglogical_version",
		"pglogical_version_num", "xact_commit_timestamp_origin",
	},
	"pgq": {
		"batch_event_sql", "batch_event_tables", "current_event_table", "get_batch_cursor",
		"get_batch_events", "get_batch_info", "get_consumer_info", "get_queue_info", "maint_operations",
		"maint_tables_to_vacuum", "seq_getval", "version",
	},
	"pgrowlocks": {
		"pgrowlocks",
	},
	"pgstattuple": {
		"pg_relpages", "pgstatginindex", "pgstathashindex", "pgstatindex", "pgstattuple",
		"pgstattuple_approx",
	},
	"plpgsql": {
		"plpgsql_validator",
	},
	"plpgsql_check": {
		"__plpgsql_show_dependency_tb", "plpgsql_check_function", "plpgsql_check_function_tb",
		"plpgsql_check_pragma", "plpgsql_coverage_branches", "plpgsql_coverage_statements",
		"plpgsql_make_pragma", "plpgsql_profiler_function_statements_tb", "plpgsql_profiler_function_tb",
		"plpgsql_profiler_functions_all", "plpgsql_show_dependency_tb",
	},
	"plproxy": {
		"plproxy_fdw_validator", "plproxy_validator",
	},
	"plsh": {
		"plsh_validator",
	},
	"postgis": {
		"_postgis_join_selectivity", "_postgis_selectivity", "_postgis_stats", "st_generatepoints",
		"st_symmetricdifference",
	},
	"postgres_fdw": {
		"postgres_fdw_get_connections", "postgres_fdw_validator",
	},
	"sslinfo": {
		"ssl_cipher", "ssl_client_cert_present", "ssl_client_dn", "ssl_client_dn_field",
		"ssl_client_serial", "ssl_extension_info", "ssl_is_used", "ssl_issuer_dn", "ssl_issuer_field",
		"ssl_version",
	},
	"tablefunc": {
		"normal_rand",
	},
	"tds_fdw": {
		"tds_fdw_validator",
	},
	"uuid-ossp": {
		"uuid_generate_v1", "uuid_generate_v1mc", "uuid_generate_v4",
	},
}

// reviewedIndex is reviewedVolatile keyed by function name, built once.
var reviewedIndex = func() map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for extension, names := range reviewedVolatile {
		for _, n := range names {
			if out[n] == nil {
				out[n] = map[string]bool{}
			}
			out[n][extension] = true
		}
	}
	return out
}()

// reviewedSafe reports whether the function of that extension was reviewed and
// found to leave nothing behind. A function owned by no extension is never
// reviewed: extension is empty and matches nothing.
func reviewedSafe(extension, name string) bool {
	return reviewedIndex[strings.ToLower(name)][strings.ToLower(extension)]
}
