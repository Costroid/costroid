# The ingest service

`costroid serve` receives a run's token counts, prices them against the pinned catalog and keeps the rows in Postgres. It is the same binary as the CLI.

## Run it

The service needs Postgres 15 or later, where the `public` schema grants no `CREATE` by default, built with lz4, as the `postgres:17-alpine` image is. Create two roles first. The owner role runs migrations and owns every table. The application role owns nothing and has `NOBYPASSRLS`. The service refuses to start on a superuser, a `BYPASSRLS` role or a role that owns a relation.

```sql
CREATE ROLE costroid_owner LOGIN PASSWORD '...' NOSUPERUSER NOBYPASSRLS;
CREATE ROLE costroid_app   LOGIN PASSWORD '...' NOSUPERUSER NOBYPASSRLS;
CREATE DATABASE costroid OWNER costroid_owner;
```

```sh
export COSTROID_DATABASE_URL='postgres://costroid_app:...@db/costroid'
export COSTROID_MIGRATION_DATABASE_URL='postgres://costroid_owner:...@db/costroid'
export COSTROID_OIDC_AUDIENCE='https://costroid.example'
export COSTROID_PSEUDONYM_KEY="$(openssl rand -base64 32)"
costroid serve --addr :8080 --bind github.com:example
```

`--bind` gives an SCM owner to this instance's tenant. `--gitlab-host` trusts one more GitLab instance. GitHub Actions and gitlab.com are trusted by default, and their keys still need their hosts on the egress allowlist. [`docs/install.md`](install.md) has the list and its report.

Migrations run forward only. The service refuses a migration edited after it was applied, and a database ahead of the binary.

## Paths

| Path                     | Answers                                                               |
| ------------------------ | --------------------------------------------------------------------- |
| `POST /v1/rows`          | The JSON lines `costroid run --out` writes                            |
| `POST /otlp/v1/logs`     | OTLP/HTTP logs, protobuf or JSON                                      |
| `POST /otlp/v1/traces`   | OTLP/HTTP traces, protobuf or JSON                                    |
| `POST /otlp/v1/metrics`  | Accepted and not read, so an exporter of all three signals sees no error |
| `GET /v1/pulls/{pr}`     | One pull request's lines and budget spend, with `?repo=host/owner/name` |
| `GET /healthz`           | 204                                                                   |

Set `OTEL_EXPORTER_OTLP_ENDPOINT` to `https://<host>/otlp`.

Every path but `/healthz` takes a bearer: a CI OIDC ID token, or a tenant ingest key that starts `costroid_ik_`. An absent or invalid bearer answers one `AUTH_TOKEN_INVALID`, and the answer does not say which. A token whose issuer's host is off the egress allowlist cannot be checked, so it answers `503` with `EGRESS_REFUSED`.

## Ingest keys

An ingest key authenticates a sender with no CI token. Its rows stay unverified and unattributed, and never reach a budget.

```sh
export COSTROID_MIGRATION_DATABASE_URL='postgres://costroid_owner:...@db/costroid'
costroid keys create --owner github.com:example > key
costroid keys revoke 3f1c...
```

`create` gives the key to the tenant holding that SCM owner. It prints the key on stdout, once, and its id on stderr. The ledger keeps the key's SHA-256, which is the id, so a lost key cannot be read back. `revoke` takes the id and stops the key at the next request. Both run under the owner role, because the service's own role can only read the table.

## What the token decides

Repository, run, attempt, actor and event come from the CI token, never from the payload. A stated repository, run or pull request that disagrees rejects the whole request with `JOIN_KEY_MISMATCH`, and nothing is stored.

The pull request comes from the token's ref, `refs/pull/<n>/merge`, on GitHub. A token that names none, such as one from a `push` run, cannot prove a stated pull request, so the statement is discarded. No GitLab token names a merge request: `ref_path` is the source branch there. A GitLab row therefore has no pull request and stays out of the per-PR view.

`costroid/commit_sha` means the head commit, the value of `vcs.ref.head.revision`. A token proves it only on a `push`, where the token's `sha` is the tip of the ref. On every other event the token's `sha` may be another commit, so the two are kept apart:

| Column             | Holds                                                                  |
| ------------------ | ---------------------------------------------------------------------- |
| `token_commit_sha` | The token's `sha`, the verified value. Empty under an ingest key       |
| `commit_sha`       | The head: the token's `sha` on a `push`, otherwise the sender's statement |
| `head_basis`       | `token` or `stated`, and empty when the row names no head              |

A stated head that differs from the token's `sha` on a `pull_request` run is expected, and the row keeps it as a claim. On a `push`, a stated head that differs rejects the request. A stated head that is not a commit rejects it on every event. Nothing writes a token's merge commit into `commit_sha`.

`head_basis = 'stated'` is the seam for an SCM check. No token names a pull request's head, so the join cannot disprove a stated one. A later check reads those rows and reports a head the pull request never had as its own issue. A row with `head_basis = 'stated'` and `commit_sha` equal to `token_commit_sha` on a `pull_request` run names the merge commit, which is a wrapper stating `GITHUB_SHA`. A wrapper states `github.event.pull_request.head.sha`.

| Credential                                                                    | `run_identity`   | `attribution_grade` | Counts toward a budget |
| ----------------------------------------------------------------------------- | ---------------- | ------------------- | ---------------------- |
| CI token that names a pull request, on a row with a session id and a head     | `token_verified` | `exact`             | Yes                    |
| CI token, otherwise                                                           | `token_verified` | `windowed`          | Yes                    |
| Ingest key                                                                    | `unverified`     | `unattributed`      | Never                  |

A sender may lower an `exact` row to `windowed`. It cannot raise a row. Postgres holds the last line as well: a `CHECK` refuses an unverified row with any other grade, and `budget_eligible` is a generated column.

A budget rule reads `budget.spend` in the per-PR answer. While no eligible row exists, `budget.evidence` is false and `budget.spend` is empty.

`totals` in the same answer adds up every row of the pull request, one figure per currency and cost basis. It is a reading, so an unverified row counts, and a budget rule never reads it.

`SubAccountId` is kept as the sender stated it, so a stored row can reconcile per credential. It is a claim, like every other column a sender states.

`BillingAccountId` is kept the same way, up to 512 characters, and `unknown` when a row states none. It is a stated claim that nothing verifies, and it never replaces the tenant. The per-PR view returns it as `billing_account_id` on each line, and a line never adds across two accounts.

## Prices

The service prices every row from its token count. It never reads a cost column, a unit price or a catalog version from a sender, and each one it sees is counted under `client_cost`.

`POST /v1/rows` accepts row versions 0.1, 0.2 and 0.3. `RegionId` and `x_CostroidServiceTier` state the region and the tier from version 0.2 on. `SkuPriceId` supplies the context tier, and the service tier of a version 0.1 row. They are usage facts, trusted as far as token counts are, and reconciliation checks them. An id naming another model or class is ignored. A row with no `RegionId` prices at `global`.

A cache write prices at its own class, and `cache_write_30m` and `cache_write_12h` are classes of their own. `costroid/assumed_ttl` says a rule assigned the class. It stays on the stored row and changes no figure. A value that is not the lifetime of the row's class rejects the request.

A provider with no catalog sheet is refused. It is never stored at zero. An unknown model of a known provider falls to the sheet's non-zero fallback tier, and the row says so.

## What each issuer's token says

Every fact here was read on 2026-11-01 from the page named beside it.

| Field          | GitHub Actions                                                  | GitLab CI                                                       |
| -------------- | --------------------------------------------------------------- | --------------------------------------------------------------- |
| Issuer         | `https://token.actions.githubusercontent.com` [1]               | The instance's URL, `https://gitlab.com` [3] [4]                |
| Key set        | `/.well-known/jwks` [2]                                         | `/oauth/discovery/keys` [4]                                     |
| Algorithm      | RS256 [2]                                                       | RS256 [3]                                                       |
| Repository     | `repository` [1]                                                | `project_path` [3]                                              |
| Run            | `run_id` [1]                                                    | `pipeline_id` [3]                                               |
| Attempt        | `run_attempt` [1]                                               | None. A pipeline's rows carry no attempt                        |
| Commit         | `sha` [2] [5]                                                   | `sha` [3]                                                       |
| Actor          | `actor` [1]                                                     | `user_login` [3]                                                |
| Event          | `event_name` [1]                                                | `pipeline_source` [3]                                           |
| Ref            | `ref` [1]                                                       | `ref_path` is not read, because it never names a merge request  |
| Lifetime       | Not documented. The example token's `exp` is 300 s after `iat` [5] | The job's timeout, or five minutes without one [3]           |

The vendors document no key set path. Each comes from the `jwks_uri` of the issuer's discovery document, which a change at the vendor would move. The service reads the fixed path and fails closed if it stops answering.

GitHub's `pull_request` event: the workflow reference says "`GITHUB_SHA` is the SHA of the merge commit on the merge branch", and `GITHUB_REF` is `refs/pull/<n>/merge` [6]. `pull_request_target` and `issue_comment` run on the last commit of the default branch, with the default branch as their ref [6]. Neither names a pull request. Dependabot's update jobs have the event `dynamic` [1].

GitHub's reference does not describe the `sha` claim. It appears in the example token [5] and in `claims_supported` [2]. That it equals `GITHUB_SHA` on `pull_request` is unverified: no page says so and no real token was read. The service does not depend on it. On any event but `push`, it treats `sha` as saying nothing about the head.

GitLab's token: `ref` is "the source branch ref" in a merge request pipeline, and `ref_path` is "the source branch ref path" [3]. No claim carries the merge request's number. A merged results pipeline tests "a temporary merged commit" [7], and the token cannot tell one from a detached pipeline, because both have the source `merge_request_event` [8]. In a merge request pipeline, `project_path` is the source project [3]. A fork's merge request therefore resolves to the fork's owner.

GitHub Enterprise can issue tokens from its own URL [9], and the service does not trust one by default. A GitLab instance other than gitlab.com takes `--gitlab-host`.

1. https://docs.github.com/en/actions/reference/security/oidc
2. https://token.actions.githubusercontent.com/.well-known/openid-configuration
3. https://docs.gitlab.com/ci/secrets/id_token_authentication/
4. https://gitlab.com/.well-known/openid-configuration
5. https://docs.github.com/en/actions/concepts/security/openid-connect
6. https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows
7. https://docs.gitlab.com/ci/pipelines/merged_results_pipelines/
8. https://docs.gitlab.com/ci/jobs/job_rules/
9. https://raw.githubusercontent.com/github/docs/main/content/actions/reference/security/oidc.md

## What is dropped

The edge keeps an allowlist of attributes. Everything else is dropped before anything is stored and counted in `ingest_drops`, one counter per field name per day. Prompts, messages, system instructions, tool parameters, tool output and raw API bodies have named counters. Anything else that reads as content counts as `content_other`, and the rest as `unlisted`. A value never reaches the table.

`user.email` becomes the SCM login under a CI token and a per-tenant HMAC pseudonym under an ingest key. Tags outside the `costroid/` prefix are dropped, because a free-form key and value are where content would hide.

## OTLP dialects

| Agent          | Reads                                                                       | Input includes cache | Cache writes              |
| -------------- | --------------------------------------------------------------------------- | -------------------- | ------------------------- |
| GenAI standard | `gen_ai.usage.*`, with `gen_ai.system`, `prompt_tokens` and `completion_tokens` as older names | Yes | `cache_write`, formerly `cache_creation`; lifetime assumed |
| Claude Code    | `claude_code.api_request`                                                   | No                   | Two lifetime counts       |
| Codex          | `gen_ai.usage.*` on spans, with `codex.usage.reasoning_output_tokens`. The `codex.sse_event` log repeats the span, so it is left out | Yes | `cache_write`; lifetime assumed |
| Copilot CLI    | `gen_ai.usage.*` on `chat` spans                                            | Yes                  | `cache_creation`; lifetime assumed |
| Cursor         | `cursor.api.request.*_tokens` on logs                                       | No                   | `cache_creation_tokens`; lifetime assumed |
| OpenCode V2    | `gen_ai.usage.*` on spans                                                   | Yes                  | None reported             |

Where the table says the lifetime is assumed, a row that took those tokens carries `costroid/assumed_ttl`.

A request's rows from OTLP and from the result stream share row ids when both carry the provider's request id, so a run sent both ways counts once.

Telemetry does not say how a run is billed, so an OTLP row is `list_equivalent` unless the wrapper sets `costroid.cost_basis=measured` in `OTEL_RESOURCE_ATTRIBUTES`. `costroid.enforcement_grade` works the same way.

## Tenants

Every table carries row level security with `FORCE`. The service names a tenant with `SET LOCAL` in each transaction, and no statement carries a tenant predicate of its own. The application role can insert rows and read them. It cannot update or delete them.

`egress_counters` is the deployment's own table and has no tenant column. Its policy admits every role, and the application role reads, inserts and updates it. It cannot delete from it.

## Checks

`make check` runs everything with Postgres in Docker, and fails when Docker is missing. It ends with the per-PR view's benchmark on one million rows, which prints its p99 and fails at 200 ms. `make check-short` runs without Docker. `make fuzz` runs each parser's fuzz target for `FUZZTIME`. `make bench-ingest` prints the rows per second of `POST /v1/rows`, for one stream and for four, on `INGEST_CORES` cores, 8 by default.

## Throughput

The target is 10,000 rows a second per instance. These figures are from 2026-11-01, through the whole path: token check, parse, price and one transaction per batch of 10,000 rows, from a client in the same process.

| Cores for the service | One stream, rows/s | Four streams, rows/s |
| --------------------- | ------------------ | -------------------- |
| 2                     | 8,432              | 14,773               |
| 4                     | 8,901 to 10,394    | 18,651 to 18,731     |
| 8                     | 9,645 to 10,918    | 21,875 to 24,756     |
| 16                    | 10,774             | 23,597               |
| 32                    | 10,226             | 23,569               |

A range is every run at that core count, on a machine with other work on it. One stream sits at the target, and a run can land under it.

Conditions: Postgres 17-alpine in Docker on the same machine, an AMD Ryzen AI MAX+ 395 under WSL2, with `fsync=off` and `shared_buffers=256MB`. A real disk's fsync is not in these figures. The table starts empty. With one million rows already in it, one stream on 4 cores read 9,543 rows/s.

One stream stops rising past 8 cores because one transaction runs on one Postgres backend, which takes about three quarters of each batch's second. A batch stays one transaction so that a retry is whole. Four streams use four backends.

Each change below is measured on the store alone, the best of eight batches, in one run:

| Write path                                                   | Rows/s |
| ------------------------------------------------------------ | ------ |
| `unnest`, `ON CONFLICT`, `jsonb`, pglz                       | 6,991  |
| `COPY` into a staging table, then `ON CONFLICT`, same table  | 5,156  |
| Primary key read first, `ON CONFLICT` only after a collision | 8,342  |
| With lz4                                                     | 9,299  |
| With `work_mem` of 64 MB, so `unnest` stays in memory        | 10,069 |
| With `json`                                                  | 14,258 |

`COPY` cannot write this table directly, because Postgres refuses `COPY FROM` under row level security, and a staging table pays for the rows twice. Parsing, pricing and encoding rows run on every core.
