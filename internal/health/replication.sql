-- Role, replica and slot counts, receiver state and lag, with one row per inactive slot (or one row with a NULL slot).
-- On a replica, streaming comes from pg_stat_wal_receiver; lag is 0 when receive and replay positions are equal.
WITH st AS (
    SELECT
        pg_is_in_recovery() AS replica,
        (SELECT count(*) FROM pg_stat_replication) AS replicas,
        (SELECT count(*) FROM pg_replication_slots) AS slots,
        EXISTS (SELECT 1 FROM pg_stat_wal_receiver WHERE status = 'streaming') AS streaming,
        CASE
            WHEN NOT pg_is_in_recovery() THEN NULL
            WHEN pg_last_wal_receive_lsn() = pg_last_wal_replay_lsn() THEN 0
            ELSE EXTRACT(EPOCH FROM now() - pg_last_xact_replay_timestamp())
        END::float8 AS lag
)
SELECT st.replica, st.replicas, st.slots, st.streaming, st.lag,
    s.slot_name, s.slot_type, s.database
FROM st
LEFT JOIN pg_replication_slots s ON NOT s.active
ORDER BY s.slot_name
