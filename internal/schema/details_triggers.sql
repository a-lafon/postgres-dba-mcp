-- $1 relation oid. tgtype is the bit mask decoded in Go.
SELECT tgname::text AS name, tgtype::int AS type
FROM pg_trigger
WHERE tgrelid = $1::oid AND NOT tgisinternal
ORDER BY tgname COLLATE "C"
