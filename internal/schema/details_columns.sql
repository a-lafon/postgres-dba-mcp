-- $1 relation oid. attidentity is a, d or empty; attgenerated is s, v or empty.
SELECT a.attname::text AS name,
       format_type(a.atttypid, a.atttypmod) AS type,
       a.attnotnull,
       a.attidentity::text AS identity,
       a.attgenerated::text AS generated,
       pg_get_expr(d.adbin, d.adrelid) AS default_expr,
       col_description(a.attrelid, a.attnum) AS comment
FROM pg_attribute a
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE a.attrelid = $1::oid AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum
