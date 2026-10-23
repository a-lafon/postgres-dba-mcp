-- $1 relation oid. Primary keys, foreign keys and checks of that relation only (never joined by name).
SELECT con.conname::text AS name,
       con.contype::text AS type,
       pg_get_constraintdef(con.oid) AS def,
       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord) AS columns,
       rn.nspname::text AS ref_schema,
       rc.relname::text AS ref_table,
       ARRAY(SELECT a.attname::text FROM unnest(con.confkey) WITH ORDINALITY k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.confrelid AND a.attnum = k.attnum ORDER BY k.ord) AS ref_columns,
       con.confdeltype::text AS on_delete,
       con.confupdtype::text AS on_update
FROM pg_constraint con
LEFT JOIN pg_class rc ON rc.oid = con.confrelid
LEFT JOIN pg_namespace rn ON rn.oid = rc.relnamespace
WHERE con.conrelid = $1::oid AND con.contype IN ('p', 'f', 'c')
ORDER BY con.conname COLLATE "C"
