# Export the rows

Costroid records what a run cost as FOCUS rows. This page lists every way those rows leave Costroid today, and names what does not exist yet. [`docs/otlp.md`](otlp.md) sends them as OpenTelemetry traces.

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

`costroid serve` keeps every run's rows in your own Postgres. The recipes' `ingest` job sends the rows under the job's OIDC token, and the service reprices them from their token counts and takes the run's identity from the token. [`docs/ledger.md`](ledger.md) sets it up, and [`docs/serve.md`](serve.md) says what it checks.

The per-pull-request view reads them back:

```sh
curl --header "Authorization: Bearer $TOKEN" \
  "https://costroid.example/v1/pulls/42?repo=github.com/example/app"
```

`$TOKEN` is an ingest key, or a CI job's OIDC token, which reads its own repository only. The answer holds one total per currency and cost basis, the budget's spend from verified rows only, and one line per account, agent, model, token class and grade. Reading the Postgres tables directly is not a documented interface yet, and their shape may change between releases.

## Costroid Platform

`costroid export` sends a rows file to Costroid Platform as one batch, by the integration contract [`integration/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.3), or by `integration/v0.2` to a platform that states no later version. It reads the version from `GET /identity`, under `v0.3` first, and names it on standard error. The body is the rows as CSV, in the form [`focus-row/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.3) gives it: a header of its 40 columns, a blank cell for null, and `Tags` as its JSON object in one cell. Each row's `costroid/run_identity` is set as [What a row claims](#what-a-row-claims) says. Nothing else in the row changes.

The platform needs an integration token, which a tenant administrator creates there. It needs the `rows:write` and `identity:read` scopes. Keep it as a secret named `COSTROID_PLATFORM_TOKEN`. `costroid export` reads it from `COSTROID_PLATFORM_TOKEN` and never from an argument. [`docs/connect.md`](connect.md) says why one token per owner, with `installations:write` as well, serves both commands.

The platform takes rows only under an installation its tenant has bound, so bind the owner first, as [Bind the owner](#bind-the-owner) shows.

### From the action

Every GitHub recipe that runs an agent has an `ingest` job. It runs when the repository variable `COSTROID_INGEST_URL` or `COSTROID_PLATFORM_URL` is set, and sends to each one that is. To send to the platform, set two things on the repository or its organization:

```sh
gh variable set COSTROID_PLATFORM_URL --body https://app.costroid.com
gh secret set COSTROID_PLATFORM_TOKEN
```

The job passes them to the action's `ingest` stage, with the owner's installation:

```yaml
        with:
          stage: ingest
          ingest-url: ${{ vars.COSTROID_INGEST_URL }}
          ingest-audience: ${{ vars.COSTROID_INGEST_AUDIENCE }}
          platform-url: ${{ vars.COSTROID_PLATFORM_URL }}
          installation-id: github-owner:${{ github.repository_owner_id }}
          platform-token: ${{ secrets.COSTROID_PLATFORM_TOKEN }}
```

`installation-id` names the installation the platform bound: `github-owner:` and the owner's number for a repository that runs only the action, or `github:` and the App installation's id where the GitHub App is installed. GitHub states that `github.repository_owner_id` "is different from the owner's name" ([contexts reference](https://docs.github.com/en/actions/reference/workflows-and-actions/contexts), read 2026-11-08), so a rename does not change it. A bare number is an App installation, and is sent as `github:<id>`. `costroid report --installation-id` takes an App installation in the same two forms and refuses any other, and its account link names it as `github:<id>`. The job keeps `id-token: write`.

`costroid lint-recipes` holds an `ingest` step that sets `platform-url` to the rest: `installation-id` of a GitHub kind, `platform-token` as one secret and never the token itself, and a job `if` that names the platform's variable beside any other it tests, so the job runs when only the platform is named. Each break is an `ingest-platform` finding.

On GitHub Actions the batch id is the repository id, the run id and the run attempt, joined by hyphens: `903114-16873409921-1`. Sending one run's rows again restates its batch, and the same bytes change nothing. A re-run attempt runs the agent again, so it adds a batch.

The job's OIDC token rides with the batch in `Costroid-Run-Token`, minted for the tenant's rows audience, which `costroid export` reads from `GET /identity`. It names the run and the repository that sent the rows, and the platform refuses a batch whose run belongs to an owner other than the installation's. It says nothing about the counts in the rows, which the agent's job wrote.

### Bind the owner

[`recipes/github-connect.yml`](../recipes/github-connect.yml) binds the repository's owner to the token's tenant. Copy it to `.github/workflows/costroid-connect.yml` on the default branch. It runs `costroid connect` once a day and on each push to the default branch, when `COSTROID_PLATFORM_URL` is set, in a job that holds `id-token: write` and no other permission. The token needs `installations:write` for it.

GitHub's [events reference](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows) states, read 2026-11-08: "Scheduled workflows will only run on the default branch." It also states: "In a public repository, scheduled workflows are automatically disabled when no repository activity has occurred in 60 days." A binding stays current for 7 days after its last proof, so in a public repository that sits idle, enable the workflow again before its binding goes stale. A push to the default branch binds at once only where that branch is protected. [`docs/connect.md`](connect.md) lists what the platform takes as a proof.

On GitLab CI, the template's `costroid:connect` job does the same in a scheduled pipeline, or on a push to the default branch. Set three CI/CD variables:

| Variable | Value |
| -------- | ----- |
| `COSTROID_PLATFORM_URL` | `https://app.costroid.com` |
| `COSTROID_PLATFORM_AUDIENCE` | `https://app.costroid.com/tenants/<tenant id>`. Run `costroid connect --platform-url https://app.costroid.com --installation-id gitlab-namespace:<namespace id>` once on your own machine, with the token set, and its line names the tenant |
| `COSTROID_PLATFORM_TOKEN` | The integration token, masked and scoped to the environment `costroid-connect`, so no agent's job can read it |

The job takes its proof through `id_tokens`, as `COSTROID_PLATFORM_PROOF`, with that audience. It runs in `.post`, and GitLab's [CI/CD YAML reference](https://docs.gitlab.com/ci/yaml/) states, read 2026-11-08: "If a pipeline contains only jobs in the `.pre` or `.post` stages, it does not run." So the scheduled pipeline needs one job of yours in another stage. Add a daily pipeline schedule on the default branch, and keep that branch protected.

### From anywhere else

Outside GitHub Actions, name the batch, and no run token is sent:

```sh
COSTROID_PLATFORM_TOKEN=costroid_it_... costroid export --rows rows.jsonl \
  --platform-url https://app.costroid.com \
  --installation-id gitlab-namespace:4410287 --batch-id nightly-2026-11-07
```

The batch id is 1 to 128 letters, digits, dots, hyphens or underscores, unique within the tenant.

`--platform-url` is an `https` origin. Plain `http` is taken only on a loopback address, such as `http://127.0.0.1:8787` or `http://[::1]:8787`, so a platform on the same machine can be reached for a test. The name `localhost` is refused, because a resolver can answer it with another address.

### What a row claims

The contract lets the sender say which rows the run token stands behind: a row that reads `token_verified` must name the token's run and repository, or the whole batch fails ([`integration/v0.2`, The run token](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#the-run-token)). `costroid export` decides each row's claim itself, whatever the file says:

| The row | Sent as |
| ------- | ------- |
| On GitHub Actions, its `costroid/workflow_run_id` is `GITHUB_RUN_ID`, its `costroid/repo` is `github.com/` and `GITHUB_REPOSITORY`, and its `costroid/attribution_grade` is not `windowed` | `token_verified` |
| Any other row, a fork's rows sent from the follow-up's own run among them | `unverified`, and `unattributed`, because the tag set allows no other grade beside it |
| Every row sent outside GitHub Actions, where no run token goes | `unverified` and `unattributed` |

The command prints how many rows went as `token_verified`. A `token_verified` row proves which run sent it. It proves nothing about its counts, as [What a row proves](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#what-a-row-proves) says.

### What the platform answers

`costroid export` prints the code the platform sent and no other part of its answer.

| Answer | `costroid export` | Exit code |
| ------ | ----------------- | --------- |
| 202 | `costroid: batch <id>, <n> rows, sha256 <digest>: accepted for ingest`, then the verdict below | |
| 200 | The same line, ending `already held, so nothing changed`, then the verdict below | |
| 402 `PLAN_WRITES_REFUSED` | A warning. Nothing in the rows is wrong, and the file is kept to send again | 0 |
| 409 `INSTALLATION_NOT_BOUND` | A warning that names `costroid connect`. The tenant holds no binding of the installation, or it was revoked or displaced. The file is kept to send again | 0 |
| 429 `QUOTA_` and the quota's name | A warning with the seconds in `Retry-After`. The file is kept to send again | 0 |
| Any other refusal | `costroid: Costroid Platform refused batch <id>: <status> <code>` | 1 |

A batch the platform took can still fail as it ingests. So `costroid export` asks `GET /exports/{batch_id}` for the verdict every 2 s, for up to 60 s:

| Verdict | `costroid export` | Exit code |
| ------- | ----------------- | --------- |
| `ingested` | `costroid: batch <id> ingested, <n> rows`, then one line for each pull request the rows name, with its page on the platform | 0 |
| `failed` | `costroid: batch <id> failed on ingest: <code>`. The contract's [failed batches](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.2#failed-batches) say what each code means | 1 |
| None within 60 s, or the request refused | A warning that the verdict is at `GET /exports/{batch_id}` | 0 |

In the `ingest` stage, each warning becomes a warning annotation, and any failure warns and passes the job, as a ledger outage does. The artifact `costroid-rows` keeps the rows for 7 days.

## Files for another tool

`costroid export --format` writes the rows file as a file another cost tool imports, and sends nothing. It writes to `--out`, or to standard output when `--out` is absent:

```sh
costroid export --rows rows.jsonl --format vantage --out costs.csv
```

Every amount is written as the row states it. None is converted, rounded or added up, and each row keeps its own currency.

| `--format` | The file |
| ---------- | -------- |
| `focus-csv` | FOCUS 1.4 CSV: the row contract's 40 columns, the same CSV Costroid Platform takes. 36 are FOCUS 1.4 columns, and 4 are Costroid's own, each named with the `x_` prefix FOCUS requires of a custom column |
| `vantage` | A Vantage custom provider's CSV |
| `datadog` | A Datadog Custom Costs CSV |

A row's `BilledCost` is 0, because Costroid issues no invoice, and FOCUS 1.4 requires 0 there of anyone who does not invoice the charge. The row's figure is in `EffectiveCost`: the priced figure of a `measured` row, and 0 on a `list_equivalent` row, whose figure is never shown as billed. [`focus-row/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.3#which-cost-column-holds-what) lists each cost column by basis. Vantage and Datadog read their cost from `BilledCost`, so both files carry the row's `EffectiveCost` there.

Those figures are the same spend the provider bills. If the tool also imports the provider's own bill, do not add the two.

### Vantage

Vantage's [custom providers page](https://docs.vantage.sh/connecting_custom_providers), read 2026-11-08, states: "Vantage supports columns from FOCUS 1.1–1.3." The file holds the 15 columns of that page a row fills, in the page's order: `ChargeCategory`, `ChargePeriodStart`, `BilledCost`, `ServiceName`, `ChargePeriodEnd`, `BillingCurrency`, `ConsumedQuantity`, `ConsumedUnit`, `BillingAccountId`, `SubAccountId`, `RegionId`, `RegionName`, `ServiceCategory`, `ServiceSubcategory` and `Tags`. Each is the row's column of the same name, but `BilledCost`, which is the row's `EffectiveCost`. A row has no `ResourceId` or `ResourceType`.

The file has no `EffectiveCost` column. The page states that when `EffectiveCost` is "provided and different from `BilledCost`, Vantage records the row at `EffectiveCost` and adds a separate `Discount` charge for the `BilledCost − EffectiveCost` difference". The row's two columns as they are would add a discount that no provider gave.

The page states: "Note that there is a maximum of 10,000 rows and a file size of 2MB allowed." It does not say whether 2MB is 2,000,000 or 2,097,152 bytes, so a file stays within 2,000,000. Past either limit the rows go to more than one file, each with the header: `--out costs.csv` writes `costs-1.csv`, `costs-2.csv` and on. Rows that need more than one file are not written to standard output.

Vantage takes a currency other than USD, and the page states: "Check that currency conversion is also enabled." The file keeps each row's currency.

### Datadog

Datadog's [Custom Costs page](https://docs.datadoghq.com/cloud_cost_management/setup/custom/), read 2026-11-08, names six required columns and states: "The required fields must appear as columns in your CSV in the order listed above." The file holds them in that order:

| Column | From the row |
| ------ | ------------ |
| `ProviderName` | `ServiceProviderName`, the column that replaced `ProviderName` in FOCUS 1.3 |
| `ChargeDescription` | `ChargeDescription` |
| `ChargePeriodStart` | The UTC day of `ChargePeriodStart`, as `YYYY-MM-DD` |
| `ChargePeriodEnd` | The UTC day of the charge's last instant. A row's `ChargePeriodEnd` is exclusive, and Datadog's is the "Last day of a charge (inclusive)", so a charge that ends at midnight ends the day before |
| `BilledCost` | `EffectiveCost` |
| `BillingCurrency` | `BillingCurrency` |

The page states of `BillingCurrency`: "Must be USD." A rows file with a row in another currency is refused whole. The line names each such row by its place and its `x_CostroidRowId`, and no file is written. Write `focus-csv` or `vantage` for those rows instead.

"For a CSV file, add a column per tag." Each tag key becomes a column after the six, named as the key, such as `costroid/repo`. The page also states: "NULL or blank ("") parameter values are not accepted." So a tag gets a column only when every row carries it with a value, and the command names the tags it left out. `costroid/cost_basis` is on every row Costroid writes, so each figure keeps its basis. A value past the page's 1,000 characters, or blank in a required column, refuses the file and names the row.

## What does not exist yet

Costroid has no writer for an S3 or GCS bucket, no Snowflake or BigQuery share, and sends to no FinOps platform besides Costroid Platform. For Vantage and Datadog it writes the file their import takes, as [Files for another tool](#files-for-another-tool) shows. To reach any other tool today, load the rows file or a `focus-csv` file with that tool's own import.
