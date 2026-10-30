# Contributing

Everyone taking part follows the [Code of Conduct](CODE_OF_CONDUCT.md).

## Where help is welcome

- Bug reports with the tool call and what it returned: the bug template asks for them. A bypass of restricted mode goes through [SECURITY.md](SECURITY.md), never a public issue.
- Documentation: a sentence of the README or MIGRATION.md that is wrong or unclear is a bug, and so is a missing setup note for an MCP client.
- A new PostgreSQL major (see below) and reviews of the function denylist.
- Feature requests that start from a problem met with a real database.

Likely declined: changing the parameters of the nine tools, which stay those of crystaldba/postgres-mcp 0.3.0 so that switching remains one line of configuration, and anything that lets restricted mode write. Open an issue before a large pull request.

## Where the truth is

`README.md` describes what the server does, and `MIGRATION.md` every difference a user of crystaldba/postgres-mcp 0.3.0 can observe. The tests are the executable reference: unit tests next to the code, integration tests against the Docker fixture. A change of behavior updates the tests and these two documents in the same pull request.

## Commands

```sh
docker compose up -d --build --wait        # test fixture: Postgres on localhost:55432, PgBouncer on 56432 (PG_MAJOR=14..18, default 17)
gofmt -l .                                 # must print nothing
golangci-lint run                          # v2, standard linters (govet = go vet) plus gosec, integration files included (.golangci.yml)
go run golang.org/x/vuln/cmd/govulncheck@v1.8.0 ./...   # known vulnerabilities in the code paths used
go test -race ./...                        # unit tests, no Docker
go test -race -tags integration -p 1 ./...  # integration tests against the fixture, one package at a time
docker compose down -v                     # stop the fixture
```

CI runs the same commands; `go.mod` pins the Go toolchain, so a stdlib fix means bumping its `toolchain` line. Integration tests read the connection strings of the fixture from `internal/testpg`; they fail, not skip, when the fixture is down.

## Adding a tool

1. Create a package under `internal/` with a `// Package <name> ...` comment. One package per tool or per family of tools.
2. Expose `Register(*server.Registry)`, which calls `r.Add(server.Tool{...})` for each tool: group, title, definition with an explicit input schema (arrays declare `items`), and a handler that returns text or an error. Tools return text only: no `structuredContent`, no `outputSchema`.
3. Compute a Go struct in the handler and render it in a `render.go` that uses only `internal/format`. Output is English, numbers use a decimal point, and every result is bounded (`--max-rows`, `--max-bytes`, `--max-cell`) and ends with the footer.
4. Put diagnostic SQL in embedded `.sql` files. For each file taken from crystaldba, add a line to `NOTICE`.
5. Add one `Register` line to `registerTools` in `cmd/postgres-dba-mcp/tools.go`.
6. Write the tests: unit tests next to the code (tables with `t.Run`, test data in `testdata/`), and integration tests behind the `integration` build tag, against the fixture. A seed file `testdata/seed/NN_<change>.sql` creates objects in its own schema, never in `public`.
7. Add the tool to the README (tools section) and, if it differs from crystaldba 0.3.0, to `MIGRATION.md`. `go test ./cmd/postgres-dba-mcp` fails when a registered tool or flag is missing from the README.

Rules that apply everywhere: wrap errors with context and test them with `errors.Is` / `errors.As`; `ctx context.Context` is the first parameter of anything doing I/O; a doc comment on every exported identifier; no connection state outside `internal/db`; no mock of the database.

## A new PostgreSQL major

Add it to the CI matrix and raise `newestTestedMajor` in `internal/db/audit.go` (a unit test compares the two). `TestCatalogVolatileClassified` (`internal/safesql`) then names every volatile function of `pg_catalog` that is neither denied nor reviewed. Run each one in a `BEGIN READ ONLY` transaction as a superuser, then either add it to `builtinDeny` in `internal/safesql/denylist.go` with a case in `testdata/safesql/attacks.sql`, or add it to `testdata/safesql/catalog_volatile.txt` with the reason it is safe.

## Clearing an extension function

A `VOLATILE` function an extension owns is refused in restricted mode until it is listed in `reviewedVolatile` (`internal/safesql/reviewed.go`). Before adding one, install the extension on a throwaway server, call the function inside `BEGIN TRANSACTION READ ONLY; … ROLLBACK;`, and then check that nothing survived: no committed row, no file on the server, no effect on another session or backend, no background worker, no reset statistics. Run `DISCARD ALL` and `SELECT hypopg_reset()` too, the two statements the pool issues when a connection goes back, and confirm the state they are supposed to clear is really gone — `hypopg_hide_index` is listed as an example of a function that survives both. A function that only computes or only reads is cleared; anything else stays out.

## Package map

One line per package under `cmd/` and `internal/`. `cmd/postgres-dba-mcp/docs_test.go` checks that every directory holding Go files appears here.

| Package | Owns |
|---|---|
| `cmd/postgres-dba-mcp` | The binary: `main`, version, the transport choice, and `tools.go`, the list of tool packages to register. |
| `internal/config` | The command line and environment turned into a validated `Config`. |
| `internal/db` | The only holder of a database connection: a lazy bounded pool, the read-only and read-write transaction runners, session settings, connection cleanup and the role audit. |
| `internal/explain` | The `explain_query` tool: plan summary, verbose plan, hypothetical indexes, always in a rolled-back read-only transaction; `analyze` goes through the volatility gate. |
| `internal/format` | Compact bounded text output: CSV tables with a footer, unit helpers (size, count, duration, percent) and error text. |
| `internal/health` | The `analyze_db_health` tool: seven independent checks plus dead rows, reported as problems only. |
| `internal/httpx` | The HTTP transports (streamable HTTP and SSE) behind the Host and Origin guard. |
| `internal/hypo` | Hypothetical indexes through hypopg, EXPLAIN with generic plans, plan parsing and index size estimates, shared by `explain` and `indexadv`. |
| `internal/indexadv` | The `analyze_query_indexes` and `analyze_workload_indexes` tools: workload intake, candidate indexes, greedy search on hypothetical-index costs, rendering. |
| `internal/locks` | The `analyze_locks` tool: one snapshot of the lock waits in progress, with the root of each blocking chain resolved in Go. |
| `internal/query` | The `execute_sql` tool: one statement per call, checked in restricted mode and passed through the volatility gate, read through a server-side cursor. |
| `internal/safesql` | The SQL check behind restricted mode: parse, statement kinds, function denylist and policy, classification. It needs no database, except `Gate`, which resolves the undecided function calls against `pg_proc`. |
| `internal/schema` | The schema tools `list_schemas`, `list_objects` and `get_object_details`. |
| `internal/server` | The MCP plumbing shared by every tool: the registry, argument decoding, error conversion, annotations and the per-call log line. |
| `internal/stats` | The tools reading server statistics, such as `get_top_queries`. |
| `internal/testpg` | Connection strings of the Docker fixture and throwaway databases for integration tests. |

## Releasing

A release is cut by pushing a tag `v*`: `.github/workflows/release.yml` runs the tests, a goreleaser snapshot build with the smoke test of the binary and the image (`scripts/smoke-initialize.sh`), then `goreleaser release --clean`. Check the pipeline locally with `goreleaser release --snapshot --clean`, which publishes nothing. Private vulnerability reporting must be enabled in the repository settings before the first tag, and the GHCR package made public after the first image is pushed.
