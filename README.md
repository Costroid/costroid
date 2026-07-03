# Costroid

**Open-source, self-hostable, [FOCUS](https://focus.finops.org/)-native cost platform (FinOps).**

Costroid ingests cost & usage data from cloud providers, SaaS services, and AI/LLM sources, normalizes everything into a single **FOCUS-conformant** data model, and provides cost allocation, unit economics, invoice reconciliation, a dashboard, and an optional natural-language (agentic) query layer. It is designed to run **entirely on your own infrastructure** — no data leaves your environment.

> **FOCUS** = FinOps Open Cost and Usage Specification, an open standard from the FinOps Foundation for representing cloud/SaaS/AI cost & usage in one schema.

---

## Status

🚧 **Early / greenfield.** Actively being built from scratch in Go + TypeScript. Not yet usable. APIs, schema, and layout will change.

Current focus: the first vertical slice — **AWS FOCUS export → normalized store → one dashboard view.**

---

## Why

Cloud, SaaS, and especially AI/token spend are exploding and fragmented across providers, each with its own billing format. Existing platforms are mostly closed SaaS that require exporting sensitive billing data to a third party, and the open-source options are narrow (e.g. Kubernetes-only). Costroid's goals:

- **One schema for everything** — cloud + SaaS + AI/token spend, all normalized to FOCUS.
- **Self-hostable / data-sovereign** — runs on your own infrastructure with no mandatory external calls.
- **Open** — transparent, auditable, no vendor lock-in.

---

## Architecture (high level)

Costroid is a **Go** backend (single static binary) plus a **TypeScript** dashboard and an optional agent service.

```
Sources ──▶ Ingestion ──▶ FOCUS engine ──▶ Storage ──▶ API ──▶ Dashboard (web)
(cloud/     (per-source   (normalize +     (DuckDB       │        Agent / MCP (optional)
 SaaS/AI)    connectors)   validate)        default)      │
                                                          └──▶ allocation · pricing · reconciliation
```

- **Backend (Go):** ingestion, FOCUS engine (schema + version-aware transforms + validation), storage, allocation, pricing/Price Sheet, invoice reconciliation, API.
- **Frontend (TypeScript/React):** dashboard consuming the API.
- **Agent (TypeScript, optional):** natural-language querying over the API via MCP.
- **Storage:** DuckDB + Parquet embedded by default (zero-ops, local); ClickHouse optional for scale-out.

For the design rules, invariants, and coding conventions, see **[`AGENTS.md`](./AGENTS.md)** — it is the source of truth for anyone (human or agent) working in this repo.

---

## Getting started

> 🚧 **Early slices** — the repo builds and runs end to end: ingest an AWS FOCUS 1.2 export (local file, or live from S3 with incremental sync) or an Azure Cost Management FOCUS 1.2-preview export (live from Blob Storage) into the embedded DuckDB store and view daily cost by service in the dashboard. Everything else is still to come.

**Prerequisites:** Go (latest stable), Node (LTS) + pnpm, DuckDB. Developed on WSL2 Ubuntu.

```bash
git clone https://github.com/Costroid/costroid.git
cd costroid
pnpm install
```

Top-level commands (see [`AGENTS.md`](./AGENTS.md) → *Working here*):

- `make dev` — run the Go API + Vite dev server together
- `make test` — run all tests (Go + TS)
- `make build` — build the dashboard + single binary at `bin/costroid`
- `make lint` — linters + format checks (Go + TS)
- `make fmt` — apply formatters
- `make generate` — regenerate Go/TS code from `contracts/openapi.yaml`

After `make build`, run `./bin/costroid serve` and open <http://localhost:8080>. To load data, stop the server (the embedded store allows a single process at a time) and ingest a FOCUS export:

```bash
# a local AWS FOCUS 1.2 export file
./bin/costroid ingest --connector aws-focus --path <your-focus-export.csv.gz>

# live from S3 (ambient AWS credential chain; incremental sync)
./bin/costroid ingest --connector aws-focus-s3 --bucket <bucket> --prefix <prefix>/<export-name>

# live from Azure Blob Storage (ambient Azure credential chain; incremental sync)
./bin/costroid ingest --connector azure-focus --account-url https://<account>.blob.core.windows.net/ \
  --container <container> --prefix <directory>/<export-name>
```

Run `./bin/costroid ingest -h` for the full flag reference. A synthetic sample lives at `testdata/aws-focus-1.2/sample-export.csv.gz`. Available environment variables are documented in `.env.example` (`.env` is git-ignored).

---

## Contributing

Read **[`AGENTS.md`](./AGENTS.md)** first — it defines the invariants, coding standards, and the **verify-before-done** workflow. Keep changes small and single-purpose; use Conventional Commits; never commit secrets.

## License

The core is licensed under the **[Apache License 2.0](./LICENSE)**. Any enterprise modules (if and when added) live in a separate directory under a separate license — see that directory's own `LICENSE`.