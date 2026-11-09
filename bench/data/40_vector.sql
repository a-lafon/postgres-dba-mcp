-- What a RAG application stores: a pgvector embedding and the text metadata around it, at scale (2000 chunks times
-- :scale, at least 10). Vectors are pseudo-random unit vectors built with hashtext, a pure function: the data is the
-- same on every load without setseed, which a parallel aggregate would not preserve.
CREATE EXTENSION IF NOT EXISTS vector;

SELECT greatest(10, (2000 * :scale)::int) AS n_chunks \gset

CREATE TABLE public.embeddings (
    id        int PRIMARY KEY,
    title     text NOT NULL,
    source    text NOT NULL,
    chunk     text NOT NULL,
    embedding vector(1536) NOT NULL
);

INSERT INTO public.embeddings (id, title, source, chunk, embedding)
SELECT i,
       'Handbook section ' || i,
       'https://docs.example.com/handbook/' || (1 + i % 50) || '#p' || i,
       left(repeat('Retrieval augmented generation chunk ' || i || '. ', 12), 400),
       l2_normalize(v.raw::vector)
FROM generate_series(1, :n_chunks) AS i,
     LATERAL (SELECT array_agg(((hashtext(i || ':' || d) % 1000)::real / 1000.0::real) ORDER BY d) AS raw
              FROM generate_series(1, 1536) AS d) AS v;
