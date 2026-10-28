-- $1 schema name, $2 row limit.
SELECT c.relname::text AS name, CASE c.relkind WHEN 'm' THEN 'materialized' ELSE 'view' END AS kind,
       coalesce(has_schema_privilege(c.relnamespace, 'USAGE') AND has_table_privilege(c.oid, 'SELECT'), false) AS readable,
       current_user::text AS role
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE n.nspname = $1 AND c.relkind IN ('v', 'm')
ORDER BY c.relname COLLATE "C"
LIMIT $2
