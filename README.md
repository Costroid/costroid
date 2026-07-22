# Costroid

**Open-source, self-hostable, [FOCUS](https://focus.finops.org/)-native cost platform (FinOps).**

Costroid ingests cost & usage data from cloud providers (AWS, Azure, Google Cloud), AI/LLM vendors (OpenAI, Anthropic), and any generic FOCUS or CSV export, normalizes everything into a single **FOCUS-conformant** data model, and gives you cost allocation, unit economics, anomaly detection, and a dashboard. It runs **entirely on your own infrastructure**. With optional outbound features unconfigured, the core sends nothing. The natural-language paths, `costroid ask` and `POST /api/v1/query`, are off unless you configure a model endpoint, and you choose that endpoint. When enabled, they send only your question, this machine's current date, the static plan schema, and discovered provider names, tag keys, currency codes, and business-metric names. They never send cost amounts, quantities, or store rows. Prompt and response content from AI sources is still never ingested, stored, logged, cached, or transmitted.

> **FOCUS** = FinOps Open Cost and Usage Specification, an open standard from the FinOps Foundation for representing cloud/SaaS/AI cost & usage in one schema.

---

## Status

**v0.3.0 is released and self-hostable today.** Download a prebuilt binary and run `costroid demo` for an instant dashboard, or point it at your own billing data. Costroid is still **pre-1.0**, so its APIs, schema, and dashboard layout may still change between releases.

What ships in v0.3.0:

- **Seven ingest connectors** — AWS FOCUS (local file, and live from S3 with incremental sync), Azure Cost Management FOCUS (live from Blob Storage, incremental), Google Cloud's FOCUS BigQuery export (Preview, incremental — see the [setup section](#google-cloud-focus-bigquery-setup-preview) below), OpenAI and Anthropic cost & usage, and a generic FOCUS/CSV importer.
- **A six-view dashboard** over the embedded store — overview, costs, tokens, usage, unit economics, and sources — with **cost allocation** (query-time rules), **unit economics** (cost per business metric), and **automatic anomaly detection**.
- **A deterministic insights digest** — plain-language observations computed from your own data, each printed with the evidence behind it so every claim can be recomputed by hand. No model is involved.
- **Signed releases** — keyless-signed checksums, GitHub build-provenance attestations, and a CycloneDX 1.6 source SBOM (see [`SECURITY.md`](./SECURITY.md)).

Newer on `main` (build from source; not yet in a tagged release):

- **Natural-language queries**: use `costroid ask` for a terminal answer or `POST /api/v1/query` for a validated plan on the already-open `serve` store. Both are off unless you configure a model endpoint of your own, and both are described under [Natural-language queries](#natural-language-queries) below.

---

## Why

Cloud, SaaS, and especially AI/token spend are exploding and fragmented across providers, each with its own billing format. Existing platforms are mostly closed SaaS that require exporting sensitive billing data to a third party, and the open-source options are each narrowly scoped — cloud-, MLOps-, or Kubernetes-focused — with none unifying cloud + SaaS + AI in one FOCUS schema. Costroid's goals:

- **One schema for everything** — cloud + SaaS + AI/token spend, all normalized to FOCUS.
- **Self-hostable / data-sovereign** — runs on your own infrastructure with no mandatory external calls.
- **Open** — transparent, auditable, no vendor lock-in.

---

## Architecture (high level)

Costroid is a **Go** backend (single static binary) with the **TypeScript** dashboard embedded in it.

```
Sources ──▶ Ingestion ──▶ FOCUS engine ──▶ Storage ──▶ API ──▶ Dashboard (web)
(cloud/     (per-source   (normalize +     (DuckDB       │
 SaaS/AI)    connectors)   validate)        default)      │
                                                          └──▶ allocation · unit economics · anomaly detection
```

- **Backend (Go):** ingestion, FOCUS engine (schema + version-aware transforms + validation), storage, allocation, unit economics, anomaly detection, and the API. Ships as a single self-contained binary.
- **Frontend (TypeScript/React):** the six-view dashboard, embedded in the binary and consuming the API.
- **Storage:** DuckDB + Parquet embedded by default (zero-ops, local). A ClickHouse scale-out backend behind the storage interface is planned.
- **Natural-language queries:** an optional translator, in the same binary, that turns a question into a validated call to the API above. Off unless you configure a model endpoint of your own.

For the design rules, invariants, and coding conventions, see **[`AGENTS.md`](./AGENTS.md)** — it is the source of truth for anyone (human or agent) working in this repo.

---

## Getting started

### Fastest path — run the demo (about 5 minutes)

1. **Download the binary** for your platform from [GitHub Releases](https://github.com/Costroid/costroid/releases), make it executable (`chmod +x costroid`), and put it on your `PATH` (or run it as `./costroid`). *Optional but recommended:* verify the release before running it — the checksums are keyless-signed and each artifact carries a GitHub build-provenance attestation; the steps are in [`SECURITY.md`](./SECURITY.md). Or install with the script (Linux and macOS), which downloads and verifies the release for you: `curl -fsSL https://raw.githubusercontent.com/Costroid/costroid/main/scripts/install.sh | sh`; see [Getting started](./docs-site/src/content/docs/getting-started.md#install-with-the-script) for options.

2. **Run the demo** — an instant, synthetic, read-only dashboard with no data setup:

   ```bash
   costroid demo
   ```

   Then open <http://localhost:8080>. The demo seeds an isolated synthetic store and serves the real dashboard read-only; it never reads your data directory, credential store, or connectors.

   Prefer a container? Each release publishes a multi-arch, distroless image: `docker run --rm -p 8080:8080 ghcr.io/costroid/costroid:latest` runs the same demo; see [Getting started](./docs-site/src/content/docs/getting-started.md#run-with-a-container).

### Then: your own data

Start the server for local single-user use, then ingest a billing export:

```bash
# Local, single-user: loopback bind with authentication explicitly disabled, so a
# browser can reach the API (a browser can't send a bearer token). For a
# network-exposed deployment, do NOT use --no-auth — set --auth-token-file or
# --auth-trusted-header instead; see docs/security.md.
costroid serve --no-auth
```

Open <http://localhost:8080>. `serve` binds `127.0.0.1:8080` by default and refuses to start until you choose an authentication mode (`--no-auth` is the explicit opt-out above). For a manual load, stop the server (the embedded store allows a single process at a time) and ingest a FOCUS export:

```bash
# a local AWS FOCUS export file
costroid ingest --connector aws-focus --path <your-focus-export.csv.gz>

# live from S3 (ambient AWS credential chain; incremental sync)
costroid ingest --connector aws-focus-s3 --bucket <bucket> --prefix <prefix>/<export-name>

# live from Azure Blob Storage (ambient Azure credential chain; incremental sync)
costroid ingest --connector azure-focus --account-url https://<account>.blob.core.windows.net/ \
  --container <container> --prefix <directory>/<export-name>

# live from Google's FOCUS BigQuery linked export (Preview; incremental sync;
# included in the released binaries since v0.2.0)
costroid ingest --connector gcp-focus-bq --dataset-project <host-project> \
  --dataset gcp_billing_immutable_<BILLING_ACCOUNT_ID>_<LOCATION> \
  --table gcp_billing_export_focus_<BILLING_ACCOUNT_ID> --location <LOCATION>

# any generic FOCUS or CSV export (declare its FOCUS version — no sniffing)
costroid ingest --connector focus-csv --path <export.csv> --focus-version 1.2
```

For unattended refreshes, configure a strict `sources.json` file and run
`costroid serve --sync`. The scheduler runs inside the serving process and
shares its open DuckDB handle, so the dashboard stays available while sources
refresh. Manual `costroid ingest` still requires stopping `serve`. See the
[scheduled-ingestion guide](./docs-site/src/content/docs/guides/operations.md#scheduled-ingestion)
and check `GET /api/v1/sync/status` for the latest result of each source.

The connectors are `aws-focus`, `aws-focus-s3`, `azure-focus`, `gcp-focus-bq`, `anthropic-cost`, `openai-cost`, and `focus-csv`; run `costroid ingest -h` for the full flag reference. For the AI vendors, first store the Admin API key in the encrypted credential store (`costroid credentials set <slot>`), then `costroid ingest --connector openai-cost` (or `anthropic-cost`). Manage stored provider credentials with the `costroid credentials` subcommands (`init`, `set`, `list`, `delete`).

**Cost allocation, unit economics, and anomaly detection** are available in the dashboard and the API — allocation rules are applied at query time (validate a rules file with `costroid allocation validate`), and business metrics for unit economics are loaded with `costroid metrics import`.

#### Google Cloud FOCUS BigQuery setup (Preview)

Google's [FOCUS billing export](https://docs.cloud.google.com/billing/docs/how-to/export-data-bigquery-focus-setup) is **Preview / Pre-GA**, available as-is, and its schema may change. It is a Google-managed read-only linked dataset, not a GA or fully-conformant surface. Enable it early: US/EU multi-regions backfill only to the start of the previous month (catch-up can take five days), single-region datasets have no backfill, and the managed table deletes rows after two years. Local Costroid ingestion is therefore the durable history.

Keep one-time setup authority separate from the connector identity:

- **One-time administrator:** Billing Account Costs Manager or Billing Account Administrator on the billing account, plus Project IAM Admin and BigQuery Admin on the host project. Use this identity only to enable the export in the console; never give these roles to Costroid.
- **Runtime reader:** Costroid recommends dataset-level `roles/bigquery.dataViewer` on the linked dataset plus `roles/bigquery.jobUser` on the project that runs query jobs. This is an inferred minimal grant — Google documents no read-role pair specifically for the FOCUS dataset — so verify it on first use. No billing-account role is expected at runtime.

Use a service-account JSON key through one of the two supported paths:

```bash
# Ambient file path (only this GOOGLE_APPLICATION_CREDENTIALS leg is supported)
export GOOGLE_APPLICATION_CREDENTIALS=/secure/path/costroid-gcp-reader.json

# Or encrypted vault input (stdin only; initialize the vault once)
costroid credentials init
costroid credentials set gcp-focus-bq < /secure/path/costroid-gcp-reader.json
```

An explicit `--credential <slot>` uses that encrypted slot even when the ambient path is set. Without an explicit slot, the ambient file wins; when it is absent, Costroid uses the default `gcp-focus-bq` vault slot. `authorized_user`, metadata-server, well-known-file, and other full-ADC credential types are not supported by this zero-new-dependency connector.

`--location` is mandatory and must match the dataset location on every job call; omitting it commonly makes an EU dataset look missing from the default US location. `--job-project` defaults to the dataset project and is where query-job permission and query billing apply. The connector uses a fixed explicit column list, probes Preview schema additions on every sync, and runs one change-token aggregate plus changed-month queries. BigQuery's on-demand billing minimums apply to those queries (10 MB per query and per referenced table), so expect a small — not zero — BigQuery cost per sync; a typical daily incremental pull costs well under a cent.

Costroid maps `x_Labels` to FOCUS Tags. `x_SystemLabels`, `x_ProjectLabels`, and `x_Tags` are deferred. Credit/CUD detail remains in `x_Credits` and is not folded in by Costroid; stored totals reflect Google's own `BilledCost` and `EffectiveCost` columns verbatim, with no additional credit arithmetic.

### Build from source (developers)

**Prerequisites:** Go (latest stable), Node (LTS) + pnpm, DuckDB. Developed on WSL2 Ubuntu.

```bash
git clone https://github.com/Costroid/costroid.git
cd costroid
pnpm install
make build          # builds the dashboard + single binary at bin/costroid
```

Other top-level commands (see [`AGENTS.md`](./AGENTS.md) → *Working here*):

- `make dev` — run the Go API + Vite dev server together
- `make test` — run all tests (Go + TS)
- `make lint` — linters + format checks (Go + TS)
- `make fmt` — apply formatters
- `make generate` — regenerate Go/TS code from `contracts/openapi.yaml`

Then run `./bin/costroid demo` or `./bin/costroid serve --no-auth` as above. A synthetic sample export lives at `testdata/aws-focus-1.2/sample-export.csv.gz`. Available environment variables are documented in `.env.example` (`.env` is git-ignored).

---

## Natural-language queries

Costroid has three ways to translate a question into a query it already knows
how to answer. When the translator is configured, the dashboard shows an ask
row between the date range and view navigation. It sends the question to
`POST /api/v1/query`, applies the validated plan to the existing dashboard
views, and shows a caption reading the question back to you, so you can see
how it was understood before you trust the number. That caption describes the
question, not the chart: a view falls back to a currency or provider the
selected window actually contains, so the two can differ. It never renders the
plan as JSON. `costroid ask` provides the terminal form,
printing the validated plan and executing it for an answer. The HTTP form
exists because the embedded store is single-writer: while `serve` has the
store open, a separate `costroid ask` process cannot open it, but the endpoint
can reuse the store that `serve` already owns.

```bash
export COSTROID_MODEL_ENDPOINT=http://localhost:11434/v1/chat/completions
export COSTROID_MODEL=<the model your endpoint serves>

costroid ask "what did we spend on AWS last month"

curl -X POST http://localhost:8080/api/v1/query \
  -H 'Content-Type: application/json' \
  -d '{"question":"what did we spend on AWS last month"}'
```

The `curl` call above assumes `serve --no-auth`; against a token-authenticated
server, add `-H "Authorization: Bearer $TOKEN"` as for any other API call.

The HTTP request body accepts only `question`; endpoint, model, and credential
always come from the server's configuration. The plan's `endpoint` names one of
`costs-daily`, `costs-summary`, `anomalies`, `tokens`, `usage` or
`unit-economics`, which map to `/api/v1/costs/daily`, `/api/v1/costs/summary`,
`/api/v1/anomalies`, `/api/v1/usage/tokens/daily`, `/api/v1/usage/metrics/daily`
and `/api/v1/unit-economics/daily`. The caller executes the plan against those
endpoints — the same ones the dashboard uses — so every cost still comes from
those handlers as an exact decimal string.

The ask row is absent when the translator is not configured, including in the
static demo, which has no inference backend. The browser submits the question
as JSON in a POST body. It does not put the question in the URL, browser
history, local or session storage, document title, or console output, and it
keeps no question history.

How it works, and why it is built this way:

- **The model never writes a query.** It translates your question into a
  structured plan naming one existing API endpoint and its parameters. The plan
  is validated against the same vocabulary the API enforces, then executed by
  the code that already serves the dashboard. There is no second query path.
- **The model never sees a number.** It receives your question, a fixed schema,
  today's date so a question like "last month" resolves against a known day,
  and the names it needs to resolve proper nouns: provider names, tag keys,
  currency codes, and business-metric names. No cost amounts, no quantities, no
  rows are sent.
- **It is off until you turn it on.** With no endpoint configured, nothing
  leaves the machine and no outbound connection is made at all. There is no
  default endpoint and no telemetry.
- **Outbound work is bounded.** One server permits at most four model calls in
  flight and rejects excess requests with HTTP 429. If the translator is
  configured, `serve` also refuses an unauthenticated non-loopback bind.
- **You choose the endpoint.** Any OpenAI-compatible endpoint works, including
  one running on your own machine. Redirects are refused, so a configured
  endpoint cannot pass your question on to a host you did not choose.

Answer times vary widely by model and machine, from a few seconds to a couple of
minutes; `COSTROID_MODEL_TIMEOUT` adjusts the bound. Running `costroid ask` with
no argument prints the full configuration — it has no flags, so any argument you
pass is treated as the question.

---

## Security & data sovereignty

Costroid is built to keep your billing data yours (see [`SECURITY.md`](./SECURITY.md) and [`docs/security.md`](docs/security.md)):

- **Self-hosted** — runs on your infrastructure; `serve` binds loopback by default and refuses to start until you choose an authentication mode. Nothing is sent anywhere unless you configure an optional outbound feature yourself.
- **Content-blind** — records cost & usage counts and categorical dimensions only, never your AI prompt or response content.
- **Least-privilege credentials** — stored provider credentials are AES-256-GCM encrypted at rest, entered via stdin, and never logged.
- **Signed releases** — keyless-signed checksums, GitHub build-provenance attestations, and a CycloneDX 1.6 source SBOM.
- **Exact money** — costs are stored and computed as exact decimals, never floating point.

---

## Contributing

Read **[`AGENTS.md`](./AGENTS.md)** first — it defines the invariants, coding standards, and the **verify-before-done** workflow. Keep changes small and single-purpose; use Conventional Commits; never commit secrets.

## License

The core is licensed under the **[Apache License 2.0](./LICENSE)**. Any enterprise modules (if and when added) live in a separate directory under a separate license — see that directory's own `LICENSE`.
