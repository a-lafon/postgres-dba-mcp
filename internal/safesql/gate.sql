-- Resolves every referenced function name the way the server would, then keeps
-- the ones that PostgreSQL does not ship and that are declared VOLATILE. $1
-- holds the schema of each reference ('' when the call is unqualified) and $2
-- the matching name. 16384 is FirstNormalObjectId: below it the function is
-- built in, and builtinDeny and catalog_volatile.txt already classify it.
WITH ref AS (
    SELECT ord, schema, name
      FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS r(schema, name, ord)
),
visible AS (
    SELECT nspname FROM unnest(current_schemas(true)) AS s(nspname)
),
-- Every function the call can reach: an unqualified name is looked up in each
-- schema of the search path, not only in the first one holding it. Which
-- overload the server picks follows from the argument types, which this query
-- does not know, so a name counts as soon as one reachable candidate is
-- volatile: an earlier schema does not hide a later one it does not shadow.
cand AS (
    SELECT r.ord, p.oid, p.provolatile
      FROM ref r
      JOIN pg_proc p ON p.proname = r.name
      JOIN pg_namespace n ON n.oid = p.pronamespace
      LEFT JOIN visible v ON v.nspname = n.nspname
     WHERE (r.schema <> '' AND n.nspname = r.schema)
        OR (r.schema = '' AND v.nspname IS NOT NULL)
)
SELECT DISTINCT c.ord, coalesce(e.extname, '')
  FROM cand c
  LEFT JOIN pg_depend d
         ON d.classid = 'pg_proc'::regclass AND d.objid = c.oid AND d.deptype = 'e'
  LEFT JOIN pg_extension e ON e.oid = d.refobjid
 WHERE c.oid >= 16384 AND c.provolatile = 'v'
