-- Cache hit rates of tables and indexes, as in crystaldba/postgres-mcp 0.3.0 (NULL when nothing was read yet),
-- plus the tables that account for at least 10 % of the heap reads, at most 3, largest first (the partitions of a
-- tree count as their root). Not in crystaldba.
SELECT
    (SELECT sum(heap_blks_hit) / nullif(sum(heap_blks_hit + heap_blks_read), 0)
     FROM pg_statio_user_tables)::float8 AS tables,
    (SELECT sum(idx_blks_hit) / nullif(sum(idx_blks_hit + idx_blks_read), 0)
     FROM pg_statio_user_indexes)::float8 AS indexes,
    coalesce((
        SELECT jsonb_agg(jsonb_build_object('table', g.name, 'share', g.reads::float8 / g.total, 'seq_scans', g.seq_scans)
                         ORDER BY g.reads DESC, g.name)
        -- The limit sits inside: on the aggregate itself it would apply to its single output row.
        FROM (
            SELECT * FROM (
                SELECT n.nspname || '.' || c.relname AS name, sum(p.reads) AS reads, sum(p.seq_scans)::bigint AS seq_scans,
                       sum(sum(p.reads)) OVER () AS total
                FROM (
                    SELECT coalesce(pg_partition_root(io.relid), io.relid) AS oid, io.heap_blks_read AS reads, st.seq_scan AS seq_scans
                    FROM pg_statio_user_tables io
                    JOIN pg_stat_user_tables st ON st.relid = io.relid
                ) p
                JOIN pg_class c ON c.oid = p.oid
                JOIN pg_namespace n ON n.oid = c.relnamespace
                GROUP BY n.nspname, c.relname
            ) all_tables
            WHERE all_tables.total > 0 AND all_tables.reads::float8 / all_tables.total >= 0.1
            ORDER BY all_tables.reads DESC, all_tables.name
            LIMIT 3
        ) g
    ), '[]'::jsonb) AS culprits
