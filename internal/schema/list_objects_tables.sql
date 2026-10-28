-- $1 schema name, $2 row limit. Partitions are not listed: a partitioned table carries the number of its leaf
-- partitions in its kind and sums their rows and sizes (get_object_details lists them).
SELECT c.relname::text AS name,
       CASE WHEN c.relkind = 'p'
            THEN 'partitioned (' || (SELECT count(*) FROM pg_partition_tree(c.oid) t WHERE t.isleaf) || ' partitions)'
            ELSE 'table'
       END AS kind,
       CASE WHEN c.relkind = 'p'
            THEN (SELECT sum(l.reltuples)::bigint FROM pg_partition_tree(c.oid) t
                  JOIN pg_class l ON l.oid = t.relid WHERE t.isleaf AND l.reltuples >= 0)
            WHEN c.reltuples >= 0 THEN c.reltuples::bigint
       END AS rows,
       CASE WHEN c.relkind = 'p'
            THEN coalesce((SELECT sum(pg_total_relation_size(t.relid)) FROM pg_partition_tree(c.oid) t WHERE t.isleaf), 0)::bigint
            ELSE pg_total_relation_size(c.oid)
       END AS size,
       coalesce(has_schema_privilege(c.relnamespace, 'USAGE') AND has_table_privilege(c.oid, 'SELECT'), false) AS readable,
       current_user::text AS role
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('r', 'p') AND NOT c.relispartition
ORDER BY c.relname COLLATE "C"
LIMIT $2
