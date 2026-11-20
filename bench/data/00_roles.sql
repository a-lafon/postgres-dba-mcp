-- Roles and extensions of the bench database. 99_grants.sql, after the load, gives bench_ro SELECT on every table and
-- makes bench_owner the owner of the bench tables.
CREATE ROLE bench_ro LOGIN PASSWORD 'bench';
GRANT pg_read_all_stats TO bench_ro;
GRANT USAGE ON SCHEMA public TO bench_ro;
-- The realistic misconfiguration of PROTOCOL.md §3: the application's role, which owns the tables and can write, but
-- is not superuser.
CREATE ROLE bench_owner LOGIN PASSWORD 'bench' NOSUPERUSER NOCREATEDB NOCREATEROLE;
GRANT USAGE, CREATE ON SCHEMA public TO bench_owner;
CREATE EXTENSION IF NOT EXISTS hypopg;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
