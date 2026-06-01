# Data model

This document defines Costroid's data model: the FOCUS records it emits, the separate subscription-limit type, the internal Rust shapes, the per-provider parsing notes, the bundled pricing schema, and the grouping dimensions. It is the spec for `costroid-focus` and `costroid-providers`. For how data flows through the system see [ARCHITECTURE.md](ARCHITECTURE.md).

**Verify before finalizing.** The FOCUS column names below were checked against the FOCUS specification, but the implementer MUST validate against the **current FOCUS 1.3 specification and changelog** at <https://focus.finops.org> before locking the schema — in particular the 1.3 Service Provider / Host Provider columns (which replace two now-deprecated provider columns) and the allowed-value lists for `ServiceCategory`, `ChargeCategory`, `ChargeFrequency`, and `PricingCategory`. Treat the FOCUS spec, not this file, as the authority on column semantics.

## FOCUS 1.3 in brief

FOCUS (FinOps Open Cost and Usage Specification) is an open standard for billing datasets. A FOCUS dataset is a flat table of **charge rows**, each describing one charge with normalized cost, quantity, time, service, and SKU columns. Version 1.3 (ratified December 4, 2025) deepened cloud/SaaS support and added a separate Contract Commitment dataset, split-cost-allocation columns, recency/completeness metadata, and the Service/Host Provider distinction. FOCUS explicitly supports **custom columns** via the `x_` prefix for anything the core spec doesn't define — which is how Costroid represents AI-specific attributes (model, token type, access path).

Costroid emits the subset of FOCUS columns that a per-token AI usage charge can populate from local data. It does not attempt the Contract Commitment dataset (no contract data exists locally) and leaves columns it cannot derive null where the spec permits.

## The FOCUS columns Costroid emits

Costroid models each unit of API usage as one or more FOCUS rows with `ChargeCategory = "Usage"`. Because input, output, and cache tokens are priced differently, **each token meter becomes its own row** (distinguished by `SkuId`/`SkuPriceId` and the `x_TokenType` custom column), so quantities and unit prices stay coherent.

Mapping (FOCUS column → how Costroid fills it for an AI usage charge):

- **BilledCost** — the estimated charge (quantity × unit price). Mandatory in FOCUS. Locally this is an estimate; see reconciliation below.
- **EffectiveCost** — amortized cost after discounts. With no local discount data, Costroid sets this equal to the estimate; reconciliation refines it.
- **ListCost** — list price × `PricingQuantity` (= `ListUnitPrice` × `PricingQuantity`).
- **ContractedCost** — equal to ListCost when no negotiated rate is known locally.
- **BillingCurrency** — the provider's pricing currency, e.g. `"USD"` (from the pricing table).
- **BillingPeriodStart / BillingPeriodEnd** — the provider's billing month containing the charge.
- **ChargePeriodStart / ChargePeriodEnd** — the time bucket of the usage event (RFC 3339, UTC).
- **ChargeCategory** — `"Usage"`.
- **ChargeClass** — null normally; `"Correction"` only if reconciling an adjustment.
- **ChargeDescription** — a human string, e.g. `"<model> output tokens"`.
- **ChargeFrequency** — `"Usage-Based"` (confirm against the allowed values).
- **ServiceName** — the offering, e.g. `"Anthropic API"`, `"OpenAI API"`, `"Cursor"`.
- **ServiceCategory** — `"AI and Machine Learning"` (confirm this is the current allowed value).
- **ProviderName / PublisherName / InvoiceIssuerName** — the vendor (e.g. Anthropic, OpenAI, Anysphere). For API usage these are typically the same entity. **In 1.3 these provider columns are being reworked into Service Provider / Host Provider — follow the 1.3 changelog for the exact column names and populate accordingly.**
- **SkuId** — a stable identifier for the model + meter (e.g. `<model-id>:output`).
- **SkuPriceId** — the specific priced rate used.
- **PricingCategory** — `"Standard"` (1.2 renamed "On-Demand" → "Standard").
- **PricingQuantity / PricingUnit** — tokens billed and the unit, e.g. `"1M tokens"`.
- **ListUnitPrice** — the per-unit list price from the pricing table.
- **ConsumedQuantity / ConsumedUnit** — tokens consumed and `"tokens"`. (FOCUS requires `ConsumedQuantity` to be non-null for `ChargeCategory = "Usage"` when not a correction.)
- Optional where derivable: **ResourceId / ResourceName** (e.g. an API key alias or project), **RegionId / RegionName** (usually null for these APIs).

Custom (`x_`) columns Costroid adds:

- **x_Model** — the model identifier.
- **x_TokenType** — `"input" | "output" | "cache_read" | "cache_write"`.
- **x_AccessPath** — `"api" | "subscription"` (see next section).
- **x_Estimated** — `true` when the cost was computed locally from token × price (the default for all Phase 1 rows).
- **x_Tool** — `"claude-code" | "codex" | "cursor"` (the tool that produced the log).
- **x_Project** — the derived project/workspace (see Grouping).

## Subscription limits are modeled separately

This is the most important modeling rule. **Subscription limits are not FOCUS cost rows.** A subscription is a flat monthly fee; its "usage" is a quota percentage against a window with a reset time, with **no per-token dollar amount**. Summing it into a bill would be wrong. So limits live in their own type, never in the FOCUS table:

```rust
/// A subscription quota window. NOT a FOCUS charge row — carries no summable cost.
pub struct LimitWindow {
    pub tool: String,              // "claude-code" | "cursor" | ...
    pub plan: Option<String>,      // plan/tier name if known
    pub kind: LimitKind,           // FiveHour | Weekly
    pub used_fraction: f64,        // 0.0..=1.0
    pub resets_at: DateTime<Utc>,  // next reset (RFC 3339, UTC)
    pub label: Option<String>,
}

pub enum LimitKind { FiveHour, Weekly }
```

**A model used both ways** appears in both places, distinguished by access path: its API traffic produces FOCUS rows with `x_AccessPath = "api"`, while its subscription consumption shows up as a `LimitWindow` in the now-screen's limits section. The two are never conflated, and recommendations (Phase 4) attach **only** to `x_AccessPath = "api"` rows, because only there does changing models change a bill.

(Forward-looking, optional: the future "is my subscription worth it?" feature may emit FOCUS rows for subscription usage with `EffectiveCost` derived as an *effective* estimate — what that usage would have cost at API rates — marked `x_AccessPath = "subscription"` and `x_Estimated = true`. This is out of Phase 1 scope and must never be summed with real API costs without clear labeling.)

## Internal Rust shapes

`costroid-providers` parses raw logs into a provider-neutral intermediate, and `costroid-core` normalizes that into FOCUS rows via `costroid-focus`.

Intermediate (provider output):

```rust
/// One usage event as parsed from a provider's local logs. Provider-neutral.
pub struct UsageEvent {
    pub tool: String,                 // "claude-code" | "codex" | "cursor"
    pub model: String,
    pub timestamp: DateTime<Utc>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub project: Option<String>,      // derived workspace/repo/cwd
    pub access_path: AccessPath,      // Api | Subscription
}

pub enum AccessPath { Api, Subscription }
```

FOCUS record (in `costroid-focus`; column names match the spec exactly via serde):

```rust
use serde::{Serialize, Deserialize};
// Decimal = rust_decimal::Decimal; timestamps = chrono::DateTime<Utc> serialized as RFC 3339.

#[derive(Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct FocusRecord {
    // Costs (Decimal, in BillingCurrency). BilledCost is mandatory in FOCUS.
    pub billed_cost: Decimal,           // BilledCost
    pub effective_cost: Decimal,        // EffectiveCost
    pub list_cost: Decimal,             // ListCost
    pub contracted_cost: Decimal,       // ContractedCost
    pub billing_currency: String,       // BillingCurrency

    // Time
    pub billing_period_start: DateTime<Utc>,  // BillingPeriodStart
    pub billing_period_end: DateTime<Utc>,    // BillingPeriodEnd
    pub charge_period_start: DateTime<Utc>,    // ChargePeriodStart
    pub charge_period_end: DateTime<Utc>,      // ChargePeriodEnd

    // Charge classification
    pub charge_category: String,        // ChargeCategory = "Usage"
    pub charge_class: Option<String>,   // ChargeClass
    pub charge_description: String,     // ChargeDescription
    pub charge_frequency: String,       // ChargeFrequency = "Usage-Based"

    // Service & provider (see 1.3 Service/Host Provider note)
    pub service_name: String,           // ServiceName
    pub service_category: String,       // ServiceCategory = "AI and Machine Learning"
    pub provider_name: String,          // ProviderName
    pub publisher_name: String,         // PublisherName
    pub invoice_issuer_name: String,    // InvoiceIssuerName

    // SKU / pricing
    pub sku_id: Option<String>,         // SkuId
    pub sku_price_id: Option<String>,   // SkuPriceId
    pub pricing_category: String,       // PricingCategory = "Standard"
    pub pricing_quantity: Decimal,      // PricingQuantity
    pub pricing_unit: String,           // PricingUnit
    pub list_unit_price: Option<Decimal>, // ListUnitPrice

    // Consumption
    pub consumed_quantity: Decimal,     // ConsumedQuantity
    pub consumed_unit: String,          // ConsumedUnit = "tokens"

    // Custom (x_ prefix per FOCUS)
    #[serde(rename = "x_Model")]      pub x_model: String,
    #[serde(rename = "x_TokenType")]  pub x_token_type: String,
    #[serde(rename = "x_AccessPath")] pub x_access_path: String,
    #[serde(rename = "x_Estimated")]  pub x_estimated: bool,
    #[serde(rename = "x_Tool")]       pub x_tool: String,
    #[serde(rename = "x_Project")]    pub x_project: Option<String>,
}
```

## Export shapes

`costroid export` serializes FOCUS rows. Two formats, identical data:

- **JSON** (`--format json`): a JSON object wrapping the rows — `{ "focusVersion": "1.3", "rows": [ ... ] }` — where each element of `rows` is a `FocusRecord` keyed by FOCUS column name (PascalCase, with `x_` custom columns). This wrapper is the canonical shape (it carries the FOCUS version for forward-compatibility); a top-level `currency` field may be added if trivial. Do not emit a bare array.
- **CSV** (`--format csv`): the first row is the exact FOCUS column-name header (PascalCase, `x_` columns appended); one row per charge; decimals formatted plainly; timestamps RFC 3339.

Limits are **not** part of the FOCUS export (they are not charges). If a limits dump is needed, export `LimitWindow`s to a separate file/stream, clearly distinct from the cost data.

## Per-provider parsing

Each adapter discovers local data (WSL-aware: also check `/mnt/c/Users/<user>/...` when the tool runs on Windows) and maps it to `UsageEvent`/`LimitWindow`. **Confirm each provider's current local path and log schema during implementation** — these change, and exact formats must be read from a real install plus a committed fixture, not assumed. Notes below are starting points, not guarantees.

- **Claude Code.** Stores per-session conversation/usage data as local JSONL (commonly under `~/.claude/`; on Windows `%USERPROFILE%\.claude\`, i.e. `/mnt/c/Users/<user>/.claude/` from WSL). Per-message usage typically includes the model and input/output (and cache) token counts plus timestamps; the **project** is derivable from the workspace/session directory. API usage maps to FOCUS rows; Max/Pro subscription consumption maps to `LimitWindow`s (5-hour and weekly) — note that local logs may not expose exact reset times, which is where Phase 2 live data helps. Cost is estimated from token × price.
- **Codex.** The Codex CLI keeps local session logs under its config/data directory (verify the exact path; commonly a `~/.codex/`-style location, and the Windows-mounted equivalent under WSL). Extract model, token counts, timestamps, and the working directory as the project. Map to FOCUS rows; mark `x_Tool = "codex"`.
- **Cursor.** Cursor's local data is the most partial of the three: some usage is in local app data, but plan, quota, and billing-reset information has historically lived in the account/session rather than purely local logs. In Phase 1, extract whatever usage is available locally and emit what can be derived; full quota/limit fidelity for Cursor is expected to require Tier-2 session reuse in Phase 2. Be explicit in the UI when Cursor data is incomplete rather than guessing.

When a provider isn't installed or no data is found, skip it gracefully — never error the whole run.

### Estimate vs. invoice reconciliation

Local cost is always an **estimate**: `Σ(tokens × unit price)` from the bundled pricing table, with no visibility into negotiated rates, free tiers, or credits. Every Phase 1 FOCUS row therefore carries `x_Estimated = true`, and `EffectiveCost`/`BilledCost` reflect the estimate.

The provider invoice is the source of truth. Reconciliation (Phase 2+ via an authorized API or a manual invoice import) aggregates Costroid's estimated cost per billing period and service, compares it to the invoiced `BilledCost`, surfaces the variance, and may calibrate the estimate. Subscriptions reconcile differently: the "bill" is the flat fee, so the relevant comparison is the *effective* estimate (subscription usage valued at API rates) against the fee — the basis for the future "is the plan worth it?" view.

## Pricing data (bundled, build-time-sourced, offline)

Costroid ships a curated pricing table embedded at build time and works fully offline against it. **Do not hardcode prices or model lists in code or in this document** — they drift constantly. The build process sources current figures from the providers' published pricing and records the source and date; the table is updated per release.

Schema (values shown as placeholders; fill at build time):

```json
{
  "schema_version": "1",
  "as_of": "YYYY-MM-DD",
  "currency": "USD",
  "sources": ["https://<provider-pricing-page>"],
  "models": [
    {
      "provider": "<provider-id>",
      "model": "<model-id>",
      "service_name": "<ServiceName>",
      "rates": [
        { "meter": "input",       "unit": "1M_tokens", "price": <decimal> },
        { "meter": "output",      "unit": "1M_tokens", "price": <decimal> },
        { "meter": "cache_read",  "unit": "1M_tokens", "price": <decimal> },
        { "meter": "cache_write", "unit": "1M_tokens", "price": <decimal> }
      ]
    }
  ]
}
```

The cost calculator joins each `UsageEvent` token meter to the matching `model` + `meter` rate to produce `ListUnitPrice`, `PricingQuantity`, and the cost columns. Unknown models (not in the table) are reported with quantities but a clearly-flagged missing/zero cost — never a silently guessed price.

## Grouping dimensions

The trends screen aggregates over a **period** (`day` / `week` / `month` / `year`, bucketed by `ChargePeriodStart` in the user's local time zone) and a **group**:

- **model** — group by `x_Model` (equivalently the model component of `SkuId`).
- **app / project** — group by `x_Project`, derived per provider from the log's workspace / repository / working-directory field (Claude Code session dir, Codex cwd, Cursor workspace). When it can't be determined, bucket as `"unknown"` rather than dropping the row.
- **total** — aggregate across everything.

Aggregation sums `BilledCost` (and `EffectiveCost`) for cost views; it never sums `LimitWindow` data, which has no dollars.