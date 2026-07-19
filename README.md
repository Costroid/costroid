# Costroid

**Open-source, self-hostable, [FOCUS](https://focus.finops.org/)-native cost platform (FinOps).**

Costroid ingests cost & usage data from cloud providers (AWS, Azure, Google Cloud), AI/LLM vendors (OpenAI, Anthropic), and any generic FOCUS or CSV export, normalizes everything into a single **FOCUS-conformant** data model, and gives you cost allocation, unit economics, anomaly detection, and a dashboard. It runs **entirely on your own infrastructure** — no data leaves your environment. (A natural-language / agentic query layer is planned, not yet shipped.)

> **FOCUS** = FinOps Open Cost and Usage Specification, an open standard from the FinOps Foundation for representing cloud/SaaS/AI cost & usage in one schema.

---

## Status

**v0.1.0 is released and self-hostable today.** Download a prebuilt binary and run `costroid demo` for an instant dashboard, or point it at your own billing data. Costroid is still **pre-1.0**, so its APIs, schema, and dashboard layout may still change between releases.

What ships in v0.1.0:

- **Six ingest connectors** — AWS FOCUS (local file, and live from S3 with incremental sync), Azure Cost Management FOCUS (live from Blob Storage, incremental), OpenAI and Anthropic cost & usage, and a generic FOCUS/CSV importer.
- **A four-view dashboard** over the embedded store, with **cost allocation** (query-time rules), **unit economics** (cost per business metric), and **automatic anomaly detection**.
- **Signed releases** — keyless-signed checksums, GitHub build-provenance attestations, and a CycloneDX 1.6 source SBOM (see [`SECURITY.md`](./SECURITY.md)).

Newer on `main` (build from source; ships in the next release):

- A seventh connector: **Google Cloud's FOCUS BigQuery export** (Preview, incremental sync) — see the [setup section](#google-cloud-focus-bigquery-setup-preview) below.

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
- **Frontend (TypeScript/React):** the four-view dashboard, embedded in the binary and consuming the API.
- **Storage:** DuckDB + Parquet embedded by default (zero-ops, local). A ClickHouse scale-out backend behind the storage interface is planned.
- **Agent (planned):** an optional natural-language / MCP query layer over the API — not yet shipped.

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
# on `main` only — not in the v0.1.0 binaries)
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

The connectors are `aws-focus`, `aws-focus-s3`, `azure-focus`, `gcp-focus-bq` (on `main` since v0.1.0), `anthropic-cost`, `openai-cost`, and `focus-csv`; run `costroid ingest -h` for the full flag reference. For the AI vendors, first store the Admin API key in the encrypted credential store (`costroid credentials set <slot>`), then `costroid ingest --connector openai-cost` (or `anthropic-cost`). Manage stored provider credentials with the `costroid credentials` subcommands (`init`, `set`, `list`, `delete`).

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

## Security & data sovereignty

Costroid is built to keep your billing data yours (see [`SECURITY.md`](./SECURITY.md) and [`docs/security.md`](docs/security.md)):

- **Self-hosted** — runs entirely on your infrastructure; `serve` binds loopback by default and refuses to start until you choose an authentication mode.
- **Content-blind** — records cost & usage counts and categorical dimensions only, never your AI prompt or response content.
- **Least-privilege credentials** — stored provider credentials are AES-256-GCM encrypted at rest, entered via stdin, and never logged.
- **Signed releases** — keyless-signed checksums, GitHub build-provenance attestations, and a CycloneDX 1.6 source SBOM.
- **Exact money** — costs are stored and computed as exact decimals, never floating point.

---

## Contributing

Read **[`AGENTS.md`](./AGENTS.md)** first — it defines the invariants, coding standards, and the **verify-before-done** workflow. Keep changes small and single-purpose; use Conventional Commits; never commit secrets.

## License

The core is licensed under the **[Apache License 2.0](./LICENSE)**. Any enterprise modules (if and when added) live in a separate directory under a separate license — see that directory's own `LICENSE`.
