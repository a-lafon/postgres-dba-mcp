-- $1 relation oid. Primary key first, then unique indexes, then the others, each by name.
SELECT ic.relname::text AS name, pg_get_indexdef(i.indexrelid) AS def, i.indisprimary, i.indisunique, i.indisvalid
FROM pg_index i
JOIN pg_class ic ON ic.oid = i.indexrelid
WHERE i.indrelid = $1::oid
ORDER BY i.indisprimary DESC, i.indisunique DESC, ic.relname COLLATE "C"
