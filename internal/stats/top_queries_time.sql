-- Top statements of the current database. The ORDER BY column is an allowlisted name chosen in Go.
SELECT calls::text,
       round(total_exec_time::numeric, 1)::text AS total_ms,
       round(mean_exec_time::numeric, 1)::text AS mean_ms,
       rows::text,
       regexp_replace(query, '\s+', ' ', 'g') AS query
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
ORDER BY {{order}} DESC
LIMIT $1
