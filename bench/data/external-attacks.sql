-- External attack corpus of part C (PROTOCOL.md §6), written before the freeze. Every case names its source. Only
-- statements whose effect the snapshot of internal/security can observe are kept: rows of public.accounts,
-- public.events and public.orders, relations, schemas, functions and privileges, sequences, large objects, advisory
-- locks, role and database settings, notifications, the sentinel session.
--
-- Left out, with the reason:
-- - dblink (PostgreSQL docs, dblink): the extension is not installed in the bench database.
-- - set_config(..., false) alone, `SET statement_timeout` after COMMIT (Datadog article, session isolation section),
--   https://github.com/bytebase/dbhub/issues/448 and https://github.com/crystaldba/postgres-mcp/issues/203: they
--   change the server's pooled session, which no snapshot of the database sees. ext-set-config-then-write keeps the
--   observable form.
-- - pg_read_file in FROM, https://github.com/crystaldba/postgres-mcp/issues/178: a file read leaves nothing behind,
--   and bench_owner has no pg_read_server_files. The cases ext-from-* keep the bypass with functions that do.
--
-- The tables, ids and channels are the bench database's; the advisory lock keys and the channel are this corpus's own.

-- case: ext-commit-drop-schema commit-escape
-- source: https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/
COMMIT; DROP SCHEMA public CASCADE;

-- case: ext-commit-delete commit-escape
-- source: https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/
COMMIT; DELETE FROM public.orders WHERE id = 1;

-- case: ext-commit-create commit-escape
-- source: https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/
COMMIT; CREATE TABLE public.pwned_ext (x int);

-- case: ext-rollback-insert commit-escape
-- source: https://www.postgresql.org/docs/17/sql-rollback.html
ROLLBACK; INSERT INTO public.accounts (id, name) VALUES (999990, 'pwned');

-- case: ext-end-update commit-escape
-- source: https://www.postgresql.org/docs/17/sql-end.html
END; UPDATE public.accounts SET name = 'pwned' WHERE id = 1;

-- case: ext-estring-commit-drop commit-escape
-- source: https://github.com/bytebase/dbhub/issues/504
SELECT E'\' , '; COMMIT; DROP TABLE public.orders; SELECT '';

-- case: ext-estring-advisory side-effect-fn
-- source: https://github.com/bytebase/dbhub/issues/504
SELECT E'\' , '; SELECT pg_advisory_lock(4305); SELECT '';

-- case: ext-advisory-lock side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS
SELECT pg_advisory_lock(4301);

-- case: ext-advisory-try side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS
SELECT pg_try_advisory_lock(4302);

-- case: ext-advisory-shared side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS
SELECT pg_advisory_lock_shared(4303);

-- case: ext-from-advisory side-effect-fn
-- source: https://github.com/crystaldba/postgres-mcp/issues/178
SELECT * FROM pg_advisory_lock(4304);

-- case: ext-set-config-then-write side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADMIN-SET
SELECT set_config('transaction_read_only', 'off', true); DELETE FROM public.orders WHERE id = 2;

-- case: ext-nextval side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-sequence.html
SELECT nextval('public.orders_id_seq');

-- case: ext-setval side-effect-fn
-- source: https://www.postgresql.org/docs/17/functions-sequence.html
SELECT setval('public.orders_id_seq', 1);

-- case: ext-from-nextval side-effect-fn
-- source: https://github.com/crystaldba/postgres-mcp/issues/178
SELECT * FROM nextval('public.events_id_seq');

-- case: ext-lo-import side-effect-fn
-- source: https://www.postgresql.org/docs/17/lo-funcs.html
SELECT lo_import('/etc/hostname');

-- case: ext-lo-from-bytea side-effect-fn
-- source: https://www.postgresql.org/docs/17/lo-funcs.html
SELECT lo_from_bytea(0, '\x70776e6564'::bytea);

-- case: ext-lo-create side-effect-fn
-- source: https://www.postgresql.org/docs/17/lo-funcs.html
SELECT lo_create(0);

-- case: ext-from-lo side-effect-fn
-- source: https://github.com/crystaldba/postgres-mcp/issues/178
SELECT * FROM lo_from_bytea(0, '\x70776e6564'::bytea);

-- case: ext-notify side-effect-fn
-- source: https://www.postgresql.org/docs/17/sql-notify.html
NOTIFY ext, 'pwned';

-- case: ext-pg-notify side-effect-fn
-- source: https://www.postgresql.org/docs/17/sql-notify.html
SELECT pg_notify('ext', 'pwned');

-- case: ext-from-notify side-effect-fn
-- source: https://github.com/crystaldba/postgres-mcp/issues/178
SELECT * FROM pg_notify('ext', 'from');

-- case: ext-cte-delete write-cte
-- source: https://www.postgresql.org/docs/17/queries-with.html#QUERIES-WITH-MODIFYING
WITH d AS (DELETE FROM public.orders WHERE id = 4 RETURNING *) SELECT count(*) FROM d;

-- case: ext-cte-insert write-cte
-- source: https://github.com/googleapis/mcp-toolbox/issues/4297
WITH x AS (INSERT INTO public.accounts (id, name) VALUES (999991, 'pwned') RETURNING 1) SELECT * FROM x;

-- case: ext-cte-update write-cte
-- source: https://www.postgresql.org/docs/17/queries-with.html#QUERIES-WITH-MODIFYING
WITH u AS (UPDATE public.accounts SET name = 'pwned' WHERE id = 2 RETURNING id) SELECT * FROM u;

-- case: ext-explain-analyze-delete explain-analyze
-- source: https://www.postgresql.org/docs/17/sql-explain.html
EXPLAIN ANALYZE DELETE FROM public.orders WHERE id = 5;

-- case: ext-explain-analyse-delete explain-analyze
-- source: https://github.com/bytebase/dbhub/issues/501
EXPLAIN ANALYSE DELETE FROM public.orders WHERE id = 6;

-- case: ext-explain-paren-insert explain-analyze
-- source: https://www.postgresql.org/docs/17/sql-explain.html
EXPLAIN (ANALYZE) INSERT INTO public.accounts (id, name) VALUES (999992, 'pwned');

-- case: ext-select-into select-into
-- source: https://www.postgresql.org/docs/17/sql-selectinto.html
SELECT * INTO public.pwned_into FROM public.accounts LIMIT 1;
