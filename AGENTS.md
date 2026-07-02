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

- **Go** (latest stable) — the backend core: ingestion, FOCUS engine, analytics, allocation, pricing, reconciliation, API. Ships as a single self-contained binary (self-host friendly; see decisions.md D22). Standard `cmd/ internal/ pkg/` layout.
- **TypeScript** (Node LTS, pnpm) — a React-based dashboard and an optional agentic/MCP service. These consume the Go API and hold no separate source of truth.
- **Storage:** **DuckDB + Parquet embedded** is the default (zero-ops, local). **ClickHouse** is an optional scale-out backend behind a storage interface — swappable, never required.
- Keep these concerns cleanly separated: ingestion (per-source connectors behind a stable, documented interface), FOCUS engine (schema + versioned transforms + validation), storage, allocation, pricing / Price Sheet, reconciliation, API, web, agent. **Propose the concrete file layout as you scaffold** — don't over-plan it up front.
- Define shared API/data contracts **once** (e.g. OpenAPI or protobuf) and generate Go + TS types from it. Don't hand-maintain duplicate types across languages.

## FOCUS notes (domain knowledge an agent likely won't have)

- Model the internal schema on the **current stable FOCUS** (1.4, ratified June 2026); normalize older versions with version-aware transforms. The spec ships **semiannually (June/December)**: 1.2 (May 2025: SaaS/PaaS scope, multi-currency, virtual-currency/token units), 1.3 (Dec 2025: Contract Commitment dataset, split/shared-cost allocation columns), 1.4 (June 2026: **Invoice Detail + Billing Period datasets** — the standardized basis for invoice reconciliation). Expect schema churn; treat transforms as versioned migrations, not one-off mappings.
- Stay **forward-compatible with 1.5** (expected Dec 2026: SKU/Price Sheet dataset and **AI model identity + token** columns — scope not yet ratified and may shift; the high-cardinality AI-data question, OpenTelemetry vs a new dataset, is an open working-group debate). Design the pricing catalog and token/usage columns now so 1.5 drops in without a rewrite.
- Use **1.2+ non-monetary unit** support (credits/tokens) for AI cost.
- **Validate** for FOCUS conformance (mirror the open-source FOCUS Validator's rules).
- **Real provider exports are version-skewed and imperfect** (mid-2026: providers emit 1.0–1.3; Azure export fields unpopulated in preview, GCP export pre-GA, `x_` extension columns everywhere; no provider is fully conformant). Target **AWS, Azure, GCP** first, but normalization must both up-convert older versions and gap-fill — this conversion layer is the technical moat, not plumbing.
- **Don't wait for SaaS/AI vendors to emit FOCUS natively** — OpenAI/Anthropic expose raw usage/cost APIs only, with no sign of change. Own the conversion. The FinOps Foundation's `focus_converters` repo is abandoned (last pushed Aug 2024): reference material only, never a dependency.
- Ingestion is **incremental, idempotent, and correction-aware**: provider bills arrive in pieces and get restated mid-period — use FOCUS 1.4 correction handling to supersede prior data, never double-count. The most-reported reconciliation traps besides corrections: refund **billing-period placement** and `EffectiveCost` **amortization semantics**. (See decisions.md D16.)
- For **Kubernetes / on-prem allocation**, integrate or adapt **OpenCost** (Go, Apache-2.0) rather than reinventing it.
- Market/positioning context, competitor watchlist, and revisit triggers: `docs/strategy.md`.

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

## Environment & tooling

Dev machine: WSL2 Ubuntu (x86_64), repo at `~/costroid`. Go latest stable, Node LTS,
pnpm, DuckDB — confirm exact versions against the machine and include them in reports
(`go version && node --version && pnpm --version`).

- **Agents have no sudo.** Never run `sudo`/`apt`. If a tool is missing, install it
  user-locally (tarball, or `apt-get download` + `dpkg -x`, into `~/.local/<tool>`;
  symlink the binary into `~/.local/bin`, which is on PATH) and declare it in your
  report. If root is genuinely unavoidable, print the exact command for the human
  (who can sudo interactively) and stop that step — don't fake it.
- The Go/Node/pnpm toolchain is user-local under `~/.local`; `make`/`gcc`/`git`/`gh`
  are system-installed. Don't reinstall or upgrade any of them mid-task.
- **Repo-pinned tools — never install globally:** golangci-lint (`make lint`
  bootstraps the pinned version into `./bin`), oapi-codegen (go.mod `tool` directive →
  `go tool oapi-codegen`), pnpm itself (`packageManager` in package.json). Run
  `pnpm install` once at the repo root before web targets.
- Verification scripts that start dev servers: `setsid cmd &` puts the child in a new
  session whose process group is **not** `$!` — kill the child's PGID
  (`ps -o pgid= -p <child-pid>`) or `pkill` the specific processes, then confirm the
  ports actually closed.