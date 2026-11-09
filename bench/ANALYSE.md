# Analysis and decisions

Source: run `20261108T222200Z`, 2026-11-08, postgres-dba-mcp `7afada9`, crystaldba 0.3.0
`sha256:dbbd3468…`, DBHub 1.6.0 `sha256:ccb6afc0…`, MCP Toolbox 1.16.0 `sha256:190bd9d9…`,
PostgreSQL 17.11, scale 1 with Pagila, every server as the read-only role `bench_ro`.
The run writes `n1.json`, `n2.json`, `security.json`, `session.json`, `ops.json` and the per-tool
breakdowns `dba.json`, `dbhub.json` and `toolbox.json` under `bench/results/<run>/`, which is not
versioned. Every figure below names the file it comes from, and the commands that rebuild them are in
the comparison section of `BENCHMARK.md`.

**Counter.** Tokens are the harness estimate `ceil(bytes/4)`; `--tokens=claude` was not used.
JSON tokenises more densely than CSV, so the estimate understates DBHub and Toolbox more than it
understates postgres-dba-mcp's CSV. The bias runs against postgres-dba-mcp.

**Price.** Dollars are claude-sonnet-5-5 input at $2.00 per million tokens, priced 2026-10-06
(`bench/internal/tokens/price.go`). Only input is priced: every figure here is what the server puts
into the model's context, never what the model writes.

## 1. Total tokens per task

One task is one request: the tool definitions the client sends plus the one answer it gets back.
The answer is the median over the six scenarios every server answered (S01, S04, S05, S06, S07, S22),
as the goal requires. The session column is the ten-turn investigation of `session.json`, where
definitions are counted once and every answer from turn 1 to 10 accumulates.

| Server | definitions | median answer | one task | $ one task | ten-turn session | $ session |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 1132 | 309 | 1441 | $0.0029 | 2679 | $0.0054 |
| crystaldba 0.3.0 | 1225 | 844 | 2069 | $0.0041 | 5881 | $0.0118 |
| DBHub 1.6.0 | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| DBHub, 4 tools | 431 | 1131 | 1562 | $0.0031 | 6152 | $0.0123 |
| DBHub, max_rows 100 | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| DBHub, default config | 276 | n/a | n/a | n/a | n/a | n/a |
| Toolbox 1.16.0, 29 tools | 4559 | 1376 | 5935 | $0.0119 | 86897 | $0.1738 |
| Toolbox, 10 tools | 1243 | 1376 | 2619 | $0.0052 | 83581 | $0.1672 |

Definitions from `n1.json`, answers from `n2.json`, sessions from `session.json`.
`dbhub-default` has no read-only guard, so it was measured on definitions only: replaying the 131
attacks through it would really write to the database.

**One request is the worst case for postgres-dba-mcp, and it is a tie.** 1441 against DBHub's 1427
is 14 tokens, 1.0 %, and DBHub answers 8 of the 22 scenarios against our 22 (`n2.json`).

**From the second answer on, the gap opens.** Computed from the same measured medians: definitions are
paid once, answers accumulate, so two answers give 1132 + 2×309 = 1750 against 296 + 2×1131 = 2558,
808 tokens below. The measured ten-turn session confirms the direction: 2679 against 6077, 56 % below.

## 2. Where postgres-dba-mcp wins

| Measure | postgres-dba-mcp | crystaldba | DBHub | Toolbox | Source |
|---|---|---|---|---|---|
| Scenarios answered, of 22 | 22 | 19 | 8 | 13 | `n2.json` |
| Of those, correct | 22 | 17 | 7 | 11 | `n2.json` |
| Failed (oversized or error) | 0 | 3 | 0 | 1 | `n2.json` |
| Attacks refused, of 131 | 131 | 106 | 80 | 0 | `security.json` |
| Attacks executed | 0 | 10 | 11 | 38 | `security.json` |
| Attacks with a side effect | 0 | 1 | 5 | 8 | `security.json` |
| False refusals, of 47 legitimate | 0 | 4 | 3 | 0 | `security.json` |
| Cold start | 5.9 ms | 1352.0 ms | 732.6 ms | 199.5 ms | `ops.json` |
| Memory after 100 S02 calls | 9.7 MB | 247.9 MB | 127.1 MB | 89.6 MB | `ops.json` |
| Binary or image | 22.9 MB | 395.0 MB | 344.8 MB | 422.5 MB | `ops.json` |
| Context at turn 10 | 2679 | 5881 | 6077 | 86897 | `session.json` |

The container figures include `docker run`; postgres-dba-mcp is the native binary for cold start and
size, and the linux image for memory.

Where the token gap is widest, all from `n2.json`:

- **Oversized cells, S04** (`SELECT id, payload FROM docs LIMIT 20`, 20 kB jsonb cells): 1154 against
  100183 (crystaldba), 100606 (DBHub) and 100153 (Toolbox). It is also the only scenario the other
  three answer *incorrectly*: none of them shortens a cell, so the grader reads `truncation missing`.
- **Unbounded results, S02** (`SELECT * FROM rental`): 2111 with the cut announced, against 76425
  (DBHub), 2519761 (Toolbox, cut not announced, graded incorrect) and a failure over 16 MiB
  (crystaldba). S03 (`SELECT * FROM events`): 1969 against 65349 (DBHub), and a failure for both
  crystaldba and Toolbox. Largest response of the run: 8443 bytes, under the 32768-byte cap, and all
  three cuts say so in their footer (`report.md`, from `n2.json`).
- **vector columns, S22**: 772 against 49065, 49322 and 49038. Phase 3 brought this from 1275 to 772
  by summarising a `vector` column as `[vector: N dims]`.
- **Exploration, S09 / S10 / S11**: 156 / 246 / 220 against Toolbox's 40030 for all three, because
  `list_tables` is its only exploration tool and it ignores arguments it does not know. crystaldba:
  1336 / 323 / 1259. DBHub answers none of the three: `search_objects` takes its own arguments, which
  the harness cannot send in place of `schema_name` / `object_name`.
- **Health and index advice, S17 to S20**: 359 / 250 / 53 / 35 against crystaldba's 1400 / 1226 /
  1991 / 250. Neither DBHub nor Toolbox has a mapped tool for any of the four.

## 3. Where postgres-dba-mcp loses

Complete list, every item measured.

1. **Tool definitions, 1132 tokens.** Fifth of the eight measured variants: DBHub default 276, DBHub
   296, DBHub max_rows 100 296, DBHub 4 tools 431, then us at 1132, crystaldba 1225, Toolbox subset
   1243, Toolbox 4559 (`n1.json`). DBHub does with 296 what we do with 1132, for two tools against ten.
2. **Deferred definitions, 223 tokens: the worst of all eight.** DBHub 7 and 13, crystaldba 38,
   Toolbox subset 39, Toolbox 136 (`n1.json`). `deferred` is the tool names plus the server
   instructions, what a client with tool search loads at session start. The server instructions are
   what makes it large; phase 3 added a line to them and it went from 213 to 223.
3. **Total per task: 14 tokens above DBHub**, 1441 against 1427 (section 1). The only server whose
   single-request total beats ours.
4. **The footer costs us the smallest answers.** S07, a column-name typo: 28 tokens / 109 bytes
   against DBHub's 24 / 95 and Toolbox's 26 / 102 (`n2.json`). Turn 8 of the session, a `count(*)`:
   7 tokens / 26 bytes against crystaldba's 4 / 16 and Toolbox's 4 / 13 (`session.json`). The footer
   carries the row count and the elapsed time, which is a fixed cost an answer of one cell cannot
   amortise.
5. **Plans with a hypothetical index, S13 and S21**: 84 and 62 against crystaldba's 25 and 25
   (`n2.json`). Our plan prints both costs and names the hypothetical index it used; crystaldba's
   prints less. Both answers are graded correct.
6. **First-call latency.** S01 p95 919.4 ms, the highest of any server on any scenario (Toolbox 655.2,
   crystaldba 105.7, DBHub 21.8). Our median p95 across scenarios is 3.9 ms, the lowest of the four
   (`n2.json`). S01 is the first `execute_sql` call of the run, and this build parses SQL through the
   WASM libpg_query, whose module compiles on first use. Not a product defect of the release image,
   but a client that calls `execute_sql` in the first second on a build without a C compiler pays it.
7. **Concurrency p95: Toolbox is faster.** 29.0 ms against 25.4 ms over 50 calls at once on one stdio
   session, 0 errors both (`ops.json`). crystaldba 100.3 ms, DBHub 60.1 ms.
8. **Two legitimate statements fail at execution**, `postgis-style` and `user-schema-function`
   (`security.json`). They fail on both postgres-dba-mcp and Toolbox because the fixture has neither
   PostGIS nor the `my_schema` schema; crystaldba and DBHub refuse them outright, which is why they
   count as false refusals there and not here. Not a loss, listed so the 0/47 is read correctly.

## 4. Decisions

### Single exploration tool with detail levels, on DBHub's `search_objects` model: do not do it

Two measures decide, and they point the same way.

- **Definition side, it costs more.** The three postgres-dba-mcp exploration tools cost 187 tokens
  together: `list_schemas` 20, `list_objects` 70, `get_object_details` 97 (`dba.json`). DBHub's single
  `search_objects` costs 212 (`dbhub.json`). Merging three tools into one would add 25 tokens, not
  save any, because the saving is three short names and three small schemas while the cost is one
  schema that has to carry `object_type`, `pattern`, `schema`, `table`, `detail_level` and `limit`.
- **Answer side, it costs much more.** Toolbox's single exploration tool returns the same 40030 tokens
  for S09, S10 and S11, against our 156, 246 and 220 (`n2.json`). A tool that must serve every detail
  level either returns too much or needs a level argument the model has to get right; Toolbox returns
  too much, 257 times our cost on S09.
- **And it would cost coverage.** S08 to S11 read `no tool` for every DBHub variant precisely because
  `search_objects` has its own argument shape (`bench/README.md`). A merged tool is a new contract to
  get right, against 187 tokens of a 1441-token task, 13 %.

Not implemented, as the goal requires. Nothing in these figures argues for revisiting it.

### Further shortening of the definitions: do, on `explain_query` only

Remaining cost per tool (`dba.json`): `explain_query` 254, `analyze_db_health` 163, `execute_sql` 137,
`get_top_queries` 126, `analyze_query_indexes` 112, `get_object_details` 97, `analyze_workload_indexes`
84, `list_objects` 70, `analyze_locks` 69, `list_schemas` 20.

Definitions are 1132 of the 1441 tokens of one task, 79 %, and 1132 of the 2679 of a ten-turn session,
42 % (section 1). On a single-request client they are the whole bill, so what is left in them matters.

`explain_query` is the costliest by 91 tokens and the only one where a measurable block is redundant:
its prose description is 170 bytes, its definition counts 1015 bytes and 254 tokens, and the
`hypothetical_indexes` parameter description alone is 573 bytes of source
(`internal/explain/tool.go`), a prose restatement of the JSON schema printed directly beneath it,
which already declares `table` as a string, `columns` as a non-empty array of strings, `using` as a
string and `table` plus `columns` as required. What the prose adds and the schema does not state is
one fact, that `using` defaults to `btree`.

Estimated, not measured: keeping that one fact in a single line and dropping the rest saves on the
order of 140 tokens, taking the definitions to about 990 and one task to about 1300, below DBHub's
1427. The figure must be re-measured with `n1` after the change; it is an estimate from byte counts.

The other nine are not worth touching. `analyze_db_health` at 163 is a seven-line list of what each
`health_type` covers, which is what lets a model ask for one check instead of `all`; the next eight
are 137 and under, where a 20 % cut is 25 tokens, 1.7 % of a task, against the risk of dropping a
limit or a parameter the model needs. Phase 3 already took the easy 227 tokens (1359 to 1132) with no
scenario regression; what is left below `explain_query` is not easy.

### The server instructions: wait

`deferred` at 223 tokens is our worst figure against every competitor (section 3, item 2), and it is
the figure a client with tool search actually loads at session start. But no measure here says what it
costs in practice: the bench measures `deferred` as a size, never as a session, and nothing in this
run tells us whether a shorter instruction block changes an answer. Cutting it blind would trade a
measured 223 tokens against an unmeasured regression in tool selection.

Wait for a measure. The one that would settle it is a `session` variant run with tool search on, which
the harness does not have today.

## 5. Phase 6: what it would take, and what a full pass would cost

`AGENT_TASKS: non`. Nothing below was run. The 27 runs per server on record (9 tasks × 3 runs) are
from 2026-10-22, on 0.1.0, before the volatility gate of 0.2.0 and before `analyze_locks`.

### What would have to change first

- **Two server configurations do not exist.** `bench/n3/dbhub.json` and `bench/n3/toolbox.json` have to
  start the same containers `bench/internal/servers/dbhub.go` and `bench/internal/servers/toolbox.go`
  start, DBHub through the `sh -c 'printf … > /tmp/dbhub.toml && exec node dist/index.js
  --config=/tmp/dbhub.toml'` trick. Both are `docker run`, which `agent.NamedConfig` already cleans up.
- **`n3` hard-codes its two servers** (`bench/cmd/bench/commands.go:441`,
  `Servers: []string{results.DBA, results.Crystal}`). It has to read `--servers` the way `n1`, `n2`,
  `security` and `session` do.
- **The judge hard-codes the same pair in seven places** (`bench/cmd/bench/judge.go:42,151,217,225`,
  `bench/internal/judge/judge.go:251`, `bench/internal/judge/attribution.go:134`,
  `bench/internal/judge/calibration.go:69`). Three servers is three pairs, four is six.

### Three obstacles that no code change fixes

- **T13 cannot be a server test for DBHub or Toolbox.** Its rubric requires a call to
  `analyze_workload_indexes` or `get_top_queries` (`bench/internal/agent/rubric.go:116`). Neither
  server has either tool, so T13 is a structural absence and would have to be counted as "no tool",
  the way `n2` counts one, not as a wrong answer.
- **T10 is not a server test on Toolbox.** The task is "delete the events older than one year", which
  must be refused. Toolbox has no read-only mode: it refused 0 of the 131 attacks and executed 38
  (`security.json`). The delete would be stopped by `bench_ro` lacking the right, not by the server,
  and the rubric would read pass for the wrong reason.
- **Toolbox agents would die on context.** An agent that runs `SELECT * FROM events` on Toolbox gets
  the answer S02 measures at 2519761 tokens (`n2.json`), which no context window holds. Those runs
  fail on size, not on the question.

So of 9 tasks, 2 do not measure the server for the two new entrants, and any task that samples a large
table is a coin flip on Toolbox. About a third of the new grid is uninformative before the first
dollar is spent.

### Cost of a full pass, estimated

Anchors, all measured, from `BENCHMARK.md` "Cost of a run" (2026-10-22, `claude -p`, Claude Opus 5.5):
the 27 agent runs cost **5.35 USD** for the postgres-dba-mcp column and **6.12 USD** for crystaldba,
and the judge **5.60 USD** for the single pair with its three repeats.

Those two anchors are the only measured cost points, and they differ by 0.77 USD. The only measured
basis on which to carry them to DBHub and Toolbox is the context each server accumulates over the
ten-turn session of this run: summing the context at each turn 1 to 10 gives dba 19264, crystaldba
38349, DBHub 33863, Toolbox 735745 tokens (`session.json`).

Method: fit a straight line through the two anchors on that basis. 5.35 USD at 19264 and 6.12 USD at
38349 give a fixed part of **4.57 USD per column**, which is the question, the system prompt, the
model's own reasoning and Claude Code's overhead, plus **40.35 USD per million tokens of session
context**. That slope is ten times the 4.00 USD/Mtok list price of Opus 5.5 input because one token of
tool output is billed again at every later turn and in each of the three repeats.

| Column, 27 runs | session context | estimated cost |
|---|---|---|
| postgres-dba-mcp | 19264 | 5.35 USD (measured) |
| crystaldba 0.3.0 | 38349 | 6.12 USD (measured) |
| DBHub 1.6.0 | 33863 | 5.94 USD (estimated) |
| Toolbox 1.16.0, 29 tools | 735745 | 34.26 USD (estimated) |
| Toolbox, 10 tools | 702585 | 32.92 USD (estimated) |

Full pass on the three servers the goal names (postgres-dba-mcp, DBHub, Toolbox), 27 runs each, plus
the judge on the three pairs they form:

| Item | Estimated |
|---|---|
| Agent tasks, 3 columns | 45.55 USD |
| Judge, 3 pairs × 5.60 USD | 16.80 USD |
| **Total** | **62.35 USD** |

Keeping crystaldba as a fourth column adds its 6.12 USD and three more judge pairs, 16.80 USD, for
about **85 USD**.

The estimate is soft in both directions. It is low if Toolbox's runs retry on an oversized answer
rather than stopping, and high if they die early; the two-point fit cannot tell which. It also
transports a shape measured on a 5-turn agent task onto a 10-turn fixed session, which is not the same
workload. Read 62 USD as an order of magnitude, not a quote.

### Is phase 6 worth running: no

- **A third of the grid would not measure the server.** T13 has no tool on either new server, T10 is
  decided by role permissions on Toolbox, and any task that samples a large table is a size failure
  there.
- **The question the goal asks is already answered without a model.** The headline figure, total
  tokens per task, comes from `n1` and `n2`, which call no model and cost nothing to re-run.
- **The judge's output never enters the verdict.** `BENCHMARK.md` states it: the go/no-go uses the
  deterministic rubrics only. Phase 6 would buy information that is explicitly non-binding.
- **Three runs per cell is a small sample**, which the existing page already says, and 62 USD plus the
  harness work (two configurations, `--servers` in `n3`, an N-way judge in seven places, two rubric
  exceptions) buys no more of them.

Run it only if the agent tasks themselves are rewritten first, so that every task is answerable by a
server with two tools. That is a larger change than re-running them.

## 6. Not verified / limits

- **The counter is `ceil(bytes/4)`**, not a tokeniser. JSON tokenises more densely than CSV, so the
  estimate understates DBHub and Toolbox more than it understates postgres-dba-mcp. The bias runs
  against postgres-dba-mcp, which is the right way round for a comparison it is a party to, but it is
  a bias and every figure here carries it.
- **Scenarios judged unfair to one server, left intact** as the goal requires, and reported here
  instead: S04 expects a shortened cell, which only postgres-dba-mcp produces; S13 needs hypothetical
  indexes, which only postgres-dba-mcp and crystaldba have; S14 sends `analyze`, which DBHub's
  `explain_sql` ignores; S15 and S19 read `pg_stat_statements`, which the bench itself keeps filling,
  so they drift with the workload.
- **The concurrency p95 is measured on the linux image.** The darwin and windows releases build with
  `CGO_ENABLED=0` (`.goreleaser.yaml`) and keep the WASM parser, whose module compiles on first use,
  about 880 ms, started in the background at start-up. A macOS client that calls `execute_sql` inside
  the first second pays part of it: measured on that build, the p95 of 50 concurrent calls is 953 ms
  cold (`ops.json` of run `20261108T212249Z`) and 26 ms once one call has gone through.
- **`halfvec` renders like `vector`**, so a `halfvec` column is labelled `[vector: N dims]`.
  `internal/format/vector.go` reads the text form, because pgvector types have no fixed OID.
- **The JSON forms of `Fact` were written against these two image digests.** Another version of DBHub
  or Toolbox may render a value differently and turn a correct answer into an incorrect one without
  the server changing.
- **`dbhub-default` was never attacked, on purpose.** It has no read-only guard, so the 131 attacks
  would really write to the database. Its only measured figure is its 276 definition tokens.
- **`fn-dynsql-lateral`** (`ts_stat` inside a `LATERAL`) kills the Toolbox process.
- **Phase 6 was not run.** Section 5 is a costing, not a measurement.
- **Correction to an earlier note.** It was reported that `report` and `compare` can no longer read the
  two `20261108T2100*` runs since `legit` became a map. That is wrong, and was checked: both runs store
  `"legit": null`, which unmarshals into a nil map, and `go run ./cmd/bench report --run=<id>` and
  `compare --run=<id>` both succeed on them. What those two runs lack is the legitimate corpus itself,
  so they carry no false-refusal figure.
