-- $1 schema name, $2 row limit.
SELECT c.relname::text AS name, CASE c.relkind WHEN 'm' THEN 'materialized' ELSE 'view' END AS kind
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('v', 'm')
ORDER BY c.relname COLLATE "C"
LIMIT $2
