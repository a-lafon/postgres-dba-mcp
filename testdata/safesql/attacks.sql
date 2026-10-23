-- case: write-insert write
INSERT INTO public.accounts (id, name) VALUES (999999, 'x');

-- case: write-update write
UPDATE public.accounts SET name = 'x' WHERE id = 1;

-- case: write-delete write
DELETE FROM public.events;

-- case: write-truncate write
TRUNCATE public.orders;

-- case: write-drop write
DROP TABLE public.orders;

-- case: write-create-table write
CREATE TABLE public.pwned (x int);

-- case: write-alter write
ALTER TABLE public.accounts ADD COLUMN pwned int;

-- case: write-create-function write
CREATE FUNCTION public.f() RETURNS int LANGUAGE sql AS 'SELECT 1';

-- case: write-grant write
GRANT ALL ON public.accounts TO PUBLIC;

-- case: write-vacuum write
VACUUM public.accounts;

-- case: write-refresh write
REFRESH MATERIALIZED VIEW public.some_view;

-- case: write-call write
CALL public.some_procedure();

-- case: write-merge write
MERGE INTO public.accounts a USING public.orders o ON a.id = o.account_id WHEN MATCHED THEN DELETE;

-- case: disguised-cte-delete disguised-write
WITH d AS (DELETE FROM public.events RETURNING *) SELECT count(*) FROM d;

-- case: disguised-cte-insert disguised-write
WITH i AS (INSERT INTO public.accounts (id, name) VALUES (999999, 'x') RETURNING *) SELECT * FROM i;

-- case: disguised-cte-update disguised-write
WITH u AS (UPDATE public.orders SET status = 'done' RETURNING id) SELECT * FROM u;

-- case: disguised-cte-nested disguised-write
SELECT * FROM (WITH d AS (DELETE FROM public.events RETURNING id) SELECT id FROM d) s;

-- case: disguised-cte-merge disguised-write
WITH m AS (MERGE INTO public.accounts a USING public.orders o ON a.id = o.account_id WHEN MATCHED THEN DELETE RETURNING a.id) SELECT * FROM m;

-- case: disguised-select-into disguised-write
SELECT * INTO public.pwned FROM public.accounts;

-- case: disguised-select-into-temp disguised-write
SELECT * INTO TEMP pwned FROM public.accounts;

-- case: disguised-select-into-subquery disguised-write
SELECT * FROM (SELECT * INTO public.pwned FROM public.accounts) s;

-- case: disguised-create-table-as disguised-write
CREATE TABLE public.pwned AS SELECT * FROM public.accounts;

-- case: disguised-explain-create-table-as disguised-write
EXPLAIN ANALYZE CREATE TABLE public.pwned AS SELECT * FROM public.accounts;

-- case: multi-drop multi
SELECT 1; DROP TABLE public.accounts;

-- case: multi-two-selects multi
SELECT 1; SELECT 2;

-- case: multi-comment-line multi
SELECT 1; -- harmless
DELETE FROM public.events;

-- case: multi-block-comment multi
SELECT 1; /* ; */ DELETE FROM public.events;

-- case: multi-dollar-quote multi
SELECT $$ ; $$; DELETE FROM public.events;

-- case: multi-dollar-tag multi
SELECT $a$ $$ ; $a$; DELETE FROM public.events;

-- case: multi-commit-first multi
BEGIN; SELECT 1; COMMIT;

-- case: multi-leading-statement multi
SET statement_timeout = 0; SELECT 1;

-- case: file-copy-to-file file
COPY public.accounts TO '/tmp/accounts.csv';

-- case: file-copy-to-program file
COPY public.accounts TO PROGRAM 'cat > /tmp/x';

-- case: file-copy-from-program file
COPY public.accounts FROM PROGRAM 'echo 1';

-- case: file-copy-from-file file
COPY public.accounts FROM '/etc/passwd';

-- case: file-copy-query file
COPY (SELECT * FROM public.accounts) TO '/tmp/accounts.csv';

-- case: file-copy-stdout file
COPY public.accounts TO STDOUT;

-- case: file-create-extension file
CREATE EXTENSION file_fdw;

-- case: file-create-foreign-table file
CREATE FOREIGN TABLE public.passwd (l text) SERVER s OPTIONS (filename '/etc/passwd');

-- case: txn-commit txn
COMMIT;

-- case: txn-rollback txn
ROLLBACK;

-- case: txn-begin txn
BEGIN;

-- case: txn-start-read-write txn
START TRANSACTION READ WRITE;

-- case: txn-set-transaction txn
SET TRANSACTION READ WRITE;

-- case: txn-set-session-characteristics txn
SET SESSION CHARACTERISTICS AS TRANSACTION READ WRITE;

-- case: txn-set-role txn
SET ROLE postgres;

-- case: txn-set-session-authorization txn
SET SESSION AUTHORIZATION postgres;

-- case: txn-set-local txn
SET LOCAL default_transaction_read_only = off;

-- case: txn-set-timeout txn
SET statement_timeout = 0;

-- case: txn-reset-all txn
RESET ALL;

-- case: txn-reset-one txn
RESET default_transaction_read_only;

-- case: txn-savepoint txn
SAVEPOINT s;

-- case: txn-discard txn
DISCARD ALL;

-- case: txn-prepare-transaction txn
PREPARE TRANSACTION 'x';

-- case: fn-lo-from fn
SELECT * FROM lo_import('/etc/passwd') AS t;

-- case: fn-lo-sub fn
SELECT (SELECT lo_unlink(1));

-- case: fn-lo-arg fn
SELECT length(lo_get(1));

-- case: fn-lo-cte fn
WITH c AS (SELECT lo_create(0)) SELECT * FROM c;

-- case: fn-lo-lateral fn
SELECT a.id, l.* FROM public.accounts a, LATERAL lowrite(0, a.name::bytea) AS l;

-- case: fn-settings-from fn
SELECT * FROM set_config('statement_timeout', '0', false);

-- case: fn-settings-sub fn
SELECT * FROM public.accounts WHERE id = (SELECT length(set_config('statement_timeout', '0', false)));

-- case: fn-settings-arg fn
SELECT upper(set_config('statement_timeout', '0', false));

-- case: fn-settings-cte fn
WITH c AS (SELECT set_config('statement_timeout', '0', false)) SELECT * FROM c;

-- case: fn-settings-lateral fn
SELECT * FROM public.accounts a, LATERAL set_config('search_path', a.name, false) s;

-- case: fn-advisory-from fn
SELECT * FROM pg_advisory_lock(1);

-- case: fn-advisory-sub fn
SELECT (SELECT pg_advisory_lock(1));

-- case: fn-advisory-arg fn
SELECT coalesce(pg_try_advisory_lock(1), false);

-- case: fn-notify-from fn
SELECT * FROM pg_notify('chan', 'payload') AS t;

-- case: fn-notify-arg fn
SELECT coalesce(pg_notify('chan', 'payload')::text, '');

-- case: fn-sessions-from fn
SELECT * FROM pg_terminate_backend(12345) AS t;

-- case: fn-sessions-cte fn
WITH c AS (SELECT pg_terminate_backend(123)) SELECT * FROM c;

-- case: fn-sessions-lateral fn
SELECT s.pid, c.* FROM pg_catalog.pg_stat_activity s, LATERAL pg_cancel_backend(s.pid) AS c;

-- case: fn-files-from fn
SELECT * FROM pg_read_file('/etc/passwd') AS t;

-- case: fn-files-sub fn
SELECT (SELECT pg_read_binary_file('/etc/passwd'));

-- case: fn-files-lateral fn
SELECT a.id, f.* FROM public.accounts a, LATERAL pg_ls_dir('/') AS f;

-- case: fn-files-arg fn
SELECT length(pg_stat_file('/etc/passwd')::text);

-- case: fn-admin-from fn
SELECT * FROM pg_reload_conf() AS t;

-- case: fn-admin-sub fn
SELECT (SELECT pg_switch_wal());

-- case: fn-admin-arg fn
SELECT length(pg_create_restore_point('x')::text);

-- case: fn-replication-from fn
SELECT * FROM pg_create_logical_replication_slot('s', 'test_decoding');

-- case: fn-replication-cte fn
WITH c AS (SELECT pg_drop_replication_slot('s')) SELECT * FROM c;

-- case: fn-replication-arg fn
SELECT length(pg_logical_emit_message(true, 'p', 'm')::text);

-- case: fn-statreset-from fn
SELECT * FROM pg_stat_reset() AS t;

-- case: fn-statreset-sub fn
SELECT (SELECT pg_stat_statements_reset());

-- case: fn-index-from fn
SELECT * FROM brin_summarize_new_values('public.events_created_brin'::regclass) AS t;

-- case: fn-index-sub fn
SELECT (SELECT brin_summarize_range('public.events_created_brin'::regclass, 0));

-- case: fn-index-arg fn
SELECT coalesce(brin_desummarize_range('public.events_created_brin'::regclass, 0)::text, '');

-- case: fn-index-cte fn
WITH c AS (SELECT gin_clean_pending_list('public.docs_payload_gin'::regclass)) SELECT * FROM c;

-- case: fn-stats-from fn
SELECT * FROM pg_restore_relation_stats('schemaname', 'public', 'relname', 'events', 'reltuples', 1::real) AS t;

-- case: fn-stats-sub fn
SELECT (SELECT pg_clear_attribute_stats('public', 'events', 'account_id', false));

-- case: fn-dynsql-from fn
SELECT * FROM query_to_xml('delete from public.events', true, false, '') AS t;

-- case: fn-dynsql-arg fn
SELECT length(cursor_to_xml('c'::refcursor, 1, true, false, '')::text);

-- case: fn-dynsql-lateral fn
SELECT a.id, t.* FROM public.accounts a, LATERAL ts_stat('select to_tsvector(name) from public.accounts') AS t;

-- case: fn-dynsql-sub fn
SELECT (SELECT ts_rewrite('a'::tsquery, 'select ''a''::tsquery, ''b''::tsquery from query_to_xml(''delete from public.events'', true, false, '''')'));

-- case: fn-ext-from fn
SELECT * FROM dblink('host=localhost', 'select 1') AS t(x int);

-- case: fn-ext-sub fn
SELECT (SELECT dblink_exec('host=localhost', 'drop table public.accounts'));

-- case: fn-ext-arg fn
SELECT length(http_get('http://169.254.169.254/')::text);

-- case: fn-ext-cte fn
WITH c AS (SELECT net.http_post('http://example.com')) SELECT * FROM c;

-- case: fn-ext-lateral fn
SELECT a.id, f FROM public.accounts a, LATERAL pg_file_write('/tmp/x', a.name, false) AS f;

-- case: lock-for-update lock
SELECT * FROM public.accounts FOR UPDATE;

-- case: lock-for-no-key-update lock
SELECT * FROM public.accounts FOR NO KEY UPDATE;

-- case: lock-for-share lock
SELECT * FROM public.accounts FOR SHARE;

-- case: lock-for-key-share lock
SELECT * FROM public.accounts FOR KEY SHARE;

-- case: lock-skip-locked lock
SELECT * FROM public.orders WHERE status = 'pending' FOR UPDATE SKIP LOCKED LIMIT 1;

-- case: lock-subquery lock
SELECT * FROM (SELECT * FROM public.accounts FOR UPDATE) s;

-- case: lock-cte lock
WITH c AS (SELECT * FROM public.accounts FOR UPDATE) SELECT * FROM c;

-- case: lock-table lock
LOCK TABLE public.accounts IN ACCESS EXCLUSIVE MODE;

-- case: evasion-mixed-case-insert evasion
iNsErT iNtO public.accounts (id, name) VALUES (999999, 'x');

-- case: evasion-mixed-case-fn evasion
SELECT pg_catalog.SET_CONFIG('statement_timeout', '0', false);

-- case: evasion-quoted-upper-fn evasion
SELECT "SET_CONFIG"('statement_timeout', '0', false);

-- case: evasion-schema-qualified-fn evasion
SELECT public.lo_import('/etc/passwd');

-- case: evasion-pg-catalog-qualified-fn evasion
SELECT pg_catalog.pg_read_file('/etc/passwd');

-- case: evasion-quoted-schema-fn evasion
SELECT "pg_catalog"."pg_terminate_backend"(1);

-- case: evasion-comment-inside-name evasion
SELECT pg_/* x */terminate_backend(1);

-- case: evasion-explain-analyze-delete evasion
EXPLAIN ANALYZE DELETE FROM public.events;

-- case: evasion-explain-paren-update evasion
EXPLAIN (ANALYZE, BUFFERS) UPDATE public.accounts SET name = 'x';

-- case: evasion-explain-insert evasion
EXPLAIN INSERT INTO public.accounts (id, name) VALUES (999999, 'x');

-- case: evasion-prepare evasion
PREPARE p AS DELETE FROM public.events;

-- case: evasion-prepare-select evasion
PREPARE p AS SELECT 1;

-- case: evasion-execute evasion
EXECUTE p;

-- case: evasion-deallocate evasion
DEALLOCATE ALL;

-- case: evasion-do evasion
DO $$ BEGIN DELETE FROM public.events; END $$;

-- case: evasion-do-language evasion
DO LANGUAGE plpgsql $x$ BEGIN PERFORM pg_notify('c', 'p'); END $x$;

-- case: evasion-declare-cursor evasion
DECLARE c CURSOR FOR SELECT 1;

-- case: evasion-listen evasion
LISTEN chan;

-- case: evasion-notify evasion
NOTIFY chan, 'payload';

-- case: evasion-load evasion
LOAD 'auto_explain';

-- case: evasion-checkpoint evasion
CHECKPOINT;

-- case: evasion-syntax-error evasion
SELEC 1;

-- case: evasion-empty evasion
-- nothing here

-- case: evasion-unterminated-string evasion
SELECT 'abc;
