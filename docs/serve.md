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

`COSTROID_OIDC_AUDIENCE` names this service. The service refuses to start on GitHub's default audience, any URL on `github.com` such as `https://github.com/example`. A workflow that requests a token without naming an audience gets that one, so a token meant for any other service would pass here too. GitHub's [OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) gives the repository owner's URL as the default. That page was not re-read on 2026-11-07, so the statement is unverified here.

`--bind` gives an SCM owner to this instance's tenant. `--gitlab-host` trusts one more GitLab instance. GitHub Actions and gitlab.com are trusted by default, and their keys still need their hosts on the egress allowlist. [`docs/ledger.md`](ledger.md#egress) has the list and its report.

Migrations run forward only. The service refuses a migration edited after it was applied, and a database ahead of the binary.

## Paths

| Path                     | Answers                                                               |
| ------------------------ | --------------------------------------------------------------------- |
| `POST /v1/rows`          | The JSON lines `costroid run --out` writes                            |
| `POST /v1/credentials`   | A minting job's attestation of a gateway key                          |
| `POST /v1/credentials/{key}/spend` | A closing job's attestation of what the gateway counted on the key |
| `POST /otlp/v1/logs`     | OTLP/HTTP logs, protobuf or JSON                                      |
| `POST /otlp/v1/traces`   | OTLP/HTTP traces, protobuf or JSON                                    |
| `POST /otlp/v1/metrics`  | Accepted and not read, so an exporter of all three signals sees no error |
| `GET /v1/pulls/{pr}`     | One pull request's lines and budget spend, with `?repo=host/owner/name` |
| `POST /v1/pulls/{pr}/verdict` | The policy's verdict on that pull request's rows, with `?repo=host/owner/name` |
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

| Credential                                                                                                 | `run_identity`   | `attribution_grade` | Counts toward a budget |
| ---------------------------------------------------------------------------------------------------------- | ---------------- | ------------------- | ---------------------- |
| CI token that names a pull request, on a row with a head and a `SubAccountId` attested for the token's run | `token_verified` | `exact`             | Yes                    |
| CI token, otherwise                                                                                        | `token_verified` | `windowed`          | Yes                    |
| Ingest key                                                                                                 | `unverified`     | `unattributed`      | Never                  |

A sender may lower an `exact` row to `windowed`. It cannot raise a row. Postgres holds the last line as well: a `CHECK` refuses an unverified row with any other grade, and `budget_eligible` is a generated column.

A budget rule reads `budget` in the per-PR answer. `budget.spend` adds up the eligible rows' list cost per currency. `budget.basis` says what stands behind that spend, and `budget.evidence` is true only for the last two:

| `budget.basis`     | When                                                                                                   |
| ------------------ | ------------------------------------------------------------------------------------------------------ |
| `none`             | No eligible row exists. `budget.spend` is empty                                                        |
| `agent_counts`     | The rows are token-verified, and their token counts are the agent's job's own                          |
| `gateway_measured` | The gateway's attested count covers every eligible row, each in USD. `budget.gateway_usd` holds it     |
| `reconciled`       | A provider's report wrote every eligible row                                                           |

A CI token proves which run sent a row. It does not prove the row's token counts, which the agent's job wrote. So a token-verified row is a budget's evidence only when the gateway's own count, or a provider's report, stands behind its spend. No path stores a provider's report in this release.

`totals` in the same answer adds up every row of the pull request, one figure per currency and cost basis. It is a reading, so an unverified row counts, and a budget rule never reads it.

`SubAccountId` is kept as the sender stated it, so a stored row can reconcile per credential. It is a claim, like every other column a sender states. `exact` means a provider's report can be filtered by the row's credential, and a key every run shares can be filtered by too, so a stated credential is not enough. `exact` needs a credential attested as minted for the token's run, as the next section says. A shared key, a provider's key id, or any credential the service cannot prove reads `windowed`. A session id joins a row and never earns `exact`, so a `SubAccountId` that repeats the session id reads `windowed`. A row from OTLP states no credential, so it stops at `windowed` too.

`BillingAccountId` is kept the same way, up to 512 characters, and `unknown` when a row states none. It is a stated claim that nothing verifies, and it never replaces the tenant. The per-PR view returns it as `billing_account_id` on each line, and a line never adds across two accounts.

## Which credential earns `exact`

`costroid gateway mint` registers one key per run on a LiteLLM gateway, and the rows name the key's id in `SubAccountId`. Once it has registered the key, the minting job attests it: it posts to `POST /v1/credentials` under its own OIDC token. [`docs/gateway.md`](gateway.md) describes the jobs.

```json
{"key_id": "<64 hexadecimal characters>", "repository": "github.com/acme/widgets", "run_id": "16873409921", "run_attempt": "1"}
```

The service stores an attestation only when all of these hold:

- the bearer is a CI token, not an ingest key;
- the token's `environment` claim equals `COSTROID_GATEWAY_ENVIRONMENT`, `costroid-gateway` by default;
- the token's repository, run and attempt equal the attestation's.

A refusal answers `403` with `ATTESTATION_FORBIDDEN` for the first two, and `JOIN_KEY_MISMATCH` for the third. A key already attested for another run answers `409` with `JOIN_KEY_MISMATCH`, and the first attestation stands. The same attestation again answers `204` and changes nothing.

A row reads `exact` only when its `SubAccountId` is an attested key and the row's own CI token names the attestation's repository, run and attempt, besides naming a pull request and a head. A key attested for another run, another attempt or another repository reads `windowed`, and so does a key never attested.

The environment's protection rules are what make an attestation trustworthy. The repository's owner sets them in GitHub, and the service only reads the claim GitHub puts in the token. A job admitted to the environment can attest a key for its own run, and no other. With required reviewers on the environment, a person approves each run before its job can attest. Without them, anyone who can push a branch of the repository can run a job in that environment, and attest a key that is not a run key.

GitHub's [OIDC reference](https://docs.github.com/en/actions/reference/security/oidc) lists the `environment` claim. It was not re-read on 2026-11-07, and no real token from an environment job has been read, so the claim's presence on that job is unverified.

`credential_basis` records the outcome on each row: `minted` for an attested key, `stated` otherwise, and empty when the row names no credential. Postgres refuses an `exact` row whose basis is not `minted`. Rows stored before this rule keep their grade.

## The gateway's count

`costroid gateway close` holds the gateway's admin key in the gateway's environment. When `COSTROID_INGEST_URL` names this service, close reads what the gateway counted on the run's key before it deletes the key, and then attests the figure under its own OIDC token, for `COSTROID_INGEST_AUDIENCE`:

```json
{"repository": "github.com/acme/widgets", "run_id": "16873409921", "run_attempt": "1", "currency": "USD", "spend": "0.412"}
```

It posts the body to `POST /v1/credentials/{key}/spend`, where `{key}` is the key's 64-character id. The service stores the figure only when all of these hold:

- the bearer is a CI token whose `environment` claim equals `COSTROID_GATEWAY_ENVIRONMENT`;
- the token's repository, run and attempt equal the body's;
- the key is attested as minted for that repository, run and attempt;
- `currency` is `USD`, the only currency the service takes for the count, and `spend` is a decimal of zero or more with at most 12 fractional digits.

A refusal answers `403` with `ATTESTATION_FORBIDDEN` for the first, `403` with `JOIN_KEY_MISMATCH` for the second, `409` with `JOIN_KEY_MISMATCH` for the third, and `400` with `BODY_MALFORMED` for the fourth. A key's spend is the largest figure attested for it, because a gateway's count only grows. A smaller figure later answers `204` and changes nothing.

`gateway_spend` keeps one figure per key, and Postgres refuses a figure for a key that `minted_credentials` does not hold. The service keeps it only after the checks above, so the figure's key was minted for the run the closing job's token names.

LiteLLM writes spend in binary floating point, so its figure can carry more than 12 fractional digits: `0.018000000000000002` for three requests of `0.006`. Close rounds such a figure up to 12 digits, `0.018000000001`, so the figure the service keeps is never below the gateway's. Close waits up to 30 seconds for the figure to stop moving before it reads it, because LiteLLM writes spend to its store in batches.

A row's spend reads `gateway_measured` when it is budget-eligible, its `credential_basis` is `minted`, its `SubAccountId` is a key attested for the row's own repository, run and attempt, and a closing job attested that key's spend. The pull request's figure adds up the attested spend of those keys, each key once.

The gateway counts what passed through it, at its own prices. Spend on a credential that bypassed it is not in the count. In the LiteLLM recipe, the agent's job holds no provider key, only the run's key.

## The verdict

`POST /v1/pulls/{pr}/verdict` runs the policy pack over the pull request's rows in this ledger. The service holds no SCM token, so the caller states what only the repository can tell:

```json
{"config": "<costroid.yml at the base commit, in base64>", "config_ref": "<the base commit>", "workflow_added": "2026-11-01T10:00:00Z", "workflow_not_on_base": false, "measured_pull_requests": 4}
```

An absent `config` is a base commit with no `costroid.yml`. An absent `workflow_added` is a start the caller could not read, and the warn-only period keeps running. `measured_pull_requests` counts the repository's pull requests with a measured cost. At 20 the warn-only period ends, whichever of that and its 7 days comes first, and `warn_only` then reads `ended` with no `ends`. A count can only end the period early, so a caller that overstates it holds its own pull requests to the policy sooner. A CI token asks for its own repository only, and the answer uses the service's clock.

The answer holds `budget`, as the per-PR view has it, and the verdict under `policy`, in the shape [`docs/policy.md`](policy.md) gives:

- `trust` is `token_verified` only when every row of the pull request is.
- `evidence` names what stands behind token-verified spend: `agent_counts`, `gateway_measured` or `reconciled`. Only the last two let a budget rule or `model_allowlist` pass. On `agent_counts`, such a rule that would pass concludes `unproven` with reason `unmeasured_spend`, because the token counts and the model names are the agent's job's own.
- `pr_budget` is held to the larger of the rows' USD list cost and the gateway's count.
- The answer covers the whole pull request, never one run. `run_budget`, `run_cost_threshold`, `run_increase` and `run_increase_percent` conclude `unproven` with reason `no_run_rows`.
- `monthly_budget` concludes `unproven` with reason `no_ledger`, because the pack reads no month to date yet.

Over a limit, the agent's own counts are enough: a rule that fails on them fails here too.

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

| Agent          | Reads                                                                       | Input includes cache | Cache writes              | Recorded |
| -------------- | --------------------------------------------------------------------------- | -------------------- | ------------------------- | -------- |
| GenAI standard | `gen_ai.usage.*`, with `gen_ai.system`, `prompt_tokens` and `completion_tokens` as older names | Yes | `cache_write`, formerly `cache_creation`; lifetime assumed | No |
| Claude Code    | `claude_code.api_request`                                                   | No                   | Two lifetime counts       | Claude Code 2.1.316 |
| Codex          | `gen_ai.usage.*` on spans, with `codex.usage.reasoning_output_tokens`. The `codex.sse_event` log repeats the span, so it is left out | Yes | `cache_write`; lifetime assumed | Codex 0.164.0-alpha.11.1 |
| Copilot CLI    | `gen_ai.usage.*` on `chat` spans                                            | Yes                  | `cache_creation`; lifetime assumed | No |
| Cursor         | `cursor.api.request.*_tokens` on logs                                       | No                   | `cache_creation_tokens`; lifetime assumed | No |
| OpenCode V2    | `gen_ai.usage.*` on spans                                                   | Yes                  | None reported             | No |
| Gemini CLI     | `*_token_count` on `gemini_cli.api_response` logs. The `llm_call` span and the `gen_ai.client.inference.operation.details` log repeat it without cache reads or thoughts, so they are left out | Yes | None reported | No. Built from the source at v0.67.0 |

A version in the last column is a run in [`docs/adapters.md`](adapters.md#recorded-runs) that sent its telemetry to a local sink. The bodies it sent are not kept as test data. `No` means the dialect rests on no run at all.

The Gemini CLI dialect is built from Gemini CLI's telemetry source at tag `v0.67.0`, read on 2026-11-08, and so is its test data. Each file under `internal/otlp/testdata/gemini/` says `from-source` in its name. Every attribute is a whole number of tokens:

| Attribute                    | Set from                                           | Priced as                                                     |
| ---------------------------- | -------------------------------------------------- | ------------------------------------------------------------- |
| `model`                      | The response's `modelVersion`, else the request's model | The model. One the Google sheet does not name takes its fallback tier |
| `input_token_count`          | `promptTokenCount`                                 | `input_uncached`, less the cached tokens                      |
| `cached_content_token_count` | `cachedContentTokenCount`                          | `cache_read`                                                  |
| `output_token_count`         | `candidatesTokenCount`                             | `output`                                                      |
| `thoughts_token_count`       | `thoughtsTokenCount`                               | `output`, and repeated in `reasoning_output`                  |

[`types.ts`](https://github.com/google-gemini/gemini-cli/blob/v0.67.0/packages/core/src/telemetry/types.ts) sets each count, and [`loggingContentGenerator.ts`](https://github.com/google-gemini/gemini-cli/blob/v0.67.0/packages/core/src/core/loggingContentGenerator.ts) names the model. The prompt count holds the cached tokens: Gemini CLI's own statistics take uncached input as the prompt count less the cached count ([`uiTelemetry.ts`](https://github.com/google-gemini/gemini-cli/blob/v0.67.0/packages/core/src/telemetry/uiTelemetry.ts)). They list thoughts apart from output ([`ModelStatsDisplay.tsx`](https://github.com/google-gemini/gemini-cli/blob/v0.67.0/packages/cli/src/ui/components/ModelStatsDisplay.tsx)), and the catalog's Google sheet bills thinking tokens as output, so thoughts are added to `output`. That `candidatesTokenCount` leaves thoughts out is unverified: no run was recorded.

`tool_token_count` is not read, so a tool-use prompt's tokens reach no row. Gemini CLI writes the version of Node.js that runs it as `service.version`, so its rows name no agent version. Every Gemini CLI request is priced on the Google sheet, whose source is the Gemini API's price page, including a request made through Vertex AI.

Where the table says the lifetime is assumed, a row that took those tokens carries `costroid/assumed_ttl`.

A request's rows from OTLP and from the result stream share row ids when both carry the provider's request id, so a run sent both ways counts once.

Telemetry does not say how a run is billed, so an OTLP row is `list_equivalent` unless the wrapper sets `costroid.cost_basis=measured` (or `costroid.cost.basis`, as `costroid emit-otlp` writes it) in `OTEL_RESOURCE_ATTRIBUTES`. `costroid.enforcement_grade` works the same way.

## Tenants

Every table carries row level security with `FORCE`. The service names a tenant with `SET LOCAL` in each transaction, and no statement carries a tenant predicate of its own. The application role can insert rows and read them. It cannot update or delete them. In `gateway_spend` it can also update a key's figure and the time it was attested, and nothing else, so a key's larger figure replaces its smaller one.

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
