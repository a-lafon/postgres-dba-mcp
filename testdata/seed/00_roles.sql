-- Roles of the test fixture. Later seed files put their objects in their own schema and grant pgdba_ro themselves.
CREATE ROLE pgdba_ro LOGIN PASSWORD 'pgdba';
GRANT pg_read_all_stats TO pgdba_ro;
GRANT USAGE ON SCHEMA public TO pgdba_ro;
ALTER DEFAULT PRIVILEGES IN SCHEMA public GRANT SELECT ON TABLES TO pgdba_ro;

CREATE ROLE pgdba_rw LOGIN PASSWORD 'pgdba';
CREATE SCHEMA app_rw AUTHORIZATION pgdba_rw;
GRANT USAGE ON SCHEMA app_rw TO pgdba_ro;
