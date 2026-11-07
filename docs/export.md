# Export the rows

Costroid records what a run cost as FOCUS rows. This page lists every way those rows leave Costroid today, and names what does not exist yet.

## The rows file

`costroid run --out rows.jsonl` and `costroid ingest-run --out rows.jsonl` write one JSON object per line. Each line is one FOCUS row: one model request, or one model's whole run where the agent reports no more, for one token class.

The columns are the [row contract `focus-row/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.3), in FOCUS 1.4. Every amount is a decimal string, and every row names its currency. The `Tags` column carries the [`costroid/` tags](https://github.com/Costroid/costroid-schemas/tree/main/tags/v0.2) that join the row to its repository, pull request, commit and CI run.

Any tool that reads newline-delimited JSON reads this file. Load amounts as decimals, never as floating point, and never add two currencies into one figure.

## The artifact

In GitHub Actions, the action's `run` and `read` stages keep the file as the artifact `costroid-rows` for 7 days. To fetch it from a finished run:

```sh
gh run download <run-id> --name costroid-rows
```

The GitLab template keeps `costroid-rows.jsonl` as the agent job's artifact, also for 7 days. Beside the rows, both keep the run's result object as `costroid-rows.run.json`, which names why a run wrote no rows.

The agent's job wrote this file, so every row in it is a claim. Its rows read `unverified` and `unattributed` until a ledger checks them against the job's OIDC token.

## A ledger you host

`costroid serve` keeps every run's rows in your own Postgres. The recipes' `ingest` job sends the rows under the job's OIDC token, and the service reprices them from their token counts and takes the run's identity from the token. [`docs/install.md`](install.md) sets it up, and [`docs/serve.md`](serve.md) says what it checks.

The per-pull-request view reads them back:

```sh
curl --header "Authorization: Bearer $TOKEN" \
  "https://costroid.example/v1/pulls/42?repo=github.com/example/app"
```

`$TOKEN` is an ingest key, or a CI job's OIDC token, which reads its own repository only. The answer holds one total per currency and cost basis, the budget's spend from verified rows only, and one line per account, agent, model, token class and grade. Reading the Postgres tables directly is not a documented interface yet, and their shape may change between releases.

## Costroid Platform

`costroid export` sends a rows file to Costroid Platform as one batch, by the integration contract `integration/v0.2`, which `Costroid/costroid-schemas` publishes when Costroid Platform opens to it. The body is the rows as CSV, in the form [`focus-row/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.3) gives it: a header of its 40 columns, a blank cell for null, and `Tags` as its JSON object in one cell. Nothing else in the row changes.

The platform needs an integration token, which a tenant administrator creates there. It needs the `rows:write` scope, and `identity:read` on GitHub Actions. Keep it as a repository secret. `costroid export` reads it from `COSTROID_PLATFORM_TOKEN` and never from an argument.

### From the action

The `ingest` stage sends to the platform when `platform-url` is set, and to a ledger as well when `ingest-url` is. Add three inputs to the recipe's `ingest` step:

```yaml
        with:
          stage: ingest
          platform-url: https://app.costroid.com
          installation-id: github-owner:${{ github.repository_owner_id }}
          platform-token: ${{ secrets.COSTROID_PLATFORM_TOKEN }}
```

`installation-id` names the installation the platform bound: `github-owner:` and the owner's number for a repository that runs only the action, or `github:` and the App installation's id where the GitHub App is installed. The recipes run the `ingest` job only when `COSTROID_INGEST_URL` is set, so to send to the platform alone, change that job's condition to `always() && needs.agent.result != 'skipped'`. The job keeps `id-token: write`.

On GitHub Actions the batch id is the repository id, the run id and the run attempt, joined by hyphens: `903114-16873409921-1`. Sending one run's rows again restates its batch, and the same bytes change nothing. A re-run attempt runs the agent again, so it adds a batch.

The job's OIDC token rides with the batch in `Costroid-Run-Token`, minted for the tenant's rows audience, which `costroid export` reads from `GET /identity`. It names the run and the repository that sent the rows, and the platform refuses a batch whose run belongs to an owner other than the installation's. It says nothing about the counts in the rows, which the agent's job wrote.

### From anywhere else

Outside GitHub Actions, name the batch, and no run token is sent:

```sh
COSTROID_PLATFORM_TOKEN=costroid_it_... costroid export --rows rows.jsonl \
  --platform-url https://app.costroid.com \
  --installation-id gitlab-namespace:4410287 --batch-id nightly-2026-11-07
```

The batch id is 1 to 128 letters, digits, dots, hyphens or underscores, unique within the tenant.

### What the platform answers

`costroid export` prints the code the platform sent and no other part of its answer.

| Answer | `costroid export` | Exit code |
| ------ | ----------------- | --------- |
| 202 | `costroid: batch <id>, <n> rows, sha256 <digest>: accepted for ingest` | 0 |
| 200 | The same line, ending `already held, so nothing changed` | 0 |
| 402 `PLAN_WRITES_REFUSED` | A warning. Nothing in the rows is wrong, and the file is kept to send again | 0 |
| 429 `QUOTA_` and the quota's name | A warning with the seconds in `Retry-After`. The file is kept to send again | 0 |
| Any other refusal | `costroid: Costroid Platform refused batch <id>: <status> <code>` | 1 |

The contract names every refusal code. A batch the platform accepted can still fail on ingest, for example `ROW_IDENTITY_MISMATCH`. The platform states that verdict at `GET /exports/{batch_id}`, after `costroid export` has ended.

In the `ingest` stage, a 402 or a 429 is a warning annotation, and any other failure warns and passes the job, as a ledger outage does. The artifact `costroid-rows` keeps the rows for 7 days.

## What does not exist yet

Costroid has no writer for an S3 or GCS bucket, no Snowflake or BigQuery share, and no connector for any FinOps platform besides Costroid Platform. To reach one today, load the rows file or the ledger's view with that tool's own import.
