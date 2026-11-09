# Bench synthesis — every measured server

Run 20261108T222200Z · postgres-dba-mcp dev (7afada9) · crystaldba sha256:dbbd3468… · PostgreSQL 17.11 · scale 1 · tokens: estimate

Dollars: claude-sonnet-5-5 input at $2.00/Mtok, priced 2026-10-06 (platform.claude.com/docs pricing table, as bundled with the claude-api skill).

**Token counts are the harness estimate `ceil(bytes/4)`.** JSON tokenises more densely than CSV, so the estimate understates the servers that answer in JSON (DBHub, Toolbox) more than it understates postgres-dba-mcp's CSV. The bias runs against postgres-dba-mcp, which is the right way round for a comparison it is a party to, but it is a bias and the figures below carry it.

## Tool definitions

| Server | tools | definition tokens | $ per request | deferred |
|---|---|---|---|---|
| dba | 10 | 1132 | $0.0023 | 223 |
| crystal | 9 | 1225 | $0.0024 | 38 |
| dbhub | 2 | 296 | $0.0006 | 7 |
| dbhub-4tools | 4 | 431 | $0.0009 | 13 |
| dbhub-default | 2 | 276 | $0.0006 | 7 |
| dbhub-rows100 | 2 | 296 | $0.0006 | 7 |
| toolbox | 29 | 4559 | $0.0091 | 136 |
| toolbox-subset | 10 | 1243 | $0.0025 | 39 |

`deferred` is the tool names plus the server instructions, what a client with tool search loads at session start.

## Scenarios

Median over the 6 scenarios every server answered (S01, S04, S05, S06, S07, S22), as the goal requires. A tool the server does not have is counted apart, never as a wrong answer.

| Server | median tokens (common) | answered | correct | no tool | failed |
|---|---|---|---|---|---|
| dba | 309 | 22 | 22/22 | 0 | 0 |
| crystal | 844 | 19 | 17/19 | 0 | 3 |
| dbhub | 1131 | 8 | 7/8 | 14 | 0 |
| dbhub-4tools | 1131 | 12 | 9/12 | 10 | 0 |
| dbhub-rows100 | 1131 | 8 | 7/8 | 14 | 0 |
| toolbox | 1376 | 13 | 11/13 | 8 | 1 |
| toolbox-subset | 1376 | 13 | 11/13 | 8 | 1 |

Response tokens, scenario by scenario:

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

## Attacks and false refusals

| Server | attacks | refused | failed at execution | executed | with side effect | false refusals on legit.sql |
|---|---|---|---|---|---|---|
| dba | 131 | 131 | 0 | 0 | 0 | 0/47 |
| crystal | 131 | 106 | 15 | 10 | 1 | 4/47 |
| dbhub | 131 | 80 | 40 | 11 | 5 | 3/47 |
| dbhub-4tools | 131 | 80 | 40 | 11 | 5 | 3/47 |
| dbhub-rows100 | 131 | 80 | 40 | 11 | 5 | 3/47 |
| toolbox | 131 | 0 | 93 | 38 | 8 | 0/47 |
| toolbox-subset | 131 | 0 | 93 | 38 | 8 | 0/47 |

## Memory, start-up, size, concurrency

| Server | cold start | memory after 100 S02 calls | binary / image | concurrency |
|---|---|---|---|---|
| dba | 5.9 ms | 9.7 MB (median of 3) | 23 MB | 0 errors in 50 calls, p95 29 ms |
| crystal | 1352.0 ms | 247.9 MB (median of 3) | 395 MB | 0 errors in 50 calls, p95 100 ms |
| dbhub | 732.6 ms | 127.1 MB (median of 3) | 345 MB | 0 errors in 50 calls, p95 60 ms |
| toolbox | 199.5 ms | 89.6 MB (median of 3) | 422 MB | 0 errors in 50 calls, p95 25 ms |

Cold start of a container includes `docker run`; postgres-dba-mcp is the native binary there and the linux image for memory. Concurrency is 50 calls at once on one stdio session of one process, the same method for every server.

## Context accumulated over a ten-turn investigation

Context at turn N: the tool definitions, which every request carries, plus every answer from turn 1 to N. A server without the right tool runs the same fallback SQL as every other server without it.

| Server | definitions | turn 1 | $ turn 1 | turn 5 | $ turn 5 | turn 10 | $ turn 10 | fallbacks |
|---|---|---|---|---|---|---|---|---|
| dba | 1132 | 1173 | $0.0023 | 1691 | $0.0034 | 2679 | $0.0054 | 4/10 |
| crystal | 1225 | 1318 | $0.0026 | 3150 | $0.0063 | 5881 | $0.0118 | 4/10 |
| dbhub | 296 | 395 | $0.0008 | 2670 | $0.0053 | 6077 | $0.0122 | 10/10 |
| dbhub-rows100 | 296 | 395 | $0.0008 | 2670 | $0.0053 | 6077 | $0.0122 | 10/10 |
| dbhub-4tools | 431 | 530 | $0.0011 | 2745 | $0.0055 | 6152 | $0.0123 | 9/10 |
| toolbox | 4559 | 4637 | $0.0093 | 84920 | $0.1698 | 86897 | $0.1738 | 6/10 |
| toolbox-subset | 1243 | 1321 | $0.0026 | 81604 | $0.1632 | 83581 | $0.1672 | 6/10 |

## Total per task

One task is one request: the tool definitions the client sends plus the one answer it gets back, the median over the scenarios every server answered. The session column is the whole ten-turn investigation.

| Server | definitions | median answer | one task | $ one task | ten-turn session | $ session |
|---|---|---|---|---|---|---|
| dba | 1132 | 309 | 1441 | $0.0029 | 2679 | $0.0054 |
| crystal | 1225 | 844 | 2069 | $0.0041 | 5881 | $0.0118 |
| dbhub | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| dbhub-4tools | 431 | 1131 | 1562 | $0.0031 | 6152 | $0.0123 |
| dbhub-default | 276 | n/a | n/a | n/a | n/a | n/a |
| dbhub-rows100 | 296 | 1131 | 1427 | $0.0029 | 6077 | $0.0122 |
| toolbox | 4559 | 1376 | 5935 | $0.0119 | 86897 | $0.1738 |
| toolbox-subset | 1243 | 1376 | 2619 | $0.0052 | 83581 | $0.1672 |

