-- Valid plain btree indexes (no expression, no predicate) of the tables $1, as their ordered key column names.
SELECT i.indrelid::bigint AS oid, array_agg(a.attname::text ORDER BY k.ord) AS columns
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
JOIN pg_am am ON am.oid = ic.relam AND am.amname = 'btree'
CROSS JOIN LATERAL unnest(i.indkey::int2[]) WITH ORDINALITY AS k(attnum, ord)
JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = k.attnum
WHERE i.indrelid::bigint = ANY($1::bigint[])
  AND i.indisvalid AND i.indexprs IS NULL AND i.indpred IS NULL
  AND k.ord <= i.indnkeyatts
GROUP BY i.indexrelid, i.indrelid
ORDER BY i.indrelid, i.indexrelid
