-- Cluster-wide session counts, as crystaldba/postgres-mcp does.
SELECT
    count(*) AS total,
    count(*) FILTER (WHERE state = 'idle in transaction') AS idle_in_transaction
FROM pg_stat_activity
