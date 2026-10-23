-- User tables with many dead rows. $1 is the minimum dead rows, $2 the minimum dead share of all rows.
-- The share is compared by multiplication, so an empty table cannot divide by zero.
SELECT
    schemaname AS schema,
    relname AS table,
    n_dead_tup AS dead,
    n_live_tup AS live,
    greatest(last_vacuum, last_autovacuum) AS last_vacuum
FROM pg_stat_user_tables
WHERE n_dead_tup >= $1::bigint
    AND n_dead_tup::float8 >= $2::float8 * (n_live_tup + n_dead_tup)::float8
ORDER BY n_dead_tup DESC, schemaname, relname
