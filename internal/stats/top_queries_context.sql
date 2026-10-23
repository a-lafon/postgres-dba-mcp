-- Context line of get_top_queries: current database and last statistics reset (UTC, to the second).
SELECT current_database()::text,
       COALESCE(to_char(stats_reset AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:MI:SS') || '+00', 'unknown')
FROM pg_stat_statements_info
