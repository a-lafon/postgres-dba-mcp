-- Adapted from crystaldba/postgres-mcp 0.3.0 (DTA workload): current database, top-level statements with a readable
-- text only, thresholds as bind parameters, deterministic order.
SELECT queryid, query, calls, total_exec_time, mean_exec_time
FROM pg_stat_statements
WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
  AND toplevel
  AND query IS NOT NULL
  AND calls >= $1::bigint
  AND mean_exec_time >= $2::float8
ORDER BY total_exec_time DESC, queryid
LIMIT 100
