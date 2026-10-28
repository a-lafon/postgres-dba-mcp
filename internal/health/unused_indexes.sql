-- Adapted from crystaldba/postgres-mcp 0.3.0: the scan limit is the bind parameter $1.
SELECT
    schemaname AS schema,
    indexrelname AS index,
    pg_relation_size(i.indexrelid) AS size_bytes,
    idx_scan AS scans,
    relname AS table,
    -- schema.name of the root of the partition tree the table belongs to, '' for other tables
    coalesce((SELECT rn.nspname || '.' || rc.relname FROM pg_class rc JOIN pg_namespace rn ON rn.oid = rc.relnamespace
              WHERE rc.oid = pg_partition_root(i.indrelid)), '') AS root,
    w.since AS stats_since,
    extract(day FROM now() - w.since)::int AS stats_days
FROM pg_stat_user_indexes ui
INNER JOIN pg_index i ON ui.indexrelid = i.indexrelid
-- start of the window idx_scan counts over: statistics survive a clean restart, so the postmaster start only
-- stands in for a never-reset database (NULL stats_reset)
CROSS JOIN (SELECT coalesce((SELECT stats_reset FROM pg_stat_database WHERE datname = current_database()),
                            pg_postmaster_start_time()) AS since) w
WHERE NOT indisunique
    AND idx_scan <= $1::bigint
ORDER BY
    pg_relation_size(i.indexrelid) DESC,
    relname ASC,
    indexrelname ASC
