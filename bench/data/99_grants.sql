-- Runs last: every table and sequence loaded before (Pagila included) becomes readable by bench_ro, and every schema
-- but public, table, view and sequence becomes bench_owner's. Functions, types and the objects of extensions stay the
-- superuser's; indexes and the sequences owned by a column follow their table.
DO $$
DECLARE s text; r record;
BEGIN
    FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO bench_ro', s);
        EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO bench_ro', s);
        EXECUTE format('GRANT SELECT ON ALL SEQUENCES IN SCHEMA %I TO bench_ro', s);
        IF s <> 'public' THEN
            EXECUTE format('ALTER SCHEMA %I OWNER TO bench_owner', s);
        END IF;
    END LOOP;
    FOR r IN
        SELECT c.oid::regclass AS rel, c.relkind
        FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
        WHERE n.nspname NOT LIKE 'pg\_%' AND n.nspname <> 'information_schema'
          AND c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
          AND NOT EXISTS (SELECT 1 FROM pg_depend d -- members of an extension, sequences owned by a column
                          WHERE d.classid = 'pg_class'::regclass AND d.objid = c.oid
                            AND (d.deptype = 'e' OR (c.relkind = 'S' AND d.deptype IN ('a', 'i'))))
    LOOP
        EXECUTE format('ALTER %s %s OWNER TO bench_owner',
                       CASE r.relkind WHEN 'S' THEN 'SEQUENCE' WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW'
                                      WHEN 'f' THEN 'FOREIGN TABLE' ELSE 'TABLE' END, r.rel);
    END LOOP;
END
$$;
