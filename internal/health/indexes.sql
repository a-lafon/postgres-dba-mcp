-- Index list shared by the invalid and duplicate checks. Adapted from crystaldba/postgres-mcp 0.3.0 (PgHero):
-- key columns come from pg_index.indkey and pg_attribute instead of parsing pg_get_indexdef; INCLUDE columns are left out.
SELECT
    ui.schemaname AS schema,
    t.relname AS table,
    ix.relname AS name,
    am.amname AS using,
    ARRAY(
        SELECT coalesce(a.attname::text, '<expr>')
        FROM unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
        LEFT JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
        WHERE k.ord <= i.indnkeyatts
        ORDER BY k.ord
    ) AS columns,
    -- per key column: operator class, collation and options (DESC, NULLS FIRST) oids, which the duplicate check compares
    ARRAY(
        SELECT k.opc::text || '/' || k.coll::text || '/' || k.opt::text
        FROM unnest(i.indclass::oid[], i.indcollation::oid[], i.indoption::int2[]) WITH ORDINALITY AS k(opc, coll, opt, ord)
        WHERE k.ord <= i.indnkeyatts
        ORDER BY k.ord
    ) AS key_opts,
    i.indisunique AS "unique",
    i.indisprimary AS "primary",
    i.indisvalid AS valid,
    coalesce(i.indexprs::text, '') AS exprs,
    coalesce(i.indpred::text, '') AS pred,
    -- schema.name of the root of the partition tree the table belongs to, '' for other tables
    coalesce((SELECT rn.nspname || '.' || rc.relname FROM pg_class rc JOIN pg_namespace rn ON rn.oid = rc.relnamespace
              WHERE rc.oid = pg_partition_root(t.oid)), '') AS root
FROM pg_index i
INNER JOIN pg_class t ON t.oid = i.indrelid
INNER JOIN pg_class ix ON ix.oid = i.indexrelid
INNER JOIN pg_am am ON am.oid = ix.relam
INNER JOIN pg_stat_user_indexes ui ON ui.indexrelid = i.indexrelid
ORDER BY 1, 2, 3
