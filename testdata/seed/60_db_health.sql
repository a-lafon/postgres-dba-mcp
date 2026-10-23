-- Traps for analyze_db_health, all in schema health. Nothing outside it is created or altered.
SELECT setseed(0.42);

CREATE SCHEMA health;
GRANT USAGE ON SCHEMA health TO pgdba_ro;

-- Invalid index. CREATE INDEX CONCURRENTLY cannot fail here (init scripts stop on errors), so the flag is set by hand.
CREATE TABLE health.dups (id int PRIMARY KEY, label text);
INSERT INTO health.dups SELECT i, 'label-' || i FROM generate_series(1, 1000) AS i;
CREATE INDEX ix_invalid ON health.dups (label);
UPDATE pg_index SET indisvalid = false WHERE indexrelid = 'health.ix_invalid'::regclass;

-- Duplicate pair: ix_dup_a is covered by ix_dup_b.
CREATE TABLE health.events_dup (id int PRIMARY KEY, account_id int NOT NULL, created_at timestamptz NOT NULL);
INSERT INTO health.events_dup
SELECT i, 1 + i % 500, timestamptz '2025-01-01 00:00:00+00' + (i || ' seconds')::interval
FROM generate_series(1, 10000) AS i;
CREATE INDEX ix_dup_a ON health.events_dup (account_id);
CREATE INDEX ix_dup_b ON health.events_dup (account_id, created_at);

-- Rarely used index of at least 1 MB.
CREATE TABLE health.unused_big (id int PRIMARY KEY, token text NOT NULL);
INSERT INTO health.unused_big SELECT i, md5(i::text) || md5((i + 1)::text) FROM generate_series(1, 60000) AS i;
CREATE INDEX ix_unused ON health.unused_big (token);

-- Autovacuum disabled; the index on n is bloated by two rounds of updates. The integration test makes the dead rows.
CREATE TABLE health.bloated (id int PRIMARY KEY, n int NOT NULL, pad text) WITH (autovacuum_enabled = false);
INSERT INTO health.bloated SELECT i, i, repeat('x', 20) FROM generate_series(1, 100000) AS i;
CREATE INDEX ix_bloated_n ON health.bloated (n);
UPDATE health.bloated SET n = n + 100000;
UPDATE health.bloated SET n = n + 100000;

-- Integer sequence at 91 % of its maximum.
CREATE SEQUENCE health.legacy_id_seq AS integer;
CREATE TABLE health.legacy (id integer NOT NULL DEFAULT nextval('health.legacy_id_seq'), note text);
ALTER SEQUENCE health.legacy_id_seq OWNED BY health.legacy.id;
SELECT setval('health.legacy_id_seq', 1954210119);

-- NOT VALID constraints.
CREATE TABLE health.orders_nv (id int PRIMARY KEY, amount numeric NOT NULL);
INSERT INTO health.orders_nv VALUES (1, 10), (2, -5);
ALTER TABLE health.orders_nv ADD CONSTRAINT amount_positive CHECK (amount > 0) NOT VALID;

CREATE TABLE health.parent (id int PRIMARY KEY);
CREATE TABLE health.child (id int PRIMARY KEY, parent_id int NOT NULL);
INSERT INTO health.parent VALUES (1);
INSERT INTO health.child VALUES (1, 1), (2, 999);
ALTER TABLE health.child ADD CONSTRAINT child_parent_fk FOREIGN KEY (parent_id) REFERENCES health.parent (id) NOT VALID;

-- Partitioned table whose partitions each carry the same duplicate pair: the index inherited from the parent
-- (payment_p_YYYY_customer_id_idx) is covered by the one created by hand (idx_fk_payment_p_YYYY_customer_id).
CREATE TABLE health.payment_p (id int NOT NULL, customer_id int NOT NULL, created_at date NOT NULL) PARTITION BY RANGE (created_at);
CREATE INDEX ON health.payment_p (customer_id);
DO $$
DECLARE y int;
BEGIN
    FOR y IN 2023..2025 LOOP
        EXECUTE format('CREATE TABLE health.payment_p_%s PARTITION OF health.payment_p FOR VALUES FROM (%L) TO (%L)', y, y || '-01-01', (y + 1) || '-01-01');
        EXECUTE format('CREATE INDEX idx_fk_payment_p_%s_customer_id ON health.payment_p_%s (customer_id)', y, y);
        EXECUTE format('INSERT INTO health.payment_p SELECT i, 1 + i %% 50, %L::date + i FROM generate_series(1, 100) AS i', y || '-01-01');
    END LOOP;
END
$$;

GRANT SELECT ON ALL TABLES IN SCHEMA health TO pgdba_ro;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA health TO pgdba_ro;

ANALYZE health.dups, health.events_dup, health.unused_big, health.bloated, health.legacy, health.orders_nv, health.parent, health.child, health.payment_p;
