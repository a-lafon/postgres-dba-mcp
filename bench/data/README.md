# Bench data set

The database of [BENCHMARK.md](../../BENCHMARK.md), as plain `psql` and `pgbench` input. The server is PostgreSQL 17 started with `-c shared_preload_libraries=pg_stat_statements -c pg_stat_statements.track=all`, with the `hypopg` extension installed (the image of [testdata/docker](../../testdata/docker) has both). The measures used `scale=1`; run the commands as a superuser.

```sh
createdb bench
for f in 00_roles.sql 10_base.sql; do psql -v ON_ERROR_STOP=1 -v scale=1 -d bench -f "$f"; done
# Pagila: the two files of pagila.lock, at the pinned commit, checked by SHA-256
psql -v ON_ERROR_STOP=1 -d bench -f pagila-schema.sql
psql -v ON_ERROR_STOP=1 -d bench -f pagila-data.sql
for f in 20_traps.sql 30_docs.sql 99_grants.sql; do psql -v ON_ERROR_STOP=1 -v scale=1 -d bench -f "$f"; done
psql -d bench -c 'ANALYZE' -c 'SELECT pg_stat_statements_reset()'
pgbench -n -f workload.pgbench -T 120 -c 4 bench
```

Both servers then connect as the read-only role `bench_ro` created by `00_roles.sql`.
