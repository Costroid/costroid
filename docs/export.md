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

The [integration contract `integration/v0.1`](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.1) fixes how rows reach Costroid Platform: `PUT /exports/{batch_id}` takes one CSV of FOCUS rows per batch, and a retry with the same bytes changes nothing. Costroid does not send to it yet.

## What does not exist yet

Costroid has no writer for an S3 or GCS bucket, no Snowflake or BigQuery share, and no connector for any FinOps platform. To reach one today, load the rows file or the ledger's view with that tool's own import.
