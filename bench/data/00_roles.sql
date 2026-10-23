-- Role and extensions of the bench database. 99_grants.sql gives bench_ro SELECT on every table, after the load.
CREATE ROLE bench_ro LOGIN PASSWORD 'bench';
GRANT pg_read_all_stats TO bench_ro;
GRANT USAGE ON SCHEMA public TO bench_ro;
CREATE EXTENSION IF NOT EXISTS hypopg;
CREATE EXTENSION IF NOT EXISTS pg_stat_statements;
