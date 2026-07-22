# Strategy notes

Product and market notes for Costroid, distilled 2026-07-02 from a broad
web-research pass (FinOps Foundation announcements, vendor releases and docs,
industry coverage). Unlike `decisions.md`, this file is a **living document** —
revise or prune freely as the market moves. Where it proposes a choice, that choice
is **not decided** until it lands in `decisions.md`.

## Goals

Build a **working, successful, adaptable** project that can become **profitable
later** — in that order:

- **Working.** Every slice ships end-to-end and verified (D13): a self-hoster can
  always clone, build one binary, ingest a real export, and see correct numbers.
  Correctness over breadth — wrong cost numbers are worse than missing features.
- **Successful.** Occupy the currently empty slot — open-source + self-hostable +
  FOCUS-native + cloud/SaaS/AI spend in one model (see the landscape table) — and be
  the *reference implementation* for the newest FOCUS capabilities (1.4 invoice
  reconciliation, 1.5 AI/token columns when ratified). Success metric: real
  self-hosted deployments and community connectors (D16), not stars.
- **Adaptable.** FOCUS ships twice a year and providers lag behind it; version-aware
  transforms (D4), the connector contract (D16), the storage interface (D5), and the
  revisit triggers below keep the design change-tolerant instead of rewrite-prone.
- **Profitable later.** Keep the open-core seam clean (D9, D14) so a commercial
  layer (enterprise modules, hosted offering, support) can attach without touching
  the core. Comparable small-team OSS projects took years from adoption to
  meaningful revenue — design for sustainability, not a quick conversion. If/when a
  paid tier exists, prefer **flat tiers** over %-of-spend pricing (the latter meets
  well-documented buyer resistance).

## Positioning

**Self-hosted, FOCUS-native normalization of cloud + SaaS + AI spend into one
queryable model** — strongest wherever data-residency rules or confidentiality make
SaaS FinOps a non-starter. Not "an open-source FinOps dashboard": visibility alone
is the weakest foundation for a product (see risks).

As of mid-2026 this exact combination exists nowhere. The parts exist separately:

| Who | What they are | What they're missing |
|---|---|---|
| Hystax OptScale | OSS (Apache-2.0), self-hosted, multi-cloud | Not FOCUS-native; no SaaS/LLM spend |
| Microsoft FinOps Toolkit | OSS, genuinely FOCUS-native | Anchored to Azure services (Data Factory/ADX/Fabric) |
| StitcherAI | FOCUS-native cloud+SaaS+AI normalization (founded by a FOCUS co-creator) | Proprietary SaaS only |
| Langfuse / Helicone / LiteLLM / OpenMeter | OSS, self-hosted AI cost | LLM-only; no cloud billing; no FOCUS |
| OpenCost | CNCF, K8s allocation, drifting multi-cloud | Infra metering, not billing-first; FOCUS announced, not shipped |
| CloudZero / Vantage / Finout / Datadog CCM | Unified cloud+AI spend, FOCUS adopters | SaaS-only — no self-host option at all |

## Structural risks

1. **Visibility alone doesn't sustain a product.** The market rewards
   savings/automation outcomes over dashboards, and %-of-spend pricing meets
   documented buyer resistance. Counter: lean on the CFO-adjacent capabilities
   (unit economics, invoice reconciliation) and plan an eventual
   anomaly→recommendation "action" layer — a natural paid-tier candidate.
2. **"FOCUS-native" erodes as a differentiator.** Every major vendor is adopting
   FOCUS; by 1.5 it is table stakes. The durable moat is the **messy conversion
   layer**: sources that will never emit FOCUS (OpenAI/Anthropic expose raw
   usage/cost APIs), provider version skew (1.0–1.3 in the wild), conformance gaps,
   corrections, refund-period placement, amortization semantics. The upstream
   `focus_converters` project has been abandoned since Aug 2024 — the ecosystem left
   this gap open. Own it: the normalization engine is the product, not plumbing.
3. **The empty slot has a shelf life** (estimate: 12–18 months). StitcherAI runs the
   same data-model thesis as closed SaaS; if it ships a self-hosted tier, the
   differentiation collapses to execution speed. OpenCost (hyperscaler-backed) is
   drifting multi-cloud with AI costing on its roadmap. D13's smallest-slice
   discipline is the main defense: ship the unified path end-to-end before
   broadening.

## Dead ends (do not build)

- Kubernetes-only allocation — commoditized (OpenCost/Kubecost free tier).
- No-code FinOps workflow automation — OpenOps owns that OSS slot.
- Pre-deployment cost estimation — Infracost's lane.
- Per-SaaS-vendor connector scrapers — unbounded long tail; ship hyperscalers + AI
  vendors + a **generic FOCUS/CSV import** instead.
- MCP/agent layer as *headline* differentiator — official per-provider billing MCP
  servers already exist and every vendor ships agents. The defensible version is a
  natural-language layer **over the normalized cross-provider FOCUS store** (the
  data layer is the moat, the query surface is the door). D95 supersedes D12 and
  drops MCP: the layer runs in the Go binary, optional and off by default, and it
  translates a question into a call to the existing API rather than exposing a
  tool surface to someone else's client.
- Relicensing later (BSL/SSPL) — the 2024–25 fork record (OpenTofu, Valkey) says
  stay Apache-2.0 (D14) with a buyer-based open-core boundary (D9).
- Betting that SaaS/AI vendors will emit FOCUS natively — no evidence they will.

## Demand-side validation (State of FinOps 2026)

98% of organizations manage AI spend (31% two years prior); 90% manage SaaS; the top
requested tool capabilities are granular AI/token spend monitoring, shift-left
costing, and a unified single pane of glass; ~68% of $100M+/yr spenders use or pilot
FOCUS-formatted data. This is Costroid's scope, near-verbatim.

## Target market note

Self-hosting is a *requirement*, not a preference, in data-residency-bound sectors:
several jurisdictions legally require financial institutions to keep primary and
backup systems in-country, sometimes with no local hyperscaler region, and EU
sovereignty rules (Data Act, sector regulation) push the same way. Billing data is
sensitive enough that "your cost data never leaves your infrastructure" is a proven
buying criterion in this space. These buyers are unreachable by every SaaS-only
competitor in the table above — they are the natural first users.

## FOCUS spec watch

Semiannual cadence: 1.2 (May 2025: SaaS/PaaS scope, multi-currency, virtual-currency
/token units) · 1.3 (Dec 2025: Contract Commitment dataset, split/shared-cost
columns) · **1.4 (June 2026: Invoice Detail + Billing Period datasets — the
standardized basis for invoice reconciliation; almost nobody has shipped against it
yet, so implementing it early is a concrete, checkable differentiator)** · 1.5
(expected Dec 2026: SKU/Price Sheet dataset + AI model/token columns — scope not yet
ratified). Watch the Linux Foundation "Tokenomics" effort and the open
working-group debate on high-cardinality AI inference data (OpenTelemetry vs a new
dataset) — its outcome decides how AI unit economics get standardized.

## Watchlist & revisit triggers

Revisit this file when any of these fires:

- StitcherAI announces self-hosted or open-source anything.
- OpenCost ships FOCUS as its native model, or ships its AI usage costing.
- FOCUS 1.5 is ratified → align the AI/token and Price Sheet schema (D4).
  Leading indicator: the FOCUS_Spec v1.5 GitHub milestone closes development
  ~2026-10-01; ratification has historically followed ~2 months later.
- A Tokenomics/AI-cost standard is published.
- **GCP's FOCUS export reaches GA** (or announces a GA date) → start the GCP
  connector slice (D31 gates it on this).

Status check 2026-07-05 (adversarially verified research pass):

- **Fired (a status change, not the GA trigger):** GCP shipped a first-party
  FOCUS billing export in **Preview** on 2026-06-08 — FOCUS 1.2, delivered only
  as a Google-managed BigQuery linked dataset (the file-export path is
  deprecated and closed to new customers); pre-GA terms, schema may change.
  Backfill reaches at most the start of the previous month and the table has a
  2-year TTL — docs should tell GCP users to enable it *now* even though our
  connector waits (D31).
- **Partially fired:** the Linux Foundation announced *intent* to launch the
  **Tokenomics Foundation** (2026-06-03; backers incl. Google Cloud, Microsoft,
  Oracle, IBM). No standard published — token/AI-cost schema work funnels into
  FOCUS 1.5. Tailwind for the AI-vendor connectors.
- **Not fired:** StitcherAI — SaaS GA only (2026-05-19), nothing self-hosted or
  open-source. OpenCost — no FOCUS-native model, no AI costing shipped; both
  still roadmap items → re-check around KubeCon NA 2026.
- **New datapoints:** FOCUS 1.4 ratified 2026-06-04 (validates our internal
  model; no hyperscaler ships >1.2 yet). AWS holds the inaugural FinOps
  Certified FOCUS Generator badge — certified against **1.2**, the industry's
  current ceiling and exactly what we ingest. Microsoft publicly committed to
  FOCUS 1.4 "in 2026" → expect an Azure version-bump slice in H2 2026, likely
  before GCP GA. Vercel ships FOCUS **1.3** (since 2026-02-19) — version churn
  arrives via small SaaS vendors first; a datapoint for the generic-import
  tier. The upstream focus_converters project remains abandoned (last push
  2024-08-30).

## Candidate decisions

All four candidates were ratified on 2026-07-02 and now live in `decisions.md` as
**D27** (positioning language), **D28** (flat tiers, never %-of-spend), **D29**
(connector priority; no per-SaaS scrapers in core), and **D30** (first target
users: regulated / data-residency-bound organizations). Future candidates go here
first, then get ratified or rejected into the decision log.

## Key sources

- FinOps Foundation: FOCUS 1.2/1.3/1.4 announcements; State of FinOps 2026
  (data.finops.org); FinOps X 2026 coverage
- Vendor/project primary sources: OpenCost year-in-review, Microsoft FinOps Toolkit
  docs, Hystax/OpenOps/Langfuse/LiteLLM repos, Kubecost security docs, StitcherAI
  public site
- Practitioner evidence on conversion pain: CUR→FOCUS migration write-ups
  (EffectiveCost variance, refund placement, tag restructuring)

Caveats: competitor capability claims are taken from public materials and may lag
unannounced work (e.g. an on-prem tier at a SaaS vendor cannot be ruled out); spec
1.5 scope is roadmap, not ratified.
