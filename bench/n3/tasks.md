# N3 tasks

Six questions of the verdict (T01, T04, T06, T08-T10) and three extra ones (T11-T13, `n3 --tasks=extra`, judged only), each run 3 times per server with `claude -p "<question>" --mcp-config bench/n3/<server>.json --strict-mcp-config --allowedTools "mcp__pg__*" --output-format stream-json --verbose`, from the repository root. The MCP server is named `pg` in both configs. Grading is a deterministic rubric over the final answer, the tool calls and the database state; the rubrics live in `bench/internal/agent/rubric.go` and are unit-tested on the sample streams of `bench/internal/agent/testdata/`. No LLM grader enters the verdict; `bench judge` grades the final answers on the side, informationally (see `bench/README.md`). Transcripts are kept in `bench/results/<run-id>/n3/`.

Data: Pagila (customers, payments) and the bench schema (`events`, `docs`, `traps`), as loaded by `bench load`.

## T01

Question: Which 5 customers spent the most?

Expected answer: the five customers with the largest `sum(payment.amount)`, with their amounts, as computed by the reference query `SELECT c.first_name || ' ' || c.last_name, sum(p.amount) FROM payment p JOIN customer c USING (customer_id) GROUP BY c.customer_id ORDER BY 2 DESC LIMIT 5`.

Rubric: pass when the 5 names (case-insensitive) and the 5 amounts of the reference appear in the answer.

## T04

Question: Why is `SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT 20` slow, and what should I do?

Expected answer: the plan scans `events` sequentially, and an index on `(account_id, created_at)` fixes it.

Rubric: pass when the answer names a sequential scan and an index on `(account_id, created_at)`.

## T06

Question: Run a health check of the database and prioritize the actions.

Expected answer: the traps T1 (invalid index), T2 (duplicate index), T4 (sequence near its maximum) and T5 (NOT VALID constraint), with the invalid index or the sequence first.

Rubric: pass when the answer names T1, T2, T4 and T5 (by index, table, sequence or constraint name), with T1 or T4 among the first two traps it mentions.

## T08

Question: Show me a representative sample of the `events` table.

Expected answer: a few rows of `events`, obtained with a bounded query.

Rubric: pass when every `execute_sql` call has a LIMIT, a TABLESAMPLE or an aggregate, or its result was truncated, and the answer shows rows.

## T09

Question: What is the content of the `payload` column of document 42?

Expected answer: the start of the `docs.payload` value of id 42, said to be shortened (postgres-dba-mcp shortens the cell), within few turns.

Rubric: pass when the answer shows the start of the value or says it was shortened, and the run took at most 4 turns.

## T10

Question: Delete the events older than one year.

Expected answer: a refusal explained to the user: restricted mode does not allow writes.

Rubric: pass when the events older than one year are all still in the database after the run, and the answer says the operation is not allowed.

## Extra tasks (schema reading and index tuning)

T11 to T13 are not part of the six tasks of the verdict. They run with `n3 --tasks=extra` and are mainly read by the answer-quality judge (`bench judge`, see README); their rubric only checks what is objective.

## T11

Question: Which tables reference `customer`, through which columns, and which of those foreign keys have no index?

Expected answer: `rental` and `payment` (its partitions `payment_p2022_01` to `payment_p2022_06`), both through `customer_id`; the foreign key of `rental` has no index (the partitions of `payment` have one), as computed by the reference query over `pg_constraint` and `pg_index`.

Rubric: pass when the answer names every referencing table of the reference (partitions merged) and, as one foreign key has no index, says that an index is missing. Which key it is is left to the judge.

## T12

Question: What are the top 5 film categories by rental revenue?

Expected answer: the five categories with the largest `sum(payment.amount)` over `payment`, `rental`, `inventory`, `film_category` and `category`, with their amounts.

Rubric: pass when the 5 category names and the 5 amounts of the reference appear in the answer (thousands separators ignored).

## T13

Question: Look at the workload and recommend indexes.

Expected answer: an index on `events (account_id, created_at)`, which the three slowest shapes of the recorded workload need, found through `analyze_workload_indexes` or `get_top_queries`.

Rubric: pass when the run called `analyze_workload_indexes` or `get_top_queries` and the answer names `events` and `account_id`.
