-- $1 schema name, $2 sequence name, $3 sequence oid. The owner is the table column that depends on it (serial or identity).
SELECT s.data_type::text, s.start_value, s.increment_by, s.min_value, s.max_value, s.cycle, s.last_value,
       o.schema, o.table_name, o.column_name
FROM pg_sequences s
LEFT JOIN LATERAL (
    SELECT n.nspname::text AS schema, c.relname::text AS table_name, a.attname::text AS column_name
    FROM pg_depend d
    JOIN pg_class c ON c.oid = d.refobjid
    JOIN pg_namespace n ON n.oid = c.relnamespace
    JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
    WHERE d.classid = 'pg_class'::regclass AND d.objid = $3::oid
      AND d.refclassid = 'pg_class'::regclass AND d.deptype IN ('a', 'i')
    LIMIT 1
) o ON true
WHERE s.schemaname = $1 AND s.sequencename = $2
