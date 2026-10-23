-- Traps T1-T6 for analyze_db_health, all in schema traps so that public.events stays free of extra indexes.
-- T6 is sized by the psql variable :scale.
SELECT setseed(0.42);
CREATE SCHEMA traps;

-- T1: invalid index. A unique index built CONCURRENTLY on a duplicated column fails and stays INVALID. psql runs each
-- statement outside a transaction block, and the expected error must not stop the load.
CREATE TABLE traps.inval (id int PRIMARY KEY, val int NOT NULL);
INSERT INTO traps.inval SELECT i, i % 100 FROM generate_series(1, 1000) AS i;
\set ON_ERROR_STOP off
CREATE UNIQUE INDEX CONCURRENTLY inval_val_uidx ON traps.inval (val);
\set ON_ERROR_STOP on

-- T2: duplicate pair, the first covered by the second.
CREATE TABLE traps.dup_events (id int PRIMARY KEY, account_id int NOT NULL, created_at timestamptz NOT NULL);
INSERT INTO traps.dup_events
SELECT i, 1 + i % 500, timestamptz '2025-01-01 00:00:00+00' + (i || ' seconds')::interval
FROM generate_series(1, 20000) AS i;
CREATE INDEX dup_events_acc_idx ON traps.dup_events (account_id);
CREATE INDEX dup_events_acc_created_idx ON traps.dup_events (account_id, created_at);

-- T3: dead tuples. Autovacuum is off and 60 % of the rows are updated once: 37.5 % of the tuples are dead.
CREATE TABLE traps.bloated (id int PRIMARY KEY, n int NOT NULL, pad text) WITH (autovacuum_enabled = false);
INSERT INTO traps.bloated SELECT i, i, repeat('x', 20) FROM generate_series(1, 200000) AS i;
UPDATE traps.bloated SET n = n + 1 WHERE id % 5 < 3;

-- T4: int4 sequence at 91 % of its maximum.
CREATE SEQUENCE traps.legacy_id_seq AS integer;
CREATE TABLE traps.legacy (id integer NOT NULL DEFAULT nextval('traps.legacy_id_seq'), note text);
ALTER SEQUENCE traps.legacy_id_seq OWNED BY traps.legacy.id;
SELECT setval('traps.legacy_id_seq', 1954210119);

-- T5: NOT VALID constraint.
CREATE TABLE traps.orders_nv (id int PRIMARY KEY, amount numeric NOT NULL);
INSERT INTO traps.orders_nv VALUES (1, 10), (2, -5);
ALTER TABLE traps.orders_nv ADD CONSTRAINT amount_positive CHECK (amount > 0) NOT VALID;

-- T6: index over 100 MB at scale 1 that nothing ever scans (2 000 000 rows of 64 characters).
SELECT greatest(1000, (2000000 * :scale)::int) AS n_unused \gset
CREATE TABLE traps.unused (id int PRIMARY KEY, payload_hash text NOT NULL);
INSERT INTO traps.unused SELECT i, md5(i::text) || md5((i + 1)::text) FROM generate_series(1, :n_unused) AS i;
CREATE INDEX unused_payload_hash_idx ON traps.unused (payload_hash);
