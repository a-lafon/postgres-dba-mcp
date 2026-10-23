-- $1 relation oid. Foreign keys of other tables (or of itself) that point at it.
SELECT n.nspname::text AS schema,
       c.relname::text AS table,
       ARRAY(SELECT a.attname::text FROM unnest(con.conkey) WITH ORDINALITY k(attnum, ord)
             JOIN pg_attribute a ON a.attrelid = con.conrelid AND a.attnum = k.attnum ORDER BY k.ord) AS columns
FROM pg_constraint con
JOIN pg_class c ON c.oid = con.conrelid
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE con.confrelid = $1::oid AND con.contype = 'f'
ORDER BY n.nspname COLLATE "C", c.relname COLLATE "C", con.conname COLLATE "C"
