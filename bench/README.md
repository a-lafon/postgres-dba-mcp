# bench

Measures postgres-dba-mcp against crystaldba/postgres-mcp 0.3.0 and writes `report.md` with the go/no-go verdict. It is its own Go module: its dependencies (the Anthropic SDK in particular) never reach the server's `go.mod`. The method, the targets and the published results are in [`../BENCHMARK.md`](../BENCHMARK.md).

Requirements: Go, Docker with Compose (and the `golang:1.26-alpine` image, pulled on first use, which builds the linux image), and for N3 only the `claude` CLI. Port 55433 (bench database) and 55434 (security fixture) must be free; the repository's own fixture on 55432 is never touched.

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
go run ./cmd/bench load --scale=0.1        # data set, traps, Pagila, ANALYZE, then 120 s of pgbench
go run ./cmd/bench n1                      # tool-definition tokens (informational)
go run ./cmd/bench n2                      # S01-S21, 5 runs per scenario and server
go run ./cmd/bench security                # attack corpus replay through both servers
go run ./cmd/bench ops                     # cold start, memory (linux image, docker stats; native binary as information), sizes (release-flag build: -trimpath -ldflags "-s -w"), 50 concurrent calls over HTTP
go run ./cmd/bench n3                      # 6 agent tasks with `claude -p`: costs model calls, run by hand only
go run ./cmd/bench n3 --tasks=extra        # T11-T13 (schema reading, index tuning); --tasks=all runs the nine
go run ./cmd/bench judge --run=<id> [--also=<id>,<id>] [--max-cost=<usd>] [--repeat=<n>]  # LLM judge of the stored n3 answers, blind pairwise then error attribution: costs model calls
go run ./cmd/bench judge --calibration=results/<id>/calibration.md  # agreement of your filled calibration sheet with the judge (no call)
go run ./cmd/bench report                  # write report.md from the latest run
go run ./cmd/bench all                     # up, load, n1, n2, security, ops, report (never n3)
```

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

## Data

The bench database is the test fixture's base schema at scale (events 5 000 000, orders 1 000 000, accounts 50 000, times `--scale`), plus:

- schema `traps`: T1 invalid index, T2 duplicate indexes, T3 dead tuples (37.5 %), T4 sequence at 91 %, T5 NOT VALID constraint, T6 unused index (over 100 MB at scale 1);
- `public.docs` (20 kB jsonb, 50 kB text), `public.wide60` (60 columns), `public.types` (NULL, empty string, bytea, array, interval, timestamptz);
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
- The memory target of `ops` reads postgres-dba-mcp as it ships to containers, measured like crystaldba. `up` builds a static musl cgo binary (`CGO_ENABLED=1`, `-tags pgquery_cgo,netgo,osusergo`, `-trimpath`, `-ldflags "-s -w -linkmode=external -extldflags '-static -s'"`, as `.goreleaser.yaml`) inside `golang:1.26-alpine` for the architecture of the Docker daemon (Go and apk caches in the volumes `pgmcp-gocache` and `pgmcp-gomod`), then the image `postgres-dba-mcp:bench` from the root `Dockerfile` (context `bin/image/`). `ops` builds it when the image is absent (rebuild with `up`), runs it with `docker run -i --rm` on the `bench_bench` network, makes the 100 S02 calls and reads `docker stats`. This is done in 3 containers one after the other, for the image and for crystaldba; the measure is the median of the 3 (the 3 samples are in `rss_mb[].samples` of `ops.json`) and the target checks it, because one sample varied from 14 to 30 MB between runs of the same code. The cell reads `16.0 MB (linux image, docker stats; median of 3, max 30.3 MB)`. The native binary of the host (`phys_footprint` on macOS, `VmRSS` on Linux; the darwin build uses the WASM parser, so it reads more) is measured once after the same calls and shown as an informational line (`native_rss_mb` in `ops.json`); it does not decide the target. Binary size, cold start and concurrency stay on the native binary.
- The MCP client uses the legacy `initialize` handshake. The SDK's default first sends `server/discover`, which crystaldba's mcp 1.6.0 never answers.
- crystaldba runs from the digest-pinned image on the `bench_bench` network, host `pg` (or `fixture`), so its entrypoint does not rewrite the URI.
- `count_tokens` rejects an empty message, so the "empty conversation" baseline of a text is a single space.
- N3 input tokens are the `usage` input tokens of the `result` event plus the cache creation and cache read tokens.
- N3 is unit-tested on hand-written sample streams (`internal/agent/testdata/`), and was first run for real on 2026-10-21 (run `mvp-s1`, 36 runs on the maintainer's Claude Code account). `n3 --regrade --run=<id>` grades the stored transcripts again after a rubric change, without calling `claude`. `n2 --recheck --run=<id>` does the same for the stored N2 outputs (it reads the bench database for the facts and calls no server).
- N3 names the crystaldba container (`docker run --name`, in a per-run copy of `n3/crystal.json`) and removes it after each run: `claude -p` kills the `docker run -i --rm` client when it exits, which leaves the container running (reproduced: the container survives `kill -9` of the client).
- `judge` (informational, never part of the verdict) has three parts. (1) Blind pairwise pass: `claude -p`, with no tool and no MCP server, gets the question, the reference facts computed from the database (`internal/judge/facts.go`) and the two final answers as A and B (prompt: `internal/judge/prompt.md`, which says that an omission stated openly is not an error and that cited values are judged against the facts). Each pair (task, run index) is judged twice with A and B swapped; a server wins only when both orders agree, otherwise the pair is a tie. Scores 1-5 on correctness, completeness and actionability, plus the factual errors listed by the judge. (2) Attribution pass (`internal/judge/attribution.md`), not blind because the tool names reveal the server: per answer, the judge gets the tool calls of the transcript with their results (each cut to its first 1800 and last 600 characters), the facts, the answer and the errors of the pairwise pass, and classifies every error as `tool` (the tool output was wrong, ambiguous or lossy), `misread`, `unsupported` or `none`; the `tool` errors, with tool and transcript file, are the to-fix list of the product. (3) `--repeat=<n>` judges every pair n times and the report gives the share of pairs with the same outcome in every pass, per task. The raw replies are kept in `results/<run>/judge/` (a re-run does not pay for them again), the verdicts in `judge.json`, and `compare/<task>.md` shows both answers side by side. `--also=<run>,<run>` takes the tasks that `--run` lacks from other runs, `--max-cost` stops the calls above that many USD. `calibration.md` holds 10 blind pairs (fixed seed) for you to decide with a `Verdict (A / B / tie):` line each, `calibration-key.json` the mapping to servers and the judge's verdicts; `judge --calibration=<filled sheet>` prints the agreement. `BENCH_JUDGE_MODEL` sets the judge model, else the account default is used and reported; `BENCH_JUDGE_EFFORT` (`low` to `max`) sets its `--effort`, reported too. Kept replies are reused whatever the model or effort, so judge with another one under a new `--run`. The judge is a Claude grading Claude, and the answers are not fully anonymous (they may name their tools).
- pgEdge (`--with-pgedge`) is optional and untested: it calls its `query_database` tool for S01-S06 and reads `skipped` when `pgedge-postgres-mcp` is not installed.
- Verdict: GO needs N3 success, N3 tokens (non-regression: at most crystaldba's; the output-size gain is judged on N2), N3 turns and N2 trap detection met and no criterion `unverified`; the other N2 targets are reported in the verdict table but do not change the verdict.
