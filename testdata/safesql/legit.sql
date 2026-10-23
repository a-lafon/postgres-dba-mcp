-- case: simple-select legit
SELECT id, name FROM public.accounts WHERE id = 1;

-- case: select-star-limit legit
SELECT * FROM public.events ORDER BY created_at DESC LIMIT 10;

-- case: count legit
SELECT count(*) FROM public.orders;

-- case: join legit
SELECT a.name, o.status, o.amount
FROM public.accounts a
JOIN public.orders o ON o.account_id = a.id
WHERE o.status = 'pending'
ORDER BY o.amount DESC
LIMIT 20;

-- case: left-join-group legit
SELECT a.id, count(e.id) AS n_events
FROM public.accounts a
LEFT JOIN public.events e ON e.account_id = a.id
GROUP BY a.id
HAVING count(e.id) > 5
ORDER BY n_events DESC;

-- case: cte legit
WITH recent AS (
  SELECT account_id, amount FROM public.orders WHERE created_at > now() - interval '7 days'
)
SELECT account_id, sum(amount) FROM recent GROUP BY account_id;

-- case: recursive-cte legit
WITH RECURSIVE t(n) AS (
  VALUES (1)
  UNION ALL
  SELECT n + 1 FROM t WHERE n < 10
)
SELECT n FROM t;

-- case: window legit
SELECT id, account_id, amount,
       row_number() OVER (PARTITION BY account_id ORDER BY created_at) AS rn,
       sum(amount) OVER (PARTITION BY account_id) AS total
FROM public.orders;

-- case: json-operators legit
SELECT payload->>'user' AS usr, payload->'meta'->>'ip' AS ip
FROM public.events
WHERE payload @> '{"source": "web"}'::jsonb;

-- case: jsonb-functions legit
SELECT jsonb_pretty(payload), jsonb_typeof(payload) FROM public.events LIMIT 5;

-- case: generate-series legit
SELECT g, g * g AS sq FROM generate_series(1, 100) AS g;

-- case: generate-series-lateral legit
SELECT a.id, s.d
FROM public.accounts a
CROSS JOIN LATERAL generate_series(1, 3) AS s(d)
WHERE a.id < 10;

-- case: values legit
VALUES (1, 'a'), (2, 'b');

-- case: table-form legit
TABLE public.accounts;

-- case: union legit
SELECT id FROM public.accounts UNION ALL SELECT account_id FROM public.orders;

-- case: except legit
SELECT id FROM public.accounts EXCEPT SELECT account_id FROM public.events;

-- case: subquery-in legit
SELECT * FROM public.accounts WHERE id IN (SELECT account_id FROM public.orders WHERE status = 'failed');

-- case: exists legit
SELECT a.id FROM public.accounts a WHERE EXISTS (SELECT 1 FROM public.events e WHERE e.account_id = a.id);

-- case: scalar-subquery legit
SELECT id, (SELECT max(created_at) FROM public.events e WHERE e.account_id = a.id) AS last_event
FROM public.accounts a;

-- case: case-expression legit
SELECT CASE WHEN amount > 100 THEN 'big' ELSE 'small' END, count(*) FROM public.orders GROUP BY 1;

-- case: string-functions legit
SELECT upper(name), length(name), substr(name, 1, 3), name || '-x' FROM public.accounts;

-- case: date-functions legit
SELECT date_trunc('day', created_at) AS d, count(*) FROM public.events GROUP BY d ORDER BY d;

-- case: aggregate-filter legit
SELECT count(*) FILTER (WHERE status = 'done'), avg(amount), percentile_cont(0.5) WITHIN GROUP (ORDER BY amount) FROM public.orders;

-- case: array-agg legit
SELECT account_id, array_agg(DISTINCT status) FROM public.orders GROUP BY account_id;

-- case: catalog-tables legit
SELECT relname, relkind FROM pg_catalog.pg_class WHERE relnamespace = 'public'::regnamespace;

-- case: catalog-size-functions legit
SELECT relname, pg_total_relation_size(oid), pg_size_pretty(pg_relation_size(oid)) FROM pg_catalog.pg_class WHERE relkind = 'r';

-- case: catalog-info-schema legit
SELECT table_name, column_name, data_type FROM information_schema.columns WHERE table_schema = 'public';

-- case: catalog-stat-activity legit
SELECT pid, state, query FROM pg_catalog.pg_stat_activity WHERE state <> 'idle';

-- case: catalog-current-setting legit
SELECT current_setting('server_version'), current_database(), current_user, version();

-- case: catalog-has-privilege legit
SELECT has_table_privilege('public.accounts', 'SELECT');

-- case: explain legit
EXPLAIN SELECT * FROM public.events WHERE account_id = 1;

-- case: explain-analyze-buffers legit
EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM public.events WHERE account_id = 1;

-- case: explain-format-json legit
EXPLAIN (FORMAT JSON) SELECT count(*) FROM public.orders;

-- case: show legit
SHOW server_version;

-- case: show-all legit
SHOW ALL;

-- case: postgis-style legit
SELECT ST_Distance(ST_MakePoint(0, 0), ST_MakePoint(id, id)) FROM public.accounts LIMIT 5;

-- case: user-schema-function legit
SELECT my_schema.score(id), myapp.bucket(id::text) FROM public.accounts;

-- case: pg-prefixed-readonly legit
SELECT pg_typeof(id), pg_column_size(name), pg_get_viewdef('pg_catalog.pg_tables'::regclass) FROM public.accounts LIMIT 1;

-- case: trailing-semicolon-comment legit
SELECT 1; -- done

-- case: semicolon-in-string legit
SELECT 'a; DROP TABLE accounts' AS s;

-- case: dollar-quoted-string legit
SELECT $$a; DELETE FROM events$$ AS s;

-- case: leading-comment legit
/* count the accounts */ SELECT count(*) FROM public.accounts;

-- case: mixed-case-keywords legit
sElEcT Id FrOm public.accounts;

-- case: quoted-identifiers legit
SELECT "id" FROM "public"."accounts";

-- case: tablesample legit
SELECT * FROM public.events TABLESAMPLE SYSTEM (1);

-- case: distinct-on legit
SELECT DISTINCT ON (account_id) account_id, created_at FROM public.events ORDER BY account_id, created_at DESC;

-- case: grouping-sets legit
SELECT account_id, status, count(*) FROM public.orders GROUP BY GROUPING SETS ((account_id), (status), ());
