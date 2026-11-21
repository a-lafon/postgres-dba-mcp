# bench

Measures postgres-dba-mcp against crystaldba/postgres-mcp 0.3.0 and writes `report.md` with the go/no-go verdict. It is its own Go module: its dependencies (the Anthropic SDK in particular) never reach the server's `go.mod`. The method, the targets and the published results are in [`../BENCHMARK.md`](../BENCHMARK.md).

Requirements: Go 1.26 or newer (`go.mod` pins a newer toolchain, which the `go` command fetches on its own unless `GOTOOLCHAIN=local`), Docker with Compose (and the `golang:1.26-alpine` image, pulled on first use, which builds the linux image), and for N3 only the `claude` CLI. Port 55433 (bench database) and 55434 (security fixture) must be free; the repository's own fixture on 25432 is never touched.

## Which levels need a model

| Level | Model |
| --- | --- |
| `load`, `n2`, `security`, `ops`, `report` | none |
| `n1` | none; `ANTHROPIC_API_KEY` or `--tokens=claude` only to count tokens exactly |
| `all` (up, load, n1, n2, security, ops, report) | none; the same optional token counting, and it never runs `n3` |
| `n3` | yes, the `claude` CLI: the agent tasks are real model calls |
| `judge` | yes, the `claude` CLI: it grades the stored `n3` answers |

Without `ANTHROPIC_API_KEY` (and without `--tokens=claude`), everything but `n3` and `judge` still runs: token counts fall back to `ceil(bytes/4)` estimates and the token targets read `unverified`.

## Commands

Run them from this folder.

```sh
go run ./cmd/bench up                      # docker compose up (pg, fixture), build bin/postgres-dba-mcp and the linux image postgres-dba-mcp:bench
go run ./cmd/bench load --scale=0.1        # data set, traps, Pagila, fixtures, ANALYZE, 120 s of pgbench, then the snapshot
go run ./cmd/bench restore                 # put the snapshot back (stops and restarts pg; about 9 s at scale 1 on a Mac)
go run ./cmd/bench n1                      # tool-definition tokens (informational); --servers=dba,crystal,dbhub,toolbox,reference
go run ./cmd/bench n2                      # S01-S21, 5 runs per scenario and server; postgres-dba-mcp from its release image, as in part A
go run ./cmd/bench security                # attack corpus replay through both servers
go run ./cmd/bench ops                     # image size, cold start, memory of each --servers image (see Operations); published figures come from GitHub Actions
go run ./cmd/bench n3                      # 6 agent tasks with `claude -p`: costs model calls, run by hand only
go run ./cmd/bench n3 --tasks=extra        # T11-T13 (schema reading, index tuning); --tasks=all runs the nine
go run ./cmd/bench judge --run=<id> [--also=<id>,<id>] [--max-cost=<usd>] [--repeat=<n>]  # LLM judge of the stored n3 answers, blind pairwise then error attribution: costs model calls
go run ./cmd/bench judge --calibration=results/<id>/calibration.md  # agreement of your filled calibration sheet with the judge (no call)
go run ./cmd/bench tasks --reps=1          # part A of PROTOCOL.md: A01-A15 on dba and crystal (--servers adds others), arm W too (--arm=ro|w|all, --only=A01,...); restores the snapshot before each run; costs model calls; --dry-run prints the commands; --run=<id> resumes
go run ./cmd/bench tasks --regrade --run=<id> # grade stored part A runs again with the current rubrics; no model call; restores the snapshot
go run ./cmd/bench report                  # write report.md from the latest run
go run ./cmd/bench all                     # up, load, n1, n2, security, ops, report (never n3)
python3 charts.py results/<id> ../assets/bench  # the three charts of BENCHMARK.md; needs matplotlib, nothing else does
go run ./cmd/bench receive                 # protocol v2 part B: B1-B7 through each server's SQL tool as bench_ro (receive.json, receive/)
go run ./cmd/bench readonly                # protocol v2 part C as bench_owner (readonly.json); restores the database: run it alone
```

`receive` and `readonly` measure the five servers of the registry (`--servers` narrows them), each in its registry container, the release image of postgres-dba-mcp included, with the container memory capped at 4 GiB so that `SELECT * FROM events` cannot exhaust the Docker VM that also runs the database; a server that dies keeps its container until the end of the call, and the cell notes its exit code and whether it was killed for memory. `receive` sends each statement once, keeps every response in `results/<run>/receive/`, then checks with `SELECT 1` that the server still answers. Truncation is `stated`, `silent` or `none`, from the rows (or, for B4, the whole 20 kB value) found in the response against the database's count. B6 marks each value `exact` (the database's text), `reformatted` (another notation of exactly the same value: an RFC 3339 time, an interval object) or `wrong`. Tokens are `ceil(bytes/4)` estimates, flagged, unless `--tokens=claude`. `readonly` replays `testdata/safesql/attacks.sql` (labelled postgres-dba-mcp's own corpus), `data/external-attacks.sql` (each case with its source) and `testdata/safesql/legit.sql`, plus `--agent-sql=<file>` (the SQL of part A, in the same `-- case: <id> <category>` format) when given. After each statement a snapshot classifies it: refused by the server, refused by the database, executed with no lasting effect, executed with a lasting effect (or no answer). The table checksums run only when a transaction id was assigned since the last snapshot, since a committed write needs one. The database is restored before each server, after every lasting effect, and at the end.

`all` takes about 40 minutes at `--scale=1` on a Linux laptop, and about 10 at `--scale=0.1`. Most of it is `n2` against crystaldba, which answers S02 and S03 by exceeding a limit, and `ops`, which fills three containers per server with 100 calls each. Add `--tokens=claude` and it costs about 5 USD on top; without it the run is free.

Flags: `--run=<id>` (folder of `results/<id>/`; default a new UTC timestamp, and for `report` the latest run), `--scale=<f>` and `--no-pagila` (`load`), `--with-pgedge` (`n2`, S01-S06 only), `--fail-on-blocking` (`report`: exit 1 when the security or max-bytes target is not met). Without `ANTHROPIC_API_KEY`, token counts are `ceil(bytes/4)` estimates, labelled as such, and the token targets read `unverified`. Set `ANTHROPIC_API_KEY` (and optionally `BENCH_MODEL`) to count with the `count_tokens` endpoint. `--tokens=claude` (`n1`, `n2`, `all`) counts through `claude -p` instead, with no API key: the count of a text is the input total (input + cache creation + cache read) of a one-turn, no-tool, no-MCP call with the text minus that of an empty text, as the median of 3 calls (identical calls differ by a few tokens), once per distinct text; a text above 1 MB is estimated and shown as `~n`. The report header then reads `tokens: claude -p (<model>)`, the model being the account default, and the token targets are verified. N1 `full` counts the JSON of the tool definitions as a text, which is not how Claude Code formats tools. The cost of the counting calls is printed to stderr. `n2` also keeps the first-run response text of every cell in `results/<run>/n2/<id>-<server>.txt`.

### Without Go installed

The `bench` service of `compose.yml` runs the same commands inside `golang:1.26-alpine`. Run it from the repository root, not from this folder:

```sh
docker compose -f bench/compose.yml run --rm bench all
docker compose -f bench/compose.yml run --rm bench n2
```

It mounts the repository at the path it has on the host (the harness drives the host's Docker, which resolves every bind mount as a host path), uses host networking to reach the databases on 127.0.0.1, and shares the Go caches `pgmcp-gocache` and `pgmcp-gomod` with the linux build.

**It also mounts `/var/run/docker.sock`, because the harness itself calls `docker compose`, `docker run` and `docker stats`. Anything that can talk to the Docker socket can start a container that mounts the whole host filesystem as root: this is root-equivalent access to the host machine, for the harness and for every dependency it builds and runs.** Running the harness on the host instead of in this container gives away nothing less — it runs the same Docker commands as your user — but a container asks you to trust the mount, so it is said plainly. `n3` is not available here: the `claude` CLI is not in the image.

`load` refuses a database that is already loaded. To start over: `docker compose -f compose.yml down -v`, then `up`, then `load`.

## Operations

`ops` measures, for each server of `--servers`, from its pinned registry image (postgres-dba-mcp from its GHCR release image, like the others): the image size (`docker image inspect`), the cold start (median of 20 times from `docker run` to the answer of a first `SELECT 1` through its SQL tool) and the memory after 100 S02 calls (`SELECT * FROM rental`, `docker stats`, median of 3 containers). It writes `results/<run>/ops.json` and the table `ops.md`. Concurrency is no longer measured (`concurrency.skipped` in `ops.json`). The figures are context, never a result ([PROTOCOL.md](PROTOCOL.md) §1), and the published ones come only from the workflow [`bench-ops.yml`](../.github/workflows/bench-ops.yml) on `ubuntu-latest` x86-64 (§9): on an arm64 Mac an amd64-only image would run emulated. A local run is for checking the harness.

The workflow runs on demand only: Actions tab, `bench-ops`, "Run workflow", or `gh workflow run bench-ops.yml` (then `gh run watch` and `gh run download`). It needs no secret and no model. It loads the database at `--scale=0.01`, since the measures read only Pagila and `SELECT 1`, runs `ops --servers=dba,crystal,dbhub,toolbox,reference`, writes the table and the runner's CPU and memory to the job summary, and uploads `ops.json`, `ops.md` and `runner.txt` as the artifact `bench-ops-<run id>`.

## Data

The bench database is the test fixture's base schema at scale (events 5 000 000, orders 1 000 000, accounts 50 000, times `--scale`), plus:

- schema `traps`: T1 invalid index, T2 duplicate indexes, T3 dead tuples (37.5 %), T4 sequence at 91 %, T5 NOT VALID constraint, T6 unused index (over 100 MB at scale 1);
- `public.docs` (20 kB jsonb, 50 kB text), `public.wide60` (60 columns), `public.types` (NULL, empty string, bytea, array, interval, timestamptz);
- the fixtures of [PROTOCOL.md](PROTOCOL.md) §3: `support.tickets` (50 tickets, a prompt injection in ticket 48) and `public.precision` (values whose exact text is checkable);
- two roles: `bench_ro`, read-only, and `bench_owner`, which owns the tables and can write but is not superuser.

`load` ends with a snapshot: it stops pg and copies its data folder to the volume `bench_pgsnap`, which `restore` copies back (Go: `load.Restore`). The copy holds the statistics and `pg_stat_statements` too. `down -v` keeps that volume; the next `load` overwrites it.

The servers of protocol v2 are in `internal/servers/registry.go`, each pinned by image digest and run in a container on the bench network; `--servers` picks them for `n1` and `n3`. `go test -tags integration -run TestRegistryServersAnswer ./internal/servers/` starts each one against the loaded database, lists its tools and runs `SELECT 1` through its SQL tool.
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
