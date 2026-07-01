# AGENTS.md — Costroid

Guidance for AI agents and humans working in this repo. `CLAUDE.md` points here; this is the single source of truth. Read it before making changes.

## How to use this file

This file captures **intent, invariants, and non-obvious domain knowledge** — the things an agent can't infer from the code. It is deliberately *not* a spec: **implementation choices not fixed here (exact file layout, libraries, frameworks, patterns) are yours to make with good judgment.** Keep it lean and current — if an invariant or convention genuinely changes, update it and delete anything stale.

## What Costroid is

Open-source, self-hostable, **FOCUS-native** cost platform (FinOps). It ingests cost & usage data from cloud, SaaS, and AI/LLM sources, normalizes everything into a single **FOCUS-conformant** model, and provides allocation, unit economics, invoice reconciliation, a dashboard, and an optional natural-language (agentic) layer. It runs entirely on the user's own infrastructure.

- Repo: `github.com/Costroid/costroid` · Local: `~/costroid` (WSL2 Ubuntu) · Domain: `costroid.com`
- Greenfield. Built fresh in **Go + TypeScript**.
- FOCUS = FinOps Open Cost and Usage Specification (an open standard from the FinOps Foundation).

## Invariants (non-negotiable)

1. **FOCUS-native.** The internal model mirrors FOCUS; all sources normalize into it and outputs stay FOCUS-conformant. No parallel proprietary schema.
2. **Self-hostable-first.** The core runs fully on the user's own machine with **zero external dependencies by default** (embedded storage). No mandatory phone-home or cloud calls to operate.
3. **Cardinal Rule.** Costroid handles **cost & usage metadata only**. It must **never ingest, store, log, cache, or transmit raw prompt or response content** from any AI source — persist only model identity, token counts, cost, timestamps, and tags. Hard boundary, no exceptions (including "debug" logging).
4. **Least-privilege credentials.** Connectors use the **minimum, read-only** access to a source's cost/usage data (prefer IAM roles / short-lived identity over long-lived keys). Credentials are **encrypted at rest, never logged, and never leave the deployment**. (See decisions.md D17.)
5. **Open-core.** The core is open source and self-contained — no proprietary dependencies. Any enterprise features (SSO, RBAC, audit) are separate, pluggable modules the core does not depend on or require.
6. **Minimal & readable.** Prefer the standard library and small, well-maintained deps. No speculative abstractions or dependencies "just in case." Clarity over cleverness.
7. **Verification-first.** Never report work complete without building, running, and testing it, and showing the output. If you can't verify in the environment, say so and stop — don't guess or fabricate.

## Do NOT

- Reuse, copy, port, or depend on the old Costroid npm/crates.io packages (`0.7`). Those names are **reserved placeholders only** — the name is ours, the code is not. Start clean.
- Use Rust. This is **Go + TypeScript**. (The crates.io name is a placeholder; no Rust crate is published.)
- Commit secrets, credentials, or customer/billing data. Use env vars and a git-ignored `.env`; keep `.env.example` current.
- Couple the open-source core to enterprise-only modules.

## Stack & shape

- **Go** (latest stable) — the backend core: ingestion, FOCUS engine, analytics, allocation, pricing, reconciliation, API. Ships as a single static binary (self-host friendly). Standard `cmd/ internal/ pkg/` layout.
- **TypeScript** (Node LTS, pnpm) — a React-based dashboard and an optional agentic/MCP service. These consume the Go API and hold no separate source of truth.
- **Storage:** **DuckDB + Parquet embedded** is the default (zero-ops, local). **ClickHouse** is an optional scale-out backend behind a storage interface — swappable, never required.
- Keep these concerns cleanly separated: ingestion (per-source connectors behind a stable, documented interface), FOCUS engine (schema + versioned transforms + validation), storage, allocation, pricing / Price Sheet, reconciliation, API, web, agent. **Propose the concrete file layout as you scaffold** — don't over-plan it up front.
- Define shared API/data contracts **once** (e.g. OpenAPI or protobuf) and generate Go + TS types from it. Don't hand-maintain duplicate types across languages.

## FOCUS notes (domain knowledge an agent likely won't have)

- Model the internal schema on the **current stable FOCUS** (1.4 at time of writing); normalize older versions (1.0–1.3) with version-aware transforms.
- Stay **forward-compatible with 1.5** (it adds a **Price Sheet** dataset and **AI model identity + input/output token** columns) — design the pricing catalog and token/usage columns now so 1.5 drops in without a rewrite.
- Use **1.2+ non-monetary unit** support (credits/tokens) for AI cost.
- **Validate** for FOCUS conformance (mirror the open-source FOCUS Validator's rules).
- Native FOCUS emitters to target first: **AWS, Azure, GCP**. Adoption is uneven — write fallback parsers where native FOCUS is missing (especially AI vendors).
- Ingestion is **incremental, idempotent, and correction-aware**: provider bills arrive in pieces and get restated mid-period — use FOCUS 1.4 correction handling to supersede prior data, never double-count. (See decisions.md D16.)
- For **Kubernetes / on-prem allocation**, integrate or adapt **OpenCost** (Go, Apache-2.0) rather than reinventing it.

## Data model

- **Tenancy-aware from day one.** Records carry a tenant/org identifier even though the OSS core runs single-tenant, so multi-tenancy (managed/enterprise) never needs a schema rewrite.
- **Two linked data types.** Alongside FOCUS cost data, accept a **separate business/usage-metrics stream** (requests, active users, tenants served) so unit economics can be computed — complementary to, not part of, the FOCUS cost model. Anticipate **commitment amortization** (FOCUS `EffectiveCost` vs `BilledCost`) and **rule-based allocation** ("virtual tagging").
- **Migrations from v0.1.** Ship **automated, versioned, forward-only schema migrations** — users upgrade their own deployments; migrate their store without data loss.
- Rationale for the above in decisions.md (D15, D18, D19).

## Working here

- **Go:** idiomatic; `gofmt`/`goimports`; a standard linter clean; wrap errors with context; `context.Context` for I/O; table-driven tests.
- **TypeScript:** `strict` on; standard formatter/linter clean; functional components + hooks.
- Small, single-purpose commits/PRs (Conventional Commits: `feat:` / `fix:` / `chore:` / `docs:` / `refactor:` / `test:`). State what changed and paste verification output.
- New source files carry an `SPDX-License-Identifier: Apache-2.0` header (see decisions.md D14).
- Add reproducible top-level commands (build / test / lint / dev) to a Makefile (or Taskfile) as you scaffold — don't rely on ad-hoc one-offs others can't reproduce.
- **Before claiming done:** Go → `go build ./... && go vet ./... && go test ./...` (+ lint); TS → typecheck + lint + test + build. Any change to ingestion/FOCUS logic gets a test against a real sample export in `testdata/`.

## Priorities

Ship the **smallest useful vertical slice first**: AWS FOCUS export → normalized store → one dashboard view. Broaden (Azure/GCP, AI connectors, reconciliation, K8s, agent) only after that path works end-to-end. Keep the pricing / Price Sheet catalog forward-compatible with FOCUS 1.5 throughout. (A detailed roadmap, if wanted, belongs in the README or a dedicated roadmap doc if ever added — not here.)

Environment: WSL2 Ubuntu; Go latest stable, Node LTS, pnpm, DuckDB. Confirm exact versions against your machine.