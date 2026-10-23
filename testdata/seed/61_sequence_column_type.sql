-- Trap for the sequence check of analyze_db_health, in schema seqcol: a bigint sequence feeds an integer column,
-- so the column, not the sequence, bounds the range. Nothing outside the schema is created or altered.
CREATE SCHEMA seqcol;
GRANT USAGE ON SCHEMA seqcol TO pgdba_ro;

-- Bigint sequence at 97.8 % of the integer maximum.
CREATE SEQUENCE seqcol.narrow_id_seq AS bigint;
CREATE TABLE seqcol.narrow (id integer NOT NULL DEFAULT nextval('seqcol.narrow_id_seq'), note text);
ALTER SEQUENCE seqcol.narrow_id_seq OWNED BY seqcol.narrow.id;
SELECT setval('seqcol.narrow_id_seq', 2100000000);

GRANT SELECT ON ALL TABLES IN SCHEMA seqcol TO pgdba_ro;
GRANT SELECT ON ALL SEQUENCES IN SCHEMA seqcol TO pgdba_ro;
