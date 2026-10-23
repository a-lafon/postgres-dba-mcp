-- Readable sequences with the share of their range already used and the column that owns them.
-- last_value is NULL for a sequence never used or not readable by the role: those rows are left out.
SELECT
    s.schemaname AS schema,
    s.sequencename AS name,
    s.data_type::text AS data_type,
    coalesce(o.nspname, '') AS owner_schema,
    coalesce(o.relname, '') AS owner_table,
    coalesce(o.attname, '') AS owner_column,
    -- Share of the range consumed: from min toward max when ascending, from max toward min when descending.
    CASE WHEN s.increment_by > 0
        THEN (s.last_value::float8 - s.min_value::float8) / (s.max_value::float8 - s.min_value::float8)
        ELSE (s.max_value::float8 - s.last_value::float8) / (s.max_value::float8 - s.min_value::float8)
    END AS used,
    CASE WHEN s.increment_by > 0
        THEN s.max_value::float8 - s.last_value::float8
        ELSE s.last_value::float8 - s.min_value::float8
    END AS remaining
FROM pg_sequences s
INNER JOIN pg_namespace sn ON sn.nspname = s.schemaname
INNER JOIN pg_class sc ON sc.relnamespace = sn.oid AND sc.relname = s.sequencename
LEFT JOIN LATERAL (
    SELECT tn.nspname, t.relname, a.attname
    FROM pg_depend d
    INNER JOIN pg_class t ON t.oid = d.refobjid
    INNER JOIN pg_namespace tn ON tn.oid = t.relnamespace
    INNER JOIN pg_attribute a ON a.attrelid = d.refobjid AND a.attnum = d.refobjsubid
    WHERE d.classid = 'pg_class'::regclass
        AND d.objid = sc.oid
        AND d.refclassid = 'pg_class'::regclass
        AND d.refobjsubid > 0
        AND d.deptype IN ('a', 'i')
    LIMIT 1
) o ON true
WHERE s.last_value IS NOT NULL
ORDER BY remaining, s.schemaname, s.sequencename
