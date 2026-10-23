-- Resolves the relations of the workload ($1 schemas, $2 names, "" schema = search_path) to their columns.
-- long_text follows crystaldba/postgres-mcp 0.3.0 (DTA, long text columns): text, or varchar/char unbounded or
-- longer than 100, whose average width is unknown or above 40.
WITH req AS (
    SELECT ord, s, n
    FROM unnest($1::text[], $2::text[]) WITH ORDINALITY AS r(s, n, ord)
), rel AS (
    SELECT req.ord, c.oid, c.relkind::text AS relkind, ns.nspname, c.relname,
           quote_ident(ns.nspname) || '.' || quote_ident(c.relname) AS qname,
           pg_total_relation_size(c.oid) AS total_size,
           EXISTS (SELECT 1 FROM pg_stat_user_tables st
                   WHERE st.relid = c.oid AND (st.last_analyze IS NOT NULL OR st.last_autoanalyze IS NOT NULL)) AS analyzed
    FROM req
    JOIN pg_class c ON c.oid = to_regclass(CASE WHEN req.s = '' THEN quote_ident(req.n)
                                                ELSE quote_ident(req.s) || '.' || quote_ident(req.n) END)
    JOIN pg_namespace ns ON ns.oid = c.relnamespace
)
SELECT rel.ord::int AS ord, rel.oid::bigint AS oid, rel.relkind, rel.nspname, rel.relname, rel.qname,
       rel.total_size, rel.analyzed,
       a.attname::text AS attname, quote_ident(a.attname) AS qattname,
       ((a.atttypid = 'text'::regtype
         OR (a.atttypid IN ('varchar'::regtype, 'bpchar'::regtype) AND (a.atttypmod = -1 OR a.atttypmod - 4 > 100)))
        AND coalesce((SELECT max(ps.avg_width) FROM pg_stats ps
                      WHERE ps.schemaname = rel.nspname AND ps.tablename = rel.relname AND ps.attname = a.attname), 41) > 40
       ) AS long_text
FROM rel
JOIN pg_attribute a ON a.attrelid = rel.oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY rel.ord, a.attnum
