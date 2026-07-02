# Decisions

A running log of technical and architectural decisions for Costroid, with the reasoning behind each. Its purpose: so anyone — human or coding agent — can understand *why* the project is built the way it is, and not re-litigate or silently reverse settled choices.

**How to use this log**
- It is **append-only**. Add new decisions at the bottom; don't rewrite history.
- If a decision changes, add a **new** entry that supersedes the old one and mark the old one `Superseded by Dxx` — don't delete it.
- Keep entries short: the decision, the why, and (where useful) what was rejected.
- `Status: Accepted` = in effect. `Status: Open` = not yet decided.

*Seeded 2026-07-01 from the project's founding discussion.*

---

## D1 — Start from scratch; do not reuse the old Costroid code
**Status:** Accepted
**Decision:** This is a new codebase. The previous Costroid (a Rust local-inference cost tracker, last published as `0.7` on npm and crates.io) is **not** reused in any form. Those package names are kept only to reserve the namespace.
**Why:** The product is a different thing — a FOCUS-native platform for cloud + SaaS + AI/token cost — with a different architecture and stack. Carrying old code would constrain the new design for no benefit.

## D2 — Language stack: Go + TypeScript (no Rust)
**Status:** Accepted
**Decision:** Backend core in **Go**; dashboard and optional agent/MCP service in **TypeScript**. No Rust.
**Why:** Go compiles to a single static binary (ideal for self-hosting), performs well for ingestion/transform work, and matches OpenCost (also Go — see D6). TypeScript has the strongest ecosystem for the web dashboard and for LLM/MCP tooling. Rust (the previous project's language) offered no advantage here that outweighs the distribution and ecosystem benefits of Go + TS.

## D3 — FOCUS-native internal data model
**Status:** Accepted
**Decision:** The internal schema mirrors FOCUS. Every source normalizes *into* FOCUS on ingestion; queries, allocation, and reconciliation operate on FOCUS; exports stay FOCUS-conformant. There is no parallel proprietary schema that FOCUS is translated to/from.
**Why:** FOCUS is the emerging open standard for cost & usage. Being native — rather than translating at the edges, as most incumbents do — lowers transformation overhead, keeps outputs standards-compliant, and lets users query the same facts in their own warehouse with standard SQL. It is also the core technical differentiator.

## D4 — FOCUS version strategy: build on 1.4, normalize older, stay forward-compatible with 1.5
**Status:** Accepted
**Decision:** Model the internal schema on **FOCUS 1.4** (current stable). Normalize 1.0–1.3 exports into it with version-aware transforms. Use **1.2+ non-monetary unit (credit/token) support** for AI cost now. Design the pricing catalog and token/usage columns so **FOCUS 1.5** — which adds a *Price Sheet* dataset and *AI model identity + input/output token* columns — drops in without a rewrite.
**Why:** 1.2 already supports tokens/credits, so AI cost can be built today. 1.5 is on the roadmap but not shipped; designing *for* it without *depending on* it avoids both a premature dependency and a future rewrite.

## D5 — Storage: DuckDB + Parquet embedded by default; ClickHouse optional, behind an interface
**Status:** Accepted
**Decision:** Default storage is **DuckDB + Parquet**, embedded and local. **ClickHouse** is an optional scale-out backend, accessed through a storage abstraction so it is swappable and never required.
**Why:** DuckDB/Parquet gives zero-ops embedded analytics — Parquet can be queried directly with no separate server, which is exactly right for the self-host default. ClickHouse covers multi-user/always-on deployments. The abstraction keeps the embedded path first-class and the scale-out path optional.

## D6 — Integrate/adapt OpenCost for Kubernetes & on-prem allocation
**Status:** Accepted
**Decision:** For Kubernetes / on-prem cost allocation, integrate or adapt **OpenCost** rather than building an allocation engine from scratch. Keep it behind the allocation boundary.
**Why:** OpenCost is Go, Apache-2.0, CNCF, and purpose-built for K8s allocation — it aligns with the stack (D2), and reinventing it would be wasted effort and unnecessary risk.

## D7 — Cardinal Rule: cost & usage metadata only, never content
**Status:** Accepted
**Decision:** Costroid handles **cost & usage metadata only**. It must **never ingest, store, log, cache, or transmit raw prompt or response content** from any AI/LLM source. For AI spend it persists only model identity, token counts, cost, currency, timestamps, and tags. No exceptions — including debug logging.
**Why:** Data minimization is central to the product's self-hostable, data-sovereign positioning and to the trust of regulated users. It also keeps the blast radius of any incident to non-sensitive metadata.

## D8 — Self-hostable-first
**Status:** Accepted
**Decision:** The core must run fully on the user's own infrastructure with **zero external dependencies by default** — embedded storage, no mandatory phone-home or cloud calls to operate.
**Why:** Data sovereignty is the differentiator against closed SaaS; the users who most need this (regulated / EU / public sector) often legally cannot export billing data. Anything requiring an external service to function undermines that.

## D9 — Open-core boundary
**Status:** Accepted
**Decision:** The core is open source and self-contained, with no proprietary dependencies. Any enterprise-oriented features (e.g. SSO/SAML, RBAC, audit logs) live in **separate, pluggable modules** that the core does not depend on and does not require to function.
**Why (architecture):** Keeps the open-source core permissively usable and self-contained, with a clean seam between core and optional add-ons. This is a code-structure decision; how the project is funded is out of scope for this log.

## D10 — Shared contracts as a single source of truth
**Status:** Accepted
**Decision:** Define API and shared data contracts **once** (e.g. OpenAPI or protobuf) and **generate** the Go and TypeScript types from that source. Do not hand-maintain duplicate type definitions across languages.
**Why:** Go and TS both touch the same API/data shapes; a single generated source prevents the two sides from drifting.

## D11 — Single monorepo
**Status:** Accepted
**Decision:** Backend (Go), dashboard (TS), and agent (TS) live in **one repository** (`github.com/Costroid/costroid`), not split across repos.
**Why:** A small team benefits from atomic cross-cutting changes — e.g. a contract change touching Go, the generated types, and the dashboard in a single commit — and the shared contracts (D10) are simpler to keep in sync in one repo.

## D12 — Optional, modular agentic layer via MCP
**Status:** Accepted
**Decision:** The natural-language / agentic capability (querying, anomaly investigation) is an **optional** service, kept separate (TypeScript), talking to the Go API and surfaced via **MCP**. The core is fully functional without it.
**Why:** Agentic FinOps is being commoditized by larger players, so it is treated as a feature enhancer, not the foundation. Keeping it modular and optional protects the core and the self-host story.

## D13 — Scope discipline: smallest vertical slice first
**Status:** Accepted
**Decision:** Build the smallest useful end-to-end slice before broadening: **AWS FOCUS export → normalized store → one dashboard view.** Add Azure/GCP, AI connectors, reconciliation, K8s, and the agent only after that path works.
**Why:** The primary risk is scope explosion, not technical impossibility. A working thin slice de-risks the architecture and gives something real to build on.

## D14 — License: Apache-2.0 for the open core
**Status:** Accepted (2026-07-01, resolves the previously open license question)
**Decision:** The open core is licensed under the **Apache License 2.0** (see `LICENSE`). Apache-2.0 over MIT for its explicit patent grant and its status as the de-facto standard for infrastructure/CNCF projects (aligns with OpenCost, D6). Any enterprise-only modules (D9) live in a **separate directory under a separate commercial/source-available license** — not Apache-2.0 — so the permissive core never gives away the monetizable features.
**Why (over the alternatives):**
- *vs AGPL-3.0:* AGPL's network-copyleft would better deter competitors from hosting a rival service, but enterprise/regulated legal teams — exactly the target buyers — frequently restrict or ban AGPL, and it adds friction with the FinOps/CNCF ecosystem. Pre-adoption, adoption and trust matter more than hosted-competition defensibility; that defensibility comes instead from the open-core split, managed hosting, and premium data services. (If deterring hosted competitors ever outranks adoption, AGPL is the switch to flip.)
- *vs BSL/SSPL (source-available):* rejected — not OSI-approved open source, which would contradict the project's identity ("open, self-hostable, reference implementation") and its ecosystem alignment.
**Related — decide before the first external contribution:** to preserve future dual-licensing/relicensing optionality, adopt a **CLA** (lightweight via CLA Assistant) before merging outside PRs; a DCO is friendlier but does **not** grant relicensing rights. As sole author, all rights are currently retained.
**Apply headers:** new source files should carry the short Apache-2.0 header (`SPDX-License-Identifier: Apache-2.0` plus the standard copyright line). An optional `NOTICE` file may be added later.

## D15 — Multi-tenancy: single-tenant core, tenancy-aware schema, multi-tenancy in the managed layer
**Status:** Accepted
**Decision:** The open-source core runs **single-tenant** (one organization runs it for itself). However, the schema and record keys are **tenancy-aware from day one** (carry a tenant/org identifier), so nothing has to be restructured later. True multi-tenancy (tenant isolation, per-tenant auth, cross-tenant admin) lives in the **managed/enterprise layer** (D9), not the core.
**Why:** Self-hosting is inherently single-tenant, and forcing full multi-tenancy into the OSS core adds isolation/auth complexity most self-hosters don't need. But the managed SaaS and the MSP segment require multi-tenancy, and retrofitting tenant awareness into a schema and auth model after the fact is painful. A tenancy-aware schema is the cheap insurance that keeps both paths open.

## D16 — Connector & ingestion contract (stable, incremental, correction-aware)
**Status:** Accepted
**Decision:** Ingestion is built around a **stable, documented connector interface** so new sources (including community-contributed ones) can be added without touching the core. Ingestion must be **incremental** (only fetch/process new or changed data), **idempotent** (re-running a load doesn't duplicate or corrupt), and **correction-aware** (cloud bills get restated mid-period; use FOCUS 1.4 correction handling to supersede prior data rather than double-count).
**Why:** The connector interface is the ecosystem lever — a clean contract is how the project earns community connectors and adoption. Incremental + idempotent + correction-aware are not optional niceties: real provider bills are large, arrive in pieces, and get restated, so any connector that ignores this produces wrong numbers.

## D17 — Credential & billing-data access: least-privilege, read-only, never plaintext
**Status:** Accepted
**Decision:** Connectors request the **minimum, read-only** access needed to a source's cost/usage data — prefer platform-native, short-lived mechanisms (e.g. IAM roles / workload identity) over long-lived API keys. Credentials are **never persisted in plaintext (encrypted at rest)**, never logged, and never leave the user's deployment. This is a hard security boundary, alongside the Cardinal Rule (D7).
**Why:** Users are handing Costroid access to sensitive billing accounts; anything less than least-privilege, read-only, encrypted-at-rest handling is a breach waiting to happen and would destroy the trust the self-hostable/data-sovereign positioning depends on.

## D18 — In-scope FinOps depth that shapes the schema
**Status:** Accepted
**Decision:** Three capabilities are treated as **in-scope for the data model even if built later**, because designing them in after the fact means a schema rewrite: (a) **commitment amortization** — represent Reserved Instances / Savings Plans / CUDs and distinguish amortized/effective cost from billed cost (FOCUS `EffectiveCost` vs `BilledCost`); (b) **rule-based allocation ("virtual tagging")** — derive team/product/customer attribution when native tags are missing; (c) **business/usage metrics ingestion** — accept a *second* data type beyond FOCUS cost (e.g. requests, active users, tenants served) so **unit economics** (cost per customer/feature/etc.) can be computed.
**Why:** These separate a real FinOps platform from a cost viewer, and each touches the schema. (c) is a genuine model gap otherwise: everything is framed as FOCUS cost data, but unit economics needs a business-metric stream flowing in alongside it. Acknowledging them now keeps the schema honest; sequencing/when-to-build is a scope call (see D13).

## D19 — Self-hosted schema migrations from the first release
**Status:** Accepted
**Decision:** Ship **automated, versioned, forward-only schema migrations** from v0.1 onward. Upgrading a Costroid deployment must migrate a user's existing store (DuckDB/Parquet, and any scale-out backend) without manual intervention or data loss.
**Why:** Users run and upgrade Costroid themselves — there is no ops team to hand-migrate their data. Migrations are trivial to establish at the start and very expensive to bolt on once real deployments hold data.

## D20 — API contract format: OpenAPI 3.0
**Status:** Accepted (2026-07-02)
**Decision:** The shared contract (D10) is **OpenAPI 3.0.x**, living at `contracts/openapi.yaml`. Go server types/scaffolding are generated with oapi-codegen v2 (standard-library `net/http` server target); TypeScript types with openapi-typescript. Generated code is committed and regenerated via `make generate`.
**Why:** The API is REST/JSON consumed by a browser dashboard — OpenAPI needs no gateway or proxy layer (unlike gRPC), has the broadest ecosystem for third parties integrating with a self-hosted API, and its Go/TS codegen is mature. 3.0.x over 3.1 because oapi-codegen has no native 3.1 support.
**Scope note:** This fixes the contract format and type generation only; the dashboard's runtime fetch approach is an implementation detail (openapi-typescript's companion client `openapi-fetch` entered maintenance mode in 2026, so plain typed `fetch` is the default).
**Rejected:** protobuf/classic gRPC (browsers need grpc-web or a gateway — extra runtime infrastructure against the zero-ops single-binary default, D8); Connect RPC (attractive hybrid, but a heavier toolchain than warranted now and a smaller integration ecosystem than OpenAPI).

## D21 — First AWS connector ingests a local FOCUS export file; live S3 sync comes later
**Status:** Accepted (2026-07-02)
**Decision:** The first AWS connector (the D13 slice) reads an **already-downloaded AWS FOCUS export from a local path**, with a synthetic sample committed under `testdata/`. Live S3 sync — AWS SDK, least-privilege read-only IAM — plus the D17 credential-handling subsystem and incremental-fetch state are deferred to separate, subsequent slices built on the same connector interface (D16).
**Why:** Keeps the first end-to-end slice verifiable offline with no cloud account or credentials, and defers the credential subsystem until there is a working pipeline to attach it to. The connector interface is unchanged either way, so the S3 fetcher slots in without rework.

## D22 — DuckDB via CGO: single self-contained binary, not fully static
**Status:** Accepted (2026-07-02)
**Decision:** The embedded storage default (D5) is implemented with DuckDB's official Go driver, `github.com/duckdb/duckdb-go/v2`, which requires CGO (DuckDB's static libraries are bundled at build time). The distribution promise is therefore a **single self-contained binary** — one executable, no external DuckDB install — rather than a fully statically linked one. AGENTS.md was amended accordingly ("static" → "self-contained"). Cross-compilation and musl-static builds are constrained by CGO and are not release goals for now.
**Why:** There is no pure-Go DuckDB implementation, and DuckDB-embedded is settled (D5). The self-host experience users actually care about — download one file, run it — is preserved; full static linking was an implementation detail of that promise, not the promise itself.

## D23 — Money semantics at the API and view layer
**Status:** Accepted (2026-07-02, ratifying choices shipped and verified in slice 1)
**Decision:** (a) The API transports monetary values as **decimal strings** — JSON floats are never used for money. (b) The dashboard's cost views report **BilledCost** as the default metric; `EffectiveCost` stays in the schema for future amortization views (D18a). (c) Queries whose result would mix currencies **fail with an explicit error** rather than converting — conversion requires a rates-source decision that has not been made.
**Why:** Decimal strings preserve the exactness invariant end-to-end (floats corrupt billing figures); BilledCost is the invoice-facing number for the first views; silent currency conversion produces confidently wrong totals. Any future conversion feature supersedes (c) with its own decision entry naming the rates source.

## D24 — AWS credentials: ambient SDK chain; no credential persistence until a source requires stored secrets
**Status:** Accepted (2026-07-02)
**Decision:** Connectors that can use platform-native identity do so. The AWS S3 connector authenticates via the AWS SDK's **default credential chain** (env vars, shared config/SSO profiles, IAM roles) and Costroid **persists no AWS credentials anywhere**. The encrypted-at-rest credential store anticipated by D17 is built when the first source that genuinely requires stored secrets lands (AI vendors' API keys), not before.
**Why:** The ambient chain is exactly D17's preferred short-lived, least-privilege path. Building a credential store now would add unused attack surface, and storing AWS keys would nudge users toward long-lived credentials — the pattern D17 exists to avoid.

## D25 — Store decimal scale: DECIMAL(38,18); reject-never-round beyond it
**Status:** Accepted (2026-07-02)
**Decision:** The embedded store holds FOCUS money/quantity columns as **DECIMAL(38,18)** (schema migration 0002 widens the original (38,12); `storage.MaxDecimalScale` becomes 18). Values whose exactness would be lost at scale 18 are **rejected at ingest with a row-numbered error naming the column and limit** — never silently rounded.
**Why:** FOCUS permits providers to emit decimal64 (16 significant digits) and wider (ATT-NumericFormat-A-013-C); at scale 12, a single conformant row — e.g. 16-fractional-digit unit prices — aborted an entire conformant export. Scale 18 covers observed provider precision while keeping 20 integer digits for large quantities; reject-never-round preserves the exactness invariant (D23) at the boundary. Supersedes the scale-12 behavior shipped in slice 2's fix-up commit.

## D26 — Correction handling: dataset-level supersede at ingest; additive correction rows; retroactive attribution at query time
**Status:** Accepted (2026-07-02; grounded in the ratified FOCUS 1.4 spec — corrections appendix, ChargeClass column, CorrectionHandling/DeliveryHandling attributes — and AWS Data Exports delivery documentation)
**Decision:** (a) **Ingestion-level supersede** is per-(connector, source, billing-period) transactional batch replacement — FOCUS 1.4's "Replacement"/Overwrite style, which is also AWS's primary correction path (closed periods are restated in place, officially up to ~2 weeks after close and, for refunds/credits/support fees, without a documented upper bound). (b) **Correction rows** (`ChargeClass = "Correction"`) are additive rows belonging to the billing-period batch that delivered them — never rewritten into the corrected period's batch. FOCUS defines no row-level supersede mechanism. (c) **Time-series views aggregate by ChargePeriod**, so correction rows (which keep the original incurred timeframe per the spec's correction-handling examples) retroactively adjust the original days at query time. This is intended, documented, and tested behavior — cost history legitimately changes when providers issue corrections. (d) **Restatement visibility:** re-ingesting a period whose content changed reports the per-period cost delta at the CLI. (e) Delta/Ledger-style (append-based) correction sources are NOT handled yet; when a connector needs them, that gets its own decision entry.
**Why:** This is the minimal design that satisfies "supersede prior data, never double-count" (D16) while staying inside what FOCUS 1.4 actually specifies: replacement handles provider restatements, additivity handles correction rows, and ChargePeriod aggregation merges them without any dedup machinery. Never-hard-freeze follows from the documented unbounded restatement window.

## D27 — Positioning language: sovereignty-first, not "FinOps dashboard"
**Status:** Accepted (2026-07-02, ratifying strategy candidate 1 — see `docs/strategy.md`)
**Decision:** Public positioning (README, site, announcements) leads with **"self-hosted, FOCUS-native cost platform for teams whose billing data can't leave their infrastructure"** — never "open-source FinOps dashboard".
**Why:** Visibility alone is the weakest product foundation (strategy notes, Structural risks §1); the sovereignty framing names the buyers no SaaS competitor can reach.

## D28 — Pricing posture: flat tiers, never %-of-spend
**Status:** Accepted (2026-07-02, ratifying strategy candidate 2)
**Decision:** If and when any paid tier exists, pricing is **flat tiers** — never a percentage of managed spend.
**Why:** %-of-spend pricing meets well-documented buyer resistance and taxes exactly the customers whose spend grows. Recorded now so future commercial work doesn't drift into the resented default.

## D29 — Connector priority: hyperscalers → AI vendors → generic FOCUS/CSV import; no per-SaaS scrapers in core
**Status:** Accepted (2026-07-02, ratifying strategy candidate 3)
**Decision:** Connector roadmap order: (1) hyperscalers (AWS shipped; then Azure, GCP), (2) AI vendors via their usage/cost APIs (Cardinal Rule D7 applies in full), (3) a **generic FOCUS/CSV import** covering everything else. Per-SaaS-vendor scraper connectors are **not** built in the open core.
**Why:** The per-vendor SaaS long tail is unbounded; the generic import covers it at fixed cost while the conversion layer — the technical moat — stays focused on the sources that matter (strategy notes, Dead ends).

## D30 — First target users: regulated / data-residency-bound organizations
**Status:** Accepted (2026-07-02, ratifying strategy candidate 4)
**Decision:** The first users Costroid designs, documents, and validates for are **organizations that cannot use SaaS FinOps at all** — data-residency-bound and regulated sectors (finance under in-country-systems rules, EU-sovereignty-constrained, public sector).
**Why:** For these buyers self-hosting is a hard requirement, not a preference; they are unreachable by every SaaS-only competitor and are the natural proof of the D27 positioning (strategy notes, Target market note).