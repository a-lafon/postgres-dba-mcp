-- Data of the execute_sql tests, in its own schema.
CREATE SCHEMA exec;
GRANT USAGE ON SCHEMA exec TO pgdba_ro;

CREATE TABLE exec.types (
    id    int PRIMARY KEY,
    txt   text,
    empty text,
    num   numeric,
    arr   int[],
    ts    timestamptz,
    doc   jsonb,
    bin   bytea
);
INSERT INTO exec.types VALUES
    (1, 'hello', '', 532.10, '{1,2}', '2026-03-02 10:14:00+00', '{"a": 1}', decode(repeat('ab', 2355), 'hex')),
    (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL);

CREATE TABLE exec.docs (
    id      int PRIMARY KEY,
    body    text,
    payload jsonb
);
INSERT INTO exec.docs
SELECT i, left(repeat('lorem ipsum dolor sit amet ', 150), 3650), jsonb_build_object('blob', repeat('x', 20000))
FROM generate_series(1, 20) AS i;

CREATE TABLE exec.canary (
    id int PRIMARY KEY,
    v  text
);
INSERT INTO exec.canary VALUES (1, 'canary');

GRANT SELECT ON ALL TABLES IN SCHEMA exec TO pgdba_ro;
