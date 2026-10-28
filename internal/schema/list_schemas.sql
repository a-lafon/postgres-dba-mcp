-- User schemas first, then system schemas, each by name. Counts ordinary and partitioned tables of user schemas only.
SELECT n.nspname::text AS schema_name,
       pg_get_userbyid(n.nspowner)::text AS owner,
       s.type,
       CASE WHEN s.type = 'user'
            THEN (SELECT count(*) FROM pg_class c WHERE c.relnamespace = n.oid AND c.relkind IN ('r', 'p'))
       END AS tables,
       coalesce(has_schema_privilege(n.oid, 'USAGE'), false) AS readable,
       current_user::text AS role
FROM pg_namespace n
CROSS JOIN LATERAL (
    SELECT CASE WHEN n.nspname = 'information_schema' OR left(n.nspname, 3) = 'pg_' THEN 'system' ELSE 'user' END AS type
) s
WHERE n.nspname <> 'pg_toast'
  AND left(n.nspname, 8) <> 'pg_temp_'
  AND left(n.nspname, 14) <> 'pg_toast_temp_'
ORDER BY s.type DESC, n.nspname COLLATE "C"
LIMIT $1
