-- $1 relation oid. Direct partitions with their bounds.
SELECT n.nspname::text AS schema, c.relname::text AS name, pg_get_expr(c.relpartbound, c.oid) AS bound
FROM pg_inherits i
JOIN pg_class c ON c.oid = i.inhrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE i.inhparent = $1::oid
ORDER BY c.relname COLLATE "C"
