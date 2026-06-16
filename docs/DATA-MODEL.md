# Data model

This document defines Costroid's data model: the FOCUS records it emits, the separate subscription-limit type, the internal Rust shapes, the per-provider parsing notes, the bundled pricing schema, and the grouping dimensions. It is the spec for the data shapes across `costroid-focus` (the FOCUS records), `costroid-providers` (the provider-neutral intermediate — `UsageEvent` and the `LimitWindow`/`LimitMeasure`/`LimitStatus`/`LimitKind` quota types), and `costroid-core` (the `LimitAvailability` availability/render type). For how data flows through the system see [ARCHITECTURE.md](ARCHITECTURE.md); scope and build sequencing (which providers ship when, and the step at which the quota model generalizes) are governed by [PRODUCT-PLAN.md](PRODUCT-PLAN.md).

**Verify before finalizing.** The FOCUS column names below were validated against the **FOCUS 1.3 specification** at <https://focus.finops.org> during implementation. Confirmed: the active 1.3 participating-entity columns are `ServiceProviderName`, `HostProviderName`, and `InvoiceIssuerName`. The older `ProviderName` and `PublisherName` columns are **deprecated in 1.3 (removed in 1.4)** — but the export **does emit them**, mirroring the active participating-entity values, because the bundled 1.3 validator still requires their presence (see the code comment in `crates/costroid-focus/src/lib.rs` at the struct's service/provider block). Still treat the FOCUS spec, not this file, as the authority on column semantics, and re-check the allowed-value lists for `ServiceCategory`, `ChargeCategory`, `ChargeFrequency`, and `PricingCategory` against the current spec when extending the schema.

## FOCUS 1.3 in brief

FOCUS (FinOps Open Cost and Usage Specification) is an open standard for billing datasets. A FOCUS dataset is a flat table of **charge rows**, each describing one charge with normalized cost, quantity, time, service, and SKU columns. Version 1.3 (ratified December 4, 2025) deepened cloud/SaaS support and added a separate Contract Commitment dataset, split-cost-allocation columns, recency/completeness metadata, and the Service/Host Provider distinction. FOCUS explicitly supports **custom columns** via the `x_` prefix for anything the core spec doesn't define — which is how Costroid represents AI-specific attributes (model, token type, access path).

Costroid emits the full FOCUS Cost & Usage column set, populating the columns a per-token AI usage charge can fill from local data and leaving the rest null where the spec permits. It does not attempt the Contract Commitment dataset (no contract data exists locally).

## Conformance status (Phase 1)

As of **Milestone 6b**, the export carries the **full FOCUS 1.3 Cost & Usage column set** and passes the official **`focus_validator`** — run offline against the 1.3.0.1 ruleset, vendored at `scripts/focus-ruleset/` since the 2026-06-10 fix pass (the PyPI wheel bundles only 1.2.0.1) — on every mandatory column-presence, type, allowed-value, **nullability**, and provider/account check, for **both priced and unpriced rows** (the conformance fixtures now include a priced model, `claude-sonnet-4-6`, alongside the unpriced ones). Numeric columns serialize as real JSON numbers (a surgical `RawValue` serializer confined to the `Decimal` fields; CSV emits bare decimals).

**M6b closed the three deferred cost-calculator items:** (1) `PricingUnit` is now `"tokens"` (FOCUS UnitFormat-valid; `"1M tokens"` was not), with `PricingQuantity` the token count and the unit-price columns expressed **per token** (the per-1M catalog rate ÷ 1,000,000); (2) on rows with no priced SKU (`SkuPriceId` null), `ConsumedQuantity` / `PricingQuantity` / `PricingUnit` / `PricingCategory` are now **null** as FOCUS 1.3 requires (a deliberate v1.3 requirement — *"… MUST be null when SkuPriceId is null"*); (3) this is purely a **representation** change — `cost = tokens × rate` is invariant, so every now/trends/statusline dollar figure is bit-for-bit identical to M4.5.

Two honest deviations remain **documented rather than faked**: `BillingAccountId` / `BillingAccountName` / `BillingAccountType` carry obvious non-billing placeholders (Costroid has no billing identity, and `BillingAccountId` MUST NOT be null); and all costs are estimates (`x_Estimated = true`), never an authoritative bill.

Three genuine **validator-ruleset defects** are allowlisted with upstream references — only the rules that actually fire, never their passing siblings, and Costroid's values are spec-correct. Two are malformed/inverted conditions ([focus_validator#142](https://github.com/finopsfoundation/focus_validator/issues/142), [#143](https://github.com/finopsfoundation/focus_validator/issues/143)). The third is the **`ListCost`/`ContractedCost` = unit-price × quantity** check: FOCUS defines this as *exact* equality, which Costroid satisfies exactly in `rust_decimal` (e.g. `0.000015 × 20 = 0.000300 = ListCost`), but the validator loads the CSV numerics as **float64** and tests `(UnitPrice × Quantity) <> Cost` in float64 with **zero tolerance**, so a 1-ULP float product (`0.00030000000000000003 ≠ 0.0003`) is wrongly flagged. No exact-decimal producer can satisfy a bit-exact float64 equality for arbitrary token counts, and rounding/nulling to dodge it would corrupt correct data — so it is a validator defect (reported upstream), not a producer bug. The export is now **FOCUS 1.3 conformant** modulo these documented ruleset defects.

## The FOCUS columns Costroid emits

Costroid models each unit of API usage as one or more FOCUS rows with `ChargeCategory = "Usage"`. Because input, output, and cache tokens are priced differently, **each token meter becomes its own row** (distinguished by `SkuId`/`SkuPriceId` and the `x_TokenType` custom column), so quantities and unit prices stay coherent.

Mapping (FOCUS column → how Costroid fills it for an AI usage charge):

- **BilledCost** — the estimated charge (quantity × unit price). Mandatory in FOCUS. Locally this is an estimate; see reconciliation below.
- **EffectiveCost** — amortized cost after discounts. With no local discount data, Costroid sets this equal to the estimate; reconciliation refines it.
- **ListCost** — list price × `PricingQuantity` (= `ListUnitPrice` × `PricingQuantity`).
- **ContractedCost** — equal to ListCost when no negotiated rate is known locally.
- **BillingCurrency** — the provider's pricing currency, e.g. `"USD"` (from the pricing table).
- **BillingPeriodStart / BillingPeriodEnd** — the provider's billing month containing the charge.
- **ChargePeriodStart / ChargePeriodEnd** — the usage event's time (RFC 3339, UTC, truncated to whole seconds). For an instantaneous transcript turn, `ChargePeriodEnd = ChargePeriodStart + 1s`, which the validator accepts (inclusive start, exclusive end).
- **ChargeCategory** — `"Usage"`.
- **ChargeClass** — null normally; `"Correction"` only if reconciling an adjustment.
- **ChargeDescription** — a human string, e.g. `"<model> output tokens"`.
- **ChargeFrequency** — `"Usage-Based"` (a FOCUS 1.3 allowed value; validated by the conformance gate against the vendored 1.3.0.1 ruleset).
- **ServiceName** — the offering, e.g. `"Anthropic API"`, `"OpenAI API"`, `"Cursor"`.
- **ServiceCategory** — `"AI and Machine Learning"` (the FOCUS 1.3 allowed value; validated by the conformance gate against the vendored 1.3.0.1 ruleset).
- **ServiceProviderName / HostProviderName / InvoiceIssuerName** — the vendor (e.g. Anthropic, OpenAI, Anysphere). For API usage these are typically the same entity. These are the **active FOCUS 1.3** participating-entity columns. The deprecated `ProviderName` / `PublisherName` columns **are also emitted** (`ProviderName` mirrors `ServiceProviderName`; `PublisherName` mirrors `InvoiceIssuerName` — the same value today, since the engine sets all three active columns to the vendor) because the bundled 1.3 validator requires their presence despite the deprecation; drop them when moving to a 1.4 ruleset.
- **SkuId** — a stable identifier for the model + meter (e.g. `<model-id>:output`); always populated.
- **SkuPriceId** — the specific priced rate used (e.g. `<provider>:<model>:<meter>:tokens:<as-of>`); **null on unpriced rows**, which gates the nullability of the pricing columns below.
- **PricingCategory** — `"Standard"` on priced rows (1.2 renamed "On-Demand" → "Standard"); **null when `SkuPriceId` is null** (FOCUS 1.3 requires it). See the unpriced-row convention under Pricing data.
- **PricingQuantity / PricingUnit** — the **token count** and `"tokens"` on priced rows; both **null when `SkuPriceId` is null**. (PricingUnit is `"tokens"`, not `"1M tokens"`, to satisfy the FOCUS UnitFormat.)
- **ListUnitPrice / ContractedUnitPrice** — the **per-token** list/contracted price (the per-1M pricing-table rate ÷ 1,000,000); null when `SkuPriceId` is null. So `ListCost = ListUnitPrice × PricingQuantity = (rate ÷ 1e6) × tokens`, exactly the prior `(tokens ÷ 1e6) × rate` — the dollar value is unchanged, only the representation moved from per-1M to per-token.
- **ConsumedQuantity / ConsumedUnit** — the token count and `"tokens"`. `ConsumedQuantity` is **null when `SkuPriceId` is null** (FOCUS 1.3); **`ConsumedUnit` is always `"tokens"`, on priced and unpriced rows alike** — it is a non-nullable `String` in the struct, the SkuPriceId-null rule (which covers `ConsumedQuantity` / the pricing columns) does not include it, and the validator passes with it always populated. The raw token count is never lost — it always travels on `x_ConsumedTokens` (below), which the aggregation engine reads.
- Optional where derivable: **ResourceId / ResourceName** (e.g. an API key alias or project), **RegionId / RegionName** (usually null for these APIs).

Custom (`x_`) columns Costroid adds:

- **x_Model** — the model identifier.
- **x_TokenType** — `"input" | "output" | "cache_read" | "cache_write"`.
- **x_AccessPath** — `"api" | "subscription" | "unknown"` (see next section).
- **x_Estimated** — `true` when the cost was computed locally from token × price (the default for all Phase 1 rows).
- **x_Tool** — `"claude-code" | "codex" | "cursor"` (the tool that produced the log).
- **x_Project** — the derived project/workspace (see Grouping).
- **x_PricingStatus** — `"priced" | "missing_price" | "unknown_model"`: whether a rate was found in the bundled pricing table — `priced` when the `(model, meter)` join succeeds, `missing_price` for a known model that lacks that meter's rate, and `unknown_model` when the model isn't in the table at all.
- **x_ConsumedTokens** — the raw token count for the meter row, **always populated** (never null, even on unpriced rows where `ConsumedQuantity` must be null). The aggregation engine totals tokens from this column so nulling `ConsumedQuantity` never drops usage.

## Subscription limits are modeled separately

This is the most important modeling rule, and it concerns the quota **limits** specifically. A subscription *limit* is a quota percentage against a window with a reset time, carrying **no per-token dollar amount** — summing it into a bill would be wrong — so limits live in their own type — `LimitWindow` (defined in `costroid-providers` alongside `UsageEvent`, with its `LimitMeasure`/`LimitKind`/`LimitStatus` companions; the `costroid-core` `LimitAvailability` render type sits above it) — never in the FOCUS table. (Subscription token *usage* is a separate matter: it **does** produce FOCUS rows, valued at API-equivalent rates and clearly labeled as an estimate — see access path below. Only the quota windows here are non-dollar.)

```rust
/// A subscription quota window. NOT a FOCUS charge row — carries no summable cost.
pub struct LimitWindow {
    pub tool: ProviderId,          // claude-code | codex | cursor | ...
    pub plan: Option<String>,      // plan/tier name if known
    pub kind: LimitKind,           // FiveHour | Weekly | Daily | Monthly | BillingCycle
    pub measure: Option<LimitMeasure>, // the reading; None ⇒ no usable number (maps to Unavailable)
    pub resets_at: Option<DateTime<Utc>>, // next reset (UTC). Claude `resets_at` is parsed
                                   // defensively — seen as epoch seconds AND ISO across versions.
    pub captured_at: DateTime<Utc>,// when this reading was observed. Push-only sources
                                   // (Claude's statusLine) are only as fresh as the last
                                   // turn. Used at the core layer to age a reading out past
                                   // `resets_at`; T6 also threaded it onto LimitSummary, so the
                                   // render layer draws the always-on "as of HH:MM" stamp from it.
                                   // An Unavailable window (no reading) uses the UNIX-epoch sentinel.
    pub status: LimitStatus,       // Verified | Unverified | Unavailable (see below)
    pub label: Option<String>,
}

pub enum LimitKind { FiveHour, Weekly, Daily, Monthly, BillingCycle }

/// What a window meters — one shape for every provider/feature. Claude/Codex
/// report a token-fraction; Cursor (paid) and post-June-2026 Copilot map to a dollar credit
/// pool. (Antigravity's "compute-effort" quota is discovery-gated with no sanctioned source
/// today — PRODUCT-PLAN §8 — so no live measure flows from it yet.) The legacy pre-June-2026
/// Copilot **request-count** measure is intentionally NOT modeled.
pub enum LimitMeasure {
    TokenFraction(f64),            // 0.0..=1.0. Claude's statusLine gives a 0–100
                                   // `used_percentage`; sanitize the RAW percentage
                                   // (see `status`) BEFORE dividing by 100.
    Spend { used_usd: Decimal, included_usd: Option<Decimal> }, // dollar credit pool + overage
}

/// Confidence in a limit reading. Claude's `statusLine` `rate_limits` field is buggy
/// (ARCHITECTURE §9.2): out-of-range / poisoned values (epoch in `used_percentage`, 900%)
/// are sanitized to `Unavailable`; an in-range-but-wrong value (e.g. a flat 100% with no
/// throttling) that diverges from Costroid's own local token volume for the window is
/// demoted to `Unverified` — flagged, never silently trusted *or* suppressed (a high
/// reading may be legitimately real). The local estimate is a validator when the field
/// is present, and the fallback when it's absent.
pub enum LimitStatus { Verified, Unverified, Unavailable }
```

> **Quota generalization — ✅ landed in T2** (see [PRODUCT-PLAN.md](PRODUCT-PLAN.md) §2a / §3 T2). The shapes above are the **shipped** generalized model: `LimitKind` spans `FiveHour`/`Weekly`/`Daily`/`Monthly`/`BillingCycle`; `used_fraction` was replaced by `measure: Option<LimitMeasure>` (`TokenFraction` or dollar-denominated `Spend { used_usd, included_usd }`); `LimitWindow` carries `captured_at` + `status: LimitStatus`. The `costroid-core` `LimitAvailability` type (sitting above the provider `LimitWindow`) gained `Unverified` + `Estimated` variants at the **availability/render layer only**, never on the provider `LimitWindow`: `Unverified` carries the measure of a present-but-cross-check-failed reading (shown flagged); `Estimated { volume_tokens: u64, estimated_usd: Option<Decimal> }` is the volume-based fallback shown when there is no trustworthy % (absent, sanitized-out, or aged-out-stale) but local usage exists — `estimated_usd` is `None` when the window's rows are unpriced (volume shown alone, never a guessed price). The legacy pre-June-2026 Copilot **request-count** measure is intentionally **cut** (not implemented). **What is emitted today:** Codex → `Verified` `TokenFraction` windows (`captured_at` from the rollout line); Claude → reads the sanctioned cache (its `captured_at` from the cache) → `Verified`/`Unverified`/`Unavailable`; the T5 writer (`setup-statusline` / `statusline --capture-only`) now populates that cache, and an absent cache (capture not set up, or before the first Pro/Max response) degrades to two `Unavailable` windows; Cursor → no windows (live-RPC-only). The **`Unverified` + `Estimated` producers landed in T4** (the core cross-check demote + stale age-out + volume-based `Estimated`), and **T6 shipped the render** of all five arms — measure-aware: `TokenFraction` → meter; `Spend` → "$used / $included used" (no meter, never a fabricated %); `Estimated` → token volume + estimated value (no meter); `Unverified` → a neutral meter plus the color-free " ? unverified" cue; with an always-on "as of HH:MM" freshness stamp. A live **`Spend` producer** (Cursor/Copilot) is still absent — it is discovery-gated (PRODUCT-PLAN §8), so no dollar-pool window flows to the render yet.

**The Opus weekly sub-cap is not a `LimitWindow`.** Claude's `statusLine` exposes only the overall `five_hour`/`seven_day` windows — there is no per-model (Opus/Sonnet) quota field. For an Opus-heavy user the Opus weekly may bind first, but its % is unobservable, so do **not** synthesize a `LimitWindow` with a fabricated fraction for it. Lead with the overall 7d % (the measurable number). The companion Opus-specific render — **Opus 7d token volume + estimated value from local logs** alongside the 7d % (itself a local-log approximation of Anthropic's window, labeled as such), with the cap-relative % marked unavailable — is **design intent, not yet shipped** (no `opus_weekly` field exists in code; STATUSLINE-CAPTURE-BRIEF §6): today the now screen renders the overall windows plus the generic per-model calendar-week subscription-value rows, with no Opus-specific line (ARCHITECTURE §8).

**A model used both ways** is distinguished by access path. Its API traffic produces FOCUS rows with `x_AccessPath = "api"` and a real pay-as-you-go cost estimate; its subscription traffic produces FOCUS rows with `x_AccessPath = "subscription"` carrying an *estimated equivalent value* (what that usage would cost at API rates), while its quota status shows up separately as `LimitWindow`s in the now-screen's limits section. The lanes are never summed together, and recommendations attach **only** to `x_AccessPath = "api"` rows, because only there does changing models change a real bill.

**Detecting access path (never guess).** Set `x_AccessPath` from evidence, not assumption. For **Codex**, the presence of `rate_limits` windows in the rollout is a subscription signal → `subscription`. For **Claude Code**, derive from auth mode (subscription/OAuth login vs `ANTHROPIC_API_KEY`) using non-secret *presence* signals only — never read credential values. Use `api` only on a clear pay-as-you-go / API-key signal, and `unknown` only when no signal exists. Subscription-access rows carry their dollar figure as an estimate (`x_Estimated = true`), never a bill, and never feed the recommendation (frontier) view.

**Subscription usage is valued in Phase 1.** Costroid emits FOCUS rows for subscription token usage with `BilledCost`/`EffectiveCost` derived as an *estimated equivalent value* — what that usage would have cost at API rates — marked `x_AccessPath = "subscription"` and `x_Estimated = true`. This lane must be labeled unmistakably as estimated equivalent value, never presented as a bill or actual spend, never summed with the API lane, and never fed to recommendations. (The fuller "is my subscription worth it?" comparison — weighing this estimated value against the flat fee — remains a later feature; Phase 1 only produces and labels the valued rows.)

## Internal Rust shapes

`costroid-providers` parses raw logs into a provider-neutral intermediate, and `costroid-core` normalizes that into FOCUS rows via `costroid-focus`.

Intermediate (provider output):

```rust
/// One usage event as parsed from a provider's local logs. Provider-neutral.
pub struct UsageEvent {
    pub tool: ProviderId,             // ClaudeCode | Codex | Cursor
    pub model: String,
    pub timestamp: DateTime<Utc>,
    pub input_tokens: u64,
    pub output_tokens: u64,
    pub cache_read_tokens: u64,
    pub cache_write_tokens: u64,
    pub project: Option<String>,      // derived workspace/repo/cwd
    pub access_path: AccessPath,      // Api | Subscription | Unknown
}

pub enum AccessPath { Api, Subscription, Unknown }
```

FOCUS record (in `costroid-focus`; column names match the spec exactly via serde). **This listing is an ABRIDGED subset** — the shipped struct carries the *full* FOCUS 1.3 Cost & Usage column set (M6a/M6b), including `BillingAccountId`/`Name`/`Type`, `ServiceSubcategory`, `ProviderName`/`PublisherName` (see above), `InvoiceId`, `SkuMeter`/`SkuPriceDetails`, `PricingCurrency` (+ its unit-price/cost mirrors), the `CommitmentDiscount*` family, `CapacityReservation*`, and the remaining 1.3 columns, most null for a local AI-usage charge. **The struct in `crates/costroid-focus/src/lib.rs` (`FocusRecord`) is the authority for the full field set and the serialized column order**; the subset below shows only the columns Costroid actively populates with meaningful values:

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

    // Service & provider (active FOCUS 1.3 participating-entity columns)
    pub service_name: String,           // ServiceName
    pub service_category: String,       // ServiceCategory = "AI and Machine Learning"
    pub service_provider_name: String,  // ServiceProviderName
    pub host_provider_name: String,     // HostProviderName
    pub invoice_issuer_name: String,    // InvoiceIssuerName

    // SKU / pricing. The pricing columns are null when sku_price_id is None
    // (no priced rate) per FOCUS 1.3; populated (per-token) when a rate is found.
    pub sku_id: Option<String>,             // SkuId (always populated)
    pub sku_price_id: Option<String>,       // SkuPriceId (None on unpriced rows)
    pub pricing_category: Option<String>,   // PricingCategory = Some("Standard") | None
    pub pricing_quantity: Option<Decimal>,  // PricingQuantity = token count | None
    pub pricing_unit: Option<String>,       // PricingUnit = Some("tokens") | None
    pub list_unit_price: Option<Decimal>,   // ListUnitPrice (per token) | None
    pub contracted_unit_price: Option<Decimal>, // ContractedUnitPrice (per token) | None

    // Consumption
    pub consumed_quantity: Option<Decimal>, // ConsumedQuantity = token count | None
    pub consumed_unit: String,              // ConsumedUnit = "tokens", always populated
                                            // (non-nullable; even on unpriced rows)

    // Custom (x_ prefix per FOCUS)
    #[serde(rename = "x_Model")]         pub x_model: String,
    #[serde(rename = "x_TokenType")]     pub x_token_type: String,
    #[serde(rename = "x_AccessPath")]    pub x_access_path: String,
    #[serde(rename = "x_Estimated")]     pub x_estimated: bool,
    #[serde(rename = "x_Tool")]          pub x_tool: String,
    #[serde(rename = "x_Project")]       pub x_project: Option<String>,
    #[serde(rename = "x_PricingStatus")] pub x_pricing_status: String,
    // Raw token count, ALWAYS populated (even when ConsumedQuantity is null);
    // the aggregation engine reads this for token totals.
    #[serde(rename = "x_ConsumedTokens")] pub x_consumed_tokens: Decimal,
}
```

## Export shapes

`costroid export` serializes FOCUS rows. Two formats, identical data:

- **JSON** (`--format json`): a JSON object wrapping the rows — `{ "focusVersion": "1.3", "rows": [ ... ] }` — where each element of `rows` is a `FocusRecord` keyed by FOCUS column name (PascalCase, with `x_` custom columns). This wrapper is the **shipped** shape (`FocusExportEnvelope` in `crates/costroid-focus/src/lib.rs`; it carries the FOCUS version for forward-compatibility). There is no top-level `currency` field — each row carries `BillingCurrency`; adding one remains an open future option, not built. A bare array is never emitted.
- **CSV** (`--format csv`): the first row is the exact FOCUS column-name header (PascalCase, `x_` columns appended) — emitted even for a zero-row export, so consumers always see the schema; one row per charge; decimals formatted plainly; timestamps RFC 3339.

Limits are **not** part of the FOCUS export (they are not charges). If a limits dump is needed, export `LimitWindow`s to a separate file/stream, clearly distinct from the cost data.

## Per-provider parsing

Each adapter discovers local data (WSL-aware: also check `/mnt/c/Users/<user>/...` when the tool runs on Windows) and maps it to `UsageEvent`/`LimitWindow`. The **Claude Code and Codex notes below are confirmed against real installs** (and guarded by committed fixtures); **Cursor remains detect-only** — there is nothing local to parse. Formats still drift, so when extending an adapter re-confirm the provider's current local path and log schema against a real install plus a committed fixture, never assume.

- **Claude Code.** *(Confirmed against a real install.)* Usage lives in conversation-transcript JSONL under `projects/`: `~/.claude/projects/<project>/<session>.jsonl`, and (Claude Code v1.0.30+) `~/.config/claude/projects/**/*.jsonl`; honor `CLAUDE_CONFIG_DIR` (a comma-separated root list) before the defaults. Each assistant turn exposes `message.model`, `timestamp`, `cwd`, and `message.usage.{input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens}`; the **project** is derived from `cwd` / the project directory. Logs are retained ~30 days by default. **No quota/reset fields exist in the session logs** — but Claude Code's live 5h/7d subscription limits arrive *locally, inside the Phase-1 trust envelope,* through its **`statusLine` hook**: a `rate_limits` block (`five_hour`/`seven_day`, each `used_percentage` 0–100 + `resets_at`; Pro/Max only, after the first API response; zero API tokens), captured by the statusline integration and side-written to a no-secret local cache (ARCHITECTURE §8). The cache is a JSON file at `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` — `{ captured_at` (RFC3339)`, five_hour { used_percentage` 0–100`, resets_at` (epoch seconds **or** RFC3339)` }, seven_day { … } }`. **The reader is built (T4):** Claude `parse_limits` reads + sanitizes this cache; an absent/unreadable/malformed cache, or an absent window key, degrades to two `Unavailable` windows (never an error). **The writer shipped (T5):** `costroid setup-statusline` wires Claude Code's `statusLine` to tee its `rate_limits` into this cache — either by making `costroid statusline` the status line (which captures opportunistically on piped stdin) or by injecting a `costroid statusline --capture-only` snippet into an existing one — so the cache is populated after the next Pro/Max response (still absent until capture is set up, before the first response, or for API-key users — the reader degrades those to `Unavailable`). That field is **buggy** — absent for API keys, poisoned (epoch in `used_percentage`) at session start, and occasionally false-in-range (a flat 100% with no throttling) — so the reader **sanitizes the RAW percentage before ÷100** (out of range, i.e. `<0` or `>100`, **or** `used_percentage == resets_at` → no data) and the core **cross-checks** a high reading against local token volume, mapping it to a `LimitWindow` with `status` = `Verified` / `Unverified` / `Unavailable` accordingly (ARCHITECTURE §9.2); never guess. The per-model (Opus/Sonnet) weekly sub-cap is **not** exposed (see the Opus note above). Map each nonzero token meter to its own FOCUS row; cost is estimated from token × price.
- **Codex.** *(Confirmed against a real install.)* Rollout JSONL under `~/.codex/sessions/**/*.jsonl` (honor `CODEX_HOME`; Windows-mounted equivalent under WSL). Token usage is under `payload.info.last_token_usage`; subscription rate limits are under `payload.rate_limits.primary` (5-hour, `window_minutes` 300) and `payload.rate_limits.secondary` (weekly, `window_minutes` 10080), each with `used_percent` and an epoch `resets_at`. Model and `cwd` come from the rollout metadata / turn context. Map usage to FOCUS rows (`x_Tool = "codex"`); map the **latest** rollout entry's rate limits to `LimitWindow`s. Parse the JSONL only — `state_*.sqlite` is not needed.
- **Cursor.** Cursor keeps **no local usage or quota** — Cursor serves these live from its own backend (not to local logs), and Costroid does **not** call Cursor's internal endpoints, so there is nothing to parse from local logs (ARCHITECTURE §4). Cursor is **detection only**: detect its presence + the selected model from the `~/.cursor` config (honor `CURSOR_DATA_DIR`), label it **beta**, and surface usage/quota as *"unavailable — no sanctioned source."* Never read chat content; never guess a number. Cursor's quota shape is resolved: paid plans use a **monthly, dollar-denominated credit pool** plus usage-based overage (a billing-cycle, spend-$ window) as the primary limit, with the **daily token window being the free-tier rate-limit**; both map to the generalized model (a `BillingCycle`/`Monthly` `Spend` window plus a `Daily` `TokenFraction` window — see the quota-generalization note above; the types landed in T2). Only a *sanctioned source* is missing: a live Cursor fetch is **discovery-gated** (PRODUCT-PLAN §8) — pursued only if Cursor publishes a documented usage/billing API or first-party OAuth, **never** by reusing a local Cursor session against `api2.cursor.sh` (that would violate Cursor's ToS). Until then Cursor quota always stays "unavailable."

When a provider isn't installed or no data is found, skip it gracefully — never error the whole run.

### Estimate vs. invoice reconciliation

Local cost is always an **estimate**: `Σ(tokens × unit price)` from the bundled pricing table, with no visibility into negotiated rates, free tiers, or credits. Every Phase 1 FOCUS row therefore carries `x_Estimated = true`, and `EffectiveCost`/`BilledCost` reflect the estimate.

The provider invoice is the source of truth. Reconciliation aggregates Costroid's estimated cost per UTC day (and per model where honestly supported), compares it to the vendor-billed report, and surfaces the signed variance — never silently "correcting" the estimate. The engine that does this is built as of T9c (see [Reconciliation engine](#reconciliation-engine-the-comparison-as-built-in-t9c) below); the calibration the older prose mentioned is **not** implemented (it would at most be a labeled output value, never a mutation of FOCUS rows or the pricing table). Subscriptions reconcile differently and are **out of scope** here: the "bill" is the flat fee, so the relevant comparison is the *effective* estimate (subscription usage valued at API rates) against the fee — the basis for the future "is the plan worth it?" view (T10+), not this engine.

#### Vendor-report shapes (the invoice side; as built in T9b)

The parsed vendor usage/cost shapes live in **`costroid-core::vendor_report`** (provider-neutral), so the reconciliation engine (T9c) stays pure-core with **no `costroid-connect` dependency** — the dependency direction is `connect → core`, never `core → connect`. The `costroid-connect` adapters (`AnthropicAdapter`, `OpenAiAdapter`; T9b) fetch the vendor JSON and parse it *into* these types; Gemini has no adapter (see below). These shapes are **not** FOCUS rows (they are the vendor's own billed figures, pre-normalization) and are not part of `costroid export`.

- **Money is one canonical type, built only at a unit-tagged parse boundary.** `UsdAmount(Decimal)` is always **US dollars**, always exact (never `f64`). It is constructed *only* through `UsdAmount::from_decimal_cents_str` (Anthropic `cost_report` — a decimal string in **cents**, `÷100` by an exact decimal-point shift) or `UsdAmount::from_json_dollars_str` (OpenAI — a JSON number's **literal text** in dollars, via `serde_json`'s `raw_value`, never `f64`). Picking the wrong constructor is the only way to get a 100× error, and the constructor is chosen at the parse site per the vendor's pinned encoding (`docs/proposals/T9-PIN-PROPOSAL.md` §6). `MoneyParseError` carries the offending amount text (a response value — never a secret).
- **Cost report:** `VendorCostReport { days: Vec<VendorCostDay>, caveats: CostReportCaveats }`. Each `VendorCostDay { date: NaiveDate (UTC), total: UsdAmount, by_model: Vec<ModelCostAmount>, line_items: Vec<CostLineItem> }` carries the exact daily billed total, a per-model rollup, and the lossless raw result rows. `ModelCostAmount`/`CostLineItem` carry an `AmountConfidence` (`Exact` for Anthropic's vendor-attributed `model`; `DerivedBestEffort` for OpenAI's per-model figures parsed from the undocumented `line_item` string). `CostLineItem` preserves `label`, `model`, `cost_type` (Anthropic `tokens|web_search|code_execution|session_usage`), and `service_tier` (Anthropic `standard|batch`).
- **Usage report:** `VendorUsageReport { days: Vec<VendorUsageDay>, caveats: UsageReportCaveats }`. Each `VendorUsageDay { date, by_model: Vec<ModelTokenUsage> }`; `ModelTokenUsage` is normalized to Costroid's four meters (`input_tokens` uncached, `output_tokens`, `cache_read_tokens`, `cache_creation_tokens`) plus `num_requests: Option<u64>`. Per-vendor normalization: **Anthropic** `input_tokens = uncached_input_tokens`, `cache_creation_tokens = ephemeral_5m + ephemeral_1h`; **OpenAI** `input_tokens = input_tokens − input_cached_tokens` (their `input_tokens` *includes* cached), `cache_read_tokens = input_cached_tokens`, `cache_creation_tokens = 0` (not reported), `num_requests = num_model_requests`.
- **Honesty caveats are TYPED data (T9c/T10 cannot drop them).** `CostReportCaveats { priority_tier_absent, per_model_derived_best_effort }` — Anthropic sets `priority_tier_absent = true` (Priority-Tier dollars are absent from `cost_report`, so totals understate the bill for priority-tier users); OpenAI sets `per_model_derived_best_effort = true`. `UsageReportCaveats { responses_api_coverage_unconfirmed }` — OpenAI sets it `true` (the Responses API, which Codex rides, may not be covered by `usage/completions`; pending the live check), Anthropic `false`.
- **First-class "unavailable", never an error loop.** `CostReportOutcome` / `UsageReportOutcome` are each `Available(report)` **or** `Unavailable(VendorReportUnavailable)`. `VendorReportUnavailable` covers `NotConnected`, `WrongKeyClass { expected_prefix }` (detected from the key prefix before any request), `AuthenticationFailed` (401), `AccessForbidden { hint }` (403; `hint`: `IndividualAccount` / `MemberNotOwner` / `AwsOrg` / `Unknown`), `RateLimited` (429, backoff-exhausted), `ServerUnavailable { status }` (5xx/404 outage, backoff-exhausted), `RequestRejected { status }`, `NoSanctionedStaticKeyApi`, and `FetchFailed` (a **detail-free** residual hard-failure reason — a transport failure, an oversized/unparseable body, or a keychain read error; the status-mapped outages 401/403/429/5xx/other-4xx have their own variants above, and the soft ones degrade *inside* the fetch, so this is the leftover hard failure a caller surfaces without aborting a multi-vendor view; it carries no detail string, so it can never leak a secret or a URL — its `.message()` is `"the invoice request could not be completed"`). The `apps/cli` `reconcile` loop degrades a per-vendor hard fetch error to `FetchFailed` so one failing vendor no longer aborts the whole multi-vendor reconcile (the local estimate still shows). **Gemini** has no adapter: `gemini_cost_report()` / `gemini_usage_report()` return `Unavailable(NoSanctionedStaticKeyApi)`, whose `.message()` is the exact pinned string `"unavailable — no sanctioned static-key usage API"` (`GEMINI_UNAVAILABLE_MESSAGE`).
- **`DateRange`** (`[start, end)`, UTC) is the request range; the adapters format it per vendor (Anthropic RFC 3339, OpenAI Unix seconds) and parse bucket dates back to a UTC `NaiveDate` via `utc_date_from_rfc3339` / `utc_date_from_unix_seconds` (all in core, so `costroid-connect` names neither `chrono` nor `rust_decimal`).

#### Reconciliation engine (the comparison; as built in T9c)

The engine lives in **`costroid-core::reconcile`** (re-exported from the crate root). It is **pure-core, fixture-tested, and never fetches** — a future caller (T10) fetches the vendor report via `costroid-connect` and hands both sides in. It compares **one vendor's** cost report against the local estimate, per UTC day and per model.

- **The local-estimate side: `LocalCostEstimate`.** Estimated dollars per **(UTC day, model)**, **API lane only** — the only lane with a vendor invoice (subscription/unknown lanes are excluded). `LocalCostEstimate::from_focus_records(&[FocusRecord])` keeps API-lane rows, buckets them by **UTC day** (`charge_period_start.date_naive()`) and `x_Model`, and sums the estimated `BilledCost` (`= EffectiveCost` for an estimate). UTC-day bucketing is deliberate — the vendor reports bill in UTC-midnight daily buckets (live-confirmed in T9b), so the two sides must bucket the same way; this is **not** the local-timezone day grouping the trends view uses. The caller scopes the rows to the one vendor under reconciliation before building it.
- **The entry point: `reconcile_cost(&LocalCostEstimate, &CostReportOutcome) -> CostReconciliation`.** On `Available`, it compares every UTC day in the union of (local days, vendor days). On `Unavailable`, it still surfaces the local estimate day by day, but every vendor figure becomes `Unavailable(ReportUnavailable(reason))` — so e.g. Gemini reconciles to "unavailable", never to a fabricated `$0` delta.
- **`CostReconciliation { days: Vec<DayReconciliation>, caveats: CostReportCaveats, report: ReconciledReportStatus }`.** `caveats` is the vendor report's `CostReportCaveats` carried through **unchanged** (`priority_tier_absent`, `per_model_derived_best_effort`) — flattening either away is a bug; `default` (all false) when the report is unavailable. `report` is `Available` or `Unavailable(VendorReportUnavailable)`.
- **`DayReconciliation { date, local_estimate: UsdAmount, vendor_billed: VendorBilled, variance: Option<UsdAmount>, variance_pct: Option<Decimal>, by_model: Vec<ModelReconciliation> }`** and **`ModelReconciliation { model, local_estimate, vendor_billed, confidence: Option<AmountConfidence>, variance, variance_pct }`.** `variance = local_estimate − vendor_billed` (signed: **positive** ⇒ the estimate exceeds the invoice; **negative** ⇒ the invoice exceeds the estimate). `variance_pct = 100 × variance / vendor_billed` (relative to the source of truth), at full `Decimal` precision — any rounding is T10's. The per-model `confidence` carries OpenAI's `DerivedBestEffort` label (`Exact` for Anthropic), so the per-model best-effort caveat survives both on the result *and* per row.
- **Vendor-side absence is TYPED, never `$0`: `VendorBilled = Billed(UsdAmount) | Unavailable(BilledAbsence)`.** `BilledAbsence` is `ReportUnavailable(VendorReportUnavailable)` (the whole report failed/Gemini), `DayNotCovered` (the report doesn't span this UTC day — history depth/latency), or `ModelNotInReport` (the day is covered but the vendor attributes nothing to this model). When `vendor_billed` is `Unavailable`, `variance`/`variance_pct` are `None` — no fabricated delta. `variance_pct` is also `None` when the vendor billed `$0` (the percentage is undefined; the signed dollar variance is still reported). A **local** `$0`, by contrast, is a *genuine* estimate (no observed usage) — only the vendor side is guarded against fabricated zeroes, so a model the vendor billed but Costroid never saw shows `local_estimate = $0` against the real billed figure (an honest, useful "missed usage" signal).
- **What it does NOT consume.** Only the **cost** report. The usage report's `responses_api_coverage_unconfirmed` caveat bounds a *token-side* comparison the engine does not perform; OpenAI's `costs` bills all traffic (including the Responses API that Codex rides), so the dollar **day totals are complete** and that caveat does not apply to them. A token-side reconciliation (where that caveat would live) is deferred to when it is surfaced (T10+). The OpenAI per-model **dollar** figure remains best-effort, carried as `DerivedBestEffort`.
- **No FOCUS-schema change.** The reconciliation output is its own shape, not new FOCUS columns; `x_Estimated` etc. stay exactly as specified above. The engine adds **no dependency** to `costroid-core` — the `connect → core` direction holds by construction.

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

The cost calculator joins each `UsageEvent` token meter to the matching `model` + `meter` rate. The pricing table stores rates **per 1M tokens**; the calculator derives a **per-token** `ListUnitPrice` (`rate ÷ 1,000,000`), sets `PricingQuantity` to the token count and `PricingUnit` to `"tokens"`, and computes `ListCost = per-token price × token count` — the same dollar value as the prior per-1M arithmetic, just represented per token.

**Unpriced rows (no matching rate, including while the table is the empty placeholder):** the four cost columns (`BilledCost`, `EffectiveCost`, `ListCost`, `ContractedCost`) stay present and `0`; `SkuPriceId` is `null`; and because FOCUS 1.3 requires *"`ConsumedQuantity` / `PricingQuantity` / `PricingUnit` / `PricingCategory` / `ListUnitPrice` / `ContractedUnitPrice` MUST be null when `SkuPriceId` is null,"* all of those go `null` too (`ConsumedUnit` is **not** in that set — it stays `"tokens"` on unpriced rows as well). The raw token count is preserved on the always-populated `x_ConsumedTokens` column (which the aggregation engine reads, so unpriced usage is never dropped from totals), and the row is flagged `x_PricingStatus = "missing_price"` (or `"unknown_model"` when the model is absent from the table entirely). Never substitute a guessed price.

> **Conformance dependency (do not change lightly):** nulling those columns on `SkuPriceId`-null rows coexists with FOCUS's *"MUST NOT be null when `ChargeCategory` is Usage/Purchase"* sibling rules only because Costroid leaves `ChargeClass` and `CommitmentDiscountStatus` **null** on these rows — the validator's `<> 'value'` conditions are then NULL (SQL three-valued logic) and the sibling rules do not fire. Populating either field on an unpriced row would reintroduce a hard conflict. There is a matching code comment at `FocusRecord::unpriced_usage`.

## Grouping dimensions

The trends screen aggregates over a **period** (`day` / `week` / `month` / `year`, bucketed by `ChargePeriodStart` in the user's local time zone) and a **group**:

- **model** — group by `x_Model` (equivalently the model component of `SkuId`).
- **app / project** — group by `x_Project`, derived per provider from the log's workspace / repository / working-directory field (Claude Code session dir, Codex cwd, Cursor workspace). When it can't be determined, bucket as `"unknown"` rather than dropping the row.
- **total** — aggregate across everything.

Aggregation sums `BilledCost` (and `EffectiveCost`) for cost views; it never sums `LimitWindow` data, which has no dollars.