-- Runs last: every table and sequence loaded before (Pagila included) becomes readable by bench_ro.
DO $$
DECLARE s text;
BEGIN
    FOR s IN SELECT nspname FROM pg_namespace WHERE nspname NOT LIKE 'pg\_%' AND nspname <> 'information_schema' LOOP
        EXECUTE format('GRANT USAGE ON SCHEMA %I TO bench_ro', s);
        EXECUTE format('GRANT SELECT ON ALL TABLES IN SCHEMA %I TO bench_ro', s);
        EXECUTE format('GRANT SELECT ON ALL SEQUENCES IN SCHEMA %I TO bench_ro', s);
    END LOOP;
END
$$;
