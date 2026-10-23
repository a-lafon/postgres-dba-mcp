-- Statements of the current database holding more than 5 % of a resource. A zero sum gives a NULL share.
WITH s AS (
    SELECT query, calls, rows, total_exec_time, mean_exec_time,
           total_exec_time::numeric / NULLIF(SUM(total_exec_time::numeric) OVER (), 0) AS time_frac,
           (shared_blks_hit + shared_blks_read)::numeric
               / NULLIF(SUM((shared_blks_hit + shared_blks_read)::numeric) OVER (), 0) AS accessed_frac,
           shared_blks_read::numeric / NULLIF(SUM(shared_blks_read::numeric) OVER (), 0) AS read_frac,
           shared_blks_dirtied::numeric / NULLIF(SUM(shared_blks_dirtied::numeric) OVER (), 0) AS dirtied_frac,
           wal_bytes::numeric / NULLIF(SUM(wal_bytes::numeric) OVER (), 0) AS wal_frac
    FROM pg_stat_statements
    WHERE dbid = (SELECT oid FROM pg_database WHERE datname = current_database())
)
SELECT calls::text,
       round(total_exec_time::numeric, 1)::text AS total_ms,
       round(mean_exec_time::numeric, 1)::text AS mean_ms,
       round(time_frac * 100, 1)::text AS time_pct,
       round(accessed_frac * 100, 1)::text AS blks_accessed_pct,
       round(read_frac * 100, 1)::text AS blks_read_pct,
       round(dirtied_frac * 100, 1)::text AS blks_dirtied_pct,
       round(wal_frac * 100, 1)::text AS wal_pct,
       regexp_replace(query, '\s+', ' ', 'g') AS query
FROM s
WHERE time_frac > 0.05 OR accessed_frac > 0.05 OR read_frac > 0.05 OR dirtied_frac > 0.05 OR wal_frac > 0.05
ORDER BY total_exec_time DESC
LIMIT $1
