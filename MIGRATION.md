# Migrating from crystaldba/postgres-mcp 0.3.0

postgres-dba-mcp keeps the nine tool names and their parameters, so an MCP client configuration usually changes only in its `command`. Everything a user can observe that differs is listed below.

Replace the existing server entry rather than adding this one next to it: both expose the same tool names, and a client that sees both may keep calling the old one.

To get the closest behavior to 0.3.0 for a development database, start the server with `--access-mode=unrestricted`:

```json
{
  "mcpServers": {
    "pg": {
      "command": "postgres-dba-mcp",
      "args": ["--access-mode=unrestricted"],
      "env": { "DATABASE_URI": "${DATABASE_URI}" }
    }
  }
}
```

Statements are still limited to one per call in that mode (see the first table).

## Server, modes and output

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| **Default access mode** is `unrestricted`. | The default is `restricted`. `--access-mode=unrestricted` restores the old behavior, and logs a warning at startup. | Pass `--access-mode=unrestricted` where you need writes, or better, keep `restricted` and use a read-only role. |
| **Several statements per call** are accepted in both modes, and only the last result set is returned. | Exactly one statement per call in both modes. Several statements are refused: `ERROR: only one statement per call is allowed; send each statement in its own call` (restricted mode: `ERROR: restricted mode: only one statement per call is allowed`). | Send each statement in its own call. |
| **Tool errors** are normal results whose text starts with `Error: `. Restricted-mode refusals read `Error validating query: <query>`. Argument errors come from the SDK as `Error executing tool <name>: ...`. | Errors are returned with `isError: true` and text starting with `ERROR: `, then `(SQLSTATE <code>)`, `DETAIL:`, `HINT:` and `POSITION:` lines when Postgres gives them. A restricted-mode refusal states the reason, for example `ERROR: restricted mode: DeleteStmt statements are not allowed (allowed: SELECT, VALUES, TABLE, WITH, EXPLAIN, SHOW)`, and names the flag when unrestricted mode would have run the statement: `; start the server with --access-mode=unrestricted to run it` (never for a function the denylist refuses, which that mode exists to protect). | Test for `isError`, not for a text prefix. |
| **Unknown or wrongly typed arguments** are handled by the SDK. | An unknown argument is refused: `ERROR: invalid arguments: unknown field "foo"`. | Remove arguments the tool does not have. |
| **Results** are the Python `repr` of lists and dicts (`[{'id': 1, ...}]`, `Decimal('1.5')`, `datetime.datetime(...)`), with no bound on size. | Compact CSV with a header, `\N` for NULL, one record per line, and a footer such as `-- 12 rows, 38 ms`. Rows, bytes and cell length are bounded (`--max-rows`, `--max-bytes`, `--max-cell`), and a truncation is announced in the footer. A long cell (`bytea` included) is cut and ends with `…[+<n> chars]`, and a `tsvector` is summarized as `[tsvector: <n> lexemes]`. | Read results as CSV. Refine the query when the footer says `truncated`. Select `col::text` to read a tsvector. |
| **Restricted mode** parses with pglast and allows only the functions of an allowlist of 491 names (user-defined and extension functions are refused). Functions in `FROM` are not checked. It stops a call after a client-side timeout of 30 s. Unrestricted mode has no timeout. | Restricted mode checks one statement with libpg_query, against a function denylist checked on the whole statement: functions in `FROM`, subqueries, CTEs and arguments are checked too (the function-in-`FROM` bypass, CVE-2026-85620, is fixed). A function the denylist does not name is then resolved against `pg_proc`: `IMMUTABLE` and `STABLE` ones run, so PostGIS, pgvector and your own read-only functions work where 0.3.0 refused them, while a `VOLATILE` function the server does not ship is refused unless a review cleared it. `--allow-function` and `--deny-function` adjust both. The timeout is `statement_timeout`, enforced by the server in both modes (`--statement-timeout`, default 30 s). | Declare your own read-only functions `STABLE`, or pass `--allow-function`; review `--allow-function` / `--deny-function` if you relied on the old behavior. |
| **Server name** is `postgres-mcp`, with no instructions. | The server name is `postgres-dba-mcp`, and the server sends instructions describing the output format. | Update scripts that match on the server name. |
| **Session state** survives between calls: advisory locks, `SET` values and hypothetical indexes stay on the pooled connection. A SQL error marks the pool invalid, and the next call rebuilds it. The server connects at startup. | Settings are transaction-local, and every connection is reset (`hypopg_reset()`, `DISCARD ALL`) when it returns to the pool. A SQL error does not rebuild the pool, and a dead connection is retried once. The first connection is opened by the first tool call, so the server starts when the database is down. Idle connections close after 5 minutes. | Do not rely on `SET` or advisory locks across calls. |
| **Session settings** are not configured: no timeouts, and values follow the server and role defaults (time zone, date style). | Every transaction sets `TimeZone` to `UTC`, `DateStyle` to `ISO, YMD`, `IntervalStyle` to `postgres` and `bytea_output` to `hex`. | Expect UTC timestamps. |
| **Connection failures** read `Connection attempt failed: <error>` with the password masked, and no connect timeout is set. | The error reads `ERROR: cannot connect to the database: ...` without the password, and an unreachable host fails after 10 s unless the URI sets `connect_timeout`. | None. |
| No check of the database role. | After the first connection, a warning is logged on stderr when the role is a superuser, bypasses row-level security, owns tables, or has a powerful built-in role, and when PostgreSQL is newer than 18, the newest version the function denylist was checked against. Skipped in unrestricted mode. | Use a read-only role. |
| No documented annotations. | Every tool carries a title and `readOnlyHint` / `destructiveHint` annotations. `destructiveHint` is true only for `execute_sql` in unrestricted mode. | None. |
| No per-call log. | One stderr line per call: `call tool=<name> ms=<n> status=ok bytes=<n>` (or `status=error sqlstate=<code>`), never with the SQL text. | None. |
| Tools cannot be switched off. | `--tools` enables groups (`schema`, `query`, `explain`, `stats`, `health`, `index`). Only enabled tools are listed. | Use `--tools=schema,health` for a smaller surface. |

## HTTP and Docker

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| **Transports** are `stdio` and `sse`. SSE binds `localhost` by default (`--sse-host`, `--sse-port`), with no check of the caller. | `--transport=sse` is kept but deprecated (a warning is logged). `streamable-http` is added, on `/mcp`. The default bind is `127.0.0.1`. `--sse-host` and `--sse-port` are accepted as aliases of `--host` and `--port`. Binding a non-loopback address requires `--allow-remote`. The `Host` and `Origin` headers are checked (403 on mismatch), and bodies are capped at 1 MiB. | Move clients to `streamable-http` when you can. Behind a different host port, add `--allowed-hosts=localhost:<host port>`. |
| **Docker image** rewrites `localhost` in `DATABASE_URI` to `host.docker.internal` or `172.17.0.1` (found by ping), and appends `--sse-host=0.0.0.0` for SSE. | No rewriting. Use `host.docker.internal` (Docker Desktop), `--network=host` or the bridge address (Linux) in the URI. For HTTP in a container, pass `--host=0.0.0.0 --allow-remote` and publish the port on the host loopback. The image is distroless, non-root, with the binary as entrypoint. | Put the right host in the URI, and add the HTTP flags to your `docker run`. |
| Packaged on PyPI and run with `uvx` or Docker. | One static binary per platform (Homebrew, install script, release archives; `go install` builds the WASM parser, about 300 MB at start, or the cgo parser with `CGO_ENABLED=1 go install -tags pgquery_cgo,netgo,osusergo` and a C compiler), and a Docker image on `ghcr.io/a-lafon/postgres-dba-mcp`. | Replace `uvx postgres-mcp` by `postgres-dba-mcp`. |

## New flags

These flags did not exist in 0.3.0: `--tools`, `--max-rows`, `--max-rows-limit`, `--max-bytes`, `--max-cell`, `--max-cell-limit`, `--statement-timeout`, `--allow-function`, `--deny-function`, `--host`, `--port`, `--allow-remote`, `--allowed-hosts`, `--allowed-origins`, and `--version`. The flags `--access-mode`, `--transport`, `--sse-host` and `--sse-port` and the positional URI are kept (`DATABASE_URI` still wins over the argument).

## execute_sql

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| `sql` defaults to the string `all`, so a call without `sql` fails with a syntax error. | `sql` is required: `ERROR: invalid arguments: sql is required`. | None. |
| No row limit. | New optional `max_rows` (default `--max-rows`, at most `--max-rows-limit`). Rows are read through a server-side cursor, never more than `max_rows` + 1. New optional `max_cell` (default `--max-cell`, at most `--max-cell-limit`) sets the longest cell. | Use `max_rows` or a `LIMIT`, and `max_cell` to read a long value. |
| A long value comes back whole, however long. | A cell longer than `max_cell` (200 characters by default) is cut and ends with `…[+<n> chars]`, and the footer names its column: `-- 1 cell shortened (body); pass max_cell (up to 10000) to read more`. Long text columns (descriptions, JSON documents, logs) are cut on a plain `SELECT`. | Pass `max_cell` to read a long value whole, or select `substr(col, <from>, <n>)`. |
| A statement without a result set returns `No results`. | It returns its command tag in the footer, for example `-- INSERT 0 3, 4 ms`. A `RETURNING` result is rendered under the same row and byte bounds. | None. |
| An empty or comment-only query is sent to the server. | `ERROR: empty query`, with no database access. A syntax error is `ERROR: syntax error: ...`. | None. |
| A query with `$1` placeholders is sent as written and no value is ever bound: Postgres answers `there is no parameter $1`, returned as a success. | `ERROR: execute_sql takes no parameters: inline the values in the SQL (explain_query accepts $n)`, with no database access. | Inline the values, or pass the query to `explain_query`, which plans `$n` without them. |
| A statement that exceeds the timeout fails with Postgres's bare `canceling statement due to statement timeout`. | The same error carries a `HINT:` with the limit in force and advice: bound the query with a `WHERE` on an indexed column or a `LIMIT`, or look at its plan; a retry may succeed on a cold cache. A cancel by a DBA does not get it. | None. |

## list_schemas, list_objects, get_object_details

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| `list_schemas` returns `schema_name`, `schema_owner` and `schema_type` (`User Schema`, `System Schema`, `System Information Schema`), sorted by type name, so system schemas come first. | CSV columns `schema_name,owner,type,tables`. `type` is `user` or `system`, `tables` counts the tables of user schemas, user schemas come first, and `pg_toast` and temporary schemas are omitted. | Read `owner` and `type` instead of `schema_owner` and `schema_type`. |
| `list_objects` reads `information_schema`. Tables come as `schema`, `name`, `type` (`BASE TABLE`); materialized views are not listed; sequences as `schema`, `name`, `data_type`; extensions as `name`, `version`, `relocatable` (boolean). An unknown schema returns `[]`. | Reads `pg_catalog`. Tables: `name,kind,rows,size` (`kind` is `table` or `partitioned (<n> partitions)`; partitions are not listed; `rows` is the planner estimate, written with a leading `~`, e.g. `~1.0k`). Views: `name,kind` (`view` or `materialized`). Sequences: `name,data_type,last_value,max_value,used`. Extensions: `name,version,schema,relocatable` (`yes` / `no`). An unknown schema is an error. `list_schemas` and `list_objects` (tables, views) start with a `-- <n> schema(s)/table(s)/view(s) not readable by <role>: <names>` line, before the CSV header, when the role lacks USAGE on a schema or SELECT on a table. | Adapt parsers to the new columns, skip a leading `--` line, and strip the `~` from `rows`. |
| An unsupported `object_type` returns `Error: Unsupported object type: <t>`. | `ERROR: unsupported object_type "<t>" (valid: table, view, sequence, extension)`, and `object_type` has an enum in the schema. An unknown schema returns `ERROR: schema "<name>" not found`. | None. |
| `get_object_details` returns a Python dict: columns, constraints and indexes for tables and views, and a few fields for sequences and extensions. The constraint query does not match on the table, so a constraint of the same name on another table leaks in. A missing table returns the same shape with empty lists. | A compact text: header with the estimated rows and sizes, one line per column (type, `PK`, `NOT NULL`, default or identity, foreign key, comment), then foreign keys, indexes, checks, partitions, `referenced by` and triggers. Constraints are read by the table's OID, so none leak. Views show their definition. A missing or mistyped object is an error naming the object and its real kind. | Read the text. Do not parse a dict. |
| Names with upper-case letters or special characters fail or are folded (crystaldba #109, #69). | Names are matched exactly, with no case folding, and printed as stored. | None. |
| No bound on the details. | The text never exceeds `--max-bytes`, and ends with `-- truncated (<n>-byte limit)` when cut. | None. |

## explain_query

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| The plan is rendered as a tree: `→ Hash Join (Cost: 10.50..45.30) [Rows: 100]`, with filters cut at 100 characters and no index names. | A summary: a header (`Total cost 5512.34, estimated rows 20`), then the heaviest nodes (at most 5) with their share of cost or time, rows, index, relation, conditions, and warnings (sequential scan for few rows, bad estimate, sort on disk). | Read the summary. |
| No way to get the Postgres plan text. | New parameter `verbose`: with `true`, the tool returns the Postgres TEXT plan, cut at `--max-bytes`. | Pass `verbose=true` when you need the raw plan. |
| `analyze=true` runs the statement, and in unrestricted mode commits it: `EXPLAIN ANALYZE UPDATE ...` modifies data. In restricted mode any `ANALYZE` is rejected. | `analyze` is allowed on SELECT, VALUES, TABLE or WITH ... SELECT queries in both modes, and every call runs in a read-only transaction that is rolled back. `analyze` on any other statement is refused. A plain explain of a write is allowed in unrestricted mode and executes nothing. | Use `analyze` on reads only. |
| `analyze` with hypothetical indexes returns `Error: Cannot use analyze and hypothetical indexes together`. | `ERROR: analyze cannot be combined with hypothetical indexes`. | None. |
| If hypopg is missing, the tool returns a markdown explanation as a normal result. | `ERROR: hypothetical indexes need the hypopg extension: run CREATE EXTENSION hypopg (the server needs the postgresql-<major>-hypopg package)`. | Install hypopg. |
| Hypothetical index definitions are pasted into SQL unquoted, and a string for `columns` becomes a list of letters. Hypothetical indexes stay on the connection after the call. | Table, columns and method are validated and quoted. `columns` must be a list with at least one entry, and unknown keys are refused. Hypothetical indexes are reset before and after every call. | None. |
| With `$1`, `$2` placeholders: on PG 16+ without `LIKE` a generic plan is used, otherwise values are guessed and substituted (`LIMIT $n` becomes `limit 100`). | A generic plan on every supported version (PG 14 and 15 through `PREPARE` and `plan_cache_mode = force_generic_plan`). No value is guessed. The header ends with `, generic plan ($n parameters)`. `analyze` with `$n` is refused. | Use `analyze` with literal values. |
| A nested `EXPLAIN` fails in the server. | `ERROR: pass the query without EXPLAIN; use the analyze and verbose parameters instead`. | None. |
| Errors are wrapped: `Error: Error executing explain plan: ...`. | Postgres errors come back through the common error format, with SQLSTATE, hint and position. | None. |
| The output ends with the plan. | The output ends with a footer `-- plan, <duration>`, the wall time of the whole call. | None. |
| The description ends after the cost estimates. | The description adds `Queries with $n parameters get a generic plan.` | None. |

## get_top_queries

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| Reports the statements of every database of the cluster, with no filter. | Only the current database. The description says so. | None. |
| `limit` applies to the time rankings only, and has no bounds. | `limit` (default 10) applies to every ranking and must be between 1 and 100: `ERROR: invalid limit <n>: must be between 1 and 100`. An invalid `sort_by` is `ERROR: invalid sort_by "<v>": use resources, total_time or mean_time`. | None. |
| Time rankings return `query, calls, total_exec_time, mean_exec_time, rows` as a Python list, after the line `Top <n> slowest queries by ...`. | CSV with `calls,total_ms,mean_ms,rows,query` (milliseconds, one decimal), under the line `-- by <sort_by>, database <db>, statistics since <ts>`. When the role lacks `pg_read_all_stats`, a second line `-- [<n> of <m> ]query texts hidden: grant pg_read_all_stats to <role> to read them` follows, since PostgreSQL then masks the text of statements run by other roles as `<insufficient privilege>`. Query text is on one line and cut at 500 characters. | Read the new columns. |
| `resources` returns raw fractions and counters (`stddev_exec_time`, `shared_blks_hit`, `wal_bytes`, ...), fails with `division by zero` when a sum is zero, and cannot work before PG 13. | The columns are `calls,total_ms,mean_ms,time_pct,blks_accessed_pct,blks_read_pct,blks_dirtied_pct,wal_pct,query`, with percentages (one decimal), and `\N` when the sum is zero. `rows`, `stddev_exec_time` and the raw block and WAL counters are no longer returned. Supported versions are PG 14 to 18. | None. |
| If the extension is missing, the tool returns an explanation as a normal result. It does not check that the library is preloaded. | `ERROR: pg_stat_statements is not installed in this database: ...`, or `ERROR: pg_stat_statements is installed but not loaded: ...` when it is not in `shared_preload_libraries`. | Install and preload `pg_stat_statements`. |

## analyze_db_health

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| Every check prints a line, healthy or not (`Invalid index check: ...`, `Duplicate index check: ...`, ...). | One section per selected check, in a fixed order, with `OK` or the problems only (`! ...`). Each kind of item is capped at 10, and the report stays within `--max-bytes`. | Read the sections. |
| An invalid `health_type` returns `Invalid health types provided: ...` as a normal result. | `health_type` is trimmed and case-insensitive, and an invalid or empty token is `ERROR: invalid health_type "<input>": valid values are all, buffer, connection, constraint, index, replication, sequence, vacuum` with `isError`. Sections always appear in the fixed order. | None. |
| A failing check fails the whole call: `Error calculating database health: <e>`. | Each check runs in its own savepoint. A failing check reports `! check failed: <reason>` and the others still run. A check the server does not implement, which a managed fork answers with SQLSTATE `0A000`, reports `skipped: <reason>` instead: it is no finding of your database. | None. |
| Index checks: invalid, duplicate, bloated and unused indexes, each with its own message. | Same checks. Duplicates use the column lists of the catalog, not a regular expression, and also compare the operator class, collation and sort order of each key column. Rarely used indexes (50 scans or fewer) are one item with the 10 largest, each with its scan count and the length of the statistics window, an upper bound (the window of `pg_stat_database.stats_reset`, or the server start when it was never reset), because a per-table `pg_stat_reset_single_table_counters()` is not visible there. The description says `bloated and rarely used`. | None. |
| Vacuum check covers transaction ID wraparound only. | New: dead rows. A user table with at least 10 000 dead rows and at least 20 % dead is reported with its last vacuum time. The description says `transaction id wraparound and dead rows`. | None. |
| Sequence check. | Handles identity columns, descending sequences, unreadable sequences (skipped) and mixed-case names (crystaldba #109). A `smallint` or `integer` column fed by a wider sequence is measured against the column's range. Reports sequences above 90 %. | None. |
| Replication, buffer and constraint checks. | Same checks and thresholds (95 % cache hit rate), reported as `OK (...)` facts or `!` items (inactive slots, a replica that is not streaming, `NOT VALID` constraints). | None. |

## analyze_query_indexes and analyze_workload_indexes

| crystaldba 0.3.0 | postgres-dba-mcp | What to do |
|---|---|---|
| The result is a Python dict (`summary`, `recommendations`, `query_impact`) with an internal `_langfuse_trace` by default. | A short text: a summary line, a CSV table `#,cost_change,size,ddl`, a `Per query:` line, the workload queries and optional `Skipped:` and time-budget lines. No trace. | Read the text. |
| `method="llm"` calls OpenAI (`gpt-4o`) for one query. | `method` is still accepted (`dta`, `llm`), but `llm` is refused: `ERROR: method "llm" is not supported by postgres-dba-mcp; use "dta"`. There is no OpenAI dependency. | Use `method="dta"` or omit it. |
| `max_index_size_mb` has no lower bound. | It must be at least 1. | None. |
| The workload comes from `pg_stat_statements` for every database of the cluster, and a missing extension surfaces as a raw SQL error. | The workload is the current database, top-level statements only, with the same thresholds (at least 50 calls, mean time at least 5 ms, at most 100 statements). A missing `pg_stat_statements` or `hypopg` gives a clear error with the install hint. An empty workload is a normal result. | Install the extensions. |
| Tables must have a manual `ANALYZE` (`last_analyze`). | `last_analyze` or `last_autoanalyze` is enough. | None. |
| Queries are lowercased, `$1` replacement corrupts `$10`, only SELECTs are kept silently, and a typed literal normalized by pg_stat_statements (`interval $1`) fails with `syntax error at or near "$1"`. | Query text is passed as written, except that `<type> $n` is rewritten to `$n::<type>` before the check, so those queries are analyzed. Bind parameters beyond `$9` work. In restricted mode a query must pass the safety check and be a single SELECT, in unrestricted mode a single SELECT. Other queries are skipped and named on the `Skipped:` line, with the reason. The tools never run `EXPLAIN ANALYZE`, and run in one read-only transaction. | None. |
| Candidates drop the schema name, resolve an unqualified column to the first table, and miss `JOIN` conditions that are nested. | Columns are resolved against the catalog (schema kept, ambiguous columns dropped), and join conditions are read at every nesting depth. Column order inside a candidate is deterministic. | None. |
| Sizes of recommended indexes are estimated, then discarded. Results vary between runs, and the time budget is checked loosely. | The hypopg size is used for the size budget and shown. For identical inputs the output is identical. The 30 s budget is checked before every EXPLAIN, and the output says when it was hit. A query whose EXPLAIN fails is skipped and reported instead of stopping the analysis. | None. |
| More than 10 queries or an empty list return `Error: ...` as a normal result. | Same messages with `ERROR: ` and `isError`. | None. |
