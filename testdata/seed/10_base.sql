-- Deterministic base data: no index on events(account_id, created_at) nor on orders(status), on purpose.
SELECT setseed(0.42);

CREATE TABLE public.accounts (
    id         int PRIMARY KEY,
    name       text,
    created_at timestamptz
);
INSERT INTO public.accounts
SELECT i, 'account-' || i, timestamptz '2025-01-01 00:00:00+00' + (i || ' minutes')::interval
FROM generate_series(1, 5000) AS i;

CREATE TABLE public.events (
    id         bigserial PRIMARY KEY,
    account_id int NOT NULL REFERENCES public.accounts,
    kind       text,
    created_at timestamptz NOT NULL,
    payload    jsonb
);
INSERT INTO public.events (account_id, kind, created_at, payload)
SELECT 1 + (i * 7919) % 5000,
       (ARRAY['login', 'click', 'view', 'purchase'])[1 + (i % 4)],
       timestamptz '2025-01-01 00:00:00+00' + ((i * 37) % 5000000 || ' seconds')::interval,
       jsonb_build_object('n', i, 'r', round(random()::numeric, 4))
FROM generate_series(1, 200000) AS i;

CREATE TABLE public.orders (
    id         bigserial PRIMARY KEY,
    account_id int REFERENCES public.accounts,
    status     text,
    amount     numeric(10,2),
    created_at timestamptz
);
INSERT INTO public.orders (account_id, status, amount, created_at)
SELECT 1 + (i * 7) % 5000,
       CASE WHEN i % 40 = 0 THEN 'pending' WHEN i % 40 = 1 THEN 'failed' ELSE 'done' END,
       round((random() * 1000)::numeric, 2),
       timestamptz '2025-01-01 00:00:00+00' + ((i * 53) % 4000000 || ' seconds')::interval
FROM generate_series(1, 100000) AS i;

ANALYZE;
