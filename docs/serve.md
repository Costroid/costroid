# The ingest service

`costroid serve` receives a run's token counts, prices them against the pinned catalog and keeps the rows in Postgres. It is the same binary as the CLI.

## Run it

The service needs Postgres 15 or later, where the `public` schema grants no `CREATE` by default. Create two roles first. The owner role runs migrations and owns every table. The application role owns nothing and has `NOBYPASSRLS`. The service refuses to start on a superuser, a `BYPASSRLS` role or a role that owns a relation.

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

`--bind` gives an SCM owner to this instance's tenant. `--gitlab-host` trusts one more GitLab instance. GitHub Actions and gitlab.com are trusted by default.

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

Every path but `/healthz` takes a bearer: a CI OIDC ID token, or a tenant ingest key that starts `costroid_ik_`. An absent or invalid bearer answers one `AUTH_TOKEN_INVALID`, and the answer does not say which.

## What the token decides

Repository, run, attempt, commit, actor and event come from the CI token, never from the payload. A stated repository, run, commit or pull request that disagrees rejects the whole request with `JOIN_KEY_MISMATCH`, and nothing is stored.

The pull request comes from the token's ref: `refs/pull/<n>/merge` on GitHub and `refs/merge-requests/<n>/head` on GitLab. A token that names none, such as one from a `push` run, cannot prove a stated pull request, so the statement is discarded.

| Credential                                                       | `run_identity`   | `attribution_grade` | Counts toward a budget |
| ---------------------------------------------------------------- | ---------------- | ------------------- | ---------------------- |
| CI token that names a pull request, on a row with a session id   | `token_verified` | `exact`             | Yes                    |
| CI token, otherwise                                              | `token_verified` | `windowed`          | Yes                    |
| Ingest key                                                       | `unverified`     | `unattributed`      | Never                  |

A sender may lower an `exact` row to `windowed`. It cannot raise a row. Postgres holds the last line as well: a `CHECK` refuses an unverified row with any other grade, and `budget_eligible` is a generated column.

A budget rule reads `budget.spend` in the per-PR answer. While no eligible row exists, `budget.evidence` is false and `budget.spend` is empty.

## Prices

The service prices every row from its token count. It never reads a cost column, a unit price or a catalog version from a sender, and each one it sees is counted under `client_cost`.

`SkuPriceId` supplies the service tier and the context tier on `POST /v1/rows`, because a row has no other column for them. They are usage facts, trusted as far as token counts are, and reconciliation checks them. An id naming another model or class is ignored. A row does not state its region, so this path prices at `global`.

A provider with no catalog sheet is refused. It is never stored at zero. An unknown model of a known provider falls to the sheet's non-zero fallback tier, and the row says so.

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

A request's rows from OTLP and from the result stream share row ids when both carry the provider's request id, so a run sent both ways counts once.

Telemetry does not say how a run is billed, so an OTLP row is `list_equivalent` unless the wrapper sets `costroid.cost_basis=measured` in `OTEL_RESOURCE_ATTRIBUTES`. `costroid.enforcement_grade` works the same way.

## Tenants

Every table carries row level security with `FORCE`. The service names a tenant with `SET LOCAL` in each transaction, and no statement carries a tenant predicate of its own. The application role can insert rows and read them. It cannot update or delete them.

## Checks

`make check` runs everything with Postgres in Docker, and fails when Docker is missing. It ends with the per-PR view's benchmark on one million rows, which prints its p99 and fails at 200 ms. `make check-short` runs without Docker. `make fuzz` runs each parser's fuzz target for `FUZZTIME`. `make bench-ingest` prints the rows per second of `POST /v1/rows`.
