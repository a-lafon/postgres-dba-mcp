-- $1 schema name, $2 row limit. last_value is NULL before the first nextval or without privilege.
SELECT sequencename::text AS name, data_type::text, last_value, min_value, max_value, increment_by
FROM pg_sequences
WHERE schemaname = $1
ORDER BY sequencename COLLATE "C"
LIMIT $2
