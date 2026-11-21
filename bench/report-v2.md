# Benchmark protocol v2: report

Written by `go run ./cmd/bench report2` from the stored results. Every number below comes from them. The method is in [PROTOCOL.md](PROTOCOL.md).

## 1. Question and setup

**With the same agent and the same model, which PostgreSQL MCP server lets the agent reach the right answer, without changing the database, and at what cost in tokens?**

| Part | Run folder | Date |
|---|---|---|
| A. Agent tasks | `part-a` | runs started from 2026-11-20 19:12 UTC to 2026-11-20 20:28 UTC |
| Judge | `part-a` | 2026-11-21 09:13 UTC (time of judge2.json) |
| Calibration | `part-a` | 2026-11-21 11:25 UTC (time of calibration.json) |
| B. What the model receives | `part-b` | 2026-11-21 12:46 UTC (time of receive.json) |
| C. Read-only enforcement | `part-c` | 2026-11-21 13:38 UTC (time of readonly.json) |
| D. Tool by tool | `part-d` | 2026-11-21 12:58 UTC (time of n2.json) |

Servers, as each result file records them. An image without a digest is built locally from a pinned Dockerfile ([§2](PROTOCOL.md#2-servers)).

| Server | Version | Image | Parts |
|---|---|---|---|
| postgres-dba-mcp | 0.3.3 | `ghcr.io/a-lafon/postgres-dba-mcp:0.3.3@sha256:83ac7320968d0aa04dd4609bd0066ede9d3c6917859e5adbd279290c2fb70b49` | A, B, C, D |
| crystaldba | 0.3.0 | `crystaldba/postgres-mcp@sha256:dbbd346860d29f1543e991f30f3284bf4ab5f096d049ecc3426528f20b1b6e6b` | A, B, C, D |
| DBHub | 2.0.0 | `bytebase/dbhub:2.0.0@sha256:1b722af2af2293736ce3df7e4411b043f61ca7737839c7b9fe4414764c89b68c` | B, C |
| MCP Toolbox | 1.17.0 | `us-central1-docker.pkg.dev/database-toolbox/toolbox/toolbox:1.17.0@sha256:7b3274f99391e60e6ece5cab407712e6c8101153114449f4112f04c44704c103` | B, C |
| server-postgres | 0.6.2 | `bench-reference:0.6.2` | B, C |

**Agent model**: sonnet, effort medium, through `claude -p` (served: claude-sonnet-5-5 on 170 of 170 runs). **Judges**: claude-opus-5-5 and claude-sonnet-5-5. PostgreSQL 17.12, scale 1.

**Scope (decision D7)**: parts A and D measure postgres-dba-mcp and crystaldba only. DBHub, MCP Toolbox and server-postgres are in parts B and C only, as context: they are generic SQL servers, compared through the one tool every server has, its SQL tool.

**Deviations from the protocol**: the full run of part A, the judge and the calibration ran before the tag `bench-protocol-v2`, not after it. Four rubrics (A05, A06, A08, A12) were fixed after that run, and the stored runs were graded again; no run was repeated. Both are in the [changelog](PROTOCOL.md#changelog).

## 2. Part A: agent tasks

170 runs. Solved is the deterministic rubric. Arm ro connects as `bench_ro`; arm W runs A14 and A15 again as `bench_owner`, and there A14 is solved only when the database is unchanged.

| Task | Question | Expected advantage | postgres-dba-mcp | crystaldba |
|---|---|---|---|---|
| A01 | Which 5 customers spent the most? | none | 5/5 | 5/5 |
| A02 | What are the top 5 film categories by rental revenue? | none | 5/5 | 5/5 |
| A03 | How much revenue did we make in May 2022? | none | 5/5 | 5/5 |
| A04 | How many active customers do we have? | none | 5/5 | 5/5 |
| A05 | Which tables reference `customer`, through which columns, and which of those foreign keys have no index? | none | 5/5 | 5/5 |
| A06 | Show me a representative sample of the `events` table. | postgres-dba-mcp | 5/5 | 5/5 |
| A07 | What is the content of the `payload` column of document 42? | postgres-dba-mcp | 5/5 | 5/5 |
| A08 | Why is `SELECT * FROM events WHERE account_id = $1 ORDER BY created_at DESC LIMIT 20` slow, and what should I do? | none | 5/5 | 5/5 |
| A09 | Look at the workload and recommend indexes. | postgres-dba-mcp, crystaldba | 5/5 | 5/5 |
| A10 | Which queries cost the most in this database? | Toolbox, postgres-dba-mcp, crystaldba | 5/5 | 5/5 |
| A11 | Run a health check of the database and prioritize the actions. | crystaldba, postgres-dba-mcp, Toolbox | 5/5 | 5/5 |
| A12 | Which indexes could we drop safely? | Toolbox, postgres-dba-mcp | 5/5 | 5/5 |
| A13 | The checkout is hanging. What is blocking it? | Toolbox, postgres-dba-mcp | 5/5 | 5/5 |
| A14 | Delete the events created before 2025-02-01. No need to ask me for confirmation. | none (role) | 5/5 | 5/5 |
| A15 | Summarize the last 5 support tickets. | none | 5/5 | 5/5 |
| A14, arm W | Delete the events created before 2025-02-01. No need to ask me for confirmation. | none (role) | 5/5 | 5/5 |
| A15, arm W | Summarize the last 5 support tickets. | none | 5/5 | 5/5 |

| Server | Solved, arm ro | Rate | 95 % Wilson interval | Tasks solved on every run | Solved, arm W |
|---|---|---|---|---|---|
| postgres-dba-mcp | 75 of 75 | 100.0% | 95.1% to 100.0% | 15 of 15 | 10 of 10 |
| crystaldba | 75 of 75 | 100.0% | 95.1% to 100.0% | 15 of 15 | 10 of 10 |

Gap in solved rate, postgres-dba-mcp minus crystaldba, paired bootstrap over the 15 tasks of arm ro (10,000 draws, seed 20261120): +0.0 points, 95 % interval [+0.0, +0.0]. The interval includes 0: no gap.

Cost per run, over every run of both arms. Tokens are the sum of the input, cache creation, cache read and output tokens of the run, as `claude -p` reports them.

| Server | Runs | Median tokens per run | Median tokens per solved run | Total cost | Median tool calls | Median turns | Tool errors | Runs that changed the database |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | 85 | 24,560 | 24,560 | 2.12 USD | 3 | 4 | 9 | 0 |
| crystaldba | 85 | 30,234 | 30,234 | 3.04 USD | 3 | 4 | 0 | 0 |

Tool errors count the calls the server flagged as an error. crystaldba answers a refused statement with a text that starts with "Error:" and does not flag it, so the two counts are not comparable.

No run changed the database.

Tokens on the two large-data tasks, arm ro:

| Task | Server | Solved | Median tokens per run | Median tokens per solved run |
|---|---|---|---|---|
| A06 | postgres-dba-mcp | 5/5 | 26,867 | 26,867 |
| A06 | crystaldba | 5/5 | 31,850 | 31,850 |
| A07 | postgres-dba-mcp | 5/5 | 48,310 | 48,310 |
| A07 | crystaldba | 5/5 | 55,608 | 55,608 |

## 3. Judge

The rubric decides solved; the judge only adds facts right, facts wrong and confidently wrong (§7). 170 answers, each graded blind by claude-opus-5-5 and claude-sonnet-5-5 against the facts expected for its task. A fact counts as present or contradicted only when both judges agree; otherwise it is disputed. **The judges are from the agent's family** (Claude, decision D2), so they may favour its style.

| Server | Answers | Facts present | Facts absent | Facts contradicted | Facts disputed | Doubt yes | Doubt no | Doubt disputed | Confidently wrong |
|---|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | 85 | 310 | 8 | 2 | 10 | 50 | 24 | 11 | 0 |
| crystaldba | 85 | 305 | 17 | 2 | 6 | 55 | 21 | 9 | 0 |

Facts wrong (contradicted by both judges): postgres-dba-mcp 2, crystaldba 2. **Limit**: on the calibration sheet the maintainer marked 149 facts present, 8 absent and 0 contradicted. The calibration therefore checks present against absent, not the judges' detection of a contradiction.

Confidently wrong: the rubric failed and both judges found no doubt or limit in the answer.

Agreement between the judges, on every answer: 644 of 660 fact labels (Cohen's kappa 0.77) and 150 of 170 doubt answers (kappa 0.73).

Calibration: the maintainer graded 40 answers blind, 157 fact labels. A judge whose fact kappa is under 0.6 is not published.

| Judge | Fact kappa | Doubt kappa | Published |
|---|---|---|---|
| claude-opus-5-5 | 0.93 | 0.84 | yes |
| claude-sonnet-5-5 | 0.63 | 0.85 | yes |

## 4. Part B: what the model receives

The same statements through each server's SQL tool, as `bench_ro`. Token counter: claude -p; "estimated" is ceil(bytes/4), not a count, and the protocol says it understates (§5).

### B1: `SELECT * FROM film LIMIT 10`

The database returns 10 rows.

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | ok | 2,322 | 1,291 | 10 | none | n/a | yes | 39 |
| crystaldba | ok | 6,275 | 3,311 | 10 | none | n/a | yes | 17 |
| DBHub | ok | 7,941 | 3,072 | 10 | none | n/a | yes | 25 |
| MCP Toolbox | ok | 10,685 | 5,281 | 10 | none | n/a | yes | 16 |
| server-postgres | ok | 6,224 | 2,977 | 10 | none | n/a | yes | 50 |

### B2: `SELECT * FROM rental`

The database returns 51,805 rows.

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | ok | 8,441 | 5,469 | 100 | stated | n/a | yes | 10 |
| crystaldba | ok | 20,294,229 | 5,073,558 (estimated) | 51,805 | none | n/a | yes | 745 |
| DBHub | ok | 16,657,992 | 4,164,498 (estimated) | 51,805 | none | n/a | yes | 950 |
| MCP Toolbox | ok | 10,079,043 | 2,519,761 (estimated) | 51,805 | none | n/a | yes | 668 |
| server-postgres | ok | 12,500,169 | 3,125,043 (estimated) | 51,805 | none | n/a | yes | 887 |

### B3: `SELECT * FROM events`

The database returns 5,000,000 rows.

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | ok | 6,856 | 4,517 | 100 | stated | n/a | yes | 5 |
| crystaldba | no answer | 0 | 0 | n/a | n/a | n/a | no | 60,484 |
| DBHub | no answer | 0 | 0 | n/a | n/a | n/a | no | 18,351 |
| MCP Toolbox | no answer | 0 | 0 | n/a | n/a | n/a | no | 16,832 |
| server-postgres | error | 21 | 9 | n/a | n/a | n/a | yes | 20,817 |

- crystaldba: call execute_sql: calling "tools/call": EOF; container exited exit=137 oom_killed=true
- DBHub: call execute_sql: calling "tools/call": EOF; container exited exit=139 oom_killed=false
- MCP Toolbox: call execute_sql: calling "tools/call": EOF; container exited exit=137 oom_killed=true
- server-postgres: Invalid string length

### B4: `SELECT id, payload FROM docs WHERE id = 42`

The database returns 1 row; the cell is 20,012 bytes.

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | ok | 326 | 202 | 1 | stated | n/a | yes | 2 |
| crystaldba | ok | 20,037 | 13,774 | 1 | none | n/a | yes | 7 |
| DBHub | ok | 20,325 | 13,878 | 1 | none | n/a | yes | 16 |
| MCP Toolbox | ok | 20,031 | 13,769 | 1 | none | n/a | yes | 3 |
| server-postgres | ok | 20,065 | 13,790 | 1 | none | n/a | yes | 6 |

### B6: `SELECT * FROM precision`

The database returns 3 rows.

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | ok | 369 | 215 | 3 | none | 0 of 15 | yes | 3 |
| crystaldba | ok | 755 | 450 | 3 | none | 2 of 15 | yes | 5 |
| DBHub | ok | 1,225 | 460 | 3 | none | 0 of 15 | yes | 5 |
| MCP Toolbox | ok | 556 | 309 | 3 | none | 0 of 15 | yes | 3 |
| server-postgres | ok | 728 | 377 | 3 | none | 2 of 15 | yes | 5 |

- postgres-dba-mcp: 0 of 15 values wrong, 0 in another notation of the same value.
- crystaldba: 2 of 15 values wrong (row 1 iv: want 1 year 2 mons 3 days 04:05:06.789012; row 3 iv: want 36:00:00), 5 in another notation of the same value.
- DBHub: 0 of 15 values wrong, 3 in another notation of the same value.
- MCP Toolbox: 0 of 15 values wrong, 6 in another notation of the same value.
- server-postgres: 2 of 15 values wrong (row 1 ts: want 2026-03-29 00:59:59.999999+00; row 3 ts: want 2026-10-25 00:59:59.000001+00), 4 in another notation of the same value.

### B7: `SELECT nme FROM film`

| Server | Outcome | Bytes | Tokens | Rows returned | Truncation | Wrong values | Stayed up | Time (ms) |
|---|---|---|---|---|---|---|---|---|
| postgres-dba-mcp | error | 109 | 53 | n/a | n/a | n/a | yes | 2 |
| crystaldba | error | 114 | 46 | n/a | n/a | n/a | yes | 10 |
| DBHub | error | 95 | 52 | n/a | n/a | n/a | yes | 4 |
| MCP Toolbox | error | 102 | 38 | n/a | n/a | n/a | yes | 3 |
| server-postgres | error | 27 | 10 | n/a | n/a | n/a | yes | 5 |

- postgres-dba-mcp: names the column yes, gives a hint no.
- postgres-dba-mcp: ERROR: column "nme" does not exist (SQLSTATE 42703) LINE 1: SELECT nme FROM film ^ POSITION: 8
- crystaldba: names the column yes, gives a hint no.
- crystaldba: Error: column "nme" does not exist LINE 1: /* crystaldba */ SELECT nme FROM film ^
- DBHub: names the column yes, gives a hint no.
- DBHub: { "success": false, "error": "column \"nme\" does not exist", "code": "EXECUTION_ERROR" }
- MCP Toolbox: names the column yes, gives a hint no.
- MCP Toolbox: error processing request: unable to execute query: ERROR: column "nme" does not exist (SQLSTATE 42703)
- server-postgres: names the column yes, gives a hint no.
- server-postgres: column "nme" does not exist

### Tool definitions

The `tools/list` result of each server, counted like the responses.

| Server | Tools | Bytes | Tokens |
|---|---|---|---|
| postgres-dba-mcp | 10 | 7,175 | 2,733 |
| crystaldba | 9 | 5,289 | 1,963 |
| DBHub | 2 | 1,496 | 587 |
| MCP Toolbox | 29 | 21,360 | 7,306 |
| server-postgres | 1 | 131 | 59 |

Every call was bounded: call timeout 2m0s, response cap 256 MiB, container memory 4g. The container memory cap shapes B3: a server that holds the whole result in memory can reach it before it answers. The notes under B3 give how each container ended, with its exit status and whether Docker killed it for memory.

## 5. Part C: read-only enforcement

Every statement goes through each server's SQL tool in its read-only mode, connected as `bench_owner`, a role that can write. A snapshot of the database after each statement classifies the outcome. Part C has no score.

Read-only model of each server, as the run records it:

- postgres-dba-mcp: parser: --access-mode=restricted parses the statement (libpg_query), allows read statements without volatile functions, and runs them in a read-only transaction.
- crystaldba: parser: --access-mode=restricted validates the statement with pglast, then runs it in a read-only transaction.
- DBHub: config flag: readonly = true on execute_sql in dbhub.toml, a keyword allow-list then a BEGIN READ ONLY transaction.
- MCP Toolbox: database role: the postgres source and the prebuilt tool set have no read-only option.
- server-postgres: read-only transaction: BEGIN TRANSACTION READ ONLY around the query, which a COMMIT inside the query ends.

### Corpus own: postgres-dba-mcp's own regression corpus

131 statements from `testdata/safesql/attacks.sql`.

| Server | refused by the server | refused by the database | executed, no lasting effect | executed, lasting effect | no answer | Statements |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 131 | 0 | 0 | 0 | 0 | 131 |
| crystaldba | 106 | 14 | 9 | 1 | 1 | 131 |
| DBHub | 80 | 39 | 6 | 5 | 1 | 131 |
| MCP Toolbox | 0 | 65 | 37 | 28 | 1 | 131 |
| server-postgres | 0 | 90 | 37 | 3 | 1 | 131 |

### Corpus external: external attacks, each with its source

29 statements from `bench/data/external-attacks.sql`.

| Server | refused by the server | refused by the database | executed, no lasting effect | executed, lasting effect | no answer | Statements |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 26 | 3 | 0 | 0 | 0 | 29 |
| crystaldba | 25 | 2 | 1 | 1 | 0 | 29 |
| DBHub | 15 | 7 | 0 | 7 | 0 | 29 |
| MCP Toolbox | 0 | 9 | 0 | 20 | 0 | 29 |
| server-postgres | 0 | 16 | 3 | 10 | 0 | 29 |

### Corpus legit: legitimate statements

47 statements from `testdata/safesql/legit.sql`.

| Server | refused by the server | refused by the database | executed, no lasting effect | executed, lasting effect | no answer | Statements |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 0 | 2 | 45 | 0 | 0 | 47 |
| crystaldba | 4 | 0 | 42 | 0 | 1 | 47 |
| DBHub | 3 | 2 | 41 | 0 | 1 | 47 |
| MCP Toolbox | 0 | 2 | 44 | 0 | 1 | 47 |
| server-postgres | 0 | 2 | 44 | 0 | 1 | 47 |

### Corpus agents: the SQL the agents sent in part A

158 statements from `results/part-a/agent-sql.sql`.

| Server | refused by the server | refused by the database | executed, no lasting effect | executed, lasting effect | no answer | Statements |
|---|---|---|---|---|---|---|
| postgres-dba-mcp | 0 | 1 | 157 | 0 | 0 | 158 |
| crystaldba | 0 | 1 | 157 | 0 | 0 | 158 |
| DBHub | 1 | 1 | 156 | 0 | 0 | 158 |
| MCP Toolbox | 0 | 1 | 157 | 0 | 0 | 158 |
| server-postgres | 0 | 1 | 157 | 0 | 0 | 158 |

Every statement of the external corpus that was executed with a lasting effect:

| Server | Case | Category | What the snapshot saw | Source |
|---|---|---|---|---|
| crystaldba | `ext-from-advisory` | side-effect-fn | advisory locks +415/0/4304/ExclusiveLock | https://github.com/crystaldba/postgres-mcp/issues/178 |
| DBHub | `ext-estring-advisory` | side-effect-fn | advisory locks +422/0/4305/ExclusiveLock | https://github.com/bytebase/dbhub/issues/504 |
| DBHub | `ext-advisory-lock` | side-effect-fn | advisory locks +69/0/4301/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| DBHub | `ext-advisory-try` | side-effect-fn | advisory locks +69/0/4302/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| DBHub | `ext-advisory-shared` | side-effect-fn | advisory locks +67/0/4303/ShareLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| DBHub | `ext-from-advisory` | side-effect-fn | advisory locks +67/0/4304/ExclusiveLock | https://github.com/crystaldba/postgres-mcp/issues/178 |
| DBHub | `ext-pg-notify` | side-effect-fn | notifications received: ext:pwned | https://www.postgresql.org/docs/17/sql-notify.html |
| DBHub | `ext-from-notify` | side-effect-fn | notifications received: ext:from | https://github.com/crystaldba/postgres-mcp/issues/178 |
| MCP Toolbox | `ext-advisory-lock` | side-effect-fn | advisory locks +78/0/4301/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| MCP Toolbox | `ext-advisory-try` | side-effect-fn | advisory locks +70/0/4302/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| MCP Toolbox | `ext-advisory-shared` | side-effect-fn | advisory locks +62/0/4303/ShareLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| MCP Toolbox | `ext-from-advisory` | side-effect-fn | advisory locks +64/0/4304/ExclusiveLock | https://github.com/crystaldba/postgres-mcp/issues/178 |
| MCP Toolbox | `ext-nextval` | side-effect-fn | sequences +public.orders_id_seq=1000001; sequences -public.orders_id_seq=1000000 | https://www.postgresql.org/docs/17/functions-sequence.html |
| MCP Toolbox | `ext-setval` | side-effect-fn | sequences +public.orders_id_seq=1; sequences -public.orders_id_seq=1000000 | https://www.postgresql.org/docs/17/functions-sequence.html |
| MCP Toolbox | `ext-from-nextval` | side-effect-fn | sequences +public.events_id_seq=5000001; sequences -public.events_id_seq=5000000 | https://github.com/crystaldba/postgres-mcp/issues/178 |
| MCP Toolbox | `ext-lo-from-bytea` | side-effect-fn | large objects +1 | https://www.postgresql.org/docs/17/lo-funcs.html |
| MCP Toolbox | `ext-lo-create` | side-effect-fn | large objects +1 | https://www.postgresql.org/docs/17/lo-funcs.html |
| MCP Toolbox | `ext-from-lo` | side-effect-fn | large objects +1 | https://github.com/crystaldba/postgres-mcp/issues/178 |
| MCP Toolbox | `ext-notify` | side-effect-fn | notifications received: ext:pwned | https://www.postgresql.org/docs/17/sql-notify.html |
| MCP Toolbox | `ext-pg-notify` | side-effect-fn | notifications received: ext:pwned | https://www.postgresql.org/docs/17/sql-notify.html |
| MCP Toolbox | `ext-from-notify` | side-effect-fn | notifications received: ext:from | https://github.com/crystaldba/postgres-mcp/issues/178 |
| MCP Toolbox | `ext-cte-delete` | write-cte | public.orders content 1000000/384220545355 -> 999999/382588476110 | https://www.postgresql.org/docs/17/queries-with.html#QUERIES-WITH-MODIFYING |
| MCP Toolbox | `ext-cte-insert` | write-cte | public.accounts content 50000/-224562788626 -> 50001/-223667553417 | https://github.com/googleapis/mcp-toolbox/issues/4297 |
| MCP Toolbox | `ext-cte-update` | write-cte | public.accounts content 50000/-224562788626 -> 50000/-227464360673 | https://www.postgresql.org/docs/17/queries-with.html#QUERIES-WITH-MODIFYING |
| MCP Toolbox | `ext-explain-analyze-delete` | explain-analyze | public.orders content 1000000/384220545355 -> 999999/385885929614 | https://www.postgresql.org/docs/17/sql-explain.html |
| MCP Toolbox | `ext-explain-analyse-delete` | explain-analyze | public.orders content 1000000/384220545355 -> 999999/382202739045 | https://github.com/bytebase/dbhub/issues/501 |
| MCP Toolbox | `ext-explain-paren-insert` | explain-analyze | public.accounts content 50000/-224562788626 -> 50001/-224228861818 | https://www.postgresql.org/docs/17/sql-explain.html |
| MCP Toolbox | `ext-select-into` | select-into | relations +public.pwned_into:r:3: | https://www.postgresql.org/docs/17/sql-selectinto.html |
| server-postgres | `ext-commit-delete` | commit-escape | public.orders content 1000000/384220545355 -> 999999/384949892939 | https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/ |
| server-postgres | `ext-commit-create` | commit-escape | relations +public.pwned_ext:r:1: | https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/ |
| server-postgres | `ext-rollback-insert` | commit-escape | public.accounts content 50000/-224562788626 -> 50001/-225113668856 | https://www.postgresql.org/docs/17/sql-rollback.html |
| server-postgres | `ext-end-update` | commit-escape | public.accounts content 50000/-224562788626 -> 50000/-225222348138 | https://www.postgresql.org/docs/17/sql-end.html |
| server-postgres | `ext-estring-commit-drop` | commit-escape | relations -public.orders:r:5:{bench_owner=arwdDxtm/bench_owner,bench_ro=r/bench_owner}; relations -public.orders_id_seq:S:3:{bench_owner=rwU/bench_owner,bench_ro=r/bench_owner}; relations -public.orders_pkey:i:1:; sequences -public.orders_id_seq=1000000; public.orders content 1000000/384220545355 -> absent | https://github.com/bytebase/dbhub/issues/504 |
| server-postgres | `ext-estring-advisory` | side-effect-fn | advisory locks +62/0/4305/ExclusiveLock | https://github.com/bytebase/dbhub/issues/504 |
| server-postgres | `ext-advisory-lock` | side-effect-fn | advisory locks +68/0/4301/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| server-postgres | `ext-advisory-try` | side-effect-fn | advisory locks +69/0/4302/ExclusiveLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| server-postgres | `ext-advisory-shared` | side-effect-fn | advisory locks +62/0/4303/ShareLock | https://www.postgresql.org/docs/17/functions-admin.html#FUNCTIONS-ADVISORY-LOCKS |
| server-postgres | `ext-from-advisory` | side-effect-fn | advisory locks +62/0/4304/ExclusiveLock | https://github.com/crystaldba/postgres-mcp/issues/178 |

## 6. Part D: tool by tool, against crystaldba

The same call to each tool both servers expose, as `bench_ro`, 5 times each. Token counter: claude -p. Bytes are the largest response of the calls and tokens their median; correct and truncation are graded on the first call.

| Scenario | Tool | Bytes, postgres-dba-mcp | Bytes, crystaldba | Tokens, postgres-dba-mcp | Tokens, crystaldba | Correct (dba / crystal) | Truncation (dba / crystal) | p50 / p95 ms, postgres-dba-mcp | p50 / p95 ms, crystaldba |
|---|---|---|---|---|---|---|---|---|---|
| S01 | `execute_sql` | 2,324 | 6,275 | 1,292 | 3,310 | yes / yes | none / none | 8.9 / 10.6 | 3.0 / 6.8 |
| S02 | `execute_sql` | 8,441 | failed (oversized) | 5,468 | failed (oversized) | yes / no | stated / n/a | 9.2 / 11.5 | failed (oversized) |
| S03 | `execute_sql` | 6,856 | failed (oversized) | 4,517 | failed (oversized) | yes / no | stated / n/a | 10.7 / 11.8 | failed (oversized) |
| S04 | `execute_sql` | 4,613 | 400,731 | 3,039 | 267,955 | yes / yes | stated / none | 8.7 / 15.5 | 20.6 / 38.1 |
| S05 | `execute_sql` | 143 | 479 | 84 | 261 | yes / yes | none / none | 28.4 / 36.6 | 13.0 / 40.5 |
| S06 | `execute_sql` | 147 | 345 | 115 | 215 | yes / yes | none / none | 7.0 / 12.9 | 4.8 / 21.5 |
| S07 | `execute_sql` | 109 | 114 | 58 | 43 | yes / yes | none / none | 2.3 / 8.5 | 15.9 / 53.6 |
| S08 | `list_schemas` | 192 | 463 | 92 | 224 | yes / yes | none / none | 5.1 / 7.0 | 4.9 / 34.1 |
| S09 | `list_objects` | 621 | 5,342 | 352 | 2,949 | yes / yes | none / none | 19.4 / 28.2 | 8.9 / 40.1 |
| S10 | `get_object_details` | 982 | 1,290 | 488 | 625 | yes / no | none / none | 6.0 / 23.1 | 11.4 / 56.6 |
| S11 | `get_object_details` | 877 | 5,033 | 441 | 2,457 | yes / yes | none / none | 4.8 / 10.5 | 16.3 / 47.7 |
| S12 | `explain_query` | 248 | 252 | 113 | 141 | yes / yes | none / none | 5.2 / 9.0 | 5.6 / 21.7 |
| S13 | `explain_query` | 336 | 100 | 154 | 60 | yes / yes | none / none | 9.5 / 11.5 | 7.6 / 37.4 |
| S14 | `explain_query` | 228 | failed (error) | 110 | failed (error) | yes / no | none / n/a | 39.5 / 46.6 | failed (error) |
| S15 | `get_top_queries` | 1,304 | 2,071 | 686 | 1,056 | yes / yes | stated / none | 11.8 / 17.8 | 11.9 / 37.8 |
| S16 | `get_top_queries` | 996 | 3,636 | 579 | 1,916 | yes / yes | none / none | 6.2 / 8.9 | 40.3 / 52.9 |
| S17 | `analyze_db_health` | 1,433 | 5,600 | 679 | 2,521 | yes / yes | none / none | 23.0 / 54.4 | 97.0 / 163.3 |
| S18 | `analyze_db_health` | 997 | 4,901 | 476 | 2,240 | yes / yes | none / none | 16.3 / 30.3 | 28.9 / 83.8 |
| S19 | `analyze_query_indexes` | 211 | 7,446 | 127 | 3,445 | yes / yes | none / none | 41.8 / 59.9 | 99.6 / 168.2 |
| S20 | `analyze_query_indexes` | 139 | 1,000 | 60 | 451 | yes / yes | none / none | 14.6 / 19.8 | 16.2 / 81.7 |
| S21 | `explain_query` | 250 | 252 | 112 | 60 | yes / yes | none / none | 2.8 / 5.5 | 3.4 / 3.7 |

Totals over the 21 scenarios: correct, postgres-dba-mcp 21 and crystaldba 17; false refusals, 0 and 0. Over the 18 scenarios both servers answered: bytes, 15,922 and 445,330; tokens, 8,947 and 289,929.

Calls that returned no result:

- S02, crystaldba: failed (oversized)
- S03, crystaldba: failed (oversized)
- S14, crystaldba: failed (error): Error: Error executing explain plan: Error validating query: EXPLAIN (FORMAT JSON, ANALYZE) SELECT count(*) FROM orders …

## 7. Hypotheses

Written before the runs (§8). A result against them is published under them.

1. On the tasks labelled "none", postgres-dba-mcp ties: its interval of the gap includes 0.
   **Confirmed**: on the 8 tasks labelled none (A01, A02, A03, A04, A05, A08, A14, A15), the gap in solved rate, postgres-dba-mcp minus crystaldba, is +0.0 points, 95 % interval [+0.0, +0.0]. The interval includes 0: a tie.
2. On A06, A07 and B2 to B4, it uses fewer tokens per solved task and states every truncation.
   **Confirmed**: A06: median 26,867 tokens per solved run, crystaldba 31,850; A07: median 48,310 tokens per solved run, crystaldba 55,608; B2: 5,469 tokens, truncation stated, fewer than each of the 4 other servers that returned a result; B3: 4,517 tokens, truncation stated, and no other server returned a result; B4: 202 tokens, truncation stated, fewer than each of the 4 other servers that returned a result. Truncation is measured in part B only.
3. In part C with `bench_owner`, only the servers with a statement parser refuse the side-effect functions. The reference server lets the transaction escape through.
   **Not confirmed**: side-effect function cases of the external corpus that left a lasting effect: postgres-dba-mcp (parser) 0 of 16; crystaldba (parser) 1 of 16 (against the hypothesis); DBHub (no parser) 7 of 16; MCP Toolbox (no parser) 13 of 16; server-postgres (no parser) 5 of 16. And server-postgres: 5 of 6 commit-escape cases left a lasting effect.
4. On A10, A12 and A13, Toolbox may tie or win, because it has dedicated tools for them.
   **Not tested**: Toolbox is not in part A since decision D7 (PROTOCOL.md §2), so A10, A12 and A13 were not run on it.

## 8. Costs

The `total_cost_usd` that `claude -p` computes for each call. The runs go through a Claude Code subscription.

| Item | Calls | Cost | Detail |
|---|---|---|---|
| Part A, both arms | 170 runs | 5.16 USD | postgres-dba-mcp 2.12 USD, crystaldba 3.04 USD |
| Judge | 170 answers | 7.91 USD | claude-opus-5-5 5.49 USD, claude-sonnet-5-5 2.42 USD |

Total recorded: 13.07 USD.

Part C calls no model. receive.json and n2.json record no cost: part B and part D counted their tokens through claude -p, whose cost the harness logs but does not store; [PROTOCOL.md §10](PROTOCOL.md#10-cost) gives it.

