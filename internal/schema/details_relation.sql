-- $1 schema name, $2 object name. No row when the relation does not exist. Sizes and rows of a partitioned
-- table sum its leaf partitions.
SELECT c.oid AS oid,
       c.relkind::text,
       CASE WHEN c.relkind = 'p'
            THEN (SELECT sum(l.reltuples)::bigint FROM pg_partition_tree(c.oid) t
                  JOIN pg_class l ON l.oid = t.relid WHERE t.isleaf AND l.reltuples >= 0)
            WHEN c.reltuples >= 0 THEN c.reltuples::bigint
       END AS rows,
       CASE WHEN c.relkind = 'p'
            THEN coalesce((SELECT sum(pg_table_size(t.relid)) FROM pg_partition_tree(c.oid) t WHERE t.isleaf), 0)::bigint
            ELSE pg_table_size(c.oid)
       END AS table_size,
       CASE WHEN c.relkind = 'p'
            THEN coalesce((SELECT sum(pg_indexes_size(t.relid)) FROM pg_partition_tree(c.oid) t WHERE t.isleaf), 0)::bigint
            ELSE pg_indexes_size(c.oid)
       END AS index_size,
       obj_description(c.oid, 'pg_class') AS comment,
       CASE WHEN c.relkind = 'p' THEN pg_get_partkeydef(c.oid) END AS partition_key,
       pn.nspname::text AS parent_schema,
       pc.relname::text AS parent_name,
       pg_get_expr(c.relpartbound, c.oid) AS bound,
       CASE WHEN c.relkind IN ('v', 'm') THEN pg_get_viewdef(c.oid, true) END AS definition
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
LEFT JOIN pg_inherits i ON i.inhrelid = c.oid AND c.relispartition
LEFT JOIN pg_class pc ON pc.oid = i.inhparent
LEFT JOIN pg_namespace pn ON pn.oid = pc.relnamespace
WHERE n.nspname = $1 AND c.relname = $2
