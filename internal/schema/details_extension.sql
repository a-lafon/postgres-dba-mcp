-- $1 extension name. No row when it is not installed.
SELECT e.extversion, n.nspname::text AS schema, e.extrelocatable, d.description
FROM pg_extension e
JOIN pg_namespace n ON n.oid = e.extnamespace
LEFT JOIN pg_description d ON d.objoid = e.oid AND d.classoid = 'pg_extension'::regclass AND d.objsubid = 0
WHERE e.extname = $1
