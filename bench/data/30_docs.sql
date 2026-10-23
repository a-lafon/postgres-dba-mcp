-- Output-shaping tables: big documents, a wide table and one row of every awkward type.
SELECT setseed(0.42);

CREATE TABLE public.docs (
    id      int PRIMARY KEY,
    body    text,
    payload jsonb
);
INSERT INTO public.docs
SELECT i,
       left(repeat('lorem ipsum dolor sit amet ', 1900), 50000),
       jsonb_build_object('blob', repeat(md5(i::text), 625))
FROM generate_series(1, 100) AS i;

DO $$
BEGIN
    EXECUTE 'CREATE TABLE public.wide60 (' ||
        (SELECT string_agg(format('c%s int', lpad(i::text, 2, '0')), ', ' ORDER BY i) FROM generate_series(1, 60) AS i) || ')';
    EXECUTE 'INSERT INTO public.wide60 SELECT ' ||
        (SELECT string_agg('g', ', ') FROM generate_series(1, 60)) || ' FROM generate_series(1, 10) AS g';
END
$$;

CREATE TABLE public.types (
    id    int PRIMARY KEY,
    txt   text,
    empty text,
    num   numeric,
    arr   int[],
    iv    interval,
    ts    timestamptz,
    bin   bytea
);
INSERT INTO public.types VALUES
    (1, 'hello', '', 532.10, '{1,2}', '1 day 02:00', '2026-03-02 10:14:00+00', '\xdeadbeef'),
    (2, NULL, NULL, NULL, NULL, NULL, NULL, NULL);
