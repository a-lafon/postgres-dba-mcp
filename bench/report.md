# postgres-dba-mcp vs crystaldba/postgres-mcp 0.3.0 — bench report

Run 20261108T222200Z · postgres-dba-mcp dev (7afada9) · crystaldba sha256:dbbd3468… · PostgreSQL 17.11 · scale 1 · tokens: estimate

## Verdict: INCONCLUSIVE

Unverified: N2 median tokens S01-S06; N3 median total tokens; N3 median turns; N3 success rate

| Criterion | Target | Result | Met |
|---|---|---|---|
| Security: attacks not refused, side effects, false refusals | 0 / 0 / 0 | 0 / 0 / 0 | yes |
| N2 median tokens S01-S06 | <= 50% of crystaldba | 36.6% (estimate) | unverified |
| N2 largest response | <= --max-bytes | 8443 bytes (limit 32768) | yes |
| N2 truncations signalled | 100% | 3/3 | yes |
| N2 correctness | >= crystaldba on every scenario | 22/22 scenarios | yes |
| N2 trap detection (S17, S18) | >= crystaldba | S17 6/6 vs 5/6, S18 2/2 vs 2/2 | yes |
| N2 S19 index advice | same index, or gain within ±10% of crystaldba | dba events(account_id,created_at) (-100.0%) / crystal events(account_id) (-97.0%) | yes |
| N2 S20 no recommendation | no recommendation | none | yes |
| N2 S21 hypothetical index leak | none in the second plan | none | yes |
| N2 false refusals | 0 | 0 | yes |
| N3 success rate | >= crystaldba | not run | unverified |
| N3 median total tokens | <= 0% | not run | unverified |
| N3 median turns | <= crystaldba + 1 | not run | unverified |

## N2 — deterministic responses

| ID | Tool | Tokens dba | Tokens crystal | Δ | Bytes dba | p50 / p95 ms dba | Correct dba / crystal | Truncation dba |
|---|---|---|---|---|---|---|---|---|
| S01 | execute_sql | 581 | 1569 | -63.0% | 2324 | 1 / 919 | yes / yes | none |
| S02 | execute_sql | 2111 | failed (oversized) | n/a | 8443 | 2 / 3 | yes / no | signalled |
| S03 | execute_sql | 1969 | failed (oversized) | n/a | 7874 | 2 / 2 | yes / no | signalled |
| S04 | execute_sql | 1154 | 100183 | -98.8% | 4613 | 2 / 3 | yes / yes | signalled |
| S05 | execute_sql | 36 | 120 | -70.0% | 143 | 9 / 14 | yes / yes | none |
| S06 | execute_sql | 37 | 87 | -57.5% | 147 | 1 / 1 | yes / yes | none |
| S07 | execute_sql | 28 | 29 | -3.4% | 109 | 1 / 1 | yes / yes | none |
| S08 | list_schemas | 41 | 93 | -55.9% | 162 | 1 / 1 | yes / yes | none |
| S09 | list_objects | 156 | 1336 | -88.3% | 624 | 4 / 7 | yes / yes | none |
| S10 | get_object_details | 246 | 323 | -23.8% | 982 | 4 / 5 | yes / no | none |
| S11 | get_object_details | 220 | 1259 | -82.5% | 877 | 4 / 4 | yes / yes | none |
| S12 | explain_query | 63 | 63 | 0.0% | 250 | 1 / 2 | yes / yes | none |
| S13 | explain_query | 84 | 25 | +236.0% | 336 | 2 / 2 | yes / yes | none |
| S14 | explain_query | 57 | failed (error) | n/a | 228 | 25 / 31 | yes / no | none |
| S15 | get_top_queries | 314 | 509 | -38.3% | 1256 | 5 / 5 | yes / yes | signalled |
| S16 | get_top_queries | 530 | 2635 | -79.9% | 2117 | 3 / 3 | yes / yes | signalled |
| S17 | analyze_db_health | 359 | 1400 | -74.4% | 1433 | 24 / 32 | yes / yes | none |
| S18 | analyze_db_health | 250 | 1226 | -79.6% | 997 | 17 / 18 | yes / yes | none |
| S19 | analyze_query_indexes | 53 | 1991 | -97.3% | 211 | 24 / 27 | yes / no | none |
| S20 | analyze_query_indexes | 35 | 250 | -86.0% | 138 | 4 / 5 | yes / yes | none |
| S21 | explain_query | 62 | 25 | +148.0% | 248 | 1 / 2 | yes / yes | none |
| S22 | execute_sql | 772 | 49065 | -98.4% | 3088 | 2 / 2 | yes / yes | signalled |

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
| crystal | 131 | 106 | 15 | 10 | 1 |
| dba | 131 | 131 | 0 | 0 | 0 |
| dbhub | 131 | 80 | 40 | 11 | 5 |
| dbhub-4tools | 131 | 80 | 40 | 11 | 5 |
| dbhub-rows100 | 131 | 80 | 40 | 11 | 5 |
| toolbox | 131 | 0 | 93 | 38 | 8 |
| toolbox-subset | 131 | 0 | 93 | 38 | 8 |

postgres-dba-mcp: 0 attack cases not refused or failed at execution, 0 with a side effect, 0 false refusals on legit.sql (47 cases). Target: 0 / 0 / 0.

crystaldba on the function-in-FROM cases (`fn-*-from`, CVE-2026-85620), a reported finding that does not fail the bench:

| Case | Outcome | Side effect |
|---|---|---|
| fn-lo-from | failed at execution | none |
| fn-settings-from | executed | none |
| fn-advisory-from | executed | advisory locks +2331/0/1/ExclusiveLock |
| fn-notify-from | executed | none |
| fn-sessions-from | executed | none |
| fn-files-from | failed at execution | none |
| fn-admin-from | failed at execution | none |
| fn-replication-from | failed at execution | none |
| fn-statreset-from | failed at execution | none |
| fn-index-from | failed at execution | none |
| fn-stats-from | failed at execution | none |
| fn-dynsql-from | failed at execution | none |
| fn-ext-from | refused | none |

Every case that executed or had a side effect:

| Server | Case | Outcome | Side effect |
|---|---|---|---|
| crystal | multi-two-selects | executed | none |
| crystal | fn-settings-from | executed | none |
| crystal | fn-settings-lateral | executed | none |
| crystal | fn-advisory-from | executed | advisory locks +2331/0/1/ExclusiveLock |
| crystal | fn-notify-from | executed | none |
| crystal | fn-sessions-from | executed | none |
| crystal | evasion-prepare-select | executed | none |
| crystal | evasion-deallocate | executed | none |
| crystal | evasion-declare-cursor | executed | none |
| crystal | evasion-empty | executed | none |
| dbhub | multi-two-selects | executed | none |
| dbhub | fn-advisory-from | executed | advisory locks +1090/0/1/ExclusiveLock |
| dbhub | fn-advisory-sub | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub | fn-advisory-arg | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub | fn-notify-from | executed | notifications received: chan:payload |
| dbhub | fn-notify-arg | executed | notifications received: chan:payload |
| dbhub | fn-sessions-from | executed | none |
| dbhub | fn-sessions-cte | executed | none |
| dbhub | fn-replication-arg | executed | none |
| dbhub | fn-dynsql-lateral | executed | none |
| dbhub | evasion-quoted-schema-fn | executed | none |
| dbhub-4tools | multi-two-selects | executed | none |
| dbhub-4tools | fn-advisory-from | executed | advisory locks +656/0/1/ExclusiveLock |
| dbhub-4tools | fn-advisory-sub | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub-4tools | fn-advisory-arg | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub-4tools | fn-notify-from | executed | notifications received: chan:payload |
| dbhub-4tools | fn-notify-arg | executed | notifications received: chan:payload |
| dbhub-4tools | fn-sessions-from | executed | none |
| dbhub-4tools | fn-sessions-cte | executed | none |
| dbhub-4tools | fn-replication-arg | executed | none |
| dbhub-4tools | fn-dynsql-lateral | executed | none |
| dbhub-4tools | evasion-quoted-schema-fn | executed | none |
| dbhub-rows100 | multi-two-selects | executed | none |
| dbhub-rows100 | fn-advisory-from | executed | advisory locks +650/0/1/ExclusiveLock |
| dbhub-rows100 | fn-advisory-sub | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub-rows100 | fn-advisory-arg | executed | advisory locks +105/0/1/ExclusiveLock |
| dbhub-rows100 | fn-notify-from | executed | notifications received: chan:payload |
| dbhub-rows100 | fn-notify-arg | executed | notifications received: chan:payload |
| dbhub-rows100 | fn-sessions-from | executed | none |
| dbhub-rows100 | fn-sessions-cte | executed | none |
| dbhub-rows100 | fn-replication-arg | executed | none |
| dbhub-rows100 | fn-dynsql-lateral | executed | none |
| dbhub-rows100 | evasion-quoted-schema-fn | executed | none |
| toolbox | write-grant | executed | none |
| toolbox | write-vacuum | executed | none |
| toolbox | disguised-select-into-temp | executed | none |
| toolbox | file-copy-stdout | executed | none |
| toolbox | txn-commit | executed | none |
| toolbox | txn-rollback | executed | none |
| toolbox | txn-begin | executed | none |
| toolbox | txn-start-read-write | executed | none |
| toolbox | txn-set-transaction | executed | none |
| toolbox | txn-set-session-characteristics | executed | none |
| toolbox | txn-set-local | executed | none |
| toolbox | txn-set-timeout | executed | none |
| toolbox | txn-reset-all | executed | none |
| toolbox | txn-reset-one | executed | none |
| toolbox | txn-discard | executed | none |
| toolbox | txn-prepare-transaction | executed | none |
| toolbox | fn-lo-cte | executed | large objects +1 |
| toolbox | fn-settings-from | executed | none |
| toolbox | fn-settings-sub | executed | none |
| toolbox | fn-settings-arg | executed | none |
| toolbox | fn-settings-cte | executed | none |
| toolbox | fn-settings-lateral | executed | none |
| toolbox | fn-advisory-from | executed | advisory locks +99/0/1/ExclusiveLock |
| toolbox | fn-advisory-sub | executed | advisory locks +98/0/1/ExclusiveLock |
| toolbox | fn-advisory-arg | executed | advisory locks +99/0/1/ExclusiveLock |
| toolbox | fn-notify-from | executed | notifications received: chan:payload |
| toolbox | fn-notify-arg | executed | notifications received: chan:payload |
| toolbox | fn-sessions-from | executed | none |
| toolbox | fn-sessions-cte | executed | none |
| toolbox | fn-replication-arg | executed | none |
| toolbox | evasion-mixed-case-fn | executed | none |
| toolbox | evasion-quoted-schema-fn | executed | none |
| toolbox | evasion-prepare | executed | none |
| toolbox | evasion-deallocate | executed | none |
| toolbox | evasion-do-language | executed | notifications received: c:p |
| toolbox | evasion-listen | executed | none |
| toolbox | evasion-notify | executed | notifications received: chan:payload |
| toolbox | evasion-empty | executed | none |
| toolbox-subset | write-grant | executed | none |
| toolbox-subset | write-vacuum | executed | none |
| toolbox-subset | disguised-select-into-temp | executed | none |
| toolbox-subset | file-copy-stdout | executed | none |
| toolbox-subset | txn-commit | executed | none |
| toolbox-subset | txn-rollback | executed | none |
| toolbox-subset | txn-begin | executed | none |
| toolbox-subset | txn-start-read-write | executed | none |
| toolbox-subset | txn-set-transaction | executed | none |
| toolbox-subset | txn-set-session-characteristics | executed | none |
| toolbox-subset | txn-set-local | executed | none |
| toolbox-subset | txn-set-timeout | executed | none |
| toolbox-subset | txn-reset-all | executed | none |
| toolbox-subset | txn-reset-one | executed | none |
| toolbox-subset | txn-discard | executed | none |
| toolbox-subset | txn-prepare-transaction | executed | none |
| toolbox-subset | fn-lo-cte | executed | large objects +1 |
| toolbox-subset | fn-settings-from | executed | none |
| toolbox-subset | fn-settings-sub | executed | none |
| toolbox-subset | fn-settings-arg | executed | none |
| toolbox-subset | fn-settings-cte | executed | none |
| toolbox-subset | fn-settings-lateral | executed | none |
| toolbox-subset | fn-advisory-from | executed | advisory locks +99/0/1/ExclusiveLock |
| toolbox-subset | fn-advisory-sub | executed | advisory locks +99/0/1/ExclusiveLock |
| toolbox-subset | fn-advisory-arg | executed | advisory locks +99/0/1/ExclusiveLock |
| toolbox-subset | fn-notify-from | executed | notifications received: chan:payload |
| toolbox-subset | fn-notify-arg | executed | notifications received: chan:payload |
| toolbox-subset | fn-sessions-from | executed | none |
| toolbox-subset | fn-sessions-cte | executed | none |
| toolbox-subset | fn-replication-arg | executed | none |
| toolbox-subset | evasion-mixed-case-fn | executed | none |
| toolbox-subset | evasion-quoted-schema-fn | executed | none |
| toolbox-subset | evasion-prepare | executed | none |
| toolbox-subset | evasion-deallocate | executed | none |
| toolbox-subset | evasion-do-language | executed | notifications received: c:p |
| toolbox-subset | evasion-listen | executed | none |
| toolbox-subset | evasion-notify | executed | notifications received: chan:payload |
| toolbox-subset | evasion-empty | executed | none |

## N3 — agent tasks

not run

## N3 — answer quality (judge)

not run

## Operations

| Measure | dba | crystal | Target | Met |
|---|---|---|---|---|
| Cold start (median of 20) | 5.9 ms | 1352 ms (includes docker run) | < 50 ms | yes |
| Memory after 100 S02 calls | 9.7 MB (linux image, docker stats; median of 3, max 24.6 MB) | 247.9 MB (median of 3, max 248.5 MB) | < 30 MB | yes |
| Binary / image size | 22.9 MB (binary) | 395 MB (image) | < 30 MB | yes |
| 50 concurrent S01 on one stdio session | 0 errors in 50 calls, p95 29 ms | 0 errors in 50 calls, p95 100 ms | 0 errors | yes |

Native binary of the host after the same calls (informational, not checked against the target): 55.3 MB (RSS).

## N1 — tool definitions (informational)

| Server | tools | full | deferred |
|---|---|---|---|
| dba | 10 | 1132 | 223 |
| crystal | 9 | 1225 | 38 |
| dbhub | 2 | 296 | 7 |
| dbhub-rows100 | 2 | 296 | 7 |
| dbhub-4tools | 4 | 431 | 13 |
| dbhub-default | 2 | 276 | 7 |
| toolbox | 29 | 4559 | 136 |
| toolbox-subset | 10 | 1243 | 39 |

`full` counts every tool's name, description and input schema; `deferred` counts the tool names plus the server instructions, which is what Claude Code loads at session start with tool search on. Counts are in estimate. N1 is not part of the verdict.

## False refusals and skipped items

- false refusal on legit.sql: aggregate-filter on crystal (Error: Error validating query: SELECT count(*) FILTER (WHERE status = 'done'), avg(amount), percentile_cont(0.5) WITHIN GROUP (ORDER BY amount) FROM public.orders;)
- false refusal on legit.sql: explain-analyze-buffers on crystal (Error: Error validating query: EXPLAIN (ANALYZE, BUFFERS) SELECT * FROM public.events WHERE account_id = 1;)
- false refusal on legit.sql: postgis-style on crystal (Error: Error validating query: SELECT ST_Distance(ST_MakePoint(0, 0), ST_MakePoint(id, id)) FROM public.accounts LIMIT 5;)
- false refusal on legit.sql: table-form on dbhub ({)
- false refusal on legit.sql: table-form on dbhub-4tools ({)
- false refusal on legit.sql: table-form on dbhub-rows100 ({)
- false refusal on legit.sql: trailing-semicolon-comment on dbhub ({)
- false refusal on legit.sql: trailing-semicolon-comment on dbhub-4tools ({)
- false refusal on legit.sql: trailing-semicolon-comment on dbhub-rows100 ({)
- false refusal on legit.sql: user-schema-function on crystal (Error: Error validating query: SELECT my_schema.score(id), myapp.bucket(id::text) FROM public.accounts;)
- false refusal on legit.sql: values on dbhub ({)
- false refusal on legit.sql: values on dbhub-4tools ({)
- false refusal on legit.sql: values on dbhub-rows100 ({)
- no analyze_db_health tool: S17 on dbhub
- no analyze_db_health tool: S17 on dbhub-4tools
- no analyze_db_health tool: S17 on dbhub-rows100
- no analyze_db_health tool: S17 on toolbox
- no analyze_db_health tool: S17 on toolbox-subset
- no analyze_db_health tool: S18 on dbhub
- no analyze_db_health tool: S18 on dbhub-4tools
- no analyze_db_health tool: S18 on dbhub-rows100
- no analyze_db_health tool: S18 on toolbox
- no analyze_db_health tool: S18 on toolbox-subset
- no analyze_query_indexes tool: S19 on dbhub
- no analyze_query_indexes tool: S19 on dbhub-4tools
- no analyze_query_indexes tool: S19 on dbhub-rows100
- no analyze_query_indexes tool: S19 on toolbox
- no analyze_query_indexes tool: S19 on toolbox-subset
- no analyze_query_indexes tool: S20 on dbhub
- no analyze_query_indexes tool: S20 on dbhub-4tools
- no analyze_query_indexes tool: S20 on dbhub-rows100
- no analyze_query_indexes tool: S20 on toolbox
- no analyze_query_indexes tool: S20 on toolbox-subset
- no explain_query tool: S12 on dbhub
- no explain_query tool: S12 on dbhub-rows100
- no explain_query tool: S12 on toolbox
- no explain_query tool: S12 on toolbox-subset
- no explain_query tool: S13 on dbhub
- no explain_query tool: S13 on dbhub-rows100
- no explain_query tool: S13 on toolbox
- no explain_query tool: S13 on toolbox-subset
- no explain_query tool: S14 on dbhub
- no explain_query tool: S14 on dbhub-rows100
- no explain_query tool: S14 on toolbox
- no explain_query tool: S14 on toolbox-subset
- no explain_query tool: S21 on dbhub
- no explain_query tool: S21 on dbhub-rows100
- no explain_query tool: S21 on toolbox
- no explain_query tool: S21 on toolbox-subset
- no get_object_details tool: S10 on dbhub
- no get_object_details tool: S10 on dbhub-4tools
- no get_object_details tool: S10 on dbhub-rows100
- no get_object_details tool: S11 on dbhub
- no get_object_details tool: S11 on dbhub-4tools
- no get_object_details tool: S11 on dbhub-rows100
- no get_top_queries tool: S15 on dbhub
- no get_top_queries tool: S15 on dbhub-4tools
- no get_top_queries tool: S15 on dbhub-rows100
- no get_top_queries tool: S16 on dbhub
- no get_top_queries tool: S16 on dbhub-4tools
- no get_top_queries tool: S16 on dbhub-rows100
- no list_objects tool: S09 on dbhub
- no list_objects tool: S09 on dbhub-4tools
- no list_objects tool: S09 on dbhub-rows100
- no list_schemas tool: S08 on dbhub
- no list_schemas tool: S08 on dbhub-4tools
- no list_schemas tool: S08 on dbhub-rows100
