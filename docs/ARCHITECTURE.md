# Costroid — Architecture (technical canon)

The single **technical** source of truth. When this doc disagrees with the code, **the code wins** — verify any symbol/path here against the source before relying on it. Scope/sequencing and the going-forward roadmap live in [ROADMAP.md](ROADMAP.md); the design language lives in [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md). Status: **v0.6.0 — feature-complete** (Steps 0–6 done).

## 1. Purpose & principles

Costroid is a local, private FinOps tool for AI coding assistants: it shows API spend by model **and** the subscription quota windows no invoice exists for (Claude/Codex 5-hour + weekly caps with reset countdowns), by default entirely from local data with nothing leaving the machine. It is *metadata-only* (never prompt/completion content), not a platform/proxy/chatbot. Invariants the whole build inherits: the default/local-only build makes **zero** network calls; **no telemetry** ever; secrets live **only** in the OS keychain; cost is **always an estimate** (tokens × price), never the authoritative bill; **no `unwrap`/`expect`/`panic` in library crates**; `--plain` ASCII for every visual and **never color alone**; **permissive licenses only** (no GPL/AGPL/LGPL/SSPL). Rust, edition 2021, Apache-2.0; MSRV **1.88** (libs + CLI), **1.92** (the bar); `Cargo.lock` committed.

## 2. Crates & dependency direction

7 published packages — 5 libraries + 2 apps:

| Crate | Role |
|---|---|
| `costroid-focus` | FOCUS 1.3 record types + (de)serialization. Pure data, no internal deps. |
| `costroid-providers` | `Provider` trait + `Capability` descriptor; Claude Code / Codex / Cursor adapters; WSL-aware log discovery. Emits provider-neutral `UsageEvent`/`LimitWindow`. No internal deps. |
| `costroid-core` | Engine: orchestration, cost calc, FOCUS normalization, bundled pricing, `bench`/frontier, `vendor_report`, `reconcile`, display helpers. No UI code, no `connect` dep. |
| `costroid-config` | Shared **read-only** `[budget]`/`[alerts]` TOML schema + loader for both apps. Leaf; no network/keychain/writer. |
| `costroid-connect` | **All** network + credential code. Feature-gated, **off by default**. |
| `costroid` (`apps/cli`) | clap CLI + Ratatui TUI + statusline + `--live` + `--plain`. |
| `costroid-bar` (`apps/bar`) | egui/eframe + `tray-icon` taskbar. |

**Direction (no cycles):** `apps → core → {providers, focus}`; `apps → config → core`; under the apps' off-by-default `connect` feature, `app → connect → core`. `apps/cli → {core, config, providers}`; `apps/bar → {core, config}`. crates.io publish ladder: **focus → providers → core → config → connect → costroid → costroid-bar**.

The `apps/bar` source **names no `rust_decimal`** — money/share/multiple render to string via core display helpers (`format_money_usd`, `format_over_by_usd`, `decimal_share_percent`, `anomaly_multiple_phrase`, `forecast_daily_fractions`, `now_api_spend_display`).

## 3. Security & credential boundary (the trust story)

- **Secrets only in the OS keychain** (`keyring`: macOS Keychain / Windows Credential Manager / Linux Secret Service). Never written to disk, config, or logs; carried in memory as `secrecy::SecretString`. The non-secret on-disk index `connections.json` (vendor slugs + control-char-sanitized org labels) is written `0600` on Unix.
- **No Costroid backend** — credentials flow only device↔provider. TLS via **rustls** (no OpenSSL), trust from the **OS-native root store** (`rustls-native-certs`, never a bundled `webpki-roots`).
- **`AuthorizedClient` (`ureq` + `rustls`, blocking, no async runtime)** is the only HTTP client any build may carry. Constructed over **one** authorized host; HTTPS-only + GET-only; off-host URLs refused in the type **before any I/O**; redirects + env-proxies disabled; bounded timeouts + body size. Network happens **only** under `--features connect` on an explicit user-initiated `connect` / `connections --check` / `reconcile` action.
- **No certificate pinning** — TLS validates against the OS trust store only, so a planted/corporate-MITM root could intercept a connect/reconcile request (which carries an org-wide admin key). Standard native-roots residual; SPKI pinning is deferred hardening (disclosed in [SECURITY.md](../SECURITY.md)). The connect flow prints a blast-radius **WARNING** before the key is pasted and recommends a dedicated, instantly-revocable key.
- **Provider logs are untrusted input** — parsed defensively; malformed data yields a clean error or "unavailable", never a crash, and the parser evaluates nothing from log content.

**Auth source ladder** — sources are chosen most-sanctioned-first; **only tiers 0–3 are ever built, tier 4 is the ToS line:**

0. **Local artifacts** — logs/config on disk, no login/credential/network (today's default).
1. **Sanctioned push/hook** — Claude Code's `statusLine` `rate_limits` capture; still zero network, zero credential.
2. **Sanctioned OAuth** — first-party where documented (e.g. GitHub; **deferred**): system browser + loopback redirect, PKCE, token to keychain only.
3. **Your own API key** — Anthropic/OpenAI *usage* APIs with the user's own admin key (Step 4 reconcile). Gemini: deferred (no sanctioned static-key usage API).
4. **NEVER** reuse any credential, session, or token against a non-sanctioned, undocumented, or internal endpoint (incl. Cursor's `api2.cursor.sh`); **never read browser cookies.** That datum stays "unavailable," never fetched.

Enforcement: a per-binary resolved-graph **forbidden-crates test** (`apps/cli/tests/offline.rs` — the CLI graph forbids all networking/TLS/telemetry + `async-io`; `--features connect` is a reviewed subset-allowlist `CONNECT_ALLOWED`; the bar admits only the local-IPC AccessKit subtree `BAR_ACCESSKIT_ALLOWED`) **+** the strace offline-acceptance script (`scripts/offline_acceptance.sh` — default + feature-on no-network baselines, a netns fail-closed connect check, and a `costroid-bar --self-check` no-`AF_INET` run).

## 4. Degrade-never-crash + the Claude `rate_limits` rules

Any source that is absent, malformed, or stale returns data **or** a clean "unavailable" — never a crash. The Claude `statusLine` `rate_limits` field is untrusted and **wrong in two distinct ways**:

- **Out-of-range / poisoned** (epoch in `used_percentage`; impossible values like 900%): a value `<0`, `>100`, or `== resets_at` means *no data* — sanitize the RAW percentage **before** dividing by 100.
- **In-range but wrong** (e.g. a flat 100% with no throttling): no range check catches it. Guard with a **divergence cross-check** (`finalize_limit_status`): if the % is high (≥ `0.80`) but local token volume for the window is trivial (`< 5_000`, `UNVERIFIED_TOKEN_FLOOR`), **demote to `Unverified`** rather than render a confident "you're maxed." The cross-check only *flags*, never corrects (a high reading may be legitimately real). So the local estimate is a **validator when the field is present**, a fallback when it's absent.

**Staleness** is push-only (the field moves only on a new Pro/Max API response; `refreshInterval` re-renders but does not freshen it) and breaks two ways: *high across an idle reset* is **fixable** (age out via `resets_at` to "unknown"); *low because quota was spent on claude.ai chat* (same 5h/7d pool) is **not fixable, only disclosable**. The hook carries `five_hour`/`seven_day` only — **no per-model weekly window**; never synthesize an Opus cap-%. Lead with the overall 7d % as the measurable number.

Connect side: a hard per-vendor fetch failure degrades to detail-free `VendorReportUnavailable::FetchFailed` (carries no string that could leak a secret/URL) so the local estimate still renders and other vendors still reconcile — one vendor failing never aborts a multi-vendor `reconcile`.

## 5. Data flow

`discover()` → parse usage + limits → **`costroid-core` normalizes to FOCUS + estimates cost** → aggregate (period, group) → render (now / trends / statusline) **or** export (JSON/CSV). Renderers and exporters are pure consumers of one in-memory model. `discover()` returns "no data" (not an error) when a provider is absent — missing providers are skipped. Adding a provider should require **no changes outside `costroid-providers`**.

**Log discovery (WSL-aware):** Claude Code = `~/.claude/projects/**/*.jsonl` + `~/.config/claude/projects/**/*.jsonl` (honor `CLAUDE_CONFIG_DIR`, comma-separated, before defaults); Codex = `~/.codex/sessions/**/*.jsonl` (honor `CODEX_HOME`); Cursor = presence + selected model from `~/.cursor` config only (honor `CURSOR_DATA_DIR`). Merge all roots. Detect WSL via `/proc/sys/kernel/osrelease`; under WSL scan `/mnt/c/Users/*` for the profile(s) that actually hold logs (fallback `USERPROFILE`, then legacy `$USER`). Claude's live cache is at `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` (no secret).

## 6. Data model

### FOCUS 1.3 output

Each unit of API usage → FOCUS Cost & Usage rows with `ChargeCategory = "Usage"`. Input / output / cache-read / cache-write each become a **separate row** (distinct `SkuId`/`SkuPriceId` + `x_TokenType`) so quantities and unit prices stay coherent. `ServiceCategory = "AI and Machine Learning"`, `ChargeFrequency = "Usage-Based"`. Unit prices are **per token**; `PricingUnit = "tokens"`.

- **Active participating-entity columns:** `ServiceProviderName` / `HostProviderName` / `InvoiceIssuerName` (same vendor for API usage). The **deprecated** `ProviderName` / `PublisherName` are *also* emitted (mirroring the active values) only because the bundled 1.3 validator requires their presence — drop with a 1.4 ruleset.
- **Costs** (`Decimal`, in `BillingCurrency`): `BilledCost` (mandatory) = `EffectiveCost` for an estimate; `ListCost` = `ContractedCost` = `ListUnitPrice × PricingQuantity`.
- **SKU/pricing nullability:** `SkuId` always populated; on unpriced rows `SkuPriceId` is null, which (per FOCUS 1.3) forces `PricingCategory` / `PricingQuantity` / `PricingUnit` / `ListUnitPrice` / `ContractedUnitPrice` / `ConsumedQuantity` to null. `ConsumedUnit` stays `"tokens"` always (non-nullable, not in that rule). The four cost columns stay present and `0`. This coexists with the "MUST NOT be null when ChargeCategory is Usage" sibling rules only because `ChargeClass`/`CommitmentDiscountStatus` stay null (SQL three-valued logic). **Never substitute a guessed price.**
- **Custom `x_` columns:** `x_Model`, `x_TokenType` (input|output|cache_read|cache_write), `x_AccessPath` (api|subscription|unknown), `x_Estimated`, `x_PricingStatus` (priced|missing_price|unknown_model), `x_Tool` (claude-code|codex|cursor), `x_Project`, and **`x_ConsumedTokens`** (raw count, **always populated** — the aggregation engine totals from it, so unpriced usage is never dropped).
- `BillingAccountId`/`Name`/`Type` carry honest non-billing placeholders (Costroid has no billing identity). `FocusRecord` in `crates/costroid-focus/src/lib.rs` is the authority for the full column set + serialized order.

### Three lanes, never summed across each other

(1) **API usage** → FOCUS rows, real cost estimate, `x_AccessPath = "api"`; (2) **subscription usage** → FOCUS rows valued at API-equivalent (`x_Estimated = true`, `x_AccessPath = "subscription"`), labeled estimated value, **never a bill**; (3) **subscription quota** → `LimitWindow`s (%). The cross-provider "total" is **per-lane**. Recommendations attach **only** to API rows. Access path is detected from evidence (Codex: `rate_limits` present ⇒ subscription; Claude: from auth-mode *presence* signals, never reading credential values), `unknown` otherwise.

### Core Rust shapes

```rust
// costroid-providers — provider-neutral intermediate
pub enum ProviderId { ClaudeCode, Codex, Cursor }
pub enum AccessPath { Api, Subscription, Unknown }
pub struct UsageEvent {                  // one parsed usage event
    pub tool: ProviderId, pub model: String, pub timestamp: DateTime<Utc>,
    pub input_tokens: u64, pub output_tokens: u64,
    pub cache_read_tokens: u64, pub cache_write_tokens: u64,
    pub project: Option<String>, pub access_path: AccessPath,
}
pub enum LimitKind { FiveHour, Weekly, Daily, Monthly, BillingCycle }
pub enum LimitMeasure {                   // what a window meters
    TokenFraction(f64),                   // 0.0..=1.0 (sanitize raw % BEFORE ÷100)
    Spend { used_usd: Decimal, included_usd: Option<Decimal> },  // dollar credit pool
}
pub enum LimitStatus { Verified, Unverified, Unavailable }   // confidence in a reading
pub struct LimitWindow {                  // a quota window — NOT a FOCUS row, no summable cost
    pub tool: ProviderId, pub plan: Option<String>, pub kind: LimitKind,
    pub measure: Option<LimitMeasure>,    // None ⇒ no usable number
    pub resets_at: Option<DateTime<Utc>>, // parsed defensively (epoch s AND ISO)
    pub captured_at: DateTime<Utc>,       // freshness (UNIX-epoch sentinel when Unavailable)
    pub status: LimitStatus, pub label: Option<String>,
}
```

The legacy pre-June-2026 Copilot **request-count** measure is intentionally **not** modeled. Above the provider `LimitWindow`, `costroid-core` carries the availability/render type with **five arms**:

```rust
pub enum LimitAvailability {
    Available  { measure, resets_at, reset_in_seconds },
    Partial    { measure: Option<LimitMeasure>, resets_at, reset_in_seconds, reason },
    Unverified { measure, resets_at, reset_in_seconds },  // present but cross-check-failed
    Estimated  { volume_tokens: u64, estimated_usd: Option<Decimal> },  // volume fallback, no % / no measure
    Unavailable{ reason },
}
```

`Unverified` + `Estimated` exist **only** at this layer, never on the provider `LimitWindow`. `estimated_usd` is `None` when the window's rows are unpriced (volume shown alone, never a guessed price).

### Per-provider field paths

- **Claude Code** — transcript JSONL: `message.model`, `timestamp`, `cwd` (→ project), `message.usage.{input_tokens, output_tokens, cache_read_input_tokens, cache_creation_input_tokens}`. **No quota in session logs** — live 5h/7d arrive via the `statusLine` `rate_limits` block (`five_hour`/`seven_day` → `used_percentage` 0–100 + `resets_at`; Pro/Max only, after the first API response; zero API tokens) side-written to the no-secret cache (`captured_at` RFC3339; each window `used_percentage` + `resets_at` epoch-seconds-or-RFC3339). The reader sanitizes + cross-checks (§4); absent/malformed cache → two `Unavailable` windows.
- **Codex** — rollout JSONL: usage at `payload.info.last_token_usage`; limits at `payload.rate_limits.primary` (5h, `window_minutes` 300) + `.secondary` (weekly, `window_minutes` 10080), each `used_percent` + epoch `resets_at`; map the **latest** entry. Parse JSONL only (`state_*.sqlite` not needed).
- **Cursor** — **detection only**: presence + selected model; cost/quota *"unavailable — no sanctioned source."* Quota *shape* is resolved (paid = monthly $-credit pool `BillingCycle`/`Monthly` `Spend` + overage; free-tier `Daily` `TokenFraction` rate-limit) — only a sanctioned *source* is missing. Discovery-gated; never via session reuse.

### Bundled pricing schema

Embedded in `costroid-core` via `include_str!` at `crates/costroid-core/pricing/pricing.vN.json` (cargo packages only files under the crate dir). Records `as_of` + `sources`; rates per meter (input/output/cache_read/cache_write) **per 1M tokens**; the calculator derives the per-token price (rate ÷ 1e6). Catalog key = model id (e.g. `claude-sonnet-4-6`), not display name. Works fully offline; figures sourced at build time, never hardcoded.

```json
{ "schema_version": "1", "as_of": "YYYY-MM-DD", "currency": "USD",
  "sources": ["https://<provider-pricing-page>"],
  "models": [ { "provider": "<id>", "model": "<id>", "service_name": "<ServiceName>",
    "rates": [ { "meter": "input", "unit": "1M_tokens", "price": <decimal> }, … ] } ] }
```

### Export shapes

- **JSON** (`--format json`): a wrapper `{ "focusVersion": "1.3", "rows": [...] }` (`FocusExportEnvelope`; **never** a bare array).
- **CSV** (`--format csv`): exact FOCUS PascalCase header (`x_` columns appended), emitted even for a zero-row export; one row per charge; decimals plain; timestamps RFC 3339.
- `LimitWindow`s export **separately**, never mixed into the cost data (they are not charges).
- **Grouping/aggregation:** by `x_Model`, by `x_Project` (`"unknown"` when undeterminable), or `total`; period buckets by `ChargePeriodStart` in the user's **local** time zone. Sums `BilledCost`/`EffectiveCost`; never sums `LimitWindow` data.

### Reconciliation (vendor-report + reconcile, pure-core)

`costroid-core::vendor_report` holds provider-neutral invoice shapes the connect adapters parse into; `costroid-core::reconcile` compares one vendor's `CostReportOutcome` against `LocalCostEstimate` per **UTC day** (vendors bill UTC-midnight buckets — not the trends-view local day) and per model, API lane only. Money is `UsdAmount(Decimal)` (always USD, exact, never `f64`), built only at a unit-tagged parse boundary. Output `CostReconciliation` carries the vendor's typed honesty caveats through unchanged; vendor-side absence is **typed** (`VendorBilled::Unavailable`), never a fabricated `$0`; `variance = local − vendor` (positive ⇒ estimate exceeds invoice). The invoice is ground truth; reconciliation surfaces signed variance, never silently "corrects" the estimate. No FOCUS-schema change, no new dependency (direction stays `connect → core`).

## 7. Rendering mechanics

- **Ratatui + crossterm.** Braille is computed directly from `U+2800` + an 8-dot bitmask — never Ratatui's braille constants or `Canvas`. The sparkline is hand-rasterized to braille so the one-shot and TUI paths stay identical.
- **`RenderMode`:** `Braille` (default) / `Ascii` (fully-ASCII visual fallback when braille is unsupported — internal, not a user flag) / `Plain` (`--plain`: no chrome, no color, no braille — linear labeled text, screen-reader friendly; Costroid-generated text is pure ASCII, provider names pass through verbatim). Chosen from `--plain`, TTY detection (non-tty ⇒ Plain), `NO_COLOR`, and a braille-capability check. The only user-facing mode flag is `--plain`.
- **Fill = glyph shape, reinforced by color.** Used vs remaining is carried by glyph shape (`⣿` vs the `⣀` track), color layered on top via the `SemanticStyle` palette ([DESIGN-SYSTEM.md](DESIGN-SYSTEM.md)) — never color-alone. Under `NO_COLOR` only ANSI is dropped; braille meters stay (shape-distinct).
- **Warning thresholds** `warn = 0.80`, `critical = 0.95` (configurable). The warning state turns amber (red at critical/over) **and always carries a textual cue** (`!`, `!!`, `OVER`, "near limit") so it survives `NO_COLOR`/color-blindness/`--plain`. Cost bars never go amber.
- **One styled document, two adapters** (one-shot serializer + TUI mapper) keep both renderers identical; the serializer is snapshot-tested. Terminal is always restored on quit/error/panic (restore guard + panic hook leaving the alternate screen, exit 101). `--live` re-collects ~2s; otherwise a snapshot refreshed with `r`.
- **TUI:** 9 numbered tabs (1 now, 2 trends, 3 providers, 4 models, 5 history, 6 budget, 7 forecast, 8 anomalies, 9 activity) + the `a`/`esc` Frontier overlay; colorful via `SemanticStyle` (cyan `Data`, lime `Accent`, Ash `Muted`); `--plain`/`NO_COLOR` strip all color.
- **`costroid statusline`** prints one compact line from the same core data, side-effect-free on interactive stdin (tmux/Starship); with piped Claude Code `statusLine` JSON it captures `rate_limits` into the cache before rendering. Supports `--capture-only` and `--wrap '<cmd>'`; all paths degrade to a blank line + exit 0, never breaking the prompt. `costroid setup-statusline` (with `--undo`) wires Claude Code's `statusLine`.
- **Taskbar (`costroid-bar`):** tray glance (the most-constrained quota meter, in the 0–8 dot-density warning language) + a live cockpit (Overview meters, opt-in `active_alerts` banner, Budget/Forecast/Anomalies/Providers panels — each ONE core view fn; the Providers lane is display-only + zero-network). **AccessKit on** (default `accesskit` feature; painted widgets carry accessible names). Linux `accesskit_unix → zbus → async-io` is local AT-SPI/D-Bus IPC, admitted only for the bar (§3).

## 8. Distribution

- **cargo-dist** (`dist`): nothing publishes on a normal push — only a pushed `vX.Y.Z` tag (must equal `[workspace.package].version`) triggers build + publish; PRs run `dist plan`. Targets (6): Linux gnu x86_64/aarch64, Linux musl x86_64, macOS x86_64/aarch64, Windows x86_64.
- **Channels:** shell + PowerShell installers (GitHub Releases), Homebrew tap (`Costroid/homebrew-tap`), npm wrapper (`npx costroid`) — **CLI only**; the bar ships **binary archives + `cargo install costroid-bar`** (no npm/Homebrew/musl this cut). crates.io via `cargo publish` in the 7-crate ladder order (§2; see [RELEASING.md](../RELEASING.md)).
- **Signing:** releases are **not OS-code-signed**; each artifact ships keyless GitHub **build-provenance attestations + SHA-256 checksums** (`gh attestation verify <file> --repo Costroid/costroid`). Trade-off: first run shows a macOS "unidentified developer" / Windows SmartScreen prompt. Notarization/Authenticode are config-toggle deferrals.
- CI gate (green = releasable): `rustfmt` + `clippy -D warnings` + `cargo test` + FOCUS-conformance (vendored `focus_validator` 1.3.0.1 ruleset at `scripts/focus-ruleset/`) + `cargo deny` (license + bans offline; advisories online) + the offline-acceptance script + the forbidden-crates test.

## 9. Honest flags & known limitations

- **Cost is always an estimate** (`x_Estimated = true`); the invoice is ground truth (reconciliation surfaces variance, never corrects).
- **Opus weekly sub-cap unobservable** — the hook carries only overall 5h/7d, no per-model window; lead with the 7d % (not the binding cap for an Opus-heavy user). The Opus-specific render (7d volume/value + cap-% marked unavailable) is **design intent, not yet shipped** (no `opus_weekly` field in code).
- **Claude `rate_limits` is buggy** (absent for API keys; poisoned; impossible 900%; false-in-range 100%) — sanitize **+** cross-check, degrade to "unavailable"/"unverified," never a confident wrong number (§4).
- **Cursor is detection-only** until a sanctioned per-user API/OAuth exists — never via session reuse against `api2.cursor.sh`.
- **Deferred / discovery-gated, never built speculatively:** Cursor live quota, GitHub Copilot, Antigravity CLI, Gemini own-key (each slots in via the `Capability` descriptor only after a live-install discovery confirms its real data/auth/quota shape).
- **No certificate pinning** — OS-trust-store validation only; an attacker-planted/corporate-MITM root could intercept a connect/reconcile request carrying the org-wide admin key (§3).
- **Opus real-log quirk:** ~0.08% under ccusage on opus totals — isolated to re-logged sub-agent (sidechain) cache-read de-dup; mainline matches to the cent.
- **WSL Windows-root scan** is `/mnt/c` only and evidence-based (includes any Windows profile holding logs); a set `USERPROFILE` (even empty) suppresses the scan.
- **DeepSWE vs CursorBench:** quality/score from the benchmarks (DeepSWE primary/neutral, CursorBench corroborating/vendor); the **dollar** axis from Costroid's own cache-correct cost (DeepSWE's $/task is cache-miss-priced ~5×). Un-benchmarked models show as gaps, never guessed.
- **`tracing` + TOML config** are planned conventions, not wired (zero-config on built-in consts today). No `costroid-mcp` (crates.io name intentionally unclaimed). macOS/Windows tray paths compile but are **not field-verified**.

## 10. Costroid-Next (post-v0.6.0) — M0 decisions & scaffold

The next build ([`COSTROID-NEXT.md`](COSTROID-NEXT.md), tracked live in [`../PROGRESS.md`](../PROGRESS.md)) adds a measured/estimated **local-inference economics** engine, a **cloud/API** cost lane, a **break-even** calculator, and a **local web UI**. The **M0** decisions/spikes are canon here (the code wins on conflict):

- **Two new workspace members (M0 scaffold; both `publish = false` until M6):**
  - `crates/costroid-power` (new lib, **leaf**) — the **four-source** `PowerSampler` (`WallMeter`/`Sysfs`/`WindowsLhm`/`Estimated`) + the **wall-meter-led** selector (M3a, §6.3), the `MeasurementMode` stamp (`x_MeasurementMode` ∈ `measured_wallmeter`/`measured_sysfs`/`measured_lhm`/`estimated`), the §3.2 energy/cost model, the **subprocess runner** (llama.cpp/Ollama via CLI/stdout — A2, not FFI/HTTP), the **benchmark harness**, and the bundled dated **hardware/electricity profile** (`profiles/hardware.v1.json`) + **Gemma 4 manifest** (`models/gemma4.v1.json`), each sha256-stamped (R8). **All Linux/GPU code behind the off-by-default `power` feature** (named `power`, **never** `telemetry`, R1) **+** `#[cfg(target_os = "linux")]`; non-Linux / feature-off compiles a clean "unavailable" stub. **Stays a leaf (M3a):** rather than a `core → power` edge, the **`costroid bench` CLI** (the only place both meet, under the off-by-default `power` CLI feature) runs the harness and hands a pre-computed enriched `costroid-providers::LocalRunEvent` to `core::focus_records_from_canonical`; `core::local_run_to_focus` is a pure mapping (no cost math, no power dep), so `core`/`providers`/the default CLI graph are dependency-unchanged.
  - `apps/server` → **`costroid-server`** (new **binary**, mirrors `apps/bar`) — the local HTTP API + web UI. A **separate binary, never linked into `costroid`/`costroid-bar`**: `tiny_http`/`axum`/`hyper`/`tokio` are name-banned in the CLI/bar offline gate. It uses **blocking `tiny_http`** (no async runtime), binds **`127.0.0.1` by construction**, and makes **no outbound call**.
- **The server's offline guarantee — "loopback-bind, no egress" (distinct from the CLI/bar's "no socket at all"):** a 127.0.0.1 listen *does* create an `AF_INET` socket, so the proof is *no egress*. Enforced by (a) a **per-binary static allowlist** `SERVER_ALLOWED` in `apps/cli/tests/offline.rs` (BFS rooted at `costroid-server`; admits only the reviewed inbound-listen subtree `tiny_http`+`ascii`+`chunked_transfer`+`httpdate`; subset-allowlist fail-closes new deps; regenerate via `print_server_delta`), and (b) a **runtime** `assert_loopback_only` in `scripts/offline_acceptance.sh` (allow a loopback bind; forbid any non-loopback bind/connect). The `costroid` CLI stays byte-for-byte no-network.
- **Storage = SQLite, not DuckDB (R11 fallback taken).** The M0 spike **rejected DuckDB** on three independent grounds: it pulls `webpki-roots` (CDLA-Permissive-2.0, not on the deny allowlist), its graph contains `reqwest`/`hyper`/`tokio` (banned; the offline BFS walks **build-deps**), and `libduckdb-sys` has a **build-dep on `reqwest`** (build fetches network). `rusqlite` (bundled) is clean (15 pkgs, all-permissive, MSRV 1.88, no build-net). The store lands behind a feature / in a dedicated crate at M1 to keep `costroid-core` at MSRV 1.88.
- **Web-UI sub-stack = Maud + htmx + uPlot** (server-rendered, embedded static assets via `rust-embed` over loopback — §6.11; no WASM, no cloud backend). **Inference = subprocess** to user-installed llama.cpp/Ollama (CLI/stdout, not the localhost HTTP API), so `costroid-power` needs no HTTP client and (with `unsafe_code = "forbid"`) no FFI.
- **Local models = the Gemma 4 family (Apache-2.0), user-downloaded; Costroid ships no weights.** The M3 benchmark set standardizes on one family: `31B Dense` (dense flagship / coding counterexample), `26B A4B` (3.8B-active fast MoE), `12B Unified` + `E2B`/`E4B` (compute-efficient / edge). Apache-2.0 is on the permissive allowlist (the same license Costroid ships under); **pin to Gemma 4** — Gemma 1–3's custom *"Gemma Terms of Use"* is non-OSI / field-of-use-restricted. Quality is taken from each model's published score (cite source+date, R10); tok/s are confirmed at M3b.
- **MSRV:** the two new crates inherit **1.88** today (their M0 deps build on it); each may raise its own `rust-version` later if a heavy dep demands it (as `apps/bar` does at 1.92).
- **CI:** a new `cross-platform` job builds the workspace (`--all-targets`) on macOS + Windows per push; full cross-OS clippy/test *execution* hardens at M6. All existing gates (deny, MSRV-1.88, FOCUS-conformance, offline-acceptance, forbidden-crates) stay intact and now also cover the server's loopback-only proof.
