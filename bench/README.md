# bench

Measures postgres-dba-mcp against crystaldba/postgres-mcp 0.3.0 and writes `report.md` with the go/no-go verdict. It is its own Go module: its dependencies (the Anthropic SDK in particular) never reach the server's `go.mod`. The method, the targets and the published results are in [`../BENCHMARK.md`](../BENCHMARK.md).

Requirements: Go 1.26 or newer (`go.mod` pins a newer toolchain, which the `go` command fetches on its own unless `GOTOOLCHAIN=local`), Docker with Compose (and the `golang:1.26-alpine` image, pulled on first use, which builds the linux image), and for N3 only the `claude` CLI. Port 55433 (bench database) and 55434 (security fixture) must be free; the repository's own fixture on 55432 is never touched. To run a second harness beside a first one, set `BENCH_PROJECT`, `BENCH_PG_PORT` and `BENCH_FIXTURE_PORT` (defaults `bench`, 55433, 55434): `compose.yml` and the harness read the same three variables, so `BENCH_PROJECT=benchw1 BENCH_PG_PORT=55533 BENCH_FIXTURE_PORT=55534 go run ./cmd/bench up` is a separate project, with its own volumes, network and ports. The image tag `postgres-dba-mcp:bench` stays shared.

## Which levels need a model

| Level | Model |
| --- | --- |
| `load`, `n2`, `security`, `ops`, `session`, `compare`, `report` | none |
| `n1` | none; `ANTHROPIC_API_KEY` or `--tokens=claude` only to count tokens exactly |
| `all` (up, load, n1, n2, security, ops, report) | none; the same optional token counting, and it never runs `n3` |
| `n3` | yes, the `claude` CLI: the agent tasks are real model calls |
| `judge` | yes, the `claude` CLI: it grades the stored `n3` answers |

Without `ANTHROPIC_API_KEY` (and without `--tokens=claude`), everything but `n3` and `judge` still runs: token counts fall back to `ceil(bytes/4)` estimates and the token targets read `unverified`.

## Commands

Run them from this folder.

```sh
go run ./cmd/bench up                      # docker compose up (pg, fixture), build bin/postgres-dba-mcp and the linux image postgres-dba-mcp:bench
go run ./cmd/bench load --scale=0.1        # data set, traps, Pagila, ANALYZE, then 120 s of pgbench
go run ./cmd/bench n1                      # tool-definition tokens (informational)
go run ./cmd/bench n2                      # S01-S22, 5 runs per scenario and server
go run ./cmd/bench security                # attack corpus replay through both servers
go run ./cmd/bench ops                     # cold start, memory (linux image, docker stats; native binary as information), sizes (release-flag build: -trimpath -ldflags "-s -w"), 50 concurrent calls on one stdio session
go run ./cmd/bench session                 # ten-turn DBA investigation: what each server accumulates in context, no model call
go run ./cmd/bench n3                      # 6 agent tasks with `claude -p`: costs model calls, run by hand only
go run ./cmd/bench n3 --tasks=extra        # T11-T13 (schema reading, index tuning); --tasks=all runs the nine
go run ./cmd/bench judge --run=<id> [--also=<id>,<id>] [--max-cost=<usd>] [--repeat=<n>]  # LLM judge of the stored n3 answers, blind pairwise then error attribution: costs model calls
go run ./cmd/bench judge --calibration=results/<id>/calibration.md  # agreement of your filled calibration sheet with the judge (no call)
go run ./cmd/bench report                  # write report.md from the latest run
go run ./cmd/bench compare                 # write compare.md: one column per measured server, from the raw results
go run ./cmd/bench all                     # up, load, n1, n2, security, ops, report (never n3)
python3 charts.py results/<id> ../assets/bench  # the three charts of BENCHMARK.md; needs matplotlib, nothing else does
```

`all` takes about 40 minutes at `--scale=1` on a Linux laptop, and about 10 at `--scale=0.1`. Most of it is `n2` against crystaldba, which answers S02 and S03 by exceeding a limit, and `ops`, which fills three containers per server with 100 calls each. Add `--tokens=claude` and it costs about 5 USD on top; without it the run is free.

Flags: `--run=<id>` (folder of `results/<id>/`; default a new UTC timestamp, and for `report` the latest run), `--scale=<f>` and `--no-pagila` (`load`), `--with-pgedge` (`n2`, S01-S06 only), `--servers=<a,b>` (`n1`, `n2`, `security`: measure these servers only, in that order; default `dba,crystal`), `--fail-on-blocking` (`report`: exit 1 when the security or max-bytes target is not met). Without `ANTHROPIC_API_KEY`, token counts are `ceil(bytes/4)` estimates, labelled as such, and the token targets read `unverified`. Set `ANTHROPIC_API_KEY` (and optionally `BENCH_MODEL`) to count with the `count_tokens` endpoint. `--tokens=claude` (`n1`, `n2`, `all`) counts through `claude -p` instead, with no API key: the count of a text is the input total (input + cache creation + cache read) of a one-turn, no-tool, no-MCP call with the text minus that of an empty text, as the median of 3 calls (identical calls differ by a few tokens), once per distinct text; a text above 1 MB is estimated and shown as `~n`. The report header then reads `tokens: claude -p (<model>)`, the model being the account default, and the token targets are verified. N1 `full` counts the JSON of the tool definitions as a text, which is not how Claude Code formats tools. The cost of the counting calls is printed to stderr. `n2` also keeps the first-run response text of every cell in `results/<run>/n2/<id>-<server>.txt`.

### Without Go installed

The `bench` service of `compose.yml` runs the same commands inside `golang:1.26-alpine`. Run it from the repository root, not from this folder:

```sh
docker compose -f bench/compose.yml run --rm bench all
docker compose -f bench/compose.yml run --rm bench n2
```

It mounts the repository at the path it has on the host (the harness drives the host's Docker, which resolves every bind mount as a host path), uses host networking to reach the databases on 127.0.0.1, and shares the Go caches `pgmcp-gocache` and `pgmcp-gomod` with the linux build.

**It also mounts `/var/run/docker.sock`, because the harness itself calls `docker compose`, `docker run` and `docker stats`. Anything that can talk to the Docker socket can start a container that mounts the whole host filesystem as root: this is root-equivalent access to the host machine, for the harness and for every dependency it builds and runs.** Running the harness on the host instead of in this container gives away nothing less — it runs the same Docker commands as your user — but a container asks you to trust the mount, so it is said plainly. `n3` is not available here: the `claude` CLI is not in the image.

`load` refuses a database that is already loaded. To start over: `docker compose -f compose.yml down -v`, then `up`, then `load`.

## Data

The bench database is the test fixture's base schema at scale (events 5 000 000, orders 1 000 000, accounts 50 000, times `--scale`), plus:

- schema `traps`: T1 invalid index, T2 duplicate indexes, T3 dead tuples (37.5 %), T4 sequence at 91 %, T5 NOT VALID constraint, T6 unused index (over 100 MB at scale 1);
- `public.docs` (20 kB jsonb, 50 kB text), `public.wide60` (60 columns), `public.types` (NULL, empty string, bytea, array, interval, timestamptz);
- `public.embeddings` (2 000 rows times `--scale`, at least 10): a `vector(1536)` column of pseudo-random unit vectors and its text metadata, what a RAG application stores. It needs pgvector, which the image of `testdata/docker` installs;
- Pagila (`film`, `rental`, `payment`, ...), fetched from the commit pinned in `data/pagila.lock` and checked against its sha256 before anything is loaded. The pinned commit is the last one whose schema runs on PostgreSQL 17 (later ones use `uuidv7()`); with `PG_MAJOR=18` a newer commit can be pinned.

How `load` was loaded is recorded in the database comment, which every level reads (scale, Pagila).

## Checks done by hand on the data set (scale 0.1)

Queries run with `docker compose -f compose.yml exec -T pg psql -U postgres -d bench`:

- Traps present: `SELECT count(*) FROM pg_index WHERE NOT indisvalid` over schema `traps` returns 1; `traps.dup_events` holds the 2 indexes; `traps.bloated` has 37.5 % dead tuples; `traps.legacy_id_seq` is at 91.0 %; `amount_positive` is not validated; `traps.unused_payload_hash_idx` has 0 scans (18 MB at scale 0.1, over 100 MB at scale 1).
- Workload recorded: `SELECT count(*) FROM pg_stat_statements WHERE calls >= 50 AND query ~ 'public\.(events|orders|accounts)'` returns 10.
- Pagila checksum mismatch: put a wrong sha256 in `data/pagila.lock`, run `load` on a fresh database: it stops with `pagila: <file> checksum mismatch: expected sha256 <lock>, got <actual>` before loading anything.

## Notes

- The workload (`data/workload.pgbench`) keeps its three slowest shapes on `events(account_id, created_at)`, which is what S19 expects, and avoids `BETWEEN $n::timestamptz AND $m::timestamptz` and `timestamptz $n` literals: crystaldba 0.3.0's parameter substitution makes its advisor fail on both (`Error evaluating configuration`, `syntax error at or near`), which would turn S19 into a comparison of that bug rather than of the advice.
- At scale 0.1, crystaldba's S02 and S03 answers exceed the 16 MiB cap (`failed (oversized)`). For the RSS measure of `ops` the cap is raised to 512 MiB so that S02 can be called 100 times on crystaldba.
- The memory target of `ops` reads postgres-dba-mcp as it ships to containers, measured like crystaldba. `up` builds a static musl cgo binary (`CGO_ENABLED=1`, `-tags pgquery_cgo,netgo,osusergo`, `-trimpath`, `-ldflags "-s -w -linkmode=external -extldflags '-static -s'"`, as `.goreleaser.yaml`) inside `golang:1.26-alpine` for the architecture of the Docker daemon (Go and apk caches in the volumes `pgmcp-gocache` and `pgmcp-gomod`), then the image `postgres-dba-mcp:bench` from the root `Dockerfile` (context `bin/image/`, binary under `bin/image/<os>/<arch>/` and `TARGETPLATFORM` passed as a build arg, because that Dockerfile is goreleaser's and reads the binary under that variable). `ops` builds it when the image is absent (rebuild with `up`), runs it with `docker run -i --rm` on the `bench_bench` network, makes the 100 S02 calls and reads `docker stats`. This is done in 3 containers one after the other, for the image and for crystaldba; the measure is the median of the 3 (the 3 samples are in `rss_mb[].samples` of `ops.json`) and the target checks it, because one sample varied from 14 to 30 MB between runs of the same code. The cell reads `16.0 MB (linux image, docker stats; median of 3, max 30.3 MB)`. The native binary of the host (`phys_footprint` on macOS, `VmRSS` on Linux; the darwin build uses the WASM parser, so it reads more) is measured once after the same calls and shown as an informational line (`native_rss_mb` in `ops.json`); it does not decide the target. Binary size and cold start stay on the native binary; concurrency is measured on the image, for the reason given below.
- **S15 depends on the host.** Its expected facts are the two heavy statements the bench itself sent (`SELECT * FROM events`, `SELECT * FROM rental`), and it only passes when they outrank the 120 s pgbench workload in `get_top_queries(total_time, 10)`. On a fast host the workload accumulates more total time and S15 reads `no` for both servers. It is a property of the fixture: the comparison between the two servers is unaffected, and `n2 --recheck` will not change it.
- **A server that answers with an error has not answered.** `gradeCell` marks the cell `failed (error)` and incorrect, everywhere but S07, whose expected answer is an error. Without that, crystaldba's own 30 s limit on S03 returns a 44-token error text in milliseconds, which would be graded a correct answer and would enter the S01-S06 token median.
- The MCP client uses the legacy `initialize` handshake. The SDK's default first sends `server/discover`, which crystaldba's mcp 1.6.0 never answers.
- crystaldba runs from the digest-pinned image on the `bench_bench` network, host `pg` (or `fixture`), so its entrypoint does not rewrite the URI.
- `count_tokens` rejects an empty message, so the "empty conversation" baseline of a text is a single space.
- N3 input tokens are the `usage` input tokens of the `result` event plus the cache creation and cache read tokens.
- N3 is unit-tested on hand-written sample streams (`internal/agent/testdata/`), and was first run for real on 2026-10-21 (run `mvp-s1`, 36 runs on the maintainer's Claude Code account). `n3 --regrade --run=<id>` grades the stored transcripts again after a rubric change, without calling `claude`. `n2 --recheck --run=<id>` does the same for the stored N2 outputs (it reads the bench database for the facts and calls no server).
- N3 names the crystaldba container (`docker run --name`, in a per-run copy of `n3/crystal.json`) and removes it after each run: `claude -p` kills the `docker run -i --rm` client when it exits, which leaves the container running (reproduced: the container survives `kill -9` of the client).
- `judge` (informational, never part of the verdict) has three parts. (1) Blind pairwise pass: `claude -p`, with no tool and no MCP server, gets the question, the reference facts computed from the database (`internal/judge/facts.go`) and the two final answers as A and B (prompt: `internal/judge/prompt.md`, which says that an omission stated openly is not an error and that cited values are judged against the facts). Each pair (task, run index) is judged twice with A and B swapped; a server wins only when both orders agree, otherwise the pair is a tie. Scores 1-5 on correctness, completeness and actionability, plus the factual errors listed by the judge. (2) Attribution pass (`internal/judge/attribution.md`), not blind because the tool names reveal the server: per answer, the judge gets the tool calls of the transcript with their results (each cut to its first 1800 and last 600 characters), the facts, the answer and the errors of the pairwise pass, and classifies every error as `tool` (the tool output was wrong, ambiguous or lossy), `misread`, `unsupported` or `none`; the `tool` errors, with tool and transcript file, are the to-fix list of the product. (3) `--repeat=<n>` judges every pair n times and the report gives the share of pairs with the same outcome in every pass, per task. The raw replies are kept in `results/<run>/judge/` (a re-run does not pay for them again), the verdicts in `judge.json`, and `compare/<task>.md` shows both answers side by side. `--also=<run>,<run>` takes the tasks that `--run` lacks from other runs, `--max-cost` stops the calls above that many USD. `calibration.md` holds 10 blind pairs (fixed seed) for you to decide with a `Verdict (A / B / tie):` line each, `calibration-key.json` the mapping to servers and the judge's verdicts; `judge --calibration=<filled sheet>` prints the agreement. `BENCH_JUDGE_MODEL` sets the judge model, else the account default is used and reported; `BENCH_JUDGE_EFFORT` (`low` to `max`) sets its `--effort`, reported too. Kept replies are reused whatever the model or effort, so judge with another one under a new `--run`. The judge is a Claude grading Claude, and the answers are not fully anonymous (they may name their tools).
- pgEdge (`--with-pgedge`) is optional and untested: it calls its `query_database` tool for S01-S06 and reads `skipped` when `pgedge-postgres-mcp` is not installed.
- Verdict: GO needs N3 success, N3 tokens (non-regression: at most crystaldba's; the output-size gain is judged on N2), N3 turns and N2 trap detection met and no criterion `unverified`; the other N2 targets are reported in the verdict table but do not change the verdict.

## Levels that compare every server

`report.md` is the go/no-go of postgres-dba-mcp against crystaldba: two columns and a verdict. `compare.md` (`go run ./cmd/bench compare [--run=<id>]`) is the cross-server synthesis, generated from the same raw JSON: one column per measured server for tool definitions, scenarios, attacks, false refusals, memory, start-up, concurrency, session accumulation and the total per task. They are two commands over one run because folding eight columns into the verdict table would make both unreadable.

- **Dollars.** `internal/tokens/price.go` holds one row per model: input and output price per million tokens, the date they were read and their source. Every dollar figure `compare.md` prints says at what price and on what date it was computed. To update a price, change that row and its date; nothing else reads one.
- **`session`.** A fixed sequence of ten DBA intentions (schemas, tables, one table in detail, table sizes, the plan of a slow query, the costliest queries, the indexes, then three targeted reads). Each server answers with its best tool; a server that has no such tool falls back to `execute_sql` with the same SQL as every other server that falls back, and that SQL is written to `session.json`. The figure is the context at turn N: the tool definitions, which every request carries, plus every answer from turn 1 to N. It calls no model and costs nothing.
- **`ops` concurrency.** One method for every server: one process, one stdio session, 50 `execute_sql` calls in flight at once, counting failures and the p95. It replaces the old measure, which sent 50 calls to postgres-dba-mcp alone over streamable HTTP and compared nothing. A server that cannot answer concurrent calls on one session is recorded as such, not measured another way. postgres-dba-mcp is measured on the linux image, like its RSS: the host build parses SQL through the WASM libpg_query, whose module compiles on first use (about 880 ms, started in the background at start-up), and the session opens the instant the process answers `initialize`, so all 50 calls would wait on that compilation; measured on the native build the p95 is 953 ms cold and 26 ms once one call has gone through, and on the image 22 ms cold. The containerised servers finish their own start-up before they answer `initialize` at all, so none of them is charged for it.
- **`security` false refusals.** The 47 legitimate queries are replayed through every measured server, not through postgres-dba-mcp alone; `security.json` holds `legit` as a map by server. An older `security.json` with a `legit` array no longer loads.
- **Facts are graded on the value, not the rendering.** `Fact` has a third form, a regular expression for the servers that answer in JSON, because DBHub and Toolbox render arrays, intervals and bytea differently from each other. A fact with no form for a server is not asked of it, as the SQLSTATE is not asked of crystaldba.
- **The token counter understates JSON.** Without `ANTHROPIC_API_KEY` and without `--tokens=claude`, counts are `ceil(bytes/4)`. JSON tokenises more densely than CSV, so the estimate understates DBHub and Toolbox more than it understates postgres-dba-mcp's CSV: the bias runs against postgres-dba-mcp, which is the right way round for a comparison it is a party to, but it is a bias.

## DBHub 1.6.0

`internal/servers/dbhub.go` registers four DBHub variants; `--servers=dba,dbhub` (and the others below) measures them on `n1`, `n2` and `security`. The image is pinned by digest, `bytebase/dbhub@sha256:ccb6afc0bc1f05c143e15c3c927eb11e8103d49b06947900c33e337f2453f5e3` (1.6.0), and runs on the bench network like crystaldba, as `bench_ro`.

1.6.0 removed `--readonly`, `--max-rows`, `READONLY` and `MAX_ROWS`: each of them now exits with an error that points at a `dbhub.toml`, where read-only mode and the row cap are per-tool fields of the `[[tools]]` block of `execute_sql`. The harness writes that file inside the container from a `DBHUB_TOML` environment variable (`sh -c 'printf … > /tmp/dbhub.toml && exec node dist/index.js --config=/tmp/dbhub.toml'`), so no file is needed on the host. A source with no `[[tools]]` block gets the two default tools, so a configuration that keeps the default tool set must still list `execute_sql` and `search_objects` explicitly.

| variant | tools | configuration |
| --- | --- | --- |
| `dbhub` | 2 | `readonly = true`, `max_rows = 1000`; the reference |
| `dbhub-rows100` | 2 | the same with `max_rows = 100` |
| `dbhub-4tools` | 4 | the reference plus `explain_sql` and `health_check` |
| `dbhub-default` | 2 | `--dsn` alone, no `dbhub.toml`: writes allowed, no row cap |

The four built-in tools are `execute_sql`, `search_objects`, `explain_sql` and `health_check`; the first two are the default set, the other two only exist once a `[[tools]]` block names them. Only `execute_sql` (and `explain_sql` in `dbhub-4tools`, as `explain_query`) is mapped to a canonical bench tool:

- `search_objects` covers listing and describing objects, but under its own arguments (`object_type`, `pattern`, `schema`, `table`, `detail_level`, `limit`), which the harness has no way to send in place of `schema_name`/`object_name`. S08-S11 therefore read `no tool`.
- `health_check` reports connection pool and buffer cache metrics, not the trap detection S17 and S18 grade, so `analyze_db_health` stays unmapped.
- DBHub has nothing for `get_top_queries` or `analyze_query_indexes`.

`ClassifyDBHub` reads the `code` field of DBHub's error JSON: `READONLY_VIOLATION` is the read-only gate answering before the database is touched (refused), every other code means the statement reached Postgres (`EXECUTION_ERROR`: `permission denied for function lo_import`, `cannot execute SELECT FOR UPDATE in a read-only transaction`) and is counted as failed at execution.

`go run ./cmd/toolmeta --run=<id>` writes `results/<run>/dbhub.json` after `n1`: image and digest, the version the server reports, the command line and the `dbhub.toml` of every variant, the tools it exposes with the token cost of each definition, and the environment read back from `n1.json`. `n1` gives the total for the same tool sets; this is the per-tool breakdown it does not print. It takes any registered server, so `--servers=dba --out=dba` writes the same breakdown for postgres-dba-mcp.

Three limits of the harness used to show up on DBHub; all three are fixed. `Fact` now has a JSON form, so S02, S04, S05, S06 and S07 are graded on content; `Spec.Truncated` lets DBHub say that it signals a cut result with `"truncated": true`, so S02 and S03 read `signalled`; and the setup steps go through `Spec.ToolName`, so S21 runs on `dbhub-4tools`. What remains is not a harness limit: DBHub never shortens a cell, so S04 reads `truncation missing` for every DBHub variant, and `explain_sql` ignores `analyze`, so S14 finds no actual rows.
## MCP Toolbox 1.16.0

Two variants, both from the image pinned by digest in `internal/servers/toolbox.go`
(`us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox@sha256:190bd9d9…`, which reports `toolbox version
1.16.0+container.release.linux.amd64.06ea49d`), started like crystaldba: `docker run -i --rm` on the bench network,
database host `pg` (or `fixture`), user `bench_ro`. The prebuilt `postgres` source reads the connection from
`POSTGRES_HOST`, `POSTGRES_PORT`, `POSTGRES_DATABASE`, `POSTGRES_USER` and `POSTGRES_PASSWORD`.

```sh
go run ./cmd/bench n1 --servers=dba,toolbox,toolbox-subset --run=<id>
go run ./cmd/bench n2 --servers=dba,toolbox,toolbox-subset --run=<id>
go run ./cmd/bench security --servers=toolbox --run=<id>
go run ./cmd/toolboxmeta --run=<id>        # results/<id>/toolbox.json: image, digest, version, command lines, cost per tool
```

- `--servers=toolbox`: `--prebuilt postgres --stdio --disable-version-check`, the 29 prebuilt tools.
- `--servers=toolbox-subset`: the same definitions cut to 10, the number postgres-dba-mcp exposes. The file is
  `internal/servers/toolbox-subset.yaml`, copied entry by entry from the prebuilt configuration of 1.16.0 and mounted
  at `/config/tools.yaml`; its header says which tool was kept for which postgres-dba-mcp tool. A file is the only way
  to pick a subset over stdio: `--prebuilt` serves all 29, and the prebuilt toolsets (`data`, `monitor`, `health`,
  `view-config`, `replication`) are only addressable by URL path in HTTP mode. The file is written to the host's
  temporary folder for the Docker daemon to mount, so this variant does not run from the `bench` compose service.

Three canonical tools are left unmapped, so their scenarios read `no tool` instead of a failure:

- `explain_query`: Toolbox has `get_query_plan`, but its required parameter is named `query`, not `sql`, so the
  harness call is rejected before it runs (`provided parameters were invalid: parameter "query" is required`). It has
  no ANALYZE and no hypothetical index either, so S14 and S21 are out of reach whatever the parameter name.
- `analyze_db_health`: Toolbox has no health tool, it has a `health` toolset of eight separate checks.
- `analyze_query_indexes`: Toolbox recommends no index.

`list_tables` answers both `list_objects` and `get_object_details`: it is the only exploration tool Toolbox has. It
takes neither `schema_name`, `object_type` nor `object_name`, and silently ignores arguments it does not know (unlike
a missing required one), so S09, S10 and S11 all return every user table in full detail, 160 kB each.

**Read-only mode: there is none for a self-hosted Postgres in 1.16.0.** The `postgres` source has no such option, and
`internal/sources/postgres/postgres.go` hard-codes `func (s *Source) IsReadOnly() bool { return false }`, which is
what makes `execute_sql` annotate itself `readOnlyHint: false, destructiveHint: true`. Over the 131 attacks Toolbox
refused none: 93 failed at execution (54 of them `SQLSTATE 42501`, permission denied, which is `bench_ro` stopping
them, not the server) and 38 executed. `Spec.Classify` is therefore nil, the default classification fits.

**Known limit of the comparison.** `Fact.For` only has a postgres-dba-mcp form and a crystaldba one, and falls back to
the postgres-dba-mcp (CSV) form for any other server. Toolbox answers one JSON object per row, so S05, S06 and S10
read incorrect although the values are there: the facts are searched as `526,358.12`, `\N` or `→ customer(customer_id)`.
S02 and S04 read incorrect for a real reason (no bound, no truncation signal) and S03 (`SELECT * FROM events`) exceeds
the 16 MiB response cap.
