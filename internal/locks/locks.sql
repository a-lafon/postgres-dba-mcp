-- One row per (session waiting on a lock, session directly blocking it), from one snapshot of pg_locks and
-- pg_stat_activity. pg_blocking_pids() is the server's own answer to "who blocks this pid": it needs no
-- privilege, reports parallel workers through their leader, and spares us PostgreSQL's lock conflict matrix.
WITH waiting AS (
    -- A waiting session has one ungranted lock row; DISTINCT ON keeps the report to one lock per session
    -- should the snapshot catch a second one. The full ORDER BY is the tie-break: without it the kept row
    -- would be arbitrary and the lock cell would change between two calls on the same contention.
    SELECT DISTINCT ON (pid) pid, locktype, mode, database, relation
    FROM pg_locks
    WHERE NOT granted
    ORDER BY pid, locktype, mode, relation NULLS LAST
)
SELECT
    w.pid AS blocked_pid,
    -- pg_stat_activity has no timestamp for the start of a lock wait, so this is the age of the blocked
    -- statement: an upper bound on the wait, exact when the statement blocked as soon as it started.
    (now() - a.state_change)::interval(0)::text AS waiting,
    w.locktype || ' ' || w.mode || coalesce(' on ' || n.nspname || '.' || c.relname, '') AS lock,
    bp.pid AS blocking_pid,
    b.state AS blocking_state,
    (now() - b.xact_start)::interval(0)::text AS tx_age,
    -- The literal pg_stat_activity returns instead of the text is no value to report: NULL leaves the cell empty.
    nullif(b.query, '<insufficient privilege>') AS blocking_query,
    -- USAGE, not MEMBER: from PostgreSQL 15 on, pg_stat_activity masks on has_privs_of_role, so a NOINHERIT
    -- member of pg_read_all_stats is a member that still reads nothing, and MEMBER here would hide the note
    -- above an all-NULL table. PostgreSQL 14 masks on mere membership, where that role does read the details;
    -- the note is gated on a hidden cell as well, so it stays right on both.
    pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') AS has_stats,
    current_user::text AS role,
    -- The chains are ordered by the age of the root transaction, which the text form cannot sort past a day.
    extract(epoch FROM now() - b.xact_start)::float8 AS tx_seconds
FROM waiting w
CROSS JOIN LATERAL unnest(pg_blocking_pids(w.pid)) AS bp(pid)
LEFT JOIN pg_stat_activity a ON a.pid = w.pid
LEFT JOIN pg_stat_activity b ON b.pid = bp.pid
-- pg_locks is cluster-wide but pg_class is not: an OID of another database would name the wrong relation.
LEFT JOIN pg_class c ON c.oid = w.relation
    AND w.database = (SELECT oid FROM pg_database WHERE datname = current_database())
LEFT JOIN pg_namespace n ON n.oid = c.relnamespace
-- pg_locks is cluster-wide, so without this the report carries waits of other databases, which the OID guard
-- above correctly leaves unnamed: rows an operator cannot act on. datname stays readable without the role.
WHERE a.datname = current_database()
ORDER BY a.state_change ASC NULLS LAST, w.pid
