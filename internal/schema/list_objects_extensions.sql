-- $1 row limit. Every installed extension, whatever the schema asked for.
SELECT e.extname::text AS name, e.extversion, n.nspname::text AS schema, e.extrelocatable
FROM pg_extension e
JOIN pg_namespace n ON n.oid = e.extnamespace
ORDER BY e.extname COLLATE "C"
LIMIT $1
