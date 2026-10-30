# postgres-dba-mcp vs crystaldba/postgres-mcp 0.3.0 — bench report

Run rc2 · postgres-dba-mcp dev (c933662) · crystaldba sha256:dbbd3468… · PostgreSQL 17.11 · scale 1 · tokens: claude -p (claude-opus-5-5)

## Verdict: GO

| Criterion | Target | Result | Met |
|---|---|---|---|
| Security: attacks not refused, side effects, false refusals | 0 / 0 / 0 | 0 / 0 / 0 | yes |
| N2 median tokens S01-S06 | <= 50% of crystaldba | 39.3% | yes |
| N2 largest response | <= --max-bytes | 8454 bytes (limit 32768) | yes |
| N2 truncations signalled | 100% | 3/3 | yes |
| N2 correctness | >= crystaldba on every scenario | 21/21 scenarios | yes |
| N2 trap detection (S17, S18) | >= crystaldba | S17 6/6 vs 5/6, S18 2/2 vs 2/2 | yes |
| N2 S19 index advice | same index, or gain within ±10% of crystaldba | dba events(account_id,created_at) (-100.0%) / crystal events(account_id), events(created_at) (-99.7%) | yes |
| N2 S20 no recommendation | no recommendation | none | yes |
| N2 S21 hypothetical index leak | none in the second plan | none | yes |
| N2 false refusals | 0 | 0 | yes |
| N3 success rate | >= crystaldba (92.6%) | 100.0% | yes |
| N3 median total tokens | <= 0% | -19.7% | yes |
| N3 median turns | <= 6.0 | 5.0 | yes |

## N2 — deterministic responses

| ID | Tool | Tokens dba | Tokens crystal | Δ | Bytes dba | p50 / p95 ms dba | Correct dba / crystal | Truncation dba |
|---|---|---|---|---|---|---|---|---|
| S01 | execute_sql | 1289 | 3309 | -61.0% | 2322 | 9 / 818 | yes / yes | none |
| S02 | execute_sql | 5473 | failed (oversized) | n/a | 8454 | 7 / 7 | yes / no | signalled |
| S03 | execute_sql | 4924 | failed (timeout) | n/a | 7886 | 6 / 8 | yes / no | signalled |
| S04 | execute_sql | 3015 | 267957 | -98.9% | 4559 | 8 / 16 | yes / yes | signalled |
| S05 | execute_sql | 82 | 259 | -68.3% | 143 | 17 / 25 | yes / yes | none |
| S06 | execute_sql | 112 | 213 | -47.4% | 145 | 6 / 13 | yes / yes | none |
| S07 | execute_sql | 55 | 45 | +22.2% | 109 | 3 / 5 | yes / yes | none |
| S08 | list_schemas | 75 | 176 | -57.4% | 160 | 6 / 7 | yes / yes | none |
| S09 | list_objects | 317 | 2912 | -89.1% | 575 | 14 / 22 | yes / yes | none |
| S10 | get_object_details | 484 | 617 | -21.6% | 982 | 17 / 24 | yes / no | none |
| S11 | get_object_details | 442 | 2456 | -82.0% | 877 | 9 / 11 | yes / yes | none |
| S12 | explain_query | 104 | 142 | -26.8% | 233 | 8 / 12 | yes / yes | none |
| S13 | explain_query | 139 | 59 | +135.6% | 320 | 7 / 13 | yes / yes | none |
| S14 | explain_query | 98 | 66 | +48.5% | 212 | 50 / 63 | yes / no | none |
| S15 | get_top_queries | 654 | 1023 | -36.1% | 1289 | 18 / 22 | yes / yes | signalled |
| S16 | get_top_queries | 476 | 1912 | -75.1% | 832 | 12 / 15 | yes / yes | none |
| S17 | analyze_db_health | 632 | 2516 | -74.9% | 1339 | 46 / 69 | yes / yes | none |
| S18 | analyze_db_health | 451 | 2237 | -79.8% | 952 | 21 / 35 | yes / yes | none |
| S19 | analyze_query_indexes | 125 | 4317 | -97.1% | 212 | 116 / 152 | yes / no | none |
| S20 | analyze_query_indexes | 58 | 453 | -87.2% | 139 | 25 / 27 | yes / yes | none |
| S21 | explain_query | 104 | 59 | +76.3% | 233 | 5 / 9 | yes / yes | none |

Trap detection: S17 (`all`) must find T1-T6 and S18 (`index`) T1 and T2. The last column counts only the traps crystaldba can detect (T1, T2, T4, T5, T6: its vacuum check covers wraparound, not dead tuples); the target compares that column.

| Scenario | Server | Detected | Expected found | Crystal-detectable found |
|---|---|---|---|---|
| S17 | dba | T1, T2, T3, T4, T5, T6 | 6/6 | 5/5 |
| S17 | crystal | T1, T2, T4, T5, T6 | 5/6 | 5/5 |
| S18 | dba | T1, T2, T6 | 2/2 | 2/2 |
| S18 | crystal | T1, T2, T6 | 2/2 | 2/2 |

Sessions of both servers ran as bench_ro only: yes.

## Security — attack corpus replay

| Server | Cases | refused | failed at execution | executed | with side effect |
|---|---|---|---|---|---|
| crystal | 124 | 102 | 12 | 10 | 1 |
| dba | 124 | 124 | 0 | 0 | 0 |

postgres-dba-mcp: 0 attack cases not refused or failed at execution, 0 with a side effect, 0 false refusals on legit.sql (47 cases). Target: 0 / 0 / 0.

crystaldba on the function-in-FROM cases (`fn-*-from`, CVE-2026-85620), a reported finding that does not fail the bench:

| Case | Outcome | Side effect |
|---|---|---|
| fn-lo-from | failed at execution | none |
| fn-settings-from | executed | none |
| fn-advisory-from | executed | advisory locks +60372/0/1/ExclusiveLock |
| fn-notify-from | executed | none |
| fn-sessions-from | executed | none |
| fn-files-from | failed at execution | none |
| fn-admin-from | failed at execution | none |
| fn-replication-from | failed at execution | none |
| fn-statreset-from | failed at execution | none |
| fn-dynsql-from | failed at execution | none |
| fn-ext-from | refused | none |

Every case that executed or had a side effect:

| Server | Case | Outcome | Side effect |
|---|---|---|---|
| crystal | multi-two-selects | executed | none |
| crystal | fn-settings-from | executed | none |
| crystal | fn-settings-lateral | executed | none |
| crystal | fn-advisory-from | executed | advisory locks +60372/0/1/ExclusiveLock |
| crystal | fn-notify-from | executed | none |
| crystal | fn-sessions-from | executed | none |
| crystal | evasion-prepare-select | executed | none |
| crystal | evasion-deallocate | executed | none |
| crystal | evasion-declare-cursor | executed | none |
| crystal | evasion-empty | executed | none |

## N3 — agent tasks

Model: claude-opus-5-5

| Task | Pass dba | Tokens dba | Turns dba | Pass crystal | Tokens crystal | Turns crystal |
|---|---|---|---|---|---|---|
| T01 | 3/3 | 269395 | 6.0 | 3/3 | 228748 | 6.0 |
| T04 | 3/3 | 180602 | 5.0 | 2/3 | 180635 | 5.0 |
| T06 | 3/3 | 181537 | 5.0 | 3/3 | 135668 | 3.0 |
| T08 | 3/3 | 182405 | 6.0 | 3/3 | 225904 | 6.0 |
| T09 | 3/3 | 222172 | 5.0 | 2/3 | 248473 | 5.0 |
| T10 | 3/3 | 221782 | 5.0 | 3/3 | 223384 | 5.0 |
| T11 | 3/3 | 133131 | 3.0 | 3/3 | 133203 | 3.0 |
| T12 | 3/3 | 132275 | 3.0 | 3/3 | 131984 | 3.0 |
| T13 | 3/3 | 134205 | 4.0 | 3/3 | 305601 | 14.0 |

## N3 — answer quality (judge)

Informational: not part of the verdict. Judge: claude-opus-5-5 through `claude -p` (no tool, no MCP), the same family of model as the agents, so it may favour its own style. Each pair (same task, same run index) is judged twice with the A/B order swapped; a server wins only when both orders agree, otherwise the pair is a tie. Scores are 1 to 5, the mean of both orders and all runs of the task. Judge cost: 5.60 USD.

| Task | dba wins | Ties | crystal wins | dba mean (correct. / compl. / action.) | crystal mean (correct. / compl. / action.) |
|---|---|---|---|---|---|
| T01 | 0 | 3 | 0 | 5.0 / 5.0 / 5.0 | 5.0 / 4.8 / 5.0 |
| T04 | 0 | 3 | 0 | 5.0 / 4.8 / 5.0 | 5.0 / 4.2 / 5.0 |
| T06 | 3 | 0 | 0 | 5.0 / 5.0 / 4.7 | 3.5 / 2.7 / 3.5 |
| T08 | 2 | 0 | 1 | 4.7 / 4.7 / 4.3 | 5.0 / 3.7 / 4.2 |
| T09 | 0 | 3 | 0 | 5.0 / 5.0 / 4.8 | 5.0 / 4.8 / 4.7 |
| T10 | 0 | 2 | 1 | 4.7 / 3.3 / 3.7 | 5.0 / 4.5 / 4.5 |
| T11 | 0 | 3 | 0 | 5.0 / 4.8 / 5.0 | 5.0 / 4.7 / 4.8 |
| T12 | 0 | 3 | 0 | 5.0 / 4.8 / 4.0 | 5.0 / 5.0 / 4.3 |
| T13 | 0 | 0 | 3 | 4.2 / 3.5 / 4.3 | 4.7 / 5.0 / 5.0 |
| all | 5 | 17 | 5 | 4.8 / 4.6 / 4.5 | 4.8 / 4.4 / 4.6 |

Factual errors and hallucinations listed by the judge:

- crystal T06 run 2: Says vacuum needs no action, but traps.bloated has 37.5% dead tuples (120k dead)
- crystal T06 run 2: Dismisses the low cache hit rate as a likely cold cache and never finds that public.events has no index besides its primary key despite a workload dominated by (account_id, created_at) queries
- crystal T06 run 2: States vacuum needs no action, but traps.bloated has 37.5% dead tuples (120k dead for 200k live)
- crystal T06 run 3: Calls the database 'mostly healthy' and misses its biggest problem: public.events has no index on (account_id, created_at) despite a workload dominated by those queries
- crystal T06 run 3: Reports vacuum as healthy, although traps.bloated has 37.5% dead tuples (120k dead for 200k live)
- crystal T06 run 3: Calls the database 'mostly healthy' and misses the dominant problem: public.events has no index besides its primary key under an events (account_id, created_at) workload
- dba T08 run 1: States that kind cycles in the order click, login, purchase, view. Its own data (id mod 4 and the first-event timestamps) shows the order is login, click, view, purchase.
- dba T08 run 1: States kind cycles 'click, login, purchase, view', but the sampled ids show the cycle is login, click, view, purchase (id mod 4 = 0, 1, 2, 3).
- crystal T13 run 1: Says the orders account_id/status/created_at query is not in the top 10, but the facts place that filter shape among the ten recorded workload shapes
- crystal T13 run 1: Says the orders account_id+status+created_at query is 'not in the top 10', but the workload has only ten shapes and this query is one of them

Stability: the same pairs judged again (each time in both orders); agreement is the share of pairs whose outcome is identical in every repeat (the table above uses the first pass).

| Task | Pairs repeated | Identical outcome |
|---|---|---|
| T01 | 3 | 3 (100%) |
| T04 | 3 | 2 (67%) |
| T06 | 3 | 3 (100%) |
| T08 | 3 | 3 (100%) |
| T09 | 3 | 3 (100%) |
| T10 | 3 | 3 (100%) |
| T11 | 3 | 2 (67%) |
| T12 | 3 | 3 (100%) |
| T13 | 3 | 3 (100%) |
| all | 27 | 25 (93%) |

Error origin: a second pass, **not blind** (it reads the tool calls, whose names reveal the server), gives the judge the question, the facts, the tool calls with their results (each result cut to its head and tail) and the final answer. It classifies every error it finds, and every error of the blind pass for that answer, as `tool` (the tool output was wrong, ambiguous or lossy and the answer relayed it), `misread` (the tool output said otherwise), `unsupported` (nothing supports it) or `none` (not an error after all). The counts are of errors, not of answers, and an error that both passes found is counted twice.

| Server | tool | misread | unsupported | none | Errors |
|---|---|---|---|---|---|
| dba | 0 | 2 | 13 | 0 | 15 |
| crystal | 7 | 2 | 12 | 0 | 21 |

Errors that come from a tool (tool, transcript file):

- crystal T06 run 2, `analyze_db_health` (`T06-crystal-2.jsonl`): Vacuum needs no action (it misses traps.bloated with 37.5% dead tuples) (The visible part of the vacuum section only reports on transaction ID wraparound and does not flag dead tuples, so the answer relayed a check that misses traps.bloated.)
- crystal T06 run 2, `analyze_db_health` (`T06-crystal-2.jsonl`): States vacuum needs no action, but traps.bloated has 37.5% dead tuples (120k dead for 200k live) (This duplicates the first vacuum error: the tool's vacuum check covers only wraparound and did not report dead-tuple bloat.)
- crystal T06 run 3, `analyze_db_health` (`T06-crystal-3.jsonl`): Calls the database 'mostly healthy' and misses its biggest problem: public.events has no index on (account_id, created_at) despite a workload dominated by those queries (analyze_db_health has no missing-index or workload check, so its output never flagged public.events, and the answer relayed that picture.)
- crystal T06 run 3, `analyze_db_health` (`T06-crystal-3.jsonl`): Reports vacuum as healthy, although traps.bloated has 37.5% dead tuples (The vacuum check in the tool output only reports transaction ID wraparound danger and never shows dead-tuple bloat on traps.bloated, so the answer faithfully relayed a lossy check.)
- crystal T06 run 3, `analyze_db_health` (`T06-crystal-3.jsonl`): Calls the database 'mostly healthy' and misses the dominant problem: public.events has no index besides its primary key under an events (account_id, created_at) workload (Same issue as the first report: the health check output contains no missing-index finding for public.events.)
- crystal T13 run 1, `get_top_queries` (`T13-crystal-1.jsonl`): The orders account_id/status/created_at query is 'not in the top 10' (first reviewer) (get_top_queries(limit 10) returned a short list that included a one-off COMMENT ON DATABASE statement and apparently left out this 679-call shape, so the answer relayed the tool's lossy ranking.)
- crystal T13 run 1, `get_top_queries` (`T13-crystal-1.jsonl`): The orders account_id+status+created_at query is 'not in the top 10' even though the workload has only ten shapes (first reviewer) (This is the same claim; the top-queries output omitted this workload shape, and the assistant reported faithfully what it saw.)

## Operations

| Measure | dba | crystal | Target | Met |
|---|---|---|---|---|
| Cold start (median of 20) | 11.3 ms | 1369 ms (includes docker run) | < 50 ms | yes |
| Memory after 100 S02 calls | 15.6 MB (linux image, docker stats; median of 3, max 30.9 MB) | 247.9 MB (median of 3, max 249.0 MB) | < 30 MB | yes |
| Binary / image size | 22.0 MB (binary) | 296 MB (image) | < 30 MB | yes |
| 50 concurrent S01 over HTTP | 0 errors in 50 calls | n/a | 0 errors | yes |

Native binary of the host after the same calls (informational, not checked against the target): 40.0 MB (phys_footprint).

## N1 — tool definitions (informational)

| Server | tools | full | deferred |
|---|---|---|---|
| dba | 9 | 2459 | 236 |
| crystal | 9 | 1965 | 66 |

`full` counts every tool's name, description and input schema; `deferred` counts the tool names plus the server instructions, which is what Claude Code loads at session start with tool search on. Counts are in claude -p. N1 is not part of the verdict.

## False refusals and skipped items

none
