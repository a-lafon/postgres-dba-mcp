# Benchmark

postgres-dba-mcp against [crystaldba/postgres-mcp](https://github.com/crystaldba/postgres-mcp) 0.3.0, the latest release (May 2025). Tool responses, restricted mode, operations and tool definitions were measured on 2026-11-01 on [`959bb72`](https://github.com/a-lafon/postgres-dba-mcp/commit/959bb72); the agent tasks and the judge are older and say so where they appear. This page gives the method and every number the harness produced, including the ones that do not favour postgres-dba-mcp. The harness is published in [bench](bench), with its inputs: the data set in [bench/data](bench/data), the 131 attacks and 47 legitimate statements in [testdata/safesql](testdata/safesql), and the scenarios and tasks in the tables below. `cd bench && go run ./cmd/bench all --tokens=claude` reproduces everything but the agent tasks, and writes [bench/report.md](bench/report.md), which is the run behind this page as the harness wrote it.

A second comparison, against [DBHub](https://github.com/bytebase/dbhub) 1.6.0 and [MCP Toolbox](https://github.com/googleapis/genai-toolbox) 1.16.0 as well as crystaldba, ran on 2026-11-08 on [`7afada9`](https://github.com/a-lafon/postgres-dba-mcp/commit/7afada9). It has its own section below, it counts tokens with the harness estimate rather than a model, and its numbers are not comparable with the ones above.

## At a glance

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/bench/tokens-dark.png">
  <img alt="Tokens per tool response, 21 scenarios, log scale" src="assets/bench/tokens-light.png">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/bench/security-dark.png">
  <img alt="131 attack statements in restricted mode" src="assets/bench/security-light.png">
</picture>

<picture>
  <source media="(prefers-color-scheme: dark)" srcset="assets/bench/ops-dark.png">
  <img alt="Memory after 100 calls and cold start" src="assets/bench/ops-light.png">
</picture>

## Setup

| | |
|---|---|
| Database | PostgreSQL 17.11 in Docker; `events` 5,000,000 rows, `orders` 1,000,000, `accounts` 50,000, plus [Pagila](https://github.com/devrimgunduz/pagila) and a `traps` schema (below); `ANALYZE`, then 120 s of pgbench to fill `pg_stat_statements` |
| crystaldba | Docker image `crystaldba/postgres-mcp` pinned by digest `sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b` (`postgres_mcp` 0.3.0), `--access-mode=restricted` |
| postgres-dba-mcp | built from source, `--access-mode=restricted`, default limits (`--max-bytes` 32768) |
| Role | both servers connect as the same read-only role `bench_ro` |
| Tokens | counted by Claude Code (`claude -p`, Claude Opus 5.5): input tokens of a one-turn call holding the text, minus those of an empty call, median of 3 calls |
| Agent and judge model | Claude Opus 5.5 through `claude -p`, default effort |
| Host | Linux, x86-64, Docker 29. Cold start, binary size and the crystaldba image size depend on it; the memory figures are read inside a Linux container on either host |

**Commits.** The tool responses, restricted mode, operations and tool definitions ran on [`959bb72`](https://github.com/a-lafon/postgres-dba-mcp/commit/959bb72), the head of `main` that day, so the measured code can be checked out.

The agent tasks and the judge were not run again. They are from 2026-10-22, on commits of the development repository (`f0d7f20`, and `daf8c3a` for T13) that were squashed into `Initial release` when this repository was opened: **no public revision matches them**, and they predate both the volatility gate that 0.2.0 adds to restricted mode and the `analyze_locks` tool. They are kept because re-running them costs model calls, and because what they measure, whether an agent reaches the right answer, moves little between patch releases. Read them as dated, not as current.

**Where a model is involved at all.** Most of this page is measured without one: response sizes are bytes, restricted mode is a parser and a snapshot of the database taken after every statement, memory and cold start are `docker stats` and a clock. A model appears in exactly two places. It counts tokens, which is a tokeniser and not a judgment, and the byte column above says what the ratio becomes without it. And it runs the agent tasks and their judge, which are informational, which never enter the verdict, and which a reader who distrusts them can ignore without touching any other number on this page. `go run ./cmd/bench all` without `--tokens=claude` needs no account of any kind.

## Data

Generated with a fixed random seed, then `ANALYZE`.

| Table | Rows | Columns | Why it is there |
|---|---|---|---|
| `public.accounts` | 50,000 | `id int`, `name text`, `created_at timestamptz` | joins, primary-key lookups |
| `public.events` | 5,000,000 | `id bigserial`, `account_id int`, `kind text` (4 values), `created_at timestamptz`, `payload jsonb` (small object) | large table; no index on `(account_id, created_at)` on purpose, for the plan and index-advice tasks |
| `public.orders` | 1,000,000 | `id bigserial`, `account_id int`, `status text` (`done` 95 %, `pending` and `failed` 2.5 % each), `amount numeric(10,2)`, `created_at timestamptz` | low-selectivity filter, date ranges |
| `public.docs` | 100 | `id int`, `body text` (50 kB), `payload jsonb` (20 kB) | oversized cells |
| `public.wide60` | 10 | 60 `int` columns | wide rows and object details |
| `public.types` | 2 | `text`, an empty `text`, `numeric`, `int[]`, `interval`, `timestamptz`, `bytea`; the second row all `NULL` | value rendering, NULL against empty string |
| Pagila | about 16,000 rentals and payments | the sample DVD-rental schema: films, customers, rentals, payments, categories, foreign keys, views | realistic joins and schema questions; [devrimgunduz/pagila](https://github.com/devrimgunduz/pagila) at a pinned commit, checked by SHA-256 |

The `traps` schema holds six problems a health check should find:

| Trap | Object |
|---|---|
| T1 invalid index | a unique index built `CONCURRENTLY` on duplicate values (`traps.inval`) |
| T2 duplicate indexes | `(account_id)` covered by `(account_id, created_at)` (`traps.dup_events`) |
| T3 dead tuples | `traps.bloated`, 200,000 rows with 37.5 % dead tuples, autovacuum off |
| T4 sequence near its maximum | `traps.legacy_id_seq`, an `integer` sequence at 91 % |
| T5 unvalidated constraint | a `NOT VALID` check on `traps.orders_nv`, which one row breaks |
| T6 unused index | `traps.unused_payload_hash_idx`, over 100 MB and never scanned |

**Workload.** `pg_stat_statements` is filled by 120 s of pgbench over 10 statement shapes:
- three on `events` filtered by `account_id` and ordered or filtered by `created_at`, which are the slowest;
- `orders` by status, by account and status, and by date range;
- primary-key lookups;
- a join of `accounts` and `orders`;
- a `jsonb` field read.

**Attack corpus** (131 statements in 8 families):

| Family | Statements | Examples |
|---|---|---|
| functions with side effects | 45 | the same function in `FROM`, `LATERAL`, a subquery, a CTE and an argument |
| evasions | 24 | mixed case, quoted names, `EXPLAIN ANALYZE` of a write, `PREPARE`, `DO`, `LISTEN`, empty or broken statements |
| transaction and session control | 15 | `COMMIT`, `SET ROLE`, `SET default_transaction_read_only`, `RESET ALL` |
| plain writes | 13 | `INSERT`, `UPDATE`, `DELETE`, DDL, `GRANT`, `VACUUM`, `MERGE` |
| disguised writes | 10 | data-modifying `WITH`, `SELECT INTO` |
| several statements | 8 | a write hidden after `;`, in comments or dollar quotes |
| files | 8 | `COPY` to and from files and programs, `CREATE EXTENSION` |
| row and table locks | 8 | `FOR UPDATE`, `LOCK TABLE` |

The 47 legitimate statements cover joins, CTEs, window functions, `jsonb`, catalog and size functions, `EXPLAIN (ANALYZE, BUFFERS)`, `SHOW`, `TABLESAMPLE`, `GROUPING SETS` and tricky lexing (semicolons in strings, dollar quotes, comments).

## Tool responses (21 deterministic scenarios)

Each scenario calls one tool with fixed arguments, 5 times per server. A call that takes over 120 s or returns over 16 MiB counts as failed. Correct means every expected fact (the 10 titles, the 60 column names, the trap names, ...) is in the text.

| ID | Tool | Call | Tokens dba | Tokens crystal | Change | Correct dba / crystal |
|---|---|---|---|---|---|---|
| S01 | execute_sql | `SELECT * FROM film LIMIT 10` | 1290 | 3310 | -61 % | yes / yes |
| S02 | execute_sql | `SELECT * FROM rental` | 5468 (cut, announced) | failed: over 16 MiB | | yes / no |
| S03 | execute_sql | `SELECT * FROM events` | 4717 (cut, announced) | failed: its own 30 s limit | | yes / no |
| S04 | execute_sql | `SELECT id, payload FROM docs LIMIT 20` (20 kB jsonb cells) | 3039 (cells cut, announced) | 267958 | -99 % | yes / yes |
| S05 | execute_sql | top 10 customers by payments | 84 | 263 | -68 % | yes / yes |
| S06 | execute_sql | `SELECT * FROM types` (NULL, '', bytea, array, interval) | 111 | 215 | -48 % | yes / yes |
| S07 | execute_sql | `SELECT nme FROM film` (typo) | 56 | 43 | +30 % | yes / yes |
| S08 | list_schemas | | 79 | 179 | -56 % | yes / yes |
| S09 | list_objects | tables of `public` | 339 | 2915 | -88 % | yes / yes |
| S10 | get_object_details | `public.rental` | 488 | 623 | -22 % | yes / no |
| S11 | get_object_details | `public.wide60` (60 columns) | 440 | 2456 | -82 % | yes / yes |
| S12 | explain_query | seq scan on `events` | 113 | 142 | -20 % | yes / yes |
| S13 | explain_query | S12 with a hypothetical index | 152 | 61 | +149 % | yes / yes |
| S14 | explain_query | `analyze=true` | 112 | failed: refused by its own validator | | yes / no |
| S15 | get_top_queries | `total_time`, 10 | 728 | 1092 | -33 % | no / no |
| S16 | get_top_queries | `resources` | 476 | 5043 | -91 % | yes / yes |
| S17 | analyze_db_health | `all` | 678 | 2520 | -73 % | yes / yes |
| S18 | analyze_db_health | `index` | 475 | 2242 | -79 % | yes / yes |
| S19 | analyze_query_indexes | the 3 slowest workload queries | 126 | 3446 | -96 % | yes / yes |
| S20 | analyze_query_indexes | a primary-key lookup | 61 | 455 | -87 % | yes / yes |
| S21 | explain_query | S12 again after S13 | 113 | 61 | +85 % | yes / yes |

- **Headline ratio.** Median tokens of postgres-dba-mcp over the `execute_sql` scenarios S01-S06 that both servers answered (S01, S04, S05, S06), divided by crystaldba's: **39.2 %**.
- **The same ratio with no model at all: 36.6 %.** Computed on the raw bytes of those same four responses (2324 / 4613 / 143 / 147 against 6275 / 400731 / 479 / 345), where no tokeniser takes part. It is lower than the token ratio, so counting with Claude's tokeniser is the more conservative of the two measures, not the flattering one. `go run ./cmd/bench all` without `--tokens=claude` reproduces the byte column with no account.
- **Larger responses.** postgres-dba-mcp is larger on 3 small responses: an error with its SQLSTATE and hint (S07), and the two plans that print both costs and the hypothetical index used (S13, S21).
- **Largest response.** 8442 bytes, under the 32768-byte cap. All 3 cut results say so in their footer.
- **Health traps (S17).** postgres-dba-mcp names all six traps. crystaldba names five; its vacuum check covers transaction-id wraparound, not dead tuples.
- **S19.** Both servers recommend `events (account_id, created_at)` this time, and both gains are within 0.2 %. In the run of 2026-10-22 crystaldba proposed `events (account_id)` and `events (created_at)` separately; this scenario depends on what the 120 s workload recorded, and it moves between runs.
- **S15 is wrong for both servers**, and that is the fixture, not the servers. The check expects the two heavy statements the bench itself sent (`SELECT * FROM events`, `SELECT * FROM rental`) among the ten rows `get_top_queries` returns, and on this host the pgbench workload accumulated more total time than they did, so they fall outside the ten. It passed on the slower host of 2026-10-22.
- **Where crystaldba fails.** S02: over 16 MiB. S03: its own 30 s limit returns an error, which this bench counts as no answer. S10: it lists none of the three foreign keys of `rental`. S14: its validator refuses the `EXPLAIN (ANALYZE)` it built itself.

## Restricted mode (131 attacks, 47 legitimate statements)

Every statement is sent to `execute_sql` in restricted mode. An attack counts as executed when it returns a result; its side effects (locks, settings, notifications, files, sessions) are checked in the database after each call.

| | refused | failed at execution | executed | with a side effect |
|---|---|---|---|---|
| postgres-dba-mcp | **131** | 0 | 0 | 0 |
| crystaldba 0.3.0 | 106 | 15 | 10 | 1 |

- **Both rows cover the 131 cases.** The run of 2026-10-22 was missing the last 7 (`ts_rewrite`, which runs the SQL text it is given, then index maintenance and planner statistics writes), so crystaldba's row then read 102 / 12 / 10 / 1 over the first 124. The 7 new cases change where its failures fall, not what it lets through.
- **This is the one measure the repository checks on every push.** `TestCorpora` sends the 131 statements through the SQL check and `TestAttackCorpusLeavesNoSideEffect` sends them through the server itself, on PostgreSQL 14 to 18, as a read-only role and as a superuser, comparing a snapshot of the database after each one. The bench adds the crystaldba column, not the postgres-dba-mcp one.
- **crystaldba executions.** Its 10 executed cases are:
  - functions called in `FROM` or `LATERAL`: `set_config` (twice), `pg_advisory_lock`, `pg_notify`, `pg_terminate_backend` ([CVE-2026-85620](https://github.com/advisories/GHSA-cm87-gp55-7cgj));
  - `PREPARE`, `DEALLOCATE` and `DECLARE CURSOR`;
  - two statements in one call;
  - an empty statement.

  The side effect is the advisory lock, still held after the call.
- **No false refusals.** postgres-dba-mcp refuses none of the 47 legitimate statements: CTEs, window functions, `EXPLAIN ANALYZE`, catalog functions, `SHOW`, `TABLESAMPLE`, ...

## Operations

| | postgres-dba-mcp | crystaldba 0.3.0 |
|---|---|---|
| Memory after 100 calls of S02, Linux container, `docker stats`, median of 3 containers | 9.3 MB (samples 24.6, 9.3, 8.7) | 246.3 MB (246.3, 268.5, 246.0) |
| Cold start to the `initialize` answer, median of 20 | 12.7 ms (binary on the Linux host) | 2337 ms (includes `docker run`) |
| Size | 22.9 MB static binary | 395 MB image (linux/amd64) |
| 50 concurrent S01 calls on one stdio session, p95 (run 20261108T222200Z) | 0 errors in 50 calls, 29.0 ms | 0 errors in 50 calls, 100.3 ms |

- **Concurrency is measured on a later run** than the rest of this table, 2026-11-08 on [`7afada9`](https://github.com/a-lafon/postgres-dba-mcp/commit/7afada9), because the method changed: one process, one stdio session, 50 `execute_sql` calls in flight at once, counting failures and the p95. The earlier measure sent 50 calls to postgres-dba-mcp alone over streamable HTTP and compared nothing. Every measured server now has a figure; the four of them are in the table of the section below. postgres-dba-mcp is measured on its Linux image, like its memory, because the host build compiles its WASM SQL parser on first use and all 50 calls would wait on that compilation.
- **Memory outlier.** The first of the three postgres-dba-mcp containers reads about 25 MB, as it did in the run of 2026-10-22 and in one before that, and the cause has not been looked into. The median is what the table reports.
- **Memory moved from 15.6 MB to 9.3 MB** between 2026-10-22 and 2026-11-01. `fix(query): cut cells at read time to bound a result's memory` is the change that explains it: an oversized cell is now cut while the row is read instead of after.
- **The host changed too**, and three of these numbers depend on it: the earlier run was on macOS/arm64, this one on Linux/x86-64. Cold start, binary size and the crystaldba image size are not comparable across the two (crystaldba's image is 395 MB for linux/amd64 against 296 MB for the arm64 variant of the same digest). The memory figures are read inside a Linux container either way, so they are.
- **Build without a C compiler** (`go install`). This build parses SQL with a WASM copy of PostgreSQL's parser and peaks near 300 MB of memory at start. The release binaries and the image use the native parser.

## Against DBHub 1.6.0 and MCP Toolbox 1.16.0

A second comparison, on the same harness, the same database and the same read-only role, run on 2026-11-08 on [`7afada9`](https://github.com/a-lafon/postgres-dba-mcp/commit/7afada9). It adds one scenario, S22, on a `vector(1536)` column. [bench/compare.md](bench/compare.md) is the table the harness wrote from that run. The reading of the figures, where postgres-dba-mcp loses and what was decided from it, is in [bench/ANALYSE.md](bench/ANALYSE.md).

**Reproducing it.** The raw JSON stays under `bench/results/<run>/`, which is not versioned, as for every other run on this page. `all` does not cover the other servers, so the run is these commands, from `bench`, after `up` and `load`:

```sh
cd bench
ALL=dba,crystal,dbhub,dbhub-rows100,dbhub-4tools,dbhub-default,toolbox,toolbox-subset
SEC=dba,crystal,dbhub,dbhub-rows100,dbhub-4tools,toolbox,toolbox-subset
RUN=$(date -u +%Y%m%dT%H%M%SZ)
go run ./cmd/bench up && go run ./cmd/bench load
go run ./cmd/bench n1       --run=$RUN --servers=$ALL
go run ./cmd/bench n2       --run=$RUN --servers=$SEC
go run ./cmd/bench security --run=$RUN --servers=$SEC
go run ./cmd/bench session  --run=$RUN --servers=$SEC
go run ./cmd/bench ops      --run=$RUN --servers=dbhub,toolbox
go run ./cmd/toolmeta       --run=$RUN --servers=dba --out=dba
go run ./cmd/toolmeta       --run=$RUN --servers=dbhub,dbhub-rows100,dbhub-4tools,dbhub-default --out=dbhub
go run ./cmd/toolboxmeta    --run=$RUN
go run ./cmd/bench report   --run=$RUN && go run ./cmd/bench compare --run=$RUN
```

`dbhub-default` is left out of `$SEC` on purpose: it has no read-only guard, so replaying the 131 attacks through it would write to the database. The pass takes about 1 h 20 at scale 1, and costs nothing: no model is called.

**This section counts tokens differently from the rest of the page.** Everything above is `claude -p` token counts; here they are the harness estimate `ceil(bytes/4)`, because counting eight server variants with a model costs money and changes no conclusion. JSON tokenises more densely than CSV, so the estimate understates DBHub and Toolbox more than it understates postgres-dba-mcp's CSV: the bias runs against postgres-dba-mcp. Do not compare a number from this section with one from the sections above.

**Versions and digests.** Every image is pinned by digest, no `latest` anywhere.

| | |
|---|---|
| postgres-dba-mcp | built from source at `7afada9`, `--access-mode=restricted`, default limits (`--max-bytes` 32768) |
| crystaldba | `crystaldba/postgres-mcp@sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b` (0.3.0), `--access-mode=restricted` |
| DBHub | `bytebase/dbhub@sha256:ccb6afc0bc1f05c143e15c3c927eb11e8103d49b06947900c33e337f2453f5e3`, which reports `DBHub MCP Server 1.6.0` |
| MCP Toolbox | `us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox@sha256:190bd9d9b3cfe81bed19470bef8f9991d64c113313ec499234e0dd38ca11a841`, which reports `toolbox version 1.16.0+container.release.linux.amd64.06ea49d` |
| Database | PostgreSQL 17.11, scale 1 with Pagila, plus an `embeddings` table with a pgvector `vector(1536)` column (`bench/data/40_vector.sql`) |
| Role | every server connects as the same read-only role `bench_ro` |
| Dollars | claude-sonnet-5-5 input at $2.00 per million tokens, priced 2026-10-06 (`bench/internal/tokens/price.go`); only input is priced, since every figure measured here is what the server puts into the model's context |

**Configurations.** 1.6.0 removed DBHub's `--readonly` and `--max-rows` flags: read-only mode and the row cap are now per-tool fields of a `dbhub.toml`, which the harness writes inside the container. A source with no `[[tools]]` block gets two default tools, so a configuration that keeps the default set must still name them.

| Variant | Tools | Configuration |
|---|---|---|
| `dbhub` | 2 | `readonly = true`, `max_rows = 1000` on `execute_sql`, plus `search_objects`; the reference |
| `dbhub-rows100` | 2 | the same with `max_rows = 100` |
| `dbhub-4tools` | 4 | the reference plus `explain_sql` and `health_check` |
| `dbhub-default` | 2 | `--dsn` alone, no `dbhub.toml`: writes allowed, no row cap |
| `toolbox` | 29 | `--prebuilt postgres --stdio --disable-version-check` |
| `toolbox-subset` | 10 | the same definitions cut to 10, the number postgres-dba-mcp exposes, from `bench/internal/servers/toolbox-subset.yaml`, copied entry by entry from the prebuilt configuration of 1.16.0; its header says which tool was kept for which postgres-dba-mcp tool |

`dbhub-default` was measured on tool definitions only. It has no read-only guard, so replaying the 131 attacks through it would really write to the database.

**Unmapped tools.** A tool the server does not have is counted apart, never as a wrong answer. DBHub's `search_objects` covers listing and describing objects, but under its own arguments (`object_type`, `pattern`, `schema`, `table`, `detail_level`, `limit`), which the harness cannot send in place of `schema_name` / `object_name`, so S08 to S11 read `no tool`; its `health_check` reports pool and cache metrics, not the traps S17 and S18 grade; it has nothing for `get_top_queries` or `analyze_query_indexes`. Toolbox's `get_query_plan` names its required parameter `query`, not `sql`, and has neither ANALYZE nor hypothetical indexes; it has no health tool, and it recommends no index. Its `list_tables` answers both `list_objects` and `get_object_details`, takes no `schema_name`, `object_type` or `object_name`, and silently ignores arguments it does not know, so S09, S10 and S11 all return every user table in full.

### Total tokens per task

One task is one request: the tool definitions the client sends plus the one answer it gets back, the median over the six scenarios every server answered (S01, S04, S05, S06, S07, S22). The session column is a fixed ten-turn investigation where definitions are paid once and every answer accumulates; a server without the right tool runs the same fallback SQL as every other server without it.

| Server | Definitions | Median answer | One task | $ one task | Ten-turn session | $ session |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 1132 | 309 | **1441** | $0.0029 | **2679** | $0.0054 |
| crystaldba 0.3.0 | 1225 | 844 | 2069 | $0.0041 | 5881 | $0.0118 |
| DBHub 1.6.0 | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| DBHub, 4 tools | 431 | 1131 | 1562 | $0.0031 | 6152 | $0.0123 |
| DBHub, `max_rows` 100 | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| DBHub, default config | 276 | not measured | | | | |
| Toolbox 1.16.0 | 4559 | 1376 | 5935 | $0.0119 | 86897 | $0.1738 |
| Toolbox, 10 tools | 1243 | 1376 | 2619 | $0.0052 | 83581 | $0.1672 |

- **On a single request, DBHub wins by 14 tokens**, 1427 against 1441, while answering 8 of the 22 scenarios against our 22. Its two tool definitions cost 296 where our ten cost 1132.
- **From the second answer of a session on, the order reverses**, because definitions are paid once and answers accumulate. Computed from the medians above: two answers give 1132 + 2×309 = 1750 against 296 + 2×1131 = 2558. The measured ten-turn session reads 2679 against 6077.
- **`deferred`**, the tool names plus the server instructions, which is what a client with tool search loads at session start: postgres-dba-mcp 223, crystaldba 38, DBHub 7 (13 with four tools), Toolbox 136 (39 for the subset). This is the one definition figure where postgres-dba-mcp is last of the eight.

### Tool responses (22 scenarios)

Response tokens. `no tool` means the server has no tool the scenario can call.

| ID | Tool | dba | crystal | dbhub | dbhub-4tools | dbhub-rows100 | toolbox | toolbox-subset |
|---|---|---|---|---|---|---|---|---|
| S01 | execute_sql | 581 | 1569 | 1986 | 1986 | 1986 | 2672 | 2672 |
| S02 | execute_sql | 2111 | failed (oversized) | 76425 | 76425 | 7670 | 2519761 | 2519761 |
| S03 | execute_sql | 1969 | failed (oversized) | 65349 | 65349 | 6585 | failed (oversized) | failed (oversized) |
| S04 | execute_sql | 1154 | 100183 | 100606 | 100606 | 100606 | 100153 | 100153 |
| S05 | execute_sql | 36 | 120 | 276 | 276 | 276 | 80 | 80 |
| S06 | execute_sql | 37 | 87 | 233 | 233 | 233 | 64 | 64 |
| S07 | execute_sql | 28 | 29 | 24 | 24 | 24 | 26 | 26 |
| S08 | list_schemas | 41 | 93 | no tool | no tool | no tool | 78 | 78 |
| S09 | list_objects | 156 | 1336 | no tool | no tool | no tool | 40030 | 40030 |
| S10 | get_object_details | 246 | 323 | no tool | no tool | no tool | 40030 | 40030 |
| S11 | get_object_details | 220 | 1259 | no tool | no tool | no tool | 40030 | 40030 |
| S12 | explain_query | 63 | 63 | no tool | 191 | no tool | no tool | no tool |
| S13 | explain_query | 84 | 25 | no tool | 191 | no tool | no tool | no tool |
| S14 | explain_query | 57 | failed (error) | no tool | 177 | no tool | no tool | no tool |
| S15 | get_top_queries | 314 | 509 | no tool | no tool | no tool | 786 | 786 |
| S16 | get_top_queries | 530 | 2635 | no tool | no tool | no tool | 10223 | 10226 |
| S17 | analyze_db_health | 359 | 1400 | no tool | no tool | no tool | no tool | no tool |
| S18 | analyze_db_health | 250 | 1226 | no tool | no tool | no tool | no tool | no tool |
| S19 | analyze_query_indexes | 53 | 1991 | no tool | no tool | no tool | no tool | no tool |
| S20 | analyze_query_indexes | 35 | 250 | no tool | no tool | no tool | no tool | no tool |
| S21 | explain_query | 62 | 25 | no tool | 191 | no tool | no tool | no tool |
| S22 | execute_sql | 772 | 49065 | 49322 | 49322 | 49322 | 49038 | 49038 |

| Server | Median (6 common) | Answered | Correct | No tool | Failed |
|---|---|---|---|---|---|
| postgres-dba-mcp | 309 | 22 | 22/22 | 0 | 0 |
| crystaldba 0.3.0 | 844 | 19 | 17/19 | 0 | 3 |
| DBHub 1.6.0 | 1131 | 8 | 7/8 | 14 | 0 |
| DBHub, 4 tools | 1131 | 12 | 9/12 | 10 | 0 |
| DBHub, `max_rows` 100 | 1131 | 8 | 7/8 | 14 | 0 |
| Toolbox 1.16.0 | 1376 | 13 | 11/13 | 8 | 1 |
| Toolbox, 10 tools | 1376 | 13 | 11/13 | 8 | 1 |

- **S04, 20 kB jsonb cells.** postgres-dba-mcp 1154 against 100153 to 100606 for every other server. It is also the only scenario the other three answer incorrectly: none of them shortens a cell, so the grader reads `truncation missing`.
- **S02 and S03, unbounded results.** postgres-dba-mcp cuts at 32768 bytes and says so in the footer. DBHub returns 76425 and 65349 tokens at `max_rows = 1000`, 7670 and 6585 at 100. Toolbox returns 2519761 tokens on S02 without announcing a cut, which the grader counts as incorrect, and fails S03. crystaldba fails both.
- **S09 to S11, exploration.** Toolbox returns the same 40030 tokens for all three, because `list_tables` is its only exploration tool and ignores the arguments the scenario sends. postgres-dba-mcp: 156, 246, 220.
- **S22, a `vector(1536)` column.** postgres-dba-mcp 772 against 49038 to 49322, because a vector cell is summarised as `[vector: N dims]` rather than cut at 200 characters.
- **Where postgres-dba-mcp is the more expensive.** Three scenarios of 22: S07, an error with its SQLSTATE and hint, 28 against DBHub's 24 and Toolbox's 26; S13 and S21, the two plans that print both costs and the hypothetical index used, 84 and 62 against crystaldba's 25.

### Restricted mode (131 attacks, 47 legitimate statements)

Every statement goes to `execute_sql`. An attack counts as executed when it returns a result; its side effects are checked in the database after each call.

| Server | Refused | Failed at execution | Executed | With a side effect | False refusals of 47 |
|---|---|---|---|---|---|
| postgres-dba-mcp | **131** | 0 | 0 | 0 | 0 |
| crystaldba 0.3.0 | 106 | 15 | 10 | 1 | 4 |
| DBHub 1.6.0 | 80 | 40 | 11 | 5 | 3 |
| Toolbox 1.16.0 | 0 | 93 | 38 | 8 | 0 |

The three DBHub variants with a `dbhub.toml` score identically, and so do the two Toolbox variants: read-only mode is a property of the tool, not of the tool set.

- **DBHub refuses by keyword, before the database is touched** (`READONLY_VIOLATION`); the other 51 cases reach PostgreSQL, where `bench_ro` stops 40 of them and 11 succeed. Its 5 side effects are three advisory locks still held and two notifications delivered.
- **Toolbox has no read-only mode** for a self-hosted PostgreSQL: it refuses nothing, and the 93 it does not execute are stopped by `bench_ro` lacking the right. Its 8 side effects are one large object created, three advisory locks still held and four notifications delivered. `fn-dynsql-lateral` (`ts_stat` inside a `LATERAL`) kills the Toolbox process.
- **DBHub's keyword filter refuses legitimate SQL**: `TABLE accounts`, a `VALUES` list and a statement with a trailing semicolon and comment. crystaldba refuses four, including `EXPLAIN (ANALYZE, BUFFERS)`, which it builds itself. postgres-dba-mcp and Toolbox refuse none.

### Operations

| Server | Cold start | Memory after 100 S02 calls | Binary / image | 50 concurrent calls on one stdio session |
|---|---|---|---|---|
| postgres-dba-mcp | 5.9 ms | 9.7 MB | 22.9 MB binary | 0 errors, p95 29.0 ms |
| crystaldba 0.3.0 | 1352.0 ms | 247.9 MB | 395.0 MB image | 0 errors, p95 100.3 ms |
| DBHub 1.6.0 | 732.6 ms | 127.1 MB | 344.8 MB image | 0 errors, p95 60.1 ms |
| Toolbox 1.16.0 | 199.5 ms | 89.6 MB | 422.5 MB image | 0 errors, p95 25.4 ms |

Cold start is the median of 20 and includes `docker run` for the three containers; postgres-dba-mcp is the native binary there. Memory is `docker stats` inside a Linux container, median of 3 containers. Concurrency is one method for every server: one process, one stdio session, 50 `execute_sql` calls in flight at once. **Toolbox is faster than postgres-dba-mcp on that p95**, 25.4 ms against 29.0 ms.

## Agent tasks (9 tasks × 3 runs per server)

**These runs are from 2026-10-22 and were not repeated on 2026-11-01**, because they cost model calls. They are from 0.1.0, before the volatility gate that 0.2.0 adds to restricted mode: a `VOLATILE` function of the database that no review cleared is now refused, and these runs predate that rule. They also predate `analyze_locks`, which the agents therefore never had. crystaldba is pinned by image digest, so its column is unaffected.

Claude Code answers a question with only the server's tools (`claude -p "<question>" --allowedTools "mcp__pg__*"`). A deterministic rubric grades each run, on the answer and on the database state.

| Task | Question | Solved dba | Solved crystal |
|---|---|---|---|
| T01 | Which 5 customers spent the most? | 3/3 | 3/3 |
| T04 | Why is this `events` query slow, and what should I do? | 3/3 | 2/3 |
| T06 | Run a health check of the database and prioritize the actions. | 3/3 | 3/3 |
| T08 | Show me a representative sample of the `events` table. | 3/3 | 3/3 |
| T09 | What is the content of the `payload` column of document 42? | 3/3 | 2/3 |
| T10 | Delete the events older than one year. (must be refused) | 3/3 | 3/3 |
| T11 | Which tables reference `customer`, and which of those foreign keys have no index? | 3/3 | 3/3 |
| T12 | What are the top 5 film categories by rental revenue? | 3/3 | 3/3 |
| T13 | Look at the workload and recommend indexes. | 3/3 | 3/3 |
| all | | **27/27** | 25/27 |

- **Turns.** The median is 5 turns per task for postgres-dba-mcp and 6 for crystaldba.
- **Tokens.** Median tokens per task were 1.7 % lower in a first run and 19.7 % lower in this one. With 3 runs per task, this number moves a lot from run to run; read it as "not more", not as a gain.
- **Tool definitions** (measured on 2026-11-01, not part of these runs). postgres-dba-mcp registers ten tools against crystaldba's nine, and they are larger: **2732 tokens against 1962**, or 322 against 67 when the client defers tool loading. On 2026-10-22, before `analyze_locks`, it was 2459 against 1965. Drop `locks` from `--tools` for the 0.3.0 tool set exactly.

## Answer quality (LLM judge)

**Method.** For each task and run, a judge compares the two final answers blind, as A and B, against facts computed from the database. The judge is Claude Opus 5.5 through `claude -p`, with no tool.
- Each pair is judged twice, with A and B swapped. A server wins only when both orders agree; otherwise the pair is a tie.
- The whole judgment is repeated 3 times to measure stability.

| Task | dba better | equal | crystal better |
|---|---|---|---|
| T01 | 0 | 3 | 0 |
| T04 | 0 | 3 | 0 |
| T06 | 3 | 0 | 0 |
| T08 | 2 | 0 | 1 |
| T09 | 0 | 3 | 0 |
| T10 | 0 | 2 | 1 |
| T11 | 0 | 3 | 0 |
| T12 | 0 | 3 | 0 |
| T13 | 0 | 2 | 1 |
| all | **5** | **19** | **3** |

**What the table combines.** T01-T12 come from the full run on `f0d7f20`. T13 is the postgres-dba-mcp answers of three new runs on `daf8c3a`, judged against the same crystaldba answers. Before that change, crystaldba won T13 3 times out of 3.

**The two tasks that drove changes:**
- **T06.** postgres-dba-mcp lost it until the buffer line of `analyze_db_health` named the tables behind the low cache hit rate. Agents then found the missing `events` index in 5 runs out of 5, against 1 out of 5 before.
- **T13.** crystaldba's own workload advisor fails on this workload (`Error evaluating configuration`). Its agent then tests each query by hand with hypothetical indexes, over 14 turns, and covers every access pattern. postgres-dba-mcp's advisor answers in one call, but it used to leave out an index that serves one query and little of the workload. It now names it. The one remaining loss is a choice of columns: `orders (account_id, created_at)` from the advisor's costs, against `(account_id, status, created_at)` built by hand.

**Stability.** 25 of the 27 pairs got the same outcome in all 3 repeats.

**Agreement with a human.** On a sheet of 10 blind pairs from an earlier run, the maintainer and the judge agreed on 8.

**Where the errors come from.** A second pass reads the tool calls and classifies every factual error the judge found. This pass is not blind, because tool names reveal the server.

| Errors | from a tool output | misread by the agent | unsupported by anything | total |
|---|---|---|---|---|
| postgres-dba-mcp | 0 | 2 | 13 | 15 |
| crystaldba 0.3.0 | 7 | 2 | 12 | 21 |

The tool errors on the crystaldba side come from `analyze_db_health` (dead tuples not checked, no missing-index finding) and `get_top_queries`.

**Limits.**
- The judge is a Claude grading Claude answers, and an answer may name its tools.
- 3 runs per task is a small sample.
- The judge's output is information. The go/no-go of the harness uses the deterministic rubrics only.

## Cost of a run

- Token counting of the tool definitions and the 21 scenarios: 5.13 USD on 2026-11-01 (5.07 USD on 2026-10-22). Everything else on this page costs nothing: restricted mode, operations and the scenarios themselves call no model.
- Agent tasks: 5.35 USD (postgres-dba-mcp) and 6.12 USD (crystaldba).
- Judge: 5.60 USD.
- T13 run again with its judge: 1.32 USD.

All of it ran through Claude Code `claude -p` on one account. `go run ./cmd/bench all` without `--tokens=claude` reproduces every measure but the token counts, which then read as `ceil(bytes/4)` estimates, for nothing. The 2026-11-01 run took about 40 minutes.
