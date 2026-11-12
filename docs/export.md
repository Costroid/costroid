# Export the rows

Costroid records what a run cost as FOCUS rows. This page lists every way those rows leave Costroid today, and names what does not exist yet. [`docs/otlp.md`](otlp.md) sends them as OpenTelemetry traces.

## The rows file

`costroid run --out rows.jsonl` and `costroid ingest-run --out rows.jsonl` write one JSON object per line. Each line is one FOCUS row: one model request, or one model's whole run where the agent reports no more, for one token class.

The columns are those of [the row contract](#the-row-contract), in FOCUS 1.4. Every amount is a decimal string, and every row names its currency. The `Tags` column carries the `costroid/` tags that join the row to its repository, pull request, commit and CI run.

Any tool that reads newline-delimited JSON reads this file. Load amounts as decimals, never as floating point, and never add two currencies into one figure.

### The row contract

The row and its tags each have a contract, published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas). `costroid run` and `costroid ingest-run` write rows of [`focus-row/v0.5`](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.5) with tags of [`tags/v0.4`](https://github.com/Costroid/costroid-schemas/tree/main/tags/v0.4). `costroid export` sends a batch to Costroid Platform by an integration contract, and each version of that contract names the rows and tags it carries:

| Integration contract | Rows | Tags |
| -------------------- | ---- | ---- |
| `integration/v0.3` | `focus-row/v0.5` | `tags/v0.4` |
| `integration/v0.2` | `focus-row/v0.3` | `tags/v0.2` |

[Costroid Platform](#costroid-platform) says which version `costroid export` sends by.

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

`costroid export` sends a rows file to Costroid Platform as one batch, by the integration contract [`integration/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/integration/v0.3), or by `integration/v0.2` to a platform that states no later version. It reads the version from `GET /identity`, under `v0.3` first, and names it on standard error. The body is the rows as CSV, in the form [the row contract](#the-row-contract) of that version gives it: a header of its 40 columns, a blank cell for null, and `Tags` as its JSON object in one cell. Each row's `costroid/run_identity` is set as [What a row claims](#what-a-row-claims) says. Nothing else in the row changes.

The platform needs an integration token, which a tenant administrator creates there. It needs the `rows:write` scope, and `identity:read` on GitHub Actions. Elsewhere, a token without `identity:read` still sends, by `integration/v0.2`, and `costroid export` warns that it could not read the platform's contract. Keep it as a secret named `COSTROID_PLATFORM_TOKEN`. `costroid export` reads it from `COSTROID_PLATFORM_TOKEN` and never from an argument. [`docs/connect.md`](connect.md) says why one token per owner, with `installations:write` as well, serves both commands.

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
| `harness` | A Harness custom cost CSV, in FOCUS 1.1 columns |
| `cloudability` | A Cloudability FOCUS 1.1 drop: a gzip CSV and its manifest for each billing period, under the directory `--out` names |
| `ternary` | FOCUS 1.2 CSV, one file for each UTC day, under the directory `--out` names |
| `focus-1.5-cr` | FOCUS 1.5's candidate release, with its token and credential columns and its metadata, under the directory `--out` names |

A row's `BilledCost` is 0, because Costroid issues no invoice, and FOCUS 1.4 requires 0 there of anyone who does not invoice the charge. The row's figure is in `EffectiveCost`: the priced figure of a `measured` row, and 0 on a `list_equivalent` row, whose figure is never shown as billed. [The row contract](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.5#which-cost-column-holds-what) lists each cost column by basis. Vantage and Datadog read their cost from `BilledCost`, so both files carry the row's `EffectiveCost` there. Harness reads `EffectiveCost`. The Cloudability, Ternary and FOCUS 1.5 files are whole FOCUS files, so each keeps `BilledCost` at 0 and the figure in `EffectiveCost`, as the row states them.

Every file keeps the row's `Tags`, so the repository, pull request, commit, run and cost basis of each figure travel with it. Datadog's file carries each tag that every row has as a column of its own, as [Datadog](#datadog) says.

### Older FOCUS versions

FOCUS 1.1 and 1.2 have `ProviderName` and `PublisherName`, which FOCUS 1.4 does not, and lack `ServiceProviderName` and `HostProviderName`, which 1.4 has. The `harness`, `cloudability` and `ternary` files read `ProviderName`, and `PublisherName` where the file has it, from the row's `ServiceProviderName`: the provider that sells a model's tokens and, in a row Costroid writes, the one that produces them. `HostProviderName` has no 1.1 or 1.2 column, so the Cloudability and Ternary files carry it as the custom column `x_HostProviderName`, after every FOCUS column, as FOCUS 1.1's column ordering asks of a custom column. Their four `x_Costroid` columns stay as they are. Every other column keeps its name. The column lists are the specification's at the tags [v1.1](https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.1/specification/columns) and [v1.2](https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/tree/v1.2/specification/columns), read 2026-11-12.

A row whose four date columns are not UTC instants to the second, as `2026-10-14T09:31:02Z`, is refused by these files, so no charge moves to another day or period to fit.

Those figures are the same spend the provider bills. If the tool also imports the provider's own bill, do not add the two.

### Vantage

Vantage's [custom providers page](https://docs.vantage.sh/connecting_custom_providers), read 2026-11-08, states: "Vantage supports columns from FOCUS 1.1–1.3." The file holds the 15 columns of that page a row fills, in the page's order: `ChargeCategory`, `ChargePeriodStart`, `BilledCost`, `ServiceName`, `ChargePeriodEnd`, `BillingCurrency`, `ConsumedQuantity`, `ConsumedUnit`, `BillingAccountId`, `SubAccountId`, `RegionId`, `RegionName`, `ServiceCategory`, `ServiceSubcategory` and `Tags`. Each is the row's column of the same name, but `BilledCost`, which is the row's `EffectiveCost`. A row has no `ResourceId` or `ResourceType`.

The file has no `EffectiveCost` column. The page states that when `EffectiveCost` is "provided and different from `BilledCost`, Vantage records the row at `EffectiveCost` and adds a separate `Discount` charge for the `BilledCost − EffectiveCost` difference". The row's two columns as they are would add a discount that no provider gave.

The page states: "Note that there is a maximum of 10,000 rows and a file size of 2MB allowed." It does not say whether 2MB is 2,000,000 or 2,097,152 bytes, so a file stays within 2,000,000. Past either limit the rows go to more than one file, each with the header: `--out costs.csv` writes `costs-1.csv`, `costs-2.csv` and on. Rows that need more than one file are not written to standard output.

Vantage takes a currency other than USD, and the page states: "Check that currency conversion is also enabled." The file keeps each row's currency.

[Send the day's file](#send-the-days-file) uploads it once a day from a scheduled job.

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

[Send the day's file](#send-the-days-file) uploads it once a day from a scheduled job.

### Harness

Harness's [custom cost provider page](https://developer.harness.io/cloud-cost-management/integrations/custom-cost-provider), read 2026-11-12, states: "All uploads must follow the FOCUSv1 specification. The following fields are mandatory." Its link for FOCUSv1 is the FOCUS 1.1 specification. The file holds the page's 17 fields in the page's order: `BillingAccountId`, `BillingAccountName`, `BillingPeriodStart`, `BillingPeriodEnd`, `ChargeCategory`, `ChargePeriodStart`, `ChargePeriodEnd`, `ConsumedQuantity`, `EffectiveCost`, `ProviderName`, `RegionName`, `ResourceId`, `ServiceName`, `SkuId`, `SubAccountId`, `SubAccountName` and `Tags`. Each is the row's column of the same name, but `ProviderName`, which is the row's `ServiceProviderName`. `ResourceId` is null, because a row is no resource.

`BillingCurrency` and `ConsumedUnit` follow them. The page lists neither. FOCUS 1.1 states `EffectiveCost` in `BillingCurrency` and counts `ConsumedQuantity` in `ConsumedUnit`, so each figure keeps its currency and each count its unit: `Tokens`, `Informational Tokens` or `Credits`. The page names no currency rule, so the file keeps each row's currency.

The page states of `BillingPeriodStart`: "Must match invoice month on upload." So each file holds one billing period, and the command's line names it. It also states: "File size limit: 20 MB per upload. Split larger files before uploading." It does not say whether 20 MB is 20,000,000 or 20,971,520 bytes, so a file stays within 20,000,000. Past either bound the rows go to more than one file, each with the header: `--out costs.csv` writes `costs-1.csv`, `costs-2.csv` and on, the earlier billing period first.

The page states "no nulls" for `BillingAccountId`, both billing period columns, `ChargeCategory`, both charge period columns, `EffectiveCost`, `ProviderName` and `ServiceName`, and lists `Usage`, `Purchase`, `Tax`, `Credit` and `Adjustment` as `ChargeCategory`'s allowed values. A row that misses either refuses the file, and the line names it.

The page states that `ProviderName` "Should match the ProviderType used when setting up the integration." Name the source after the provider in the rows' `ServiceProviderName`. The page documents an upload in the console only: "Select the External Cost Data Sources tab. Click + Create New. Fill in the required details and upload your FOCUS CSV file." So no command can upload it from a CI job.

### Cloudability

IBM Cloudability's [FOCUS ingress page](https://www.ibm.com/docs/en/cloudability-commercial/cloudability-essentials/saas?topic=cloudability-connect-custom-data-focus-ingress), read 2026-11-12, states that Cloudability "currently supports" FOCUS 1.0 and 1.1, that a CSV is compressed with gzip, and that each drop lies at:

```text
<Root Directory>/<Optional_Sub_Directories>/<Vendor>/<Report_Period>/<Epoch Time Folder>/<prefix>-Manifest.json
```

The rows are FOCUS 1.4, so the file is their FOCUS 1.1 form, as [Older FOCUS versions](#older-focus-versions) says. `--out` names a directory, and `--root-dir` the bucket or container the manifest names:

```sh
costroid export --rows month.jsonl --format cloudability --out drops --root-dir example-focus-drops
```

For each billing period the rows name, the command writes a folder under `drops`:

| Part of the path | What it is |
| ---------------- | ---------- |
| `Costroid` | The vendor, "A vendor name which would help in identifying the vendor type". Enter `Costroid` as the vendor when you connect the bucket |
| `20261001-20261101` | The billing period, from its first day to the day its end falls on. The page states it "must be in the formats YYYYMMDD-YYYYMM(+1)DD" |
| `1794475800` | The export's time in seconds since 1970, which "will be used to identify the latest drop for month-to-date data" |
| `costroid-cost-and-usage.csv.gz` | The rows, compressed with gzip |
| `costroid-Manifest.json` | The manifest. "The M in Manifest should always be capitalized." |

The manifest holds the page's required fields and two of its optional ones:

```json
{
  "compression": "GZIP",
  "content_type": "CSV",
  "root_dir": "example-focus-drops",
  "all_report_keys": [
    "Costroid/20261001-20261101/1794475800/costroid-cost-and-usage.csv.gz"
  ],
  "updated_at": "2026-11-12T09:30:00Z",
  "focus_version": "1.1"
}
```

`all_report_keys` is "the list of only new absolute file paths excluding the root directory". The page's table reads "CSV : gzip" for `compression`, and its one sample manifest writes the value as `GZIP`, which the manifest follows. `focus_version` "must match the exact FOCUS Spec version". The page states that dates in Parquet "must be stored as ISO-8601 UTC strings" in the form `YYYY-MM-DDTHH:mm:ssZ`, which is the form every row's dates take.

The page states: "All the data that is uploaded to any of the storage services must be the entire month-to-date data." So build each drop from every rows file of the month so far, not from one run's. The artifact `costroid-rows` lasts 7 days, so keep each run's rows file where the month's drop can be built from it. Copy the folder `Costroid` and its contents to the root of the bucket, keeping the paths. The page states no limit on size and no rule on currency, so the file keeps each row's currency.

### Ternary

LiteLLM's [Ternary page](https://docs.litellm.ai/docs/observability/ternary.md), read 2026-11-12, states that a gateway "transforms recent spend into FOCUS 1.2 CSV and uploads one file per UTC day", and that "Each day maps to one file, so re-sending a day replaces it rather than double-counting." `--out` names a directory, and the command writes one FOCUS 1.2 file in it for each UTC day a row's charge starts on, named for the day, such as `2026-10-14.csv`. A charge that runs past midnight is in the day it started.

The file is the rows' FOCUS 1.2 form, as [Older FOCUS versions](#older-focus-versions) says, with `InvoiceId` beside it. FOCUS 1.2 recommends `InvoiceId` and requires it to be null on a charge that no invoice holds, which is every row Costroid writes.

Since a day sent again replaces the day, a day's file must hold every row of that day. Write it once the UTC day is over, from every rows file of the day. LiteLLM's page states that the upload asks Ternary for a URL with `POST {TERNARY_BASE_URL}/external-cost-sources/v1/{TERNARY_CONNECTION_ID}/upload-url` and the body `{"day": "YYYY-MM-DD"}`, and that "Ternary returns a short-lived signed Google Cloud Storage URL for exactly that day's file, plus the headers the upload must carry." It does not name the fields of that answer, so this page gives no upload command.

Ternary's [Bring Your Own Data page](https://docs.ternary.app/docs/focus), read 2026-11-12, reads CSV from a bucket every four hours and states that "Column names must be valid BigQuery identifiers", which every column of the file is. It also states: "Tags or other JSON-heavy columns can cause “response too big” errors during the update lineage step. Leave these columns unmapped." A file loaded that way leaves out `Tags`, and with it the repository, pull request, run and basis of each figure.

### Microsoft FinOps hubs

Costroid writes no Parquet. Microsoft's [FinOps hubs data processing page](https://learn.microsoft.com/en-us/cloud-computing/finops/toolkit/hubs/data-processing), read 2026-11-12, states that data from outside Cost Management goes to the `ingestion` container as Parquet, at:

```text
ingestion/{dataset}/{date-folder-path}/{scope-id-path}/{ingestion-id}__{original-file-name}.parquet
```

For cost rows, `{dataset}` is `Costs`, and `{date-folder-path}` as `{yyyy}/{mm}` keeps "the latest ingestion per month". The page states of `{ingestion-id}`: "This value is used to remove previously ingested data in the same folder path." So each ingestion holds the month's rows so far. Once every file is in place, an empty `manifest.json` in the same folder starts the ingestion. The page states that "support for non-Azure data hasn't been explicitly tested in the latest release", and it names no column type or FOCUS version for such a file. Hubs convert the data they ingest to FOCUS 1.2, which the `ternary` files are.

To try it, write the month's rows as `ternary`, then convert the files to one Parquet file with a tool of your own. With DuckDB, `all_varchar` keeps every amount as the decimal string the row wrote. The command below ran with DuckDB 1.5.5 on 2026-11-12:

```sh
costroid export --rows month.jsonl --format ternary --out days
duckdb -c "COPY (SELECT * FROM read_csv('days/*.csv', all_varchar = true)) TO 'costroid.parquet' (FORMAT parquet)"
```

Place it at `ingestion/Costs/2026/11/costroid/<ingestion-id>__costroid.parquet`, then add the empty `manifest.json` beside it.

### FOCUS 1.5's candidate release

FOCUS 1.5 is a candidate release, published on 2026-11-02 as the tag [v1.5-cr](https://github.com/FinOps-Open-Cost-and-Usage-Spec/FOCUS_Spec/releases/tag/v1.5-cr) of the specification. Its text states: "This version is a candidate release but not an official publication." `--format focus-1.5-cr` writes the rows with the columns the candidate adds. Ratification may change them, and the format is then named `focus-1.5`. Read 2026-11-12, from `FOCUS_spec-v1.5-cr.md` and `model-1.5-cr.json`.

`--out` names a directory, laid out as the candidate's metadata examples lay theirs out:

| File | What it holds |
| ---- | ------------- |
| `data/cost-and-usage.csv` | The rows |
| `data/cost-and-usage-metadata.json` | The schema the rows conform to, by its `SchemaId`, and the data file's bytes and rows |
| `metadata/data_generator.json` | `{"DataGenerator": "Costroid"}` |
| `metadata/dataset_instances.json` | The one dataset instance, `Costroid rows`, of the dataset `CostAndUsage` |
| `metadata/schemas/schema-<SchemaId>.json` | `FocusVersion`, the date the schema was written, and each column's name and type, in the file's order |

The schema declares `FocusVersion` `1.5-cr`, the name the candidate's tag gives it, never `1.5`, so no reader takes the file for the ratified version. Its id is derived from the version and the columns, so the same columns keep the same id.

The file holds the row contract's 36 FOCUS columns, with four the candidate adds, in the candidate's alphabetical order, then the four `x_Costroid` columns:

| Column | From the row |
| ------ | ------------ |
| `SkuPriceDetails` | `ModelId` from `costroid/model`, and the token properties of the row's class in `SkuMeter`, as the next table shows. A row states no model developer, family or version, so the file names none |
| `CredentialId` | The row's `SubAccountId`: the credential a run named with `--credential` or `COSTROID_CREDENTIAL`, or the id of the key `costroid run --gateway` made. Null where `SubAccountId` is null or is the session id, which names no credential |
| `RequesterDetails` | `[{"key":"Credential","value":{}}]` where `CredentialId` is not null, and null where it is. The candidate asks for an entry beside every `CredentialId`. The row states no type, name or email of the credential, so the entry holds none |
| `PrincipalId` | Null on every row: no row states a principal |

| `SkuMeter` | `TokenDirection` | `TokenCacheAction` | `x_CacheWriteLifetime` |
| ---------- | ---------------- | ------------------ | ---------------------- |
| `input_uncached` | `Input` | `Uncached` | |
| `cache_read` | `Input` | `Read` | |
| `cache_write_5m`, `cache_write_30m`, `cache_write_1h`, `cache_write_12h` | `Input` | `Write` | `5m`, `30m`, `1h`, `12h` |
| `output` | `Output` | | |
| `reasoning_output`, `cached_input_reported` | | | |

The candidate gives a cache write no lifetime, and states that "cache retention duration" is not a value of either property. A 5-minute and a 1-hour write are both `Write`, and the custom property `x_CacheWriteLifetime` keeps which it was. The two informational classes count tokens that a billed row already counts, so they carry neither property, and a query that selects `TokenDirection` `Output` counts each token once.

The candidate states that `CredentialId` "MUST NOT contain a *credential* value that authenticates a request (e.g., an API key string, an access token, a password)". A `SubAccountId` that opens with `sk-`, as provider keys do, refuses the file, and the line names the row and never the value. `--credential` refuses the same shape.

### Send the day's file

Two recipes send one UTC day's rows with your own token: [`recipes/github-vantage-upload.yml`](../recipes/github-vantage-upload.yml) to Vantage, and [`recipes/github-datadog-upload.yml`](../recipes/github-datadog-upload.yml) to Datadog. Copy one to `.github/workflows/` on the default branch, beside the agent's recipe. It runs at 04:41 UTC and sends the UTC day before. To send a day the schedule did not send, run it by hand with the input `day`, such as `2026-11-10`. A day that is not over in UTC is refused. No pull request starts either recipe.

The job holds `actions: read` and no other permission. It installs Costroid, gathers the day's rows, writes the platform's file with `costroid export`, and sends it with `curl`. Each secret reaches the send step alone, through its environment. The API token or keys reach `curl` on standard input, so they are never on a command line, and no step prints a secret.

#### The day's rows

A ledger you host keeps every run's rows, and has no export of one day's rows yet. So the rows come from the artifacts. Each agent recipe keeps its run's rows as the artifact `costroid-rows` for 7 days, as [The artifact](#the-artifact) says. The job lists the repository's artifacts of that name, takes each one created on the day, reads `costroid-rows.jsonl` from it, and joins the files into one rows file. Nothing in an artifact runs.

- An artifact belongs to the UTC day it was created on, so each run's rows go out with one day, even when the run crosses midnight.
- An artifact that a fork's pull request kept is left out, because the fork wrote the job that kept it.
- A run that kept no rows file adds nothing.
- An artifact lasts 7 days, so a day more than 6 days ago is refused, and an artifact of the day that has expired stops the job before anything is sent. No day goes out in part.

The agents' jobs wrote these rows, so every figure in the file is the claim [The artifact](#the-artifact) describes.

#### To Vantage

Vantage's [custom providers page](https://docs.vantage.sh/connecting_custom_providers), read 2026-11-12, states: "Use the `token` to upload a CSV file via the `/integrations/{integration_token}/costs.csv` endpoint." Its example posts the file as the form field `csv` to `https://api.vantage.sh/v2`, with the headers `accept: application/json`, `authorization: Bearer <token>` and `content-type: multipart/form-data`. The [API reference for that call](https://docs.vantage.sh/api/integrations/upload-custom-provider-costs), read 2026-11-12, describes `csv` as "CSV file containing custom costs", answers `201` for a file it took, and lists the call under the `write` scope. The recipe sends that request. Any answer but `201` fails the job, and the log prints the `errors` the answer lists.

| Secret | Value |
| ------ | ----- |
| `VANTAGE_API_TOKEN` | An API token with the Write scope. Vantage's [authentication page](https://docs.vantage.sh/api/authentication), read 2026-11-12, names a service token as best for "Shared automation, background jobs, CI pipelines, and long-lived integrations" |
| `VANTAGE_INTEGRATION_TOKEN` | The custom provider's token. The custom providers page states: "On a successful `200` response, a Custom Provider `token` is returned." It is part of the address the job calls |

The day's file is `costroid-<owner>-<repository>-<day>.csv`. Past 10,000 rows or 2,000,000 bytes the export writes more than one file, as [Vantage](#vantage) says, and the job sends each in turn. A refused file stops the job, and the files before it stay sent.

The custom providers page states: "If you upload a file with the same name as an existing import, your new file will overwrite the original file." The reference's example answer names the stored file `costs.csv20240827-15484-pfxe9o`, not the name sent, and neither page says whether an upload through the API keeps its name. So a day sent twice may count twice. Before you send a day again, delete its import. The page states: "If you delete a CSV, the corresponding data will be deleted from the provider."

With the file in hand, from any other job:

```sh
costroid export --rows rows.jsonl --format vantage --out costs.csv
printf 'authorization: Bearer %s\n' "$VANTAGE_API_TOKEN" |
  curl --fail-with-body --silent --show-error --proto '=https' --request POST \
    --header @- --header 'accept: application/json' --header 'content-type: multipart/form-data' \
    --form csv=@costs.csv "https://api.vantage.sh/v2/integrations/$VANTAGE_INTEGRATION_TOKEN/costs.csv"
```

#### To Datadog

Datadog's [Custom Costs page](https://docs.datadoghq.com/cloud_cost_management/setup/custom/), read 2026-11-12, states that you can "upload your CSV and JSON files to Cloud Cost Management on the Custom Costs Files page or programmatically by using the API", and: "To send a file programmatically, use the `PUT api/v2/cost/custom_costs` API endpoint." Its example sends the file as the form field `file` of type `text/json`, with the headers `Content-Type: multipart/form-data`, `DD-API-KEY` and `DD-APPLICATION-KEY`. The [API reference for that call](https://docs.datadoghq.com/api/latest/cloud-cost-management/upload-custom-costs-file/), read 2026-11-12, lists its address for each Datadog site, such as `https://api.datadoghq.eu/api/v2/cost/custom_costs` for `app.datadoghq.eu`, and answers `202` "Accepted". The recipe sends the page's request, with the CSV file under the type the example names. Any answer but `202` fails the job, and the log prints the `errors` the answer lists. The page's examples follow a redirect with `-L`, and the recipe does not, so the keys reach the site's API host alone.

| Kind | Name | Value |
| ---- | ---- | ----- |
| Variable | `DD_SITE` | Your site as the reference writes it, such as `datadoghq.com` or `datadoghq.eu`. The job runs only when it is set, and refuses a site the reference does not list |
| Secret | `DD_API_KEY` | An API key |
| Secret | `DD_APP_KEY` | An application key. The reference states: "This endpoint requires the `cloud_cost_management_write` permission." |

Datadog takes USD alone, so a day with a row in another currency stops the job at `costroid export`, which names the row, and nothing is sent. The page states: "Cost data appears in Datadog after 24 hours." The reference also takes the query string `ai_provider`, "Whether the uploaded file contains the costs of an AI provider", and does not say what it changes, so the recipe leaves it at its default, `false`.

The page states: "Ensure that you don't upload the same file twice, since the cost will appear as doubled in the product." So every figure of a day counts twice when the day is sent twice: by a re-run of the job, by a manual run for a day the schedule sent, or by a second copy of the recipe. When the send step fails after Datadog may have taken the file, as on a timeout, find the day's file on the Custom Costs Files page before you send the day again, and delete it there or with the reference's [Delete Custom Costs file](https://docs.datadoghq.com/api/latest/cloud-cost-management/delete-custom-costs-file/) call.

With the file in hand, from any other job, once:

```sh
costroid export --rows rows.jsonl --format datadog --out costs.csv
printf 'DD-API-KEY: %s\nDD-APPLICATION-KEY: %s\n' "$DD_API_KEY" "$DD_APP_KEY" |
  curl --fail-with-body --silent --show-error --proto '=https' --request PUT \
    --header @- --header 'Content-Type: multipart/form-data' \
    --form 'file=@costs.csv;type=text/json' https://api.datadoghq.com/api/v2/cost/custom_costs
```

#### To Ternary

Ternary's [Bring Your Own Data page](https://docs.ternary.app/docs/focus), which now answers at `https://docs.ternary.app/docs/bring-your-own-data-byod`, read 2026-11-12, documents an import from a bucket or a BigQuery table that you own, and no call that takes a file: "Ternary checks for updates every four hours and ingests changes automatically." It also states: "Full table rebuild occurs every ingestion cycle. There is no incremental ingestion mode." Its recommended limits name 100 files per integration, which one file per UTC day passes after 100 days.

A write to your bucket goes through your cloud's own credentials, which that page does not cover. LiteLLM's page does not name the fields of the upload URL's answer, as [Ternary](#ternary) says. So no recipe sends a Ternary file.

## What does not exist yet

Costroid has no writer for an S3 or GCS bucket, no Snowflake or BigQuery share, no Parquet, and sends to no FinOps platform besides Costroid Platform. For Vantage, Datadog, Harness, Cloudability and Ternary it writes the file their import takes, as [Files for another tool](#files-for-another-tool) shows, and you upload it. To reach any other tool today, load the rows file or a `focus-csv` file with that tool's own import.
