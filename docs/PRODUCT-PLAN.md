# Costroid — Production Plan

*The single, executable build plan for Costroid as a cross-tool cost-and-quota cockpit — a terminal tool **and** an egui taskbar app, built on one shared core. Hand it to a build agent: it carries the current status (§0), the step-by-step sequence (§3), the auth model (§5), and the hard invariants (§6).*

> **Authority & relationship to canon.** This plan is the going-forward source of truth for *scope and sequencing*, and the canon is reconciled to it: [ARCHITECTURE.md](ARCHITECTURE.md) is the **technical** source of truth, and it plus [../CLAUDE.md](../CLAUDE.md) (the operating manual) defer scope/sequencing here. The **hard invariants in §6 are not superseded by anything** — they bind every step.
>
> **GUI choice:** the taskbar app is built in **egui / eframe (+ `tray-icon`)**, *not* Tauri — Rust-native, no webview, permissive licenses, shares `costroid-core` directly.
>
> **Deferred by instruction:** the Antigravity and GitHub Copilot **discovery checks and adapters are explicitly later** (§8) — the data model is generalized to *fit* them, but no adapter is built until a live-install discovery confirms its real shape.

---

## 0. Where we are today (v0.4.0 shipped — Step 4 connections complete) — ground truth

Verified against the code, not the docs. (v0.2.0 — the cost lane: frontier + Cursor-detect + WSL fix — shipped 2026-06-05 across GitHub Release, Homebrew, npm, and crates.io; **v0.3.0 — Claude live quota end to end + the generalized quota model — tagged 2026-06-06**; T1–T8 done (T8 = the keychain credential store, 2026-06-09); T9a DONE 2026-06-10, ⛔-approved; **T9b — the Anthropic + OpenAI usage-API adapters + the Gemini first-class-unavailable state — DONE 2026-06-13, both ⛔ gates cleared** (264 tests; Gate 2 live-confirmed at the envelope/transport level — the org has no raw-API usage, so populated-row checks are logged standing follow-ups); **T9c — the estimate-vs-invoice reconciliation engine (pure core, `costroid-core::reconcile`) — DONE 2026-06-13, 280 tests** — see §11.5.)

**By lane** (the §1 spine — three lanes, never summed):

| Lane | State today | Evidence |
|---|---|---|
| **API cost ($) by model** | ✅ **Done** | FOCUS 1.3-conformant records, exact-Decimal `tokens × price`, bundled dated pricing (6 models), dedup verified to the cent vs ccusage |
| **Subscription quota (windows)** | ✅ **Done (Claude live quota end to end)** | Codex 5h + weekly parsed for real; **Claude end-to-end works** — T5's `setup-statusline` writer + `--capture-only` feed the T4 reader's sanitize + cross-check, and **T6 renders all 5 states on screen** (Available/Partial/Unverified/Estimated/Unavailable + `Spend` dollar pools, the `? unverified` cue, the "as of HH:MM" freshness stamp, the claude.ai caveat); **Cursor returns empty** by design |
| **Model quality (frontier)** | ✅ **Done** | `bench.rs`: DeepSWE + CursorBench, Pareto dominance, API-cost-only re-pricing overlay |

**Solid foundation the rest builds on:** three-crate engine (`apps → core → {providers, focus}`, no cycles, no `unwrap`/`expect`/`panic!` in libs); a working 5-method `Provider` trait (`id` / `capability` / `discover` / `parse_usage` / `parse_limits`); WSL-aware multi-root discovery; three render modes (braille / ASCII / **plain**) with non-color cues; `--live`; the statusline emitter; FOCUS export; and **enforced** invariants — a strace-based offline-acceptance CI job, a two-tier resolved-graph forbidden-crates test (since T7: the default build forbids ~44 networking/TLS/telemetry crates incl. the gated `ureq`/`rustls`/`keyring` trio; `--features connect` admits only that trio), `cargo-deny` (no copyleft, openssl banned), attested releases. **304 tests, 30 render snapshots, green CI gate** (counts as of the v0.4.0 cut, 2026-06-16 — see §11.5). The cost lane is `cargo install`-able and correct today.

**Not built yet:** OAuth (tier-2), Step 5's analytical tabs + alerts, the taskbar, and the discovery-gated adapters (Antigravity & Copilot). **T10c (the `costroid reconcile` estimate-vs-invoice display) is DONE 2026-06-15** — surfaces T9c's `CostReconciliation` on screen (signed variance per UTC day + model, typed absence never `$0`, caveats footnoted, the local figure always an estimate), reusing T10a's stored key + authorized client with no new secret/network boundary (§11.5 ✅ T10c). **T10a (the `connect`/`disconnect`/`connections` CLI + connect-time key validation + the connect-action test) is DONE 2026-06-15** — the first caller of `costroid-connect` and the first real network (opt-in; default build still zero network), ⛔ GATE 2b cleared (§11.5 ✅ T10a). T10-LIVE-ROWS (§12.16) is fulfilled/closed. **T8 built the keychain credential store** in `costroid-connect` (`CredentialStore`/`ConnectionRegistry`/`ApiVendor`, `keyring` sync Secret Service — DONE 2026-06-09, ⛔-approved), **T9a built the generic authorized-host HTTP client** (`AuthorizedClient` on `ureq`+`rustls`, OS-native roots — DONE 2026-06-10, ⛔-approved), **T9b built the Anthropic + OpenAI usage-API adapters** (`AnthropicAdapter`/`OpenAiAdapter` parsing into `costroid-core::vendor_report`; Gemini = first-class unavailable — DONE 2026-06-13, both ⛔ gates cleared), and **T9c built the estimate-vs-invoice reconciliation engine** (`costroid-core::reconcile` — pure core, fixture-tested, no connect dep — DONE 2026-06-13, 280 tests), so secrets have a home, the HTTP layer exists, the adapters can fetch+parse, and the engine can compare estimate vs invoice — and **T10a (DONE 2026-06-15) wired the first caller** (the `connect`/`disconnect`/`connections` CLI), so a network call now happens **only** on an explicit `connect` / `connections --check` action (the default build still performs none); **T10c (DONE 2026-06-15) added the `costroid reconcile` display** (signed estimate-vs-invoice variance per UTC day + model, honest typed absence/caveats), so the last item on the 0.4.0 line was the release cut (T10b) — its ⛔ legal gate CLEARED (maintainer self-attestation 2026-06-16, §11.5 / `docs/proposals/T10b-LEGAL-REVIEW.md` §10) — and **v0.4.0 shipped 2026-06-16, completing Step 4 (connections)**. **Step 5 is underway: T11 Providers, T12 Models, and T13 History tabs have shipped** (now/Trends/Providers/Models/History all exist as dedicated tabs); still to build on the 0.5.0 line — the Budget, Forecast, and Anomalies tabs + alerts (T14–T17) — plus the taskbar (Step 6) and the discovery-gated Antigravity & Copilot adapters. *(Claude live quota **on screen** is now **done** — T4 landed the reader, T5 the writer, and **T6 the render**, so the captured quota surfaces end to end on a Pro/Max machine; see §11.5 ✅ T6 DONE. The generalized quota **shape** — `LimitKind`×5, `LimitMeasure`, `LimitStatus`, the reshaped `LimitAvailability` — landed in T2; its live **producers** in T4 (cross-check demotion + stale age-out + the `Estimated` fallback); its **rendering** in T6 (all 5 arms + `Spend`). The **`Capability` descriptor** — `DataSource`/`AuthMethod`/`Capability` + the required `capability()` trait method, declared by all three adapters — **landed in T3**; its consumer, the Providers tab, **shipped in T11**.)*
---

## 1. The product in one picture

Costroid is the one local-first place a developer sees, across **every** AI coding tool they use, both halves of the picture: **how much quota is left** on their subscriptions (daily / 5-hour / weekly / monthly caps that have no invoice) and **how many real dollars** their API usage costs by model — plus proactive, sourced model-choice nudges (API-cost usage only). Used the way people use ccusage: a quick `costroid` check, or parked always-on in a pane / statusline / taskbar, with opt-in alerts.

**Three lanes, never summed** (a quota % is not dollars; an estimate is not a bill):
1. **API cost ($)** — pay-as-you-go spend per model, estimated `tokens × price`, reconcilable against a usage API when a key is connected. *The only lane recommendations attach to.*
2. **Subscription quota (windows)** — used measure (token-fraction **or** spend-$/credits) against a window with a reset; **no dollars summed**. Generalized so Claude, Codex, Cursor, Copilot, Antigravity all fit one shape.
3. **Model quality** — the bundled, dated, sourced cost-vs-quality frontier (DeepSWE + CursorBench).

**Three surfaces, one core.** `costroid-core` is the only brain; every surface is a thin consumer:
- **Statusline** — a compact line for any shell / tmux / Starship / VS Code terminal.
- **CLI / TUI** — the navigable tabs; `--live`; `--plain`; export. The primary surface.
- **Taskbar (egui)** — a cross-platform tray/menu-bar app for glanceability outside a terminal. Richest and most expensive, so it's last; everything it shows, the core already computes.

---

## 2. Architecture additions the build needs

Three new capabilities, introduced in dependency order. Crate layout after this plan:

```
costroid/
├─ crates/
│  ├─ costroid-focus/        FOCUS types (unchanged)
│  ├─ costroid-providers/    Provider trait + adapters + discovery  (+ capability descriptor, generalized LimitWindow)
│  ├─ costroid-core/         engine: orchestration, cost, bench     (+ generalized LimitAvailability, budget/forecast/anomaly logic)
│  └─ costroid-connect/      NEW — ALL network + credential code, feature-gated, off by default   (Step 4)
├─ apps/
│  ├─ cli/                   package `costroid` — CLI + TUI + statusline   (unchanged crate, new tabs/commands)
│  └─ bar/                   NEW — package `costroid-bar` — egui/eframe taskbar app   (Step 6)
```

Dependency direction stays acyclic: `apps/cli → core → {providers, focus}`; `apps/bar → core → …`. The connections crate is gated by a `connect` **feature on the apps** (`apps/cli`, later `apps/bar`) — off by default — so when enabled `app → costroid-connect → {core, focus}` (matching ARCHITECTURE §5 / RELEASING.md's publish order; **the feature cannot live in the virtual workspace root**, which has no `[package]`). T7 shipped `costroid-connect` as an empty leaf (no deps); T8 gave it its first deps — `keyring` (+ `secrecy`/`serde`/`thiserror`) for the keychain credential store — and it gains the `core`/`focus` deps with the HTTP/reconciliation behavior in T9. **No crate except `costroid-connect` ever links a network or keychain dependency.**

### 2a. Generalize the quota window — the hard prerequisite (Step 3)

> ✅ **Landed in T2 (0.3.0 line).** The target shapes below are now the **shipped** types in `costroid-providers`/`costroid-core` (the "Today …" sentence is the pre-T2 motivation, kept for context). Producers (Claude live capture = T4) and the real rendering of `Spend`/`Unverified`/`Estimated` (T6) have since shipped too — so the generalized quota model is complete end to end (the 0.3.0 milestone); the legacy request-count measure stays cut. See §3 T2 and §11.5 (✅ T2 DONE / ✅ T4 DONE / ✅ T6 DONE).

Today `LimitKind = {FiveHour, Weekly}`, the measure is token-fraction only (`used_fraction: Option<f64>`), and `LimitWindow` has no freshness/confidence. To fit Cursor/Copilot/Antigravity:

```rust
// costroid-providers
pub enum LimitKind { FiveHour, Weekly, Daily, Monthly, BillingCycle }   // + 3

pub enum LimitMeasure {
    TokenFraction(f64),                                   // 0.0..=1.0 — Claude / Codex / Antigravity
    Spend { used_usd: Decimal, included_usd: Option<Decimal> }, // Cursor & Copilot dollar credit pools
    // RequestCount is the LEGACY pre-June-2026 Copilot model — do NOT implement it.
}

pub enum LimitStatus { Verified, Unverified, Unavailable }   // from the statusLine brief

pub struct LimitWindow {
    pub tool: ProviderId,
    pub plan: Option<String>,
    pub kind: LimitKind,
    pub measure: Option<LimitMeasure>,        // replaces bare used_fraction
    pub resets_at: Option<DateTime<Utc>>,
    pub captured_at: DateTime<Utc>,           // NEW — every reading carries freshness
    pub status: LimitStatus,                  // NEW — every reading carries confidence
    pub label: Option<String>,
}
```

Core's `LimitAvailability` gains an `Estimated` variant alongside `Available / Partial / Unavailable`. **`Estimated` lives at the core availability/render layer, never on the provider `LimitWindow` (which carries the 3-value `LimitStatus`)** — getting this layering right is a documented gotcha from the brief.

> **Priority note from the provider review:** Cursor (paid) **and** Copilot (post-June-2026) both converged on the *same* shape — a **monthly dollar-denominated credit pool + usage-based overage**. So `Spend` + `Monthly`/`BillingCycle` is the high-value addition that covers two providers at once; `RequestCount` is a deprecated model and is intentionally cut.

The Claude statusLine brief (Step 2) already specs the `captured_at` + `LimitStatus` half — **landing Step 2 does part of Step 3's work**, so they can overlap.

### 2b. Capability descriptor on `Provider` (Step 3)

> ✅ **Landed in T3 (0.3.0 line).** The descriptor below is now **shipped** in `costroid-providers`: the `DataSource` + `AuthMethod` enums, the `Capability` struct, and a **required** `capability()` method on the `Provider` trait, implemented for all three adapters with today's honest values (gate green, 139 tests; see §11.5 ✅ T3 DONE). The "bare structs today" sentence below is the pre-T3 motivation, kept for context. What remains is the **consumer** — the Providers tab (T11) that renders *what's unavailable and why* — and the deferred Copilot/Antigravity adapters (§8) that fill in their own descriptor.

Adapters are bare structs today; capability is implicit. Add a declarative descriptor so adding a provider is "fill in the descriptor + adapter," no core/UI change:

```rust
pub struct Capability {
    pub api_cost: DataSource,           // local_artifact | sanctioned_hook | sanctioned_oauth | api_key | unavailable
    pub subscription_quota: DataSource,
    pub model_mix: DataSource,
    pub auth: AuthMethod,               // none | oauth | api_key
    pub quota_kinds: &'static [LimitKind],
}
fn capability(&self) -> Capability;     // new trait method
```

This is what lets the Providers tab honestly render *what's unavailable and why*, and what keeps Antigravity/Copilot a descriptor-and-adapter away.

### 2c. The connections subsystem — `costroid-connect` (Step 4)

The first network and credential code. **Isolated in one feature-gated crate** so the default build stays provably local-only:
- **HTTP:** `ureq` (blocking, `rustls` TLS, no async runtime) — leanest permissive option; avoids pulling `tokio`.
- **Secrets:** the `keyring` crate → OS keychain only. Never disk/config/logs.
- **Gating:** `costroid-connect` is behind a Cargo feature (e.g. `connect`); the local-only path never compiles it in. Network happens **only** on an explicit, user-initiated `costroid connect …` action against a provider endpoint the user authorized.

### 2d. The egui taskbar — `apps/bar` (Step 6)

`eframe` + `egui` + `tray-icon` (all MIT/Apache-2.0). Shares `costroid-core` verbatim. Detail in §4.

---

## 3. Build steps — the spine

Ordered, each independently shippable. Each step lists **Goal / Deliverables / Acceptance / Invariant checks**. Target version in the heading.

### Step 0 — Reconcile canon + close loose ends · ✅ *done*
The canon (ARCHITECTURE.md, CLAUDE.md, README, SECURITY, RELEASING, DATA-MODEL, DESIGN-SYSTEM) is reconciled to this plan — tray un-cut (egui), the tab set + auth ladder widened, the never-reuse-a-subscription-token boundary pinned, the `LimitKind` generalization marked planned — and [STATUSLINE-CAPTURE-BRIEF.md](STATUSLINE-CAPTURE-BRIEF.md) is committed as the Step 2 spec.

### Step 1 — Release **0.2.0**: ship what's built · *the cost lane* · ✅ *done — shipped 2026-06-05*
- **Goal:** get the already-built frontier + Cursor-detect + WSL fix into users' hands.
- **Deliverables:** version bump, changelog, tag → cargo-dist release (shell/PS/Homebrew/npm + crates.io); README "next release" → "shipped."
- **Acceptance:** `cargo install costroid` / `cargo binstall costroid` gives a working binary with `frontier`; full pre-PR gate green.
- **Invariants:** unchanged — still zero network.

### Step 2 — **0.3.0**: Claude statusLine capture *(flagship)* · *quota half becomes real* · ✅ *done — shipped in v0.3.0 (tagged 2026-06-06; T2+T4+T5+T6)*
- **Goal:** Claude live 5h/7d quota from the sanctioned `statusLine` `rate_limits` push — zero token reuse, zero API tokens, local only.
- **Deliverables:** implement [STATUSLINE-CAPTURE-BRIEF.md](STATUSLINE-CAPTURE-BRIEF.md) end to end — the `setup-statusline` command, `statusline --capture-only`, the no-secret cache, sanitize + cross-check + age-out, `captured_at` + `LimitStatus` on `LimitWindow`, the always-on "as of HH:MM" freshness stamp + claude.ai-chat under-report caveat. Codex windows adopt the same `captured_at`/age-out (status always `Verified`).
- **Open item (T4 resolved the floor; live-install check still open):** the cross-check guard is **built** with `UNVERIFIED_TOKEN_FLOOR = 5_000` (biased low — only ever demotes). Still worth confirming against a live install whether the false-100% bug (#31820) fires on the shipped binary — that datapoint can *tighten* the floor, but the guard ships either way.
- **Acceptance:** on a Pro/Max machine, `costroid` shows real Claude 5h + 7d with reset countdowns; degrades to "unavailable"/"unverified" never a confident wrong number; **still no network calls** (offline-acceptance test unchanged and green).
- **Invariants:** the cache holds only two percentages + two reset stamps + a capture time — no token/prompt/credential.

### Step 3 — Generalize the quota model · *folds partly into 0.3.0* · ✅ *done — shipped in v0.3.0 (T2 types + T3 Capability + T4 producers + T6 render)*
- **Goal:** the data-model prerequisite for every remaining provider (§2a, §2b).
- **Deliverables:** extend `LimitKind`, introduce `LimitMeasure` (token-fraction + spend-$/credits; no request-count), add the `Capability` descriptor, add the `Estimated` availability variant at the core layer. Migrate Claude/Codex to the new shape (still token-fraction). Render `Spend`-measure windows in the limit-line + statusline (dollar pool used/included, not a fabricated %).
- **Acceptance:** a synthetic Cursor/Copilot-shaped `Spend` window renders correctly in all three render modes; existing token-fraction windows unchanged; full gate green.

### Step 4 — **0.4.0**: Connections — the safe, friendly login · *first network code*
- **Goal:** the API-cost half users connect — paste-your-key for the official usage/billing APIs — built on the §2c isolation.
- **Deliverables:** `costroid-connect` crate (ureq + rustls + keyring, feature-gated); `costroid connect <anthropic|openai>` (paste key → keychain → pull real API usage/cost to reconcile against the local estimate; **Gemini deferred** per the ⛔-signed-off T9 pins — no sanctioned static-key usage API, so `ApiVendor::Gemini` renders a first-class "unavailable"); a **Connections view** listing what's linked; `costroid disconnect <provider>` with instant revoke. The auth source-ladder (§5) enforced in code: a datum with no clean source is **unavailable, never fetched**.
- **Acceptance test** (mirrors the canon's Phase-2 test): a user enters a key, sees reconciled API cost, revokes it, and **confirms no secret was written to disk/config/logs** (inspect keychain + filesystem). With the `connect` feature **off**, the binary still passes the offline-acceptance test.
- **Invariant changes — handle explicitly:** unban `rustls` + `ureq` + `keyring` in `costroid-connect` only (update `deny.toml` + the forbidden-crates test to scope the ban to the local path); **re-scope the strace offline-acceptance test** to assert the *default/local* path makes zero network calls, and add a test asserting network occurs only on an explicit `connect` action to an authorized host. Install the deferred keychain deps (`libdbus-1-dev`, `libsecret-1-dev`) in CI. **No telemetry — still, ever.**
- **⛔ Legal review (before connections ship) — ✅ CLEARED 2026-06-16:** this is the step where the liability surface grows. Before 0.4.0 ships, get a quick **legal review of the connection flows** (own-key + sanctioned OAuth only) confirming they hit only provider endpoints the user authorized, store nothing outside the keychain, and induce no ToS violation. Not a code task — a human gate. **Satisfied via maintainer self-attestation** (`docs/proposals/T10b-LEGAL-REVIEW.md` §10; §11.5 2026-06-16) — a maintainer risk-acceptance, not counsel's opinion; counsel advisable before commercialization / 1.0 / any new credential/OAuth/endpoint.

### Step 5 — **0.5.0**: Analytical tabs + alerts
- **Goal:** the navigable cockpit. Build the tabs users ask for; ship the cheap re-cuts first, the new analytics next.
- **Deliverables (in order):**
  1. **Providers** tab — per-tool quota windows, API spend, connection status, and *what's unavailable and why* (driven by the §2b `Capability`).
  2. **Models** tab — spend/usage by model, each plotted on the frontier (promote the existing overlay).
  3. **History** tab — the full record; FOCUS export lives here (promote `export`).
  4. **Budget** — user-set monthly $ targets per tool/total vs actual API/overage spend; quota pace vs each window's cap. **Never invent a $ budget for a flat-fee subscription.**
  5. **Forecast** — "you'll hit your weekly Claude limit ~Friday"; "~$210 projected API spend this month." Always hedged as an estimate.
  6. **Anomalies** — spend spikes / unusual model mix / quota burn-rate jumps vs *your own* history. Proactive, never alarmist.
  7. **Alerts** — opt-in, quiet by default: quota ("weekly at 90%, resets Sunday"), budget ("80% of your $200 API budget"), and the model nudge — **API-cost usage only** as dollar-savings; for subscriptions, framed as **quota-extension** ("move heavy Opus tasks to Sonnet to buy weekly headroom"), never "save money."
- **Acceptance:** every tab has a complete `--plain` rendering and relies on no color alone; budget/forecast/anomaly logic is unit-tested against fixtures; alerts default off.

> **Cursor live quota is *not* a numbered step.** It is **discovery-gated (§8)**: pursued only if Cursor ever publishes a sanctioned, documented API or first-party OAuth — never by reusing a local Cursor session against its undocumented `api2.cursor.sh` RPC (that path is removed as a ToS violation; §5 tier 4). Until then Cursor stays **detect-only**, usage/quota **"unavailable."**

### Step 6 — **0.6.0**: The egui taskbar app · *the last surface*
- See §4. Ships after the CLI/TUI is feature-complete, because everything it shows the core already computes.

### → **1.0**: feature-complete across the three surfaces, after the deferred adapters (§8) land.

---

## 4. The taskbar app (egui) — detail · Step 6

- **Stack:** `eframe` (egui's app framework) + `egui` + `tray-icon`. All **MIT/Apache-2.0** — no copyleft, no webview, no Tauri. Package `apps/bar` (binary `costroid-bar`), depending only on `costroid-core` (+ `costroid-connect` behind the same feature gate as the CLI).
- **What it shows:** a tray/menu-bar icon rendering the most-constrained quota meter at a glance; click opens a compact egui window mirroring the **Overview** plus the same tabs as the TUI (Providers / Models / Budget / Forecast / Anomalies / History / Trends). Same normalized data, same estimate labeling, same lanes-never-summed rule.
- **Cross-platform:** macOS menu-bar extra, Windows system tray, Linux tray via StatusNotifierItem/AppIndicator (note the Linux-tray fragility across desktops — document supported environments).
- **Accessibility (required, not optional):** egui ships **AccessKit** screen-reader support — wire it on. `--plain` has no meaning in a GUI, but the equivalent obligation holds: **never rely on color alone** — the amber warning state needs a second non-color cue (icon/badge/text), exactly as in the terminal.
- **Constraints:** the taskbar is a *consumer* of the core, never a second brain; it adds **no** new data path, no new network call beyond what `costroid-connect` already authorizes, and no telemetry.

---

## 5. The auth model — maximally friendly, completely ToS-safe *(the spine)*

**The rule that makes "friendly" and "safe" compatible:** for each provider and each datum, use the highest-safety source that exists. **If the only path would violate the provider's terms, the datum is `unavailable` — never fetched.**

Source ladder (use the first that applies). Only tiers 0–3 are ever built; there is **no session-reuse tier** — that is the ToS line:
0. **Local artifacts** — logs the tool already writes. *The only tier built today.*
1. **Sanctioned push/hook** — a vendor-built third-party extension point (Claude Code's `statusLine` `rate_limits`). *Step 2.*
2. **Sanctioned OAuth** — the provider's own first-class third-party OAuth (GitHub). *Deferred with Copilot (§8).*
3. **Your own API key** — official usage/billing API, your key, your data. *Step 4.*
4. **Never** — reuse *any* credential, session, or token against a non-sanctioned, undocumented, or internal endpoint, and never read browser cookies. **This is the account-ban path and a ToS violation; Anthropic enforces it, and it is no safer for any other provider.** It explicitly rules out reusing a local Cursor session against `api2.cursor.sh`. Where it's the only route, the datum is **unavailable, never fetched**. A "log into &lt;provider&gt; for quota" button against an unsanctioned endpoint must never exist.

### Per-provider classification — *corrected to current facts (June 2026)*

| Provider | API $ source | Quota source | Login | Quota shape | Status |
|---|---|---|---|---|---|
| **Claude Code** | local transcripts (+ optional Anthropic usage API w/ key) | **statusLine sanctioned push** | none (`setup-statusline`) | 5h + 7d, token-% | ✅ both |
| **Codex** | local rollout logs (+ optional OpenAI usage API w/ key) | local rollout logs | none | 5h + weekly, token-% | ✅ both |
| **Cursor** | unavailable (no sanctioned source) | unavailable (no sanctioned source) | none (local model-mix only) | **monthly billing-cycle $-credit pool + overage; daily token rate-limit on free tier** | detect-only; live quota **discovery-gated (§8)** |
| **GitHub Copilot** | own token → billing API ($ by model) | **own classic PAT / `gh` OAuth → documented `…/billing/ai_credit/usage`** (tier 3/2) | own classic PAT (fine-grained unsupported) or `gh` OAuth | **monthly AI-credit pool ($) + overage** *(premium-requests is the legacy pre-June-2026 model)* | **discovery-gated (§8) — ToS-safe path identified; needs live-install check** |
| **Antigravity CLI** | **ToS-safe lane exists but is NOT T9-implementable** (the Gemini key reads nothing programmatically; BigQuery export = OAuth-class) → unavailable for now (§8) | **unavailable — compute-effort quota has no sanctioned source** (hooks not fed quota; only the internal `GetUserStatus` RPC = ban path) | n/a for usage read (a Gemini key is inference-only) | **5h + weekly, metered in "compute effort"** (+ credit overage) | **discovery-gated (§8) — $ lane ToS-safe but not T9-implementable; quota unavailable** |

**Provider-fact notes** (the easy-to-get-wrong ones — don't regress these):
- **Cursor** is *not* "daily, spend-$." Paid plans (the real users) are a **monthly dollar credit pool**; the daily *token* window is a free-tier rate-limit. Don't invert them.
- **Copilot** is *not* "request-count." As of **June 1, 2026** GitHub replaced premium requests with **AI Credits** (dollar-denominated monthly pool + overage); request-count is the legacy pre-June-2026 model. *(Its ToS-safe source — own classic PAT / `gh` OAuth → the documented `ai_credit/usage` endpoint, user-billed only — is detailed in §8.)*
- **Antigravity** quota is metered in **"compute effort,"** so its denominator may not be clean token-% (community-sourced figures only — no official numbers); confirm on a live install. *(The ToS-safe Gemini-API $ lane vs. the no-sanctioned-source compute-effort quota split is detailed in §8.)*

---

## 6. Hard invariants — never trade these away *(bind every step)*

- **Completely ToS-safe.** Never reuse any credential, session, or token against a non-sanctioned, undocumented, or internal endpoint (this includes Cursor's `api2.cursor.sh` RPC), and never read browser cookies. No sanctioned source → the datum is unavailable, never fetched.
- **Local-first; no content leaves the device.** Nothing leaves except calls to provider endpoints the user explicitly authorized. **The default build makes zero network calls** (enforced by the re-scoped offline-acceptance test).
- **No telemetry. Ever, by default.** Any update check is opt-in, disclosed, individually disableable, off by default.
- **Secrets in the OS keychain only** — never disk/config/logs; device↔provider; revocable. All such code lives only in `costroid-connect`.
- **Lanes never mix.** Quota % ≠ dollars; estimated value ≠ a bill.
- **Cost & forecast are labeled estimates** — no false precision; recommendations advisory, sourced, **API-cost usage only**; every quota reading carries freshness + confidence and degrades to "unavailable"/"unverified" rather than show a confident wrong number.
- **Permissive licenses only** (MIT / Apache-2.0 / BSD / ISC / Zlib / Unicode). No copyleft. `rustls`, never OpenSSL. Verify before adding.
- **Accessibility is required.** Every CLI visual has a `--plain` ASCII equivalent; the egui app wires AccessKit; **never rely on color alone** — the amber state always carries a second non-color cue.
- **No `unwrap`/`expect`/`panic!` in library crates.** `thiserror` in libs, `anyhow` only in `apps/`.
- **Provider logs are untrusted input** — parse defensively, never crash or execute.

---

## 7. Definition of Done — apply to every change

- [ ] `cargo build` clean; `cargo test --workspace` passes; `cargo clippy --workspace --all-targets -- -D warnings` clean; `cargo fmt --all -- --check` clean.
- [ ] No `unwrap`/`expect`/`panic!` introduced in library code.
- [ ] New behavior covered by tests against **fixtures, never real user data**.
- [ ] Every new CLI visual has a `--plain` path; every egui visual is AccessKit-labeled; no color-alone.
- [ ] New dependency licenses verified permissive; `cargo-deny` green.
- [ ] No telemetry; no network call outside an explicit, user-authorized `connect` action; default build still passes offline-acceptance.
- [ ] Secrets (if any) touch only the keychain, only in `costroid-connect`.
- [ ] Docs ([ARCHITECTURE.md](ARCHITECTURE.md) / [../CLAUDE.md](../CLAUDE.md) / this plan / READMEs) updated in the same change when behavior or interface shifts.

---

## 8. Deferred — discovery-gated, explicitly later

Per instruction, the data model is generalized to *fit* these, but **no adapter is built until a live-install discovery confirms its real shape** (same discipline as the Claude statusLine capture):

- **Cursor live quota** — *findings landed (2026-06-05).* Cursor serves usage/quota server-side only. It **does** have a sanctioned `cursor-agent /statusline` hook (~2026-04, the Claude-`statusLine` analog) and a documented Admin/Analytics usage API — **but neither carries an individual's quota**: the statusline is fed only session/runtime metadata (no quota/cost field), the CLI's JSON output has no usage field, and the Admin API is team-admin/enterprise-only (no per-individual-Pro endpoint). So today Cursor cost/quota are **"unavailable"** and Cursor is detect-only. A live fetch is pursued **only if Cursor publishes a documented per-user API/OAuth — or adds a quota field to its existing `/statusline`** (the cleanest unlock to watch) — *never* by reusing a local Cursor session against its undocumented `api2.cursor.sh` RPC (that path is removed as a ToS violation; §5 tier 4). The generalized model already supplies the `Monthly`/`BillingCycle` `Spend` shape it would render (landed in T2); only a sanctioned *source* is missing.
- **Antigravity CLI adapter** — *findings landed (2026-06-05); it splits into two lanes.* **$ lane — ToS-safe but NOT implementable under T9's own-key constraint** (⛔-signed-off correction, 2026-06-10): a Gemini API key authenticates inference only — it reads no usage/billing data programmatically (the AI Studio cost/usage views are browser UI, not API), and the only machine-readable cost source, the Cloud Billing BigQuery export, is OAuth-class (service-account JSON + RS256 JWT-bearer exchange) — so the lane is a post-T9 "Gemini (advanced)" connector at best; see `docs/proposals/T9-PIN-PROPOSAL.md` §4–§5. **Compute-effort subscription quota — no sanctioned source:** its documented Hooks are *not* fed quota (only `conversationId`/`workspacePaths`/`transcriptPath`/tool fields), local transcripts are conversation content only, the IDE `.pb` files are keychain-encrypted, and the only live quota source is the internal Language-Server `GetUserStatus` RPC via a reused token (ban path) → quota stays "unavailable." Remaining discovery: how "compute effort" is denominated (community-sourced only — not officially published) and model-mix attribution (it routes to Gemini *and* Claude). Unlock to watch: Google feeding a documented quota payload into a Hook/status bar, or a consumer usage API. *(⛔ signed off 2026-06-10 — the $-lane correction above is canon; full rationale + the unlock-to-watch in [docs/proposals/T9-PIN-PROPOSAL.md](proposals/T9-PIN-PROPOSAL.md).)*
- **GitHub Copilot adapter (own-token billing API + CLI statusLine)** — *ToS-safe path identified (2026-06-05); discovery narrowed to a live-install check.* Route: the user's **own classic PAT** (fine-grained PATs are *unsupported* on the billing endpoints per GitHub's tutorial — its permissions reference contradicts that; classic is the safe reading) **or** their `gh` OAuth token → the **documented** `GET /users/{username}/settings/billing/ai_credit/usage` (+ legacy `premium_request/usage`) → AI-credit consumption + $-by-model; the Copilot CLI `statusLine` hook adds session premium-request count/cost locally. **Scope to individually self-billed users** (org/enterprise seats aren't in user-level endpoints → "unavailable (enterprise-billed)"). **Never** the internal `api.github.com/copilot_internal/user` (ban path). Remaining check: mint a classic PAT with billing read on a personal plan, confirm a 200 + the exact `ai_credit/usage` JSON shape, then build the adapter.
- **MCP server** (`costroid-mcp`) — still speculative; the recommendation engine it would expose is already built into the frontier view. Not built; name intentionally unclaimed.

---

## 9. The map at a glance

| Step | Version | Surface(s) | First network? | Net-new risk |
|---|---|---|---|---|
| 0 — canon reconcile | — | docs | no | ✅ done |
| 1 — ship built | **0.2.0** | CLI/TUI | no | release mechanics |
| 2 — Claude capture | **0.3.0** | CLI/TUI + statusline | no | sanitize/cross-check correctness |
| 3 — generalize quota | (with 0.3.0) | core/providers | no | data-model migration |
| 4 — connections | **0.4.0** | CLI/TUI | **yes (gated)** | keychain + re-scoping the no-network guarantee |
| 5 — tabs + alerts | **0.5.0** | CLI/TUI | no (uses Step 4) | analytics correctness, alert restraint |
| 6 — taskbar (egui) | **0.6.0** | **Taskbar** | no new | cross-platform tray, AccessKit |
| later — Cursor live quota / Antigravity / Copilot | → **1.0** | all | varies | discovery-gated (sanctioned source required) |

*Step 0 (canon reconcile) and Step 1 (v0.2.0, shipped 2026-06-05) are done, and the Claude `statusLine` capture is built end to end (T2–T5 ✅). **The 0.3.0 milestone is complete — T6 (render the new limit states + Spend windows) landed, so T2 + T4 + T6 are all green.** Claude live quota now surfaces on screen. **T7 is also done** — the feature-gated `costroid-connect` crate + the re-scoped no-network guarantee — so the 0.4.0 connections line is unblocked. **T8 (keychain credential store) is done** — gate green 2026-06-09, ⛔-approved (§11.5 ✅ T8) — and **T9a (the generic authorized-host HTTPS client) is done** — gate green 2026-06-10, ⛔-approved (§11.5 ✅ T9a). **T9b (the Anthropic + OpenAI usage-API adapters + the Gemini first-class-unavailable state) is done** — 2026-06-13, both ⛔ gates cleared (§11.5 ✅ T9b), and **T9c (the estimate-vs-invoice reconciliation engine, pure `costroid-core::reconcile`) is done** — 2026-06-13, 280 tests (§11.5 ✅ T9c); **T10a (the `connect`/`disconnect`/`connections` CLI + connect-time key validation + the connect-action test) is done** — 2026-06-15, gate green, ⛔ GATE 2b cleared (§11.5 ✅ T10a): the first caller of `costroid-connect` and the first real network (opt-in; default build still zero network); **T10c (the `costroid reconcile` estimate-vs-invoice display) is done** — 2026-06-15, gate green, no new ⛔ gate (§11.5 ✅ T10c): surfaces T9c on screen, reusing T10a's stored key + authorized client (no new secret/network boundary). **Remaining on the 0.4.0 line:** the release mechanics alone (T10b §12.10) — its ⛔ legal gate is CLEARED (maintainer self-attestation 2026-06-16, §11.5 / `docs/proposals/T10b-LEGAL-REVIEW.md` §10); T10-LIVE-ROWS (§12.16) is fulfilled/closed. Build each in a fresh agent per §12.0.*

---

## 10. Executing this plan with an autonomous agent

*The intended workflow: hand **one step (or sub-unit) at a time** to an autonomous Claude Code session at high reasoning effort with multi-agent workflows. The agent reads the step below + the files it names + the Definition of Done (§7), builds, runs the full gate, and stays within the step. This works — with three rules the numbered list alone doesn't make obvious.*

> **Repo setup (important for the agent):** the `docs/` specs — this plan, the statusline brief, and the ARCHITECTURE / DATA-MODEL / DESIGN-SYSTEM specs — are **tracked in the repository**; read them from disk and edit them freely as the plan evolves (those edits commit alongside the task). Local git worktrees off this working copy contain `docs/` (the docs commit is in your local history); a clone or CI checkout from GitHub will too **once you push it** (if you've kept the docs commit local, a GitHub clone won't have them yet). Within a session, only *uncommitted* doc edits are invisible to a freshly-spawned worktree-isolated workflow agent — so when fanning out before committing, have the orchestrator pass spec content into sub-agent prompts rather than assume they see the latest unsaved state.

**Rule 1 — follow the dependency order, not the step numbers.** The steps are not a flat list:
- **Step 3 (generalize the quota model) is the lynchpin** — land it *with* Step 2 (the brief already folds its `captured_at`/`LimitStatus` half in) and *before* Steps 4 and 5, which won't compile without its `LimitMeasure` / `Capability` types.
- **Step 4's `costroid-connect` crate houses all network + keychain code** — including the discovery-gated Cursor path (§8) if it ever lands.
- **Step 6 (the taskbar) needs Steps 2–5 done** — it mirrors tabs that don't exist yet.
Safe order: 1 → **2 (+3 together)** → 4 → 5 → 6.

**Rule 2 — the human checkpoints are by design, not a failure.** The golden rules (CLAUDE.md "decide vs ask") require the agent to **stop and ask** before: touching the keychain/secrets, making any network call, changing the public CLI surface, or releasing/tagging — and, before connections (Step 4) ship, a **legal review of the connection flows** (own-key + sanctioned OAuth only; the liability surface grows there). A well-behaved Auto-Mode agent *pauses* at these — that's the safety net. So **Steps 1, 4, 5, and 6 each carry a human gate**; **Steps 2 and 3 are the closest to "hand it cold."** Don't expect to walk away from 4 / 6.

**Rule 3 — split the L/XL steps; let the workflow fan out inside each.** "One step → one session" is too coarse for the big steps; the fan-out shape is in the table (a gating prerequisite, then parallel sub-units) — this is where the workflows earn their keep.

**Pin these open decisions *before* the handoff** (else the agent guesses):
- **Step 2:** `UNVERIFIED_TOKEN_FLOOR` (N), the cache path, the freshness-stamp threshold (brief §12).
- **Step 4:** which provider usage-API endpoints + auth schemes; the `costroid connect` CLI UX; how reconciliation renders.
- **Step 5:** forecast algorithm, anomaly baseline, budget persistence (config schema), alert thresholds + copy.
- **Step 6 (taskbar):** the GUI/UX design (none exists yet) — pin before the handoff.

| Step | Autonomous fit | Size | Blocked by | Human gate | Internal fan-out |
|---|---|---|---|---|---|
| 1 — release 0.2.0 | conditional | M | — | **yes** — tag/publish is outward-facing | serial: local prep → human approves → human tags |
| 2 — Claude `statusLine` capture | **strong (best first handoff)** — the brief is a near-executable spec | L | — (folds in §3's data-model half) | the new `setup-statusline` CLI surface | A data-model → {B provider+cache · C render+fixtures · D setup cmd} |
| 3 — generalize quota | strong | M | — | layering review (`Estimated` core-only) | A enums+migrate → B `Capability` → C core `Estimated` → D `Spend` render |
| 4 — connections | **poor** | XL | **Step 3** | **yes ×4** — keychain, network, CI re-scope, CLI | 4a infra+CI re-scope → 4b keychain+http → 4c keys+CLI |
| 5 — tabs + alerts | conditional | L | **Step 3** (`Capability`) | CLI surface; alert/forecast/budget policy | Capability prereq → cheap re-cuts → analytics → alerts |
| 6 — egui taskbar | **poor** | XL | **Steps 2–5** | release; GUI/UX design (none exists yet) | scaffold → per-tab fan-out → AccessKit → cross-platform |

**Bottom line:** the model executes successfully if you (a) go in dependency order with **3 riding alongside 2**, (b) accept the human gates on 1 / 4 / 5 / 6 as checkpoints rather than fighting them, and (c) split the L/XL steps into the sub-units above. **Start with Step 2 + §3's data-model half** — the highest-fit, highest-value first handoff.

---

## 11. Driving the plan — fresh agent per task

*You run each task in a **fresh agent** (new context) for context hygiene. So every task is self-contained, ends with the repo **green and the next task unblocked**, and fits one context. §11.1–11.3 are the loop; §11.4 is the ledger you paste from. `docs/` (this plan included) is tracked in the repo — edit it on disk as you go; the changes commit with the task.*

### 11.1 The loop

1. **Pick the next task** in §11.4 whose **Prereq** is met. Don't skip prerequisites.
2. **Answer its 📌 PIN decisions** (one line each) before starting — otherwise the agent guesses.
3. **Start a fresh agent**, paste **§12.0 (the standard header) + that task's §12 body block** (resolve its 📌 first). Use **ultracode-xhigh + workflows** for tasks marked **L/XL**.
4. **Run to green.** The agent implements, runs the gate (§11.3), iterates until it passes, then reports the gate output + a ≤5-line summary + the "Next" confirmation. It does **not** commit.
5. **Verify + commit.** Skim the diff, run the gate yourself once, commit one focused commit per task (or a `step-N` branch if you want review). `docs/` is tracked now, so any plan/ledger edits the agent made — including its ticked §11.4 Progress box — are part of that diff; confirm the tick is right and include them in the commit.
6. **At a ⛔ HUMAN GATE** (release · secrets/keychain · network · public CLI/export surface · ToS text) the agent stops for your decision — by design (CLAUDE.md "decide vs ask").
7. Repeat in a brand-new agent.

A task that can't reach green isn't done: the agent stops and reports the blocker, never commits red.

### 11.2 The handoff prompt → use §12.0

The paste-ready prompt is **§12.0 (the standard header) + the task's §12.x body block** — that is the
single source of truth for what an agent receives. It already tells the agent to read §11.5 (where the
D1 type/behavior/render boundary + the pinned defaults live), to stay in the Scope fence, to finish on
the four-command green gate, to STOP on un-pinned decisions and ⛔ gates, to keep the plan current, and
not to commit. Fill in the task's 📌 answers, paste both blocks into a fresh ultracode-xhigh agent
(add "use workflows" for L/XL tasks), and go.

### 11.3 Universal Done-When (every task)

Done only when **all** hold: (1) the four-command gate above is **green**; (2) the card's **Done-when** is met **with a test that proves it**; (3) the tree is clean (`docs/` untouched) and the card's **Next** is satisfied; (4) §6 invariants held (no telemetry; no network outside `costroid-connect`; secrets keychain-only; `--plain`/no-color-alone on new visuals). A surface/secret/network/release task stops at its ⛔ for your approval before finalizing.

### 11.4 The task ledger

*Dependency-ordered. ⛔ = human gate · 📌 = pin before starting · S/M/L/XL = size. **T1 is independent; T2 is the lynchpin for all build work.** Cards **T1–T8 are all DONE** (T8: §12.9 / §11.5, gate green 2026-06-09, ⛔-approved); **T9a is DONE** (§12.11 / §11.5, gate green 2026-06-10, ⛔-approved); **T9b is DONE** (§12.12 / §11.5, 2026-06-13, 264 tests, both ⛔ gates cleared); **T9c is DONE** (§12.13 / §11.5, 2026-06-13, 280 tests — the pure-core reconciliation engine, no connect dep); **T10a is DONE** (§12.14 / §11.5, 2026-06-15 — the connect/disconnect/connections CLI + key validation + the connect-action test; the first caller + first real network; ⛔ GATE 2b cleared); **T10c is DONE** (§12.15 / §11.5, 2026-06-15 — the `costroid reconcile` estimate-vs-invoice display surfacing T9c, no new ⛔ gate); **only T10b/release (§12.10) remains** on the 0.4.0 line (its ⛔ legal gate now CLEARED 2026-06-16 — §11.5 / `docs/proposals/T10b-LEGAL-REVIEW.md` §10; only the release mechanics remain), and T10-LIVE-ROWS (§12.16) is fulfilled/closed; **T11–T17 are now PINNED + carded** (§12.17–§12.23, 2026-06-16 — Step 5 analytical tabs + alerts; pins in §11.5 "📌 STEP 5 PINNED"); build each in a fresh agent per §12.0, in order, **T11 first** (it lands the tab model T12/T13 inherit). **T18+ (egui taskbar) remains backlog.***

> These cards are the at-a-glance **map**. The full, **paste-ready prompts live in §12** and are the source of truth — when a build agent revises a task it edits §12 + logs in §11.5, not these cards. The T2/T4/T6 boundary (types vs behavior vs render) is settled in **§11.5 D1**.

**Progress — the version-controlled "where are we"** (every fresh agent and you read this; the finishing agent ticks its own box as part of its doc edits, you confirm on commit):
- [x] **T1** Release v0.2.0 — ✅ **shipped 2026-06-05** (GitHub Release + Homebrew + npm + crates.io all at 0.2.0; `cargo install costroid` → 0.2.0 verified)
- [x] **T2** Quota data-model foundation *(lynchpin)* — ✅ types + pure map + migration landed, gate green (see §11.5)
- [x] **T3** Capability descriptor — ✅ `DataSource`/`AuthMethod` enums + `Capability` struct + `capability()` trait method + 3 impls + test landed, gate green (see §11.5)
- [x] **T4** Claude statusLine capture — cache + cross-check — ✅ `parse_limits` reads/sanitizes the rate_limits cache; core `window_token_volume` + cross-check finalize (demote → `Unverified`, stale age-out, estimate fallback); 7 bad-data fixtures; gate green, 154 tests (see §11.5)
- [x] **T5** `setup-statusline` + `--capture-only` — ✅ command + `StatuslineArgs {capture_only, wrap}` + `setup.rs` (idempotent settings.json wiring, backup/undo, atomic cache writer); gate green, 58 cli tests (see §11.5)
- [x] **T6** Render new limit states + Spend windows — ✅ all 5 `LimitAvailability` arms + `Spend` dollar line + the `? unverified` cue (neutral meter) + the always-on "as of HH:MM" stamp (UTC, deterministic) + Claude chat caveat + statusline `Unverified` selection; `captured_at` threaded onto `LimitSummary`; gate green, 176 tests (see §11.5)
- [x] **T7** `costroid-connect` infra + CI re-scope — ✅ empty feature-gated crate (gate on `apps/cli`'s `connect`, off by default) + two-tier resolved-graph `offline.rs` (default forbids the trio + connect unlinked; `--features connect` admits only `ureq`/`rustls`/`keyring`) + `deny.toml` `wrappers` scoping + script STUB; gate green, 177 tests (see §11.5)
- [x] **T8** Keychain credential store — ✅ **DONE 2026-06-09 (gate green, ⛔-approved)**; `costroid-connect` credential store (`ApiVendor`/`CredentialStore`/`ConnectionRegistry`), `keyring` 3.6.3 sync Secret Service, async-io stays banned; pure-library (CLI → T10); see §11.5 ✅ T8
- [x] **T9a** `costroid-connect` HTTP infra — ✅ **DONE 2026-06-10 (gate green, ⛔-approved)**; the generic authorized-host HTTPS client (`AuthorizedClient`/`AuthHeader`/`HttpResponse`/`RequestLimits`) on blocking `ureq` 3.3.0 + `rustls` (ring), OS-native roots via `rustls-native-certs` (no `webpki-roots`), redirects + proxies disabled, GET/HTTPS-only, secrets redacted; offline.rs asserts the full trio, deny wrappers fire with zero warnings; no caller, no provider knowledge, no `core`/`focus` dep (deferred to T9b); see §11.5 ✅ T9a
- [x] **T9b** the two usage-API adapters (Anthropic + OpenAI) + the Gemini first-class-unavailable state — ✅ **DONE 2026-06-13 (gate green, 264 tests, both ⛔ gates cleared — Gate 1 secret-handling approved; Gate 2 live-confirmed at the envelope level, populated-row checks logged as follow-ups since the org has no raw-API usage)**; `AnthropicAdapter`/`OpenAiAdapter` parse into the new `costroid-core::vendor_report` types (`UsdAmount` unit-tagged money, cost/usage reports, typed caveats, `VendorReportUnavailable`); Gemini = first-class unavailable; `costroid-connect` gained its first internal dep (`costroid-core` only); see §11.5 ✅ T9b
- [x] **T9c** estimate-vs-invoice reconciliation engine (pure core) — ✅ **DONE 2026-06-13 (gate green, 280 tests)**; `costroid-core::reconcile` (`reconcile_cost` + `LocalCostEstimate`/`CostReconciliation`/`DayReconciliation`/`ModelReconciliation`/`VendorBilled`/`BilledAbsence`/`ReconciledReportStatus`) compares the local API-lane estimate (UTC-day bucketed) against T9b's vendor cost report; signed `variance`/`variance_pct`, typed vendor-side absence (never `$0`), caveats carried through; pure-core, no connect dep (core's Cargo.toml unchanged); see §11.5 ✅ T9c
- [x] **T10a** connect/disconnect/connections CLI + key validation + the connect-action test — ✅ **DONE 2026-06-15 (gate green: 290 workspace tests — incl. connect 64, core 85 — 0 failed; +11 Layer-1 connect-action tests under `--features connect-test-support`; offline.rs both tiers + cargo-deny both + offline_acceptance.sh Layer-2 green)**. First caller of `costroid-connect` + first real network (opt-in, behind `--features connect` + an explicit `connect`/`connections --check` action; default build still zero network). `connect`/`disconnect`/`connections [--check]` CLI (stdin-only key, never argv/env); Anthropic `validate()` → `GET /v1/organizations/me`; OpenAI probe = `fetch_cost_report` over a completed-day window (`GET /v1/organization/costs`); non-secret `OrgLabel` on `RegistryFile`; the injectable `AdapterSet` command core; Layer-1 (loopback + keyring mock) + Layer-2 (netns fail-closed) tests. **⛔ GATE 2b CLEARED** (the 2026-06-14/15 own-key live-confirm: money units confirmed, Responses-API coverage confirmed → `responses_api_coverage_unconfirmed → false`, `/costs` string/scientific/over-scale money-shape fix landed, real-body fixtures pinned). ⛔ CLI-surface + secret-handling approvals presented at build. (The ⛔ legal review of the flow gates T10b/release, not this card.) See §11.5 ✅ T10a.
- [x] **T10c** reconciliation display (`costroid reconcile`) — ✅ **DONE 2026-06-15 (gate green: cli 90 default tests — incl. 13 reconcile render snapshots/asserts — + core 85 + connect 64, 0 failed; +5 reconcile.rs tests (3 Layer-1 loopback + 2 window) under `--features connect-test-support`, 106 total cli; offline.rs both tiers + cargo-deny both + offline_acceptance.sh default tier green; independently adversarially reviewed, 0 critical/high/medium)**. Surfaces T9c's `CostReconciliation`: `costroid reconcile [--vendor anthropic|openai] [--period day|week|month|year]` fetches each connected vendor's `cost_report` over a COMPLETED-day window (reusing T10a's stored key + authorized client — no new secret/network boundary), vendor-scopes the FOCUS rows (`claude-code`→Anthropic, `codex`→OpenAI, `cursor`→excluded), reconciles via `reconcile_cost`, and renders the signed variance HONESTLY (typed absence as text never `$0`; caveats footnoted; local figure always `~`-labeled). The fetch rides the injectable `AdapterSet::cost_report` seam (loopback-tested, zero real network); the renderer is a pure function of the core type (snapshot-tested incl. `--plain`). Prereq: T10a. See §11.5 ✅ T10c.
- [ ] **T10-LIVE-ROWS** deferred populated-row / probe-behavior live-confirm — **carded at §12.16**; holds any ⛔ GATE 2b item T10a can't confirm for lack of real raw-API usage. Prereq: real API usage on the connected org.
- **T10b — release v0.4.0 — ✅ DONE / SHIPPED 2026-06-16** (§11.5): live on crates.io (5-crate ladder), the GitHub Release (installers + 6-target binaries + checksums + attestations), and Homebrew + npm. Closed **Step 4 (connections)**. (One failed Release run — a `dist build` libdbus-on-runner gotcha — fixed by `precise-builds = true` and a `v0.4.0` re-tag; see §11.5.)
- [x] **T11** Providers tab — ✅ **DONE 2026-06-16** (gate green: default `cargo test --workspace` cli 97 + core 85, 0 failed; `--features connect-test-support` cli 117; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green — connect-delta still the reviewed allowlist). The FIRST production consumer of `Capability`: a new `Screen::Providers` tab renders, per provider, each lane's honest source + auth + quota shape + detection health via the owned `ProviderCapabilityView` core seam (captured for every provider before the `Box<dyn Provider>` set is consumed); Cursor renders `detected` + "no sanctioned source" (never "coming soon"). Lands the numbered-tab model T12/T13 inherit (1–6 jumps + Tab/BackTab cycle; Frontier stays its a/esc overlay; footer + help enumerate tabs). Under `--features connect` only, a read-only connection lane (org label + connected/not via the dual gate; Gemini reuses the pinned `GEMINI_UNAVAILABLE_MESSAGE` verbatim; NEVER key material; NO new network). braille/ascii/plain snapshots committed + ASCII-purity gates extended. See §11.5 ✅ T11. (M · Prereq T3 ✅)
- [x] **T12** Models tab — ✅ **DONE 2026-06-16** (gate green: default `cargo test --workspace` cli 104 + core 88, 0 failed; `--features connect-test-support` cli 126; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green). A new `Screen::Models` tab (number `4` + the Tab cycle, appended to `TAB_SCREENS` — no `handle_key` change, exactly the T11 template) fuses per-model API spend + token mix (the now/trends-consistent `CostLaneSummary`) with the bench/frontier overlay (standing + equal-volume re-pricing) via the new pure-core `models_view`/`ModelsView`/`ModelRow` composite (reuses `summarize_rows` + `bench_view`, NO new pricing/bench math). API-cost rows only; spend ALWAYS `~`-hedged; un-benchmarked models render "not benchmarked" (a gap, never guessed); monochrome (`models_document_is_monochrome`). braille/ascii/plain snapshots committed + ASCII-purity gates extended. See §11.5 ✅ T12. (S · Prereq T11 ✅)
- [x] **T13** History tab — ✅ **DONE 2026-06-17** (gate green: default `cargo test --workspace` cli 114 + core 88, 0 failed; `--features connect-test-support` cli 136; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green — connect-delta unchanged, no new crate). A new `Screen::History` tab (number `5` + the Tab cycle, appended to `TAB_SCREENS` — exactly the T11 template) renders the full per-turn FOCUS record (time · model · token usage from `x_ConsumedTokens` · access path · API-only estimated cost), newest-first. Lands the TUI's FIRST scroll/viewport state — an `App::scroll` offset + Up/Down/PgUp/PgDn/Home/End, clamped in `draw_app` (no panic on empty/short lists), reset on tab switch — which the analytics tabs (T14–T16) reuse. The unchanged `costroid export` is surfaced in-tab + in help (no in-TUI file write — the pinned default). Monochrome (`history_document_is_monochrome`); braille/ascii/plain + empty snapshots committed + ASCII-purity gates extended. See §11.5 ✅ T13. (M · Prereq T11 ✅ + T12 ✅)
- [x] **T14** Budget — ✅ **DONE 2026-06-17** (gate green: default `cargo test --workspace` cli 133 + core 98, 0 failed; `--features connect-test-support` cli 155; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green — `toml` lands in BOTH graphs so the connect-delta is unchanged; `cargo deny check licenses bans` ok with `toml`; an independent adversarial-review workflow fixed 2 honesty bugs — see §11.5). The FIRST user-config file: a read-only, non-secret TOML at `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml` (`apps/cli/src/config.rs` — path resolver + forward-compat serde + a LOADER: absent ⇒ zero-config default, malformed ⇒ a typed non-panic error surfaced as a TUI status line). A new `Screen::Budget` tab (number `6` + the Tab cycle, appended to `TAB_SCREENS` — NO `handle_key` change, exactly the T11 template) compares the config's monthly $ target(s) against the **current month's API-lane spend** via the new pure, config-neutral `budget_view`/`BudgetTargets`/`BudgetView` core seam (a fill meter + pace cue + the honest over-budget state). API-lane ONLY; a flat-fee subscription gets NO $ comparison (§170 — surfaced in `flat_fee_tools`); every figure `~`-hedged. The ONE tab allowed amber/red — but ONLY paired with a non-color cue (`!`/`!!`/`OVER`, spelled out in `--plain`); empty state points at the config file. NO writer/`set` command, NO network/invoice enrichment (use the local estimate; `costroid reconcile` is invoice-true). braille/ascii/plain + no-budget snapshots committed + ASCII-purity gates extended; adds the permissive `toml` dep. See §11.5 ✅ T14. (L · Prereq T11 ✅ — the FIRST user-config file, TOML)
- [x] **T15** Forecast — ✅ **DONE 2026-06-17** (gate green after the coordinator-review fixes: default `cargo test --workspace` cli 144 + core 106, 0 failed; `--features connect-test-support` cli 166; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green — NO new dep, so the connect-delta + strace/offline + `cargo deny` are unchanged; the builder's own review reported 0 defects, but a separate independent coordinator review found + fixed 2 honesty issues — a fabricated `~$0.00` no-usage visual header + a UTC-weekday-label skew — see §11.5 🔧). A new `Screen::Forecast` tab (number `7` + the Tab cycle — the FIRST tab past the original digit range, so it extended `handle_key`'s digit match `'1'..='6'` → `'1'..='7'` + the footer/help/doc-comment trueing) projects this month's API-lane $ spend (a linear run-rate over the elapsed **UTC** month — numerator + denominator on ONE calendar, the consistency trap) + per-quota-window exhaustion ETAs (a linear burn off ONLY a fresh cross-checked `Available` `TokenFraction`, degrading to "ETA unavailable" on every other arm), via the new pure-core `forecast_view`/`ForecastView`/`SpendForecast`/`QuotaEta` seam over a NEW shared `pub(crate)` per-UTC-day API-lane $ series helper (`reconcile::api_lane_daily_usd_series` — T16 reuses it). The $ projection is suppressed below a 3-day floor; every figure is a labeled estimate; monochrome (`forecast_document_is_monochrome`); actual-vs-projected distinguished by glyph (never color) + surviving `--plain` as text. braille/ascii/plain + insufficient-data + ETA-unavailable snapshots committed + ASCII-purity gates extended. See §11.5 ✅ T15. (L · Prereq T11 ✅ — unblocks T16's shared daily-series helper)
- [x] **T16** Anomalies — ✅ **DONE 2026-06-17** (gate green after BOTH review passes: default `cargo test --workspace` cli 158 + core 116, 0 failed; `--features connect-test-support` cli 180; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers green — NO new dep, so the connect-delta + strace/offline + `cargo deny` are unchanged; a builder-run review fixed 7 findings, and a separate independent coordinator review then found + fixed 3 more — all subscription-persona honesty: a no-API-usage user shown transient "0 of 7 days", the undisclosed API-lane mix scope, and a 0.5% rounding guard — see §11.5 🔧🔧). A new `Screen::Anomalies` tab (number `8` + the Tab cycle — the LAST numbered slot, so it extended `handle_key`'s digit match `'1'..='7'` → `'1'..='8'` + the footer/help/doc-comment trueing) surfaces proactive, non-alarmist callouts vs the user's OWN recent history — **TWO signals, both off `snapshot.focus_rows`: a daily spend spike + a model-mix shift** (the quota-burn signal is DEFERRED — local data keeps no multi-day quota history; disclosed in the footnote, never faked). Baseline = median + MAD over the trailing 14 UTC days, flagged only past a conservative `3.5·MAD` with an absolute floor guarding the MAD=0 pitfall, suppressed below a 7-day history floor (honest "not enough history yet - N of 7 days" state). New pure-core `anomalies_view`/`AnomaliesView`/`Anomaly`/`AnomalySignal` seam (mirrors `forecast_view`; Serialize-only, not Eq, computed-never-persisted) + a new shared `pub(crate)` per-(UTC-day, model) token bucketer (`reconcile::api_lane_daily_token_series`) sharing the SAME lane filter as the $ series. Every magnitude is a hedged estimate; Decimal money never f64; monochrome (`anomalies_document_is_monochrome`); a non-color marker (`◆`/`*`) survives `--plain`. braille/ascii/plain + clean + insufficient-history + suppressed-multiple snapshots committed + ASCII-purity gates extended. See §11.5 ✅ T16. (L · Prereq T11 ✅ + T15 helper ✅ — its signals feed T17 Alerts)
- [x] **T17** Alerts — ✅ **BUILT 2026-06-17, ⛔ AWAITING SIGN-OFF** (gate green: default `cargo test --workspace` **cli 177 + core 123**, 0 failed, + a new `alerts_cli` integration test (3); `--features connect-test-support` cli 199; clippy `-D warnings` clean on default + `connect` + `connect-test-support`; fmt clean; offline.rs both tiers + `offline_acceptance.sh` (netns) green — **NO new dep**, so the connect-delta + strace/offline + `cargo deny` are unchanged; a builder-run review fixed 1 real issue — the render `WARN`/`CRITICAL` consts weren't actually aliasing the core consts — plus a low + an exit-2 test; a SEPARATE independent coordinator review then found + fixed 2 more LOW — an inverted quota-threshold pair (`warn>critical`) accepted verbatim, and a README tab undercount — see §11.5 🔧🔧). Opt-in threshold alerts, **default OFF**: a new pure, config-neutral core seam `active_alerts(&NowSummary, &BudgetView, &AlertThresholds) -> Vec<Alert>` detects two NEVER-mixed classes — **quota %** (one alert per window at/above WARN/CRITICAL, fired ONLY off a fresh cross-checked `LimitAvailability::Available` reading — token-fraction OR a `Spend` pool with a known allowance — never `Unverified`/`Estimated`/`Partial`/stale, the T15 discipline) and **budget $** (a `BudgetRow` strictly over its monthly target, riding `over_by_usd`). Forecast-hit + anomaly firing are explicitly OUT (deferred fast-follow). Thresholds are CORE consts `ALERT_WARN_FRACTION=0.80`/`ALERT_CRITICAL_FRACTION=0.95` (the apps/cli render `WARN_FRACTION`/`CRITICAL_FRACTION` now ALIAS them — one source, never forked), defaults of `AlertThresholds`, user-overridable via a new `[alerts]` config section (`enabled` default-false, optional `quota_warn`/`quota_critical`, forward-compat serde, hostile values fall back). Delivery = an inline `now` banner (shown atop `render_now_document` when enabled + crossing; amber/red allowed but ALWAYS paired with a `!`/`!!` (visual) or spelled-out (plain) cue, like Budget) + a new `costroid alerts [--check]` clap command (human list with honest off/clear states; `--check` is cron-friendly: exit 0 clear/off, 1 a crossing + one line, 2 a config/collect error). NO daemon, NO desktop notifications (notify-rust deferred behind a future Cargo feature), NO network/telemetry. braille/ascii/plain banner + command snapshots committed + ASCII-purity gates extended + the detector/check-line/exit-code unit-tested. See §11.5 ✅ T17. (L · ⛔ · Prereq T14 config) → **closes Step 5 / 0.5.0**
- [ ] **T16b** All-lane model-mix (fast-follow) — ⏭ **DEFERRED, Eren-confirmed 2026-06-17** (S–M · Prereq T16 ✅): widen the Anomalies model-mix-shift signal from API-lane-only to all-lane token share so a subscription-only user (Claude Code Max) gets model-mix callouts — also moves `history_days`/the 7-day suppression to count all-lane token-days (spend-spike stays API-lane — no $ on subscription). A real enhancement, but its own small design pass; NOT bolted onto T16. Card it after Step 5 (or slot before the taskbar). See §11.5 ⏭.
- T18+ — backlog (egui taskbar, Step 6; carded when its Prereq lands).

**T2 + T4 + T6 ticked = the 0.3.0 milestone** (Claude live quota + generalized model).

**T1 — Release v0.2.0** · ⛔ · S · Prereq: none (independent of the build tasks)
- **Goal:** ship the already-built cost lane (frontier, Cursor-detect, WSL fix).
- **Agent does:** confirm the gate is green on `main`; run `dist plan` + `dist build --artifacts=local` (dry-run, report only); draft the version bump in `Cargo.toml` + refresh `Cargo.lock`; add a CHANGELOG entry; flip README "next release" → "shipped." Report; **do not tag/push.**
- **⛔ You do:** review, then `git tag v0.2.0 && git push origin v0.2.0`; verify `cargo install costroid` post-release.
- **Done when:** gate green; `dist plan` clean; version + lockfile bumped; (after your tag) release CI succeeds.
- **Next:** independent — blocks nothing, but **tag v0.2.0 before T2+ work reaches `main`** (or branch T2–T6) so 0.2.0 ships only the built cost lane, not half-finished 0.3.0 quota work.

**T2 — Quota data-model foundation** · M · Prereq: none — *do this first of the build work* · ✅ **DONE (gate green — see §11.5)**
- **Files:** `crates/costroid-providers/src/lib.rs` (`LimitKind`, `LimitWindow`, the 3 `parse_limits`); `crates/costroid-core/src/lib.rs` (`LimitAvailability`, `limit_availability`).
- **Goal:** generalize the quota types so every later provider/feature fits one shape (§2a).
- **Scope fence:** types + migration only. No statusline capture, no rendering beyond compiling, no new providers, **no `RequestCount`**.
- **Deliverables:** `LimitKind += Daily, Monthly, BillingCycle`; `enum LimitMeasure { TokenFraction(f64), Spend { used_usd: Decimal, included_usd: Option<Decimal> } }`; `enum LimitStatus { Verified, Unverified, Unavailable }`; on `LimitWindow` add `captured_at: DateTime<Utc>` + `status: LimitStatus` and replace `used_fraction` with `measure: Option<LimitMeasure>`; reshape core `LimitAvailability` so its arms carry the `measure` and add `Unverified` + `Estimated` (5 variants, availability layer only — never on `LimitWindow`); add minimal placeholder render arms to stay green (T6 does the real rendering). Migrate Codex (stays `Verified` `TokenFraction`), Claude (`Unavailable` for now), Cursor (empty) + every constructor + every existing test. (Full shape in §11.5 D1 / §12.2.)
- **Done when:** gate green; existing limit tests updated and passing.
- **Next:** the new types exist → T3, T4, T6 (and later the Providers tab) build on them.

**T3 — Capability descriptor** · S · Prereq: T2 · ✅ **DONE (gate green — see §11.5)**
- **Files:** `crates/costroid-providers/src/lib.rs` (the `Provider` trait + 3 adapters).
- **Goal:** make each provider *declare* its data sources / auth / quota shape (§2b) so unavailability renders honestly and new providers slot in by descriptor.
- **Scope fence:** the descriptor + its impls only; no UI yet.
- **Deliverables:** `enum DataSource { LocalArtifact, SanctionedHook, SanctionedOauth, ApiKey, Unavailable }`; `enum AuthMethod { None, Oauth, ApiKey }`; `struct Capability { api_cost: DataSource, subscription_quota: DataSource, model_mix: DataSource, auth: AuthMethod, quota_kinds: &'static [LimitKind] }`; `fn capability(&self) -> Capability` on the trait, implemented for Claude/Codex/Cursor with today's honest values (Cursor `auth: None`).
- **Done when:** gate green; a test asserts each provider's descriptor.
- **Next:** Providers tab (T11) + the deferred adapters can rely on `Capability`.

**T4 — Claude statusLine capture: cache + parse_limits** · L · Prereq: T2 · ✅ **DONE (2026-06-06, gate green — see §11.5)**
- **Spec:** `docs/STATUSLINE-CAPTURE-BRIEF.md` — read it fully; it IS the design. **Files:** Claude adapter in `costroid-providers`; the cross-check helper in `costroid-core`.
- **📌 PIN before start:** `UNVERIFIED_TOKEN_FLOOR` (brief proposes `5_000`) · cache path (brief proposes `${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json`) · `LIMIT_FRESHNESS_STAMP_MINUTES` (brief proposes `10`).
- **Goal:** read the sanctioned `rate_limits` cache, sanitize + cross-check against local volume, map to a `LimitWindow` with the right `LimitStatus`.
- **Scope fence:** the cache read + `parse_limits` + cross-check only. `setup-statusline` is T5; rendering is T6.
- **Deliverables (per the brief):** cache read; sanitize (`>100` / `==resets_at` → no data); cross-check (high % + trivial local volume → `Unverified`); `captured_at`; epoch + RFC3339 `resets_at` parsing; the `window_token_volume` helper in core; bad-data fixtures (poisoned / 900% / false-100 / absent / stale / iso / happy).
- **Done when:** gate green; fixtures prove each degrade path (never a confident wrong number).
- **Next:** Claude windows carry real status → T5/T6 can surface them.

**T5 — `setup-statusline` + `--capture-only`** · M · ⛔ (public CLI surface) · Prereq: T4 · 📌 · ✅ **DONE (2026-06-06, gate green, ⛔-approved — see §11.5)**
- **Spec:** the brief's setup section. **Files:** `apps/cli/src/main.rs` (Command enum) + a new setup module.
- **📌 PIN:** the idempotency sentinel (brief: `# costroid:statusline-capture v1`).
- **Goal:** `costroid setup-statusline` wires Claude Code's `settings.json` to tee `rate_limits` into the cache (snippet-into-existing, or be-the-statusline) + a `statusline --capture-only` flag.
- **🔗 Reuse T4's path + shape (no drift):** the capture-write target **must** match what T4's reader uses — path `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` and the JSON shape `{ captured_at, five_hour:{used_percentage,resets_at}, seven_day:{…} }`. T4's resolver `claude_rate_limits_cache_path()` is **private in `costroid-providers`**; expose it `pub` (or a `pub fn` wrapper) and call it from the CLI rather than re-deriving the string, so read and write can never diverge.
- **Scope fence:** the command + flag + idempotent settings.json editing (backup/undo) only.
- **⛔ Human gate:** new public CLI surface — stop for approval before finalizing flags/UX.
- **Done when:** gate green; idempotent re-run tested; malformed/absent settings.json handled; capture parse-failure exits 0.
- **Next:** end-to-end Claude live quota works on a Pro/Max machine.

**T6 — Render new limit states + Spend windows** · M · Prereq: T2 (+ T4 for live data) · ✅ **DONE (2026-06-06, gate green — see §11.5)**
- **Files:** `apps/cli/src/render.rs` (`render_limit_line` / `plain_limit_line` / `state_cue`) + snapshots; **and `costroid-core`** to plumb `captured_at` (see ⚠️ below).
- **Goal:** render `Available / Partial / Unavailable / Unverified / Estimated` and `Spend` windows (dollar pool used/included, never a fabricated %), with the always-on "as of HH:MM" stamp + the claude.ai-chat under-report caveat (brief §8).
- **✅ Done in T6 (was: ⚠️ Do first, T4 handoff):** `captured_at` was threaded onto `LimitSummary` in `limit_summary`, giving the "as of HH:MM" stamp its source. Internal struct only, no export-schema gate.
- **Scope fence:** rendering + snapshots only.
- **Done when:** gate green; snapshot tests cover every availability arm in braille/ASCII/plain; plain asserts no ANSI.
- **Next:** the **0.3.0 milestone** (Claude live quota + generalized model) is complete.

**T7 — `costroid-connect` infra + CI re-scope** · L · ⛔ · Prereq: T3 · ✅ **DONE (2026-06-06, gate green, ⛔-approved — see §11.5)**
- **Files:** new `crates/costroid-connect/`; root `Cargo.toml` (member + feature); `deny.toml`; `apps/cli/tests/offline.rs`; `scripts/offline_acceptance.sh`.
- **Goal:** create the feature-gated network/credential crate **with no behavior yet**, and re-scope the no-network guarantees so the default build still *proves* zero network.
- **Scope fence:** crate skeleton + feature gate + test re-scoping only. **No keychain, no HTTP yet** (T8/T9).
- **⛔ Human gate:** changing offline-acceptance + forbidden-crates is a guarantee redefinition — stop for approval on the re-scoped assertions.
- **Deliverables:** `costroid-connect` behind feature `connect` (off by default); `deny.toml` + forbidden-crates scoped so `ureq`/`rustls`/`keyring` are allowed only in `costroid-connect`; offline-acceptance asserts the default build makes zero calls; a new test asserts network happens only with the feature on + an explicit action (stub).
- **Done when:** default `cargo build`/`test` green AND offline-acceptance still passes; `--features connect` builds.
- **Next:** keychain (T8) and HTTP (T9) have a home.

**Backlog — carded when its Prereq lands (📌 must be pinned first):**
- **T8 — keychain credential store** · ⛔ · Prereq T7 · ✅ **DONE 2026-06-09 (gate green, ⛔-approved) → §12.9 / §11.5** (pure-library; the `costroid connect` CLI + Connections view moved to T10)
- **T9 — usage-API clients + reconciliation** · ⛔📌 · Prereq T7,T8 — **📌 PINNED + ⛔ SIGNED OFF 2026-06-10** (`docs/proposals/T9-PIN-PROPOSAL.md`; logged in §11.5): **Anthropic** Admin cost/usage reports + **OpenAI** org costs/usage (tier-3 own admin key each) · **Gemini = defer** (no sanctioned static-key usage API → first-class "unavailable"). **Split:** **T9a** `costroid-connect` HTTP infra — `ureq`+`rustls` + a generic authorized-host client (the HTTP layer the T10 offline-acceptance network test exercises) + adds the `ureq`/`rustls` crates so their `deny.toml` wrappers (carried since T7 as forward-looking no-ops) finally fire — clearing the 2 benign `unused-wrapper` warnings — and adds their presence assertions to `offline.rs` (keyring's T8 precedent); ⛔ guarantee-redefinition like T7/T8, **no provider knowledge**; its first `core`/`focus` deps only if the client API actually needs them (else they defer to T9b) — **carded at §12.11; ✅ DONE 2026-06-10 (gate green, ⛔-approved; `core`/`focus` deps deferred to T9b as expected)** · **T9b** the **TWO** per-provider usage-API adapters — Anthropic + OpenAI — plus the Gemini first-class-unavailable state *(amended from "3 adapters" by the signed-off proposal)* (read keys via `CredentialStore::retrieve(ApiVendor)`; each a §8 live-shape confirm) — **carded at §12.12** (2026-06-10, against the T9a client API as built) · **T9c** the estimate-vs-invoice reconciliation engine (pure `costroid-core`, fixture-tested, **no network** — see DATA-MODEL reconciliation) — **carded at §12.13** (T9b-dependent slots marked). T8's pure-library↔CLI carve-out + §10 Rule 3 (gating prereq, then parallel sub-units) is the precedent.
- **T10 — connect/disconnect CLI + Connections view** · ⛔📌 · Prereq T8,T9 + ⛔ **GATE 2b** (populated-row live-confirm — §11.5 ✅ T9b) — 📌 connect UX, reconciliation display · ⛔ **legal review of the connection flows before this ships** (own-key + sanctioned OAuth only — see Step 4). **Also finishes** the `scripts/offline_acceptance.sh` feature-on connect-ACTION network test (the connect action reaches only the authorized host · the secret lands only in the keychain · disconnect leaves no residue — replaces the `T9/T10` STUB at the script's tail). The 0.4.0 release itself is cut by **T10b**.
- **T10b — Release v0.4.0 (connections)** · ⛔ · S · Prereq T9, T10 + the ⛔ legal review signed off → **cuts v0.4.0** — the release-mechanics cap on Step 4 (the connections analogue of T1, which cut v0.2.0). Version bump 0.3.0→0.4.0 across the **four** `[workspace.dependencies]` constraints (now incl. `costroid-connect`; the CLI has no entry — the §11.5 T1 lockstep gotcha, with all **5** `version.workspace` members bumping together) + `Cargo.lock` + CHANGELOG + README/SECURITY release line; `dist plan` / host `dist build` dry-run; then the human tags + runs the **extended** crates.io ladder `focus → providers → core → connect → cli`. **Carded at §12.10.**
- **T11 Providers tab** (§12.17) · **T12 Models tab** (§12.18) · **T13 History tab** (§12.19) — cheap re-cuts, **PINNED + carded 2026-06-16** (T11 lands the numbered-tab model)
- **T14 Budget ✅ · T15 Forecast ✅ · T16 Anomalies ✅ · T17 Alerts ⛔ (BUILT 2026-06-17, awaiting sign-off)** — T14–T16 **DONE 2026-06-17** (§12.20–§12.22); T17 built (§12.23 / §11.5 ✅ T17), the plan's only Step-5 ⛔ awaiting the CLI-surface + copy + default-off sign-off → on sign-off **Step 5 COMPLETE → 0.5.0**
- **T18+ — egui taskbar** · ⛔ · Prereq T2–T6 (CLI feature-complete) — greenfield: needs a GUI design first, then per-tab fan-out → **0.6.0**
- **Cursor live quota — discovery-gated (§8), not a numbered build task.** Pursued only if Cursor publishes a sanctioned/documented API or first-party OAuth (never session reuse against `api2.cursor.sh`); until then Cursor stays detect-only / "unavailable." Card it (like Copilot/Antigravity) only after that discovery lands.

When you reach a backlogged task, pin its 📌 and have a planning agent expand it into a full T1–T7-style card before you hand it to a build agent.

### 11.5 Decisions & limitations (living log)

*New decisions/constraints land here as tasks run — agents append (newest first), dated by the task that surfaced them. This is where "a new decision/limitation" goes.*

**✅ T17 BUILT — Alerts (2026-06-17); ⛔ AWAITING SIGN-OFF (the plan's only Step-5 ⛔).** The closing Step-5 deliverable — opt-in threshold alerts, **default OFF**. Gate green after BOTH review passes (default `cargo test --workspace` **cli 177 + core 123 + connect 65**, 0 failed, + a new `alerts_cli` integration test (3); `--features connect-test-support` **cli 199**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers + `offline_acceptance.sh` (netns) green — **NO new dep**, so the connect-delta + strace/offline + `cargo deny` are all unchanged). Built fresh-context + a **builder-run** adversarial-review workflow that fixed 1 real issue + 1 low; a **separate independent coordinator review** (5 dimensions × per-finding refute-verify) then confirmed the load-bearing ⛔ invariants clean (the detector fires quota ONLY off a fresh `Available` reading; exit codes 0/1/2 with no anyhow-Err collision; default-off master switch; copy honesty; no new dep/network) and found + fixed **2 more LOW** the builder's pass missed (🔧🔧 below). **Posture + sources were pre-resolved** (§11.5 Q4 + the 2026-06-17 sources decision); the ⛔ remaining is the standard build-time review of the **final CLI surface + the copy strings + default-off proof** — presented for sign-off, NOT yet committed.
- **Core seam = `active_alerts(&NowSummary, &BudgetView, &AlertThresholds) -> Vec<Alert>` (pure, config-neutral; `crates/costroid-core/src/lib.rs`)**, mirroring `budget_view` (thresholds are an INPUT, core reads no file). **TWO classes, NEVER mixed (separate `Alert` variants):** **(1) Quota %** — one alert per `now.limits` window at/above a fraction threshold, fired ONLY off a fresh, cross-checked `LimitAvailability::Available` reading (a `TokenFraction`, OR a `Spend` pool's `used/included` when the allowance is known + positive); EVERY other arm (`Unverified`/`Estimated`/`Partial`/`Unavailable`, or an allowance-less overage pool) yields nothing — the T15 discipline / ARCHITECTURE §9.2 (staleness is already aged-out to `Estimated` upstream). **(2) Budget $** — one alert per `BudgetRow` STRICTLY over its monthly target (riding `over_by_usd`), API-lane only. Result ordered **critical-tier first** (CRITICAL quota + any over-budget) then WARN quota, a stable sort (so a `--check`/banner headline is the most-pressing crossing). `Alert`/`AlertLevel`/`AlertThresholds` are `Serialize`-only, `PartialEq` not `Eq`, computed-never-persisted (mirrors the Step-5 view types). **Forecast projected-hits + anomaly callouts are NOT alert sources** (Eren-confirmed 2026-06-17 — deferred fast-follow; the detector wires neither `forecast_view` nor `anomalies_view`).
- **Thresholds live in CORE, never forked.** New `pub const ALERT_WARN_FRACTION = 0.80` / `ALERT_CRITICAL_FRACTION = 0.95` are the canonical near-limit fractions; `AlertThresholds::default()` uses them, AND the apps/cli render `WARN_FRACTION`/`CRITICAL_FRACTION` (render.rs:33-34) now **alias** the core consts — so the meter the user sees and the alert that fires share ONE number (the card's "don't fork a third set"). User-overridable per class via config (defaults when unset; a `NaN`/`inf`/zero/negative override falls back — never breaks the detector).
- **Config = the `[alerts]` section extending T14's `Config` (`apps/cli/src/config.rs`).** `enabled` (bool, **default false** — the master switch for BOTH the banner and the command) + optional `quota_warn`/`quota_critical` overrides. Forward-compat `#[serde(default)]` (absent ⇒ off; a newer file tolerated); malformed ⇒ the existing typed non-crash `ConfigError`. `Config::{alerts_enabled, alert_thresholds}` project it into the core `AlertThresholds`; **read-only, non-secret, NO writer** (mirrors `[budget]`).
- **Delivery (Q4, pre-approved) = an inline banner + a cron `--check`, both built-in, NO daemon, NO new dep.** **(a) Inline banner** — `render_now_with_alerts(_document)` prepends `push_alert_banner` ATOP `render_now_document` (render.rs:1149) in both the plain CLI `now` path and the TUI Now tab, **only when enabled + a crossing** (empty ⇒ byte-identical to the bare now view, test-pinned). Amber/red is allowed (the crossing state) but NEVER alone — every styled line carries a `!`/`!!` (braille/ascii) or a spelled-out `(near limit)`/`(critical)`/`(over budget)` (plain) cue, exactly like Budget; the rule separator is visual-only (Plain delimits by labels — the T11 gotcha). **(b) `costroid alerts [--check]`** — a new clap `Command::Alerts` (NO new TUI tab). `costroid alerts` = the human list (header + one line per crossing, or the honest "no active alerts"; the OFF state prints "alerts are off — enable …" + a copy-paste `[alerts]` stanza). `costroid alerts --check` = cron-friendly: **exit 0 clear/off (silent), exit 1 a crossing (one ASCII line), exit 2 a config/collect error (stderr — a distinct signal, never conflated with a crossing)**.
- **Copy (the ⛔ strings) — quota = quota-extension framing, NEVER money; budget = dollars; sentence case, no emoji.** Quota: `"{tool} {window} limit at {pct}, resets in {countdown}"` (e.g. `"claude code 5-hour limit at 97%, resets in 41m"`). Budget: `"{scope} budget over by {over}, spent {spent} of {target}"`, every $ `~`-hedged (e.g. `"codex budget over by ~$10.00, spent ~$60.00 of ~$50.00"`). The cron line: one alert → its sentence prefixed `costroid: `; several → `costroid: N active alerts; most pressing: {headline}`.
- **Honesty verified on REAL local data:** with alerts enabled on the dev box, the detector correctly fired NOTHING — the real Claude windows are `Estimated` (quota % unavailable, not a fresh `Available`) and all real spend is subscription-lane (no API lane to over-budget) — so the "never fire off an Unverified/Estimated/stale reading" + "API-lane-only budget" disciplines hold on live data, not just fixtures.
- **🔧 Independent adversarial-review fixes (2026-06-17, post-build, gate re-green).** A fresh-context review workflow (5 dimensions × per-finding refute-verify) **refuted 11 of 13 candidates** (the detector discipline, the two-classes-never-mixed, no-forecast/anomaly-firing, the no-network/no-dep/no-panic invariants, ASCII purity + no-color-alone, and the exit-code wiring all confirmed clean) and **confirmed 2**, both fixed: **(1) the contract's required single-source threshold was unwired** — I added the core consts `ALERT_WARN/CRITICAL_FRACTION` and *documented* that the apps/cli render `WARN_FRACTION`/`CRITICAL_FRACTION` alias them, but render.rs:33-34 still held standalone `0.80`/`0.95` literals (a forked third set; the meter color and the alert threshold were two number sources that merely happened to match). **Fix:** render.rs now defines `const WARN_FRACTION: f64 = costroid_core::ALERT_WARN_FRACTION;` (+ CRITICAL), with a `render_thresholds_are_the_one_core_source_never_forked` test asserting the render consts == the core consts == `AlertThresholds::default()`. **(2) the `--check` exit-2 (config/collect error) path was untested** — exit 0/1 were covered but the distinct error code wasn't. **Fix:** a new `apps/cli/tests/alerts_cli.rs` integration test spawns the binary with an isolated fixture config dir and pins exit 0 (off, silent), exit 2 (malformed config), and the human off-state.
- **🔧 Independent coordinator review (2026-06-17, read-only, 5 dimensions × per-finding refute-verify — separate from the builder's pass): SHIP, 2 LOW fixes, 0 refuted-then-real.** It **independently re-derived the load-bearing ⛔ invariants clean**: the detector fires quota ONLY off `LimitAvailability::Available` (every other arm returns `None`; both `TokenFraction` and an allowance-known `Spend` pool handled; non-finite rejected); budget fires strictly over target; the two classes are never mixed; `active_alerts` consults neither `forecast_view` nor `anomalies_view`; the exit path `std::process::exit(run_alerts(..)?)` returns `Ok(2)` for config/collect errors (caught, NOT propagated — so no anyhow-`Err`→1 collision with a crossing); `enabled` defaults false and master-switches both banner + command; the banner pairs amber/red with a `!`/`!!`/spelled-out cue and `--plain` is pure ASCII; no new dep / no network. It surfaced **2 LOW the builder's pass missed**, both fixed: **(1) an inverted quota-threshold pair** (`quota_warn > quota_critical`, e.g. 0.9/0.5) passed per-field `sane_fraction` and was stored verbatim — since `alert_level` tests CRITICAL first, WARN became dead code and CRITICAL fired at the warn floor (bounded over-alerting, never a crash/silence, opt-in only). **Fix:** `Config::alert_thresholds` now rejects an inverted pair back to the canonical defaults (mirroring `sane_fraction`'s discipline) + a regression test `alerts_inverted_threshold_pair_falls_back_to_defaults` (incl. the single-low-`critical`-vs-default-`warn` case). **(2) README undercount** — README:41 named six analytical tabs but said "the first five … are built"; corrected to "all six". Read-only review guardrail held — zero tree mutations by the review agents (safety SHA `64772153`); the fixes were applied by the coordinator. Gate re-green (default cli 177 + core 123, `--features connect-test-support` cli 199; clippy [default/`connect`/`connect-test-support`] + fmt + `cargo deny` + offline clean).
- **Scope fence held:** the detector + the `[alerts]` config + the `alerts`/`--check` command + the inline banner ONLY; no forecast/anomaly firing; the two classes never mixed; no daemon, no desktop notifications (notify-rust deferred behind a future Cargo feature + config opt-in), no network/telemetry, no new dep; Decimal money. **Files:** `crates/costroid-core/src/lib.rs` (consts + `AlertThresholds`/`AlertLevel`/`Alert` + `active_alerts` + helpers + tests); `apps/cli/src/{config.rs (the `[alerts]` section + projection + tests), render.rs (`WARN`/`CRITICAL` alias core; the alert copy + `push_alert_banner` + `render_alerts`/`render_alerts_off` + `alert_check_line`/`alerts_check_exit_code` + `render_now_with_alerts(_document)` + snapshots + ASCII-purity), main.rs (the `Alerts` clap command + `run_alerts` + banner in `run_now`), tui.rs (alert config fields + banner atop the Now tab)}`; `scripts/offline_acceptance.sh` (the two new alerts checks). **NEXT after sign-off:** commit; **Step 5 COMPLETE** → the v0.5.0 chore-cut.

**✅ T16 DONE — Anomalies tab (2026-06-17).** The sixth Step 5 analytical tab — the LAST numbered slot (`8`), closing the digit range. Surfaces proactive, non-alarmist callouts vs the user's OWN recent history. Gate green after BOTH review passes (default `cargo test --workspace` **cli 158 + core 116**, 0 failed; `--features connect-test-support` **cli 180**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers green — **NO new dep**, so the connect-delta + strace/offline + `cargo deny` are all unchanged). Built fresh-context + a **builder-run** adversarial-review workflow that found + fixed **7 findings** (2 medium render-honesty bugs + 5 test/tuning gaps — first 🔧 bullet); a **separate independent coordinator review** (5 dimensions × per-finding refute-verify) then surfaced + fixed **3 more the builder's own pass missed** — all clustered on the subscription-only persona's honesty (second 🔧 bullet).
- **Engine = `anomalies_view(&EngineSnapshot) -> AnomaliesView` (pure-core, `crates/costroid-core/src/lib.rs`)**, mirroring `forecast_view` (config-neutral, Serialize-only, not `Eq`/`Deserialize`, computed-never-persisted). **TWO signals, both off `snapshot.focus_rows` (UTC days throughout)**: (1) **spend spike** — the latest active day's API-lane `billed_cost` vs the trailing series (high-side only, so a partial current day can spike UP but never read as an unusual drop); (2) **model-mix shift** — each model's share-of-tokens on the latest active day vs its own trailing median share (two-sided — catches a surge AND a collapse). Baseline = **median + MAD** over the trailing **14 UTC days** (`ANOMALY_BASELINE_DAYS`); flag when `deviation > max(3.5·MAD, floor)` — the `max(.., floor)` is the **MAD=0 guard** (a near-flat history's MAD is 0, so a bare `3.5·MAD` threshold would be 0 and flag every nonzero change / risk a /0; spend floor `$1`, share floor `0.15`, both tunable). Suppressed below **7 distinct active days** (`ANOMALY_MIN_HISTORY_DAYS`) → an honest "not enough history yet - N of 7 days" state. All median/MAD math is exact `Decimal` (f64 only for display-text percent), panic-free on empty/odd/even/MAD=0. The **quota burn-rate signal is DEFERRED** — the Claude/Codex `rate_limits` caches persist a single point-in-time reading, so local data has no multi-day quota series to difference; `anomalies_view` consults NO quota/`LimitWindow` reading and the deferral is disclosed in the render footnote, never faked (§12.22 🔧 2-signal revision).
- **Shared token bucketer = `reconcile::api_lane_daily_token_series(&[FocusRecord]) -> BTreeMap<NaiveDate, BTreeMap<String, Decimal>>` (`pub(crate)`).** The token-side analogue of T15's `api_lane_daily_usd_series`; the lane filter was extracted into a private `api_lane_rows` iterator that BOTH the $ series and this token series build on, so the API-lane classification lives in ONE place (reconcile's behavior + tests unchanged).
- **Render = `render_anomalies_document` (braille/ascii/plain split, `apps/cli/src/render.rs`)** mirroring `render_forecast_*`. Each callout carries a NON-COLOR marker (`◆` in braille, pure-ASCII `*` in ascii/plain) that survives `--plain`, never color. Voice mirrors `insight_line` (proactive, `~`-hedged, `(estimated)`-tagged). The "~N.Nx your norm" multiple is shown ONLY when it reads honestly — suppressed (→ "well above"/"up from"/"down from" phrasing) when the displayed baseline rounds to `$0.00`/`0%` (would be self-contradictory) or the multiple rounds to `1.0x` (a flagged-but-tiny move). **Monochrome** (`anomalies_document_is_monochrome` across 5 fixtures — advisory tab, amber/red reserved; only Strong/bold header). T11's "`push_rule` skipped in Plain" gotcha honored. 6 snapshots (braille/ascii/plain flagged + clean-plain + insufficient-history-plain + no-API-usage-plain — the last a coordinator-review regression pin) committed; `render_anomalies` in BOTH `*_mode_output_is_pure_ascii` gates across all states.
- **Tab machinery:** `Screen::Anomalies` + `TAB_SCREENS` slot 8 + the `'1'..='7'` → `'1'..='8'` digit-range extension (`apps/cli/src/tui.rs`) + a `document_for_width` arm + footer label/nav (`1-8/tab switch`) + a help line + the `TAB_SCREENS`/`tab_for_digit` doc-comment trueing. Reachable by `8` + Tab/BackTab cycle; an engine→render integration test (`frame_anomalies_with_history_shows_real_callouts_end_to_end`) proves the $-unit spend spike + %-unit model-mix shift render correctly through the real TUI path.
- **🔧 Builder-run review fixes (7 confirmed findings, all addressed):** (medium) a model surge off a sub-half-percent median could render "~150x your **0%** median" — self-contradictory: the multiple is now suppressed when the displayed baseline rounds to zero, falling back to "up from your 0% median"; (medium) the `magnitude:None` (median==0) render branches were untested and a fixture comment falsely claimed coverage — added `anomalies_suppress_misleading_multiples` (covers the $0.00-median spend spike, the displayed-0% surge, and the ~1.0x case) and corrected the comment; (low) the flat `$1` spend floor is scale-independent so a ~2% bump on a tight-history high-spend user could read "~1.0x your $50.00 median" — the render now drops a `≤1.0x` multiple to the "well above" phrasing (detection stays the pinned absolute-floor algorithm — adding a relative term would contradict the explicit "absolute floor" pin; the floor value is logged as a tunable tradeoff); (low) added the model-mix MAD=0 share-floor test (both directions), the engine→render integration test, and help-line + footer (`1-8`) assertions.
- **🔧 Independent coordinator review (2026-06-17, read-only, 5 dimensions × per-finding refute-verify — separate from the builder's pass): SHIP, 3 fixes (all subscription-persona honesty), 0 refuted.** It **independently confirmed** the load-bearing invariants clean — the median/MAD math is exact-`Decimal` + panic-free (even/odd/empty/MAD=0; the `max(k·MAD, floor)` threshold is a COMPARISON, never a `/MAD` div-by-zero), high-side spend / two-sided mix, `anomalies_view` consults NO quota/`LimitWindow` reading (the deferral is disclosed, never faked), monochrome + `--plain`-pure marker, the `api_lane_rows` refactor is behavior-preserving + DRY, the +158 tui.rs is slot-8 wiring + 3 new tests (no scope creep), no new dep. It surfaced **3 findings the builder's pass missed**, all fixed: **(1) LOW — a subscription-only (no-API-lane) user saw the TRANSIENT "not enough history yet - 0 of 7 days" for a PERMANENT condition:** both signals + `history_days` are API-lane-only, so a Claude Code Max user (no API key) has `history_days==0` and never gets a callout, yet was shown thin-history/progress framing. **Fix:** `AnomaliesView` gains `no_api_usage` (= `spend_series.is_empty()`, mirroring `ForecastView`/`ModelsView`); `push_anomalies_body` branches on it FIRST with an honest permanent line ("no API-billed usage - anomaly callouts cover API-lane spend + model mix only") — new `snapshot_anomalies_no_api_usage_plain` + a regression test asserting it is NOT the thin-history copy. **(2) NIT — the model-mix signal is API-lane-only (a *lane-agnostic* token ratio), silently blinding subscription-lane mix shifts, and the on-screen scope line lacked the "API-lane" qualifier Budget/Forecast carry.** **Fix (disclosure):** `ANOMALIES_SCOPE_LINE` now reads "scope: **API-lane** spend + model mix …" and the `anomalies_view`/`AnomaliesView` doc-comments state both signals are API-lane-only by design. (Whether to make model-mix ALL-lane to serve subscription users is a deferred product decision — see the standing follow-up note; the current scope is consistent with the other API-cost analytical tabs.) **(3) NIT — the model-mix `baseline_displays_zero` guard used Decimal banker's-rounding while the displayed `percent()` uses f64 half-away**, diverging at exactly 0.5% (safe direction only — it over-suppressed, never emitting a contradictory "~Nx your 0%"). **Fix:** the guard now keys off the EXACT displayed `median_share` string (`== "0%"`), so it can never diverge from what's shown. Read-only review guardrail held — zero tree mutations by the review agents (safety SHA `9bc0fe43`); the fixes were applied by the coordinator. Gate re-green (default cli 158 + core 116, `--features connect-test-support` cli 180; clippy [default/`connect`/`connect-test-support`] + fmt + `cargo deny` + offline clean).
- **⏭ Standing product follow-up (T16, surfaced by the coordinator review) → DEFERRED as fast-follow T16b (Eren-confirmed 2026-06-17).** The model-mix-shift signal is API-lane-only, so a subscription-only user's genuine model-mix shifts are out of scope (token share is *lane-agnostic*, so an all-lane variant is technically feasible). Kept API-lane for v0.5.0 — consistent with the other API-cost analytical tabs (Models/Frontier) and now honestly disclosed. **Decision:** widening to all-lane is a real enhancement but needs `history_days`/suppression to also go all-lane — its own small design pass, so it is **deferred to the tracked fast-follow T16b** (§11.4), not bolted onto T16. Revisit after Step 5 closes.

**✅ T15 DONE — Forecast tab (2026-06-17).** The fifth Step 5 analytical tab — and the FIRST to extend the numbered-tab digit range (slot `7`, past the original `1`-`6`). Projects this month's API-lane $ spend + per-quota-window exhaustion ETAs, every figure a labeled estimate. Gate green after the coordinator-review fixes (default `cargo test --workspace` **cli 144 + core 106**, 0 failed; `--features connect-test-support` **cli 166**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers green — **NO new dep**, so the connect-delta + strace/offline + `cargo deny` are all unchanged). Built fresh-context + a **builder-run** 4-dimension adversarial-review workflow that the builder reported as 0 defects; a **separate independent coordinator review** (5 dimensions × per-finding refute-verify) then surfaced + fixed **2 verified honesty issues the builder's own pass missed** (a fabricated `~$0.00` no-usage visual header + a UTC-weekday label that could name the wrong local day) plus a card-vs-as-built doc nit — see the 🔧 bullet below.
- **Shared per-UTC-day series helper = `reconcile::api_lane_daily_usd_series(&[FocusRecord]) -> BTreeMap<NaiveDate, Decimal>` (`pub(crate)`, `crates/costroid-core/src/reconcile.rs`).** The generalization of `LocalCostEstimate::from_focus_records`'s lane+date bucketing — extracted the shared API-lane + UTC-day row classification into a private `api_lane_day_rows` iterator that BOTH the per-`(day,model)` estimate (reconcile) and this per-day series build on, so the lane filter + UTC-day key live in ONE place (no duplication; `from_focus_records` rewritten to reuse it — reconcile's behavior + tests unchanged). Exact `Decimal`, ascending by day. **T16 Anomalies reuses this verbatim** (its prereq now holds).
- **Core seam = `forecast_view(&EngineSnapshot) -> ForecastView` (pure, config-neutral, pure-local; `crates/costroid-core/src/lib.rs`).** `ForecastView { no_api_usage, spend: SpendForecast, daily_actuals: Vec<ForecastDay>, quota_etas: Vec<QuotaEta> }`, `Serialize`-only / not `Eq` (carries `f64`/`Decimal`), a computed view never persisted (mirrors `BudgetView`/`ModelsView`).
  - **$ projection = a linear run-rate, ALL on the UTC calendar (the consistency-trap resolution).** Because the shared series buckets by **UTC day**, the spend-to-date numerator AND the days-elapsed/days-in-month denominator are taken from the **UTC** calendar — `today = generated_at.date_naive()`, `days_elapsed = today.day()`, `days_in_month = days_in_month_utc(year, month)` (= first-of-next-month − first-of-this, clamped 28-31, no panic). **It deliberately does NOT reuse `period_range_for(Period::Month)`/`period_elapsed_fraction` (which are LOCAL-month)** — mixing a UTC-day sum with a local-month fraction is exactly the §12.21 trap. `SpendForecast::Projected { projected_month_usd = spend_to_date × days_in_month ÷ days_elapsed (exact `Decimal`, `checked_mul`/`checked_div`, never f64, never ÷0), spend_to_date_usd, days_elapsed, days_in_month }` when `days_elapsed >= MIN_FORECAST_DAYS` (= **3**); else `SpendForecast::InsufficientData { … min_days }` (the suppressed-below-floor state). A future-dated row (clock skew past today) is excluded from spend-to-date; `no_api_usage` (zero API-lane rows lifetime) drives the empty state.
  - **Quota ETA = a linear burn, projected ONLY off a fresh cross-checked reading.** It rides `now_summary(snapshot, NowOptions::default()).limits` (so the sanitize/cross-check/stale-age-out ladder already ran — never raw `LimitWindow`), and `quota_eta`/`project_quota_eta` project an exhaustion instant **only** off `LimitAvailability::Available { measure: TokenFraction(f), resets_at, reset_in_seconds }`: `elapsed = window_duration(kind) − reset_in`; rate `= f/elapsed`; `secs_to_full = (1−f)·elapsed/f`; `secs_to_full < reset_in` → `ProjectedHit { at }` else `ResetsFirst { resets_at }`. **Every other arm — Unverified/Estimated/Partial/Unavailable, or a dollar `Spend` measure — degrades to `Unavailable { ReadingNotProjectable }`** (ARCHITECTURE §9.2 — never a confident wrong ETA; "stale → unavailable" falls out for free since stale is aged-out to `Estimated` upstream). Edge cases handled without panic: `f<=0` → `ResetsFirst`, `f>=1` → hit now, `elapsed<=0` → `Unavailable { WindowJustStarted }`; all compared in seconds-space so no wild `DateTime` is ever built.
- **Render = `render_forecast_document` (braille/ascii/plain split, `apps/cli/src/render.rs`)** mirroring `render_budget_*`. Plain-surviving text: `projected ~$X by <Mon DD> (estimated)` + `spend so far ~$Y over N of M days (estimated)` (or the honest `insufficient data to project - N of M days elapsed (need 3+)`), then a `quota:` section with per-window `<provider> <window>: projected to hit ~<Weekday> (estimated)` / `resets before you hit it (estimated)` / `ETA unavailable (<reason>)` lines. The hit weekday is `at.format("%A")` in **UTC** and **labeled `(UTC, estimated)`** (deterministic — the freshness-stamp convention; ASCII English; the explicit UTC marker keeps a near-midnight projection from silently naming the wrong local weekday for an off-UTC user — a coordinator-review fix). **Visual modes add the actual-vs-projected sparkline** — `actual` (one cell per elapsed day, zero-filled, via the existing `sparkline`) then `projected` (a flat run-rate continuation, a distinct **glyph**: a middle-dashed braille cell, or `~` in Ascii) — distinguished by GLYPH + label, **NEVER color**. **Monochrome** (`forecast_document_is_monochrome` across all 3 fixtures — advisory tab, amber/red reserved; only Strong/bold header money). T11's "`push_rule` skipped in Plain" gotcha honored. 6 snapshots (braille/ascii/plain projected + insufficient-data-plain + ETA-unavailable-plain + a **no-usage-ascii visual** — the coordinator-review regression pin) committed; `render_forecast` added to BOTH `*_mode_output_is_pure_ascii` gates (all 3 states).
- **Tab wiring = the T11 template, but slot `7` extends the digit range (unlike T12-T14, which fit inside `1`-`6`).** `Screen::Forecast` + a `TAB_SCREENS` slot-`7` entry + the `handle_key` digit match `KeyCode::Char(ch @ '1'..='6')` → `'1'..='7'` + the footer nav `"1-6…"` → `"1-7…"` + a `draw_help` line (`7  forecast …`, popup height 17→18) + the `TAB_SCREENS`/`tab_for_digit` doc comments trued (`8` is the new inert boundary). Reachable by digit `7` + the Tab/BackTab cycle; the nav test extended to wrap through Forecast. NO scroll-machinery change (reuses T13's).
- **For T16:** the shared `reconcile::api_lane_daily_usd_series` per-UTC-day series exists (`pub(crate)`, T16's prereq); `TAB_SCREENS` now holds 7 tabs, the digit range is `'1'..='7'` — **T16 extends it to `'1'..='8'` for slot `8`**. **For T17:** the forecast signals (`SpendForecast::Projected.projected_month_usd`, `QuotaEtaOutcome::ProjectedHit`) feed the alert thresholds.
- **🔧 Independent coordinator review (2026-06-17, read-only, 5 dimensions × per-finding refute-verify — separate from the builder's own pass): SHIP, 2 fixes + 1 doc nit.** It **independently confirmed** the load-bearing invariants clean — the quota ETA projects ONLY off a fresh cross-checked `Available { TokenFraction }` (every other arm, incl. a `Spend` measure, degrades to `Unavailable`), the burn math is panic-free + bounded, the $ run-rate keeps numerator + denominator on ONE (UTC) calendar with `checked_mul`/`checked_div` + the 3-day floor, the `reconcile` refactor is behavior-preserving + DRY (the shared `api_lane_day_rows`), no new dep / no network, monochrome, no library `unwrap`/`expect`/`panic!`. It surfaced **2 verified honesty issues the builder's own review missed**, both fixed: **(1) MEDIUM — a fabricated `~$0.00` no-usage header on the DEFAULT (visual) surface:** core emits `SpendForecast::Projected { projected_month_usd: ZERO }` whenever `days_elapsed >= 3` even with zero usage, and `render_forecast_visual_document` rendered that `~$0.00` directly above "no API usage recorded - nothing to forecast yet" (the plain renderer already omitted it). **Fix:** `forecast_header_money` now returns `Option<Decimal>` (`None` when `no_api_usage`), so the visual header carries no figure — pinned by a NEW `snapshot_forecast_no_usage_ascii` + a `!visual.contains("$0.00")` regression assert. **(2) LOW — the projected-hit weekday was a bare UTC `%A`:** a Friday-23:30-UTC instant printed "Friday" for a UTC+13 user already on Saturday (the repo's own `format_bucket_start` uses Local for absolute date labels for exactly this reason). **Fix:** the line is now `projected to hit ~<Weekday> (UTC, estimated)` — the explicit marker removes the off-by-one-local-day ambiguity while keeping snapshot determinism. **(3) NIT (doc) — my own §12.21 card "Files:" line** still told the builder to reuse `period_range_for`/`period_elapsed_fraction`, which the consistency-trap the same card pins actually forbids (they are LOCAL-month) and the as-built correctly avoided; the card line is now corrected to `today.day()` + `days_in_month_utc`. The adversarial pass also **refuted** a raw-`Decimal +=`-overflow finding in the new helper (correctly — it matches the established core-wide convention: `bench.rs:304`, `lib.rs:272/405/1611`; not a T15 regression; ~7.9e28 unreachable). Read-only review guardrail held — zero tree mutations by the review agents (safety SHA `6e58071`); the fixes were applied by the coordinator. Gate re-green (default cli 144 + core 106, `--features connect-test-support` cli 166; clippy [default/`connect`/`connect-test-support`] + fmt + `cargo deny` + offline clean).

**✅ T14 DONE — Budget tab + the FIRST user-config file (2026-06-17).** The fourth Step 5 analytical tab, and the first to introduce a hand-edited user config. Gate green (default `cargo test --workspace` **cli 133 + core 98**, 0 failed; `--features connect-test-support` **cli 155**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers — connect-delta unchanged; `cargo deny check licenses bans` ok with `toml`). Built fresh-context + an independent adversarial-review workflow (5 dimensions × per-finding verify) that surfaced + fixed 2 honesty bugs before close-out (see the 🔧 bullet below).
- **Config layer = `apps/cli/src/config.rs` (read-only, non-secret TOML).** Path `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml` (resolver mirrors `costroid-connect`'s `default_registry_path`, rooted at CONFIG not STATE). `load_from(path)` is the testable seam; `load()` resolves the default path. **Absent file ⇒ the zero-config default (no budgets), NOT an error**; a present-but-malformed file ⇒ a typed `ConfigError` (`Read`/`Parse`) whose `Display` is a single status-bar line — surfaced as the TUI status (`config: …`), **never a crash**. Forward-compat: `#[serde(default)]` everywhere + serde's default unknown-key tolerance (NO `deny_unknown_fields`), so an older build reads a newer file. **Schema EXACTLY as pinned (Q2):** `[budget] total_monthly_usd` (optional) + `[budget.per_tool]` keyed by the `x_Tool` ids. **Money is `Decimal`, never f64** — a custom `Money` newtype's `deserialize_any` accepts a TOML integer / float / quoted string (integers + quoted strings exact; a bare float transits f64 via `Decimal::from_f64_retain`, documented as "quote for exactness"). **READ-ONLY: NO writer/saver, NO `budget set` command** (a CLI-surface ⛔ + unpinned UX — deliberately not built).
- **Core seam = `budget_view(&EngineSnapshot, &BudgetTargets) -> BudgetView` (pure, config-neutral; `crates/costroid-core`).** Core never reads a file — `BudgetTargets { total_monthly_usd, per_tool }` is the INPUT (the apps/cli config layer maps its TOML into it). `BudgetView { rows, excluded_tools, no_budget_set, spent_total_usd, month_elapsed_fraction }` over `BudgetRow { scope, target_usd, spent_usd, fraction, over_by_usd, pace }`. **API-lane ONLY (§170, lanes-never-summed):** spend is the **current calendar month's** API-lane `billed_cost` (via the existing `period_range_for(Period::Month, …)` — same month definition now/trends use), per `x_Tool` + an optional total; subscription rows never contribute a dollar. **Not-API-billed guard:** a budgeted tool that has *lifetime* local usage but NO API lane is surfaced in `excluded_tools: Vec<BudgetExcludedTool { tool, reason }>` — never a fabricated `$0/target` row. The `reason` distinguishes `FlatFeeSubscription` (subscription-lane rows) from `NotApiBilled` (only `UnknownAccess` rows — e.g. a Codex/Claude install with no rate-limit/credential signal, which the providers tag `AccessPath::Unknown`), so the tab never *asserts* "subscription" it can't back up. A tool with NO local usage at all stays a legitimate `$0/target` row (planning ahead). Non-positive caps are skipped (no divide-by-zero). Rows sort most-utilized first. `pace` = a lightweight `OnTrack`/`AheadOfPace`/`OverBudget` comparison of used-share vs month-elapsed-share — **NOT** the full month-end projection (that is the Forecast tab, T15; T14 builds NO shared per-day series helper). `Serialize`-only / not `Eq` (carries `f64`), a computed view never persisted (mirrors `ModelsView`).
- **Render = `render_budget_document` (braille/ascii/plain split, `apps/cli/src/render.rs`).** A fill meter, the spent/target money (always `~`-hedged), the percent + cue, the pace line, and the over-by amount. **Budget is the ONE tab where amber/red is allowed — but NEVER color-alone:** every Warn/Critical span is paired with a spelled-out `!`/`!!`/`OVER` cue (the `meter_segments` clamp at 1.0 means an over-bar reads full, so the textual `OVER` carries the over-state). The row's display state comes from a `budget_state(row)` helper that keys "over" on the core's STRICT `over_by_usd.is_some()` (NOT `limit_state`'s `>= 1.0`), so the bar color, the cue, the over-by line, and the pace all agree at the boundary (extracted a shared `state_style` from `limit_meter_span`). `--plain` = `<scope>: ~$X / ~$Y budget (NN%) … !! OVER, over by ~$Z`. The empty state spells out "no budget set - set targets in ~/.config/costroid/config.toml" + a copy-paste schema. T11's "`push_rule` skipped in Plain" gotcha honored. braille/ascii/plain + no-budget snapshots committed; `render_budget` added to BOTH `*_mode_output_is_pure_ascii` gates; a `budget_over_state_pairs_color_with_a_textual_cue` test enforces no-color-alone (so NO `budget_document_is_monochrome` — Budget is the deliberate amber/red exception).
- **Tab wiring = the exact T11 template, APPENDed (no machinery change).** `Screen::Budget` + a `TAB_SCREENS` **slot-`6`** entry (the LAST digit-reachable slot — `handle_key` already matched `'1'..='6'`, so NO `handle_key` change; `7` is now the inert boundary) + a `document_for_width` arm + footer/help label + the `App::budget_targets` field (loaded once in `run_with_dependencies`, read-only, no network; absent/malformed ⇒ default + status). Reachable by digit `6` + the Tab/BackTab cycle.
- **Dep: the permissive `toml` (`=1.1.2`, MIT/Apache-2.0, parse-only, deny-allowlist-clean).** Added to `apps/cli` as a NON-optional default dep, so it lands in BOTH the default and `--features connect` graphs → the offline connect-delta subset is **unchanged** (no `CONNECT_ALLOWED` edit). The default/local-only build still makes **zero** network calls (`toml` is parse-only; strace/offline gates unaffected). `serde` (derive) also added to `apps/cli` directly (was only transitive via `serde_json`).
- **🔧 Adversarial-review fixes (2026-06-17, post-build, gate re-green: default cli 133 + core 98, connect-test-support cli 155; fmt + clippy [default/`connect`/`connect-test-support`] clean).** A fresh-context review workflow (5 dimensions × per-finding verify, separate from the builder's pass) confirmed the load-bearing invariants clean (scope fence, no-network, library no-panic, config robustness/forward-compat, tab nav, ASCII purity) and surfaced **2 verified honesty bugs**, both fixed: **(1) the exactly-100% boundary** — `limit_state` treats `fraction >= 1.0` as `Over`, but core sets `over_by_usd`/`OverBudget` pace only on the STRICT `spent > target`, so a row at exactly-at-budget rendered a self-contradiction ("`!! OVER, over by ~$0.00`" beside an on-track pace). **Fix:** the render derives its state from a `budget_state(row)` that keys "over" on `over_by_usd.is_some()` (not `limit_state`), so exactly-100% reads "`!! at budget`" (Critical), and the bar/cue/over-by/pace agree (regression test `budget_exactly_at_budget_reads_at_budget_not_over`). **(2) the flat-fee guard missed `UnknownAccess` rows** — it required a `subscription` tag, but Codex tags ALL rows `AccessPath::Unknown` when `codex_has_rate_limits()` is false and Claude does so with no API key + no credentials, so a genuinely flat-fee user got a misleading `~$0.00 / target` row. **Fix:** the guard now excludes any budgeted tool with local usage but NO API lane, and `excluded_tools` carries a `reason` (`FlatFeeSubscription` vs the honest `NotApiBilled`) so the unclassified case never asserts "subscription" (regression tests `budget_view_unknown_access_only_tool_is_excluded_as_not_api_billed` + the legitimate-`$0`-row counter-test). The lighter findings (a stale diff comment, the empty-state hint hardcoding the canonical config path vs an `$XDG_CONFIG_HOME` override, user-supplied tool-id ASCII passthrough — consistent with the existing model/project-name carve-out) were left as documented nits.
- **🔧 Independent coordinator review (2026-06-17, read-only, 6 dimensions × verify — separate from the builder's pass): SHIP-WITH-NITS.** It **independently confirmed** the builder's 2 fixes are real + non-vacuously tested, and that the config-loader can't panic on hostile input (absent⇒default, malformed/odd-types⇒typed `ConfigError`, NaN/inf guarded), the config is non-secret + CONFIG-dir + read-only (no writer/`budget set`), the §170 API-lane guard holds, and the default build stays zero-network (`toml` parse-only). It surfaced **1 additional LOW the builder's pass missed — the sub-cent overshoot:** `over_by_usd` is exact (strict `spent > target`) but `format_money` rounds to 2dp, so a real ~$50.0003-over-$50 row rendered the self-contradictory "`!! OVER, over by ~$0.00`". **Fix (applied):** a `format_over_by` helper renders `<$0.01` when the overshoot rounds below a cent (both the meter + plain sites), with a regression test (`budget_over_by_below_a_cent_renders_less_than_a_cent_not_zero`); gate re-green (361 workspace / 158 connect-test-support). Two nits left as documented: a `0`/negative-only target shows the terser "no usable targets" copy (honest, untested branch); the empty-state hint hardcodes `~/.config` under an `$XDG_CONFIG_HOME` override (knowingly-accepted; render is config-neutral). Read-only review guardrail held — zero tree mutations.
- **For T15/T16:** the config layer (`apps/cli/src/config.rs`) + the `budget_view` composite-view shape exist; `TAB_SCREENS` now holds 6 tabs, the digit range is full at `'1'..='6'` — **T15/T16 extend `handle_key`'s digit match to reach slots `7`/`8`**. **For T17:** EXTEND `Config`/the `[budget]` section (or a new `[alerts]` section) for alert prefs — the forward-compat serde already ignores unknown sections, so an older build tolerates a newer alerts config.

**✅ T13 DONE — History tab + the TUI's first scroll/viewport state (2026-06-17).** The third Step 5 analytical tab — a scrollable, newest-first per-turn FOCUS record ("the full record") — and the first to add real scroll state, which T14–T16 reuse. Gate green (default `cargo test --workspace` **cli 114 + core 88**, 0 failed; `--features connect-test-support` **cli 136**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers — connect-delta unchanged, no new crate).
- **Tab wiring = the exact T11 template, APPENDed (no machinery change).** `Screen::History` + a `TAB_SCREENS` **slot-`5`** entry (Models took `4`) + a `document_for_width` arm + footer/help label; reachable by digit `5` + the Tab/BackTab cycle; `6` is the new reserved-inert digit. No `handle_key` change for tab nav — `tab_for_digit`/`cycle_tab` pick it up automatically.
- **Render = `render_history_document` (braille/ascii/plain split, `apps/cli/src/render.rs`)** over `snapshot.focus_rows` directly (NO new core seam — the same pipeline now/trends/export use, no re-parse). Each row: `YYYY-MM-DD HH:MM` (UTC) · model (Strong/bold) · raw token usage read from **`x_ConsumedTokens`** (the count that survives FOCUS nulling `ConsumedQuantity` on unpriced rows — NEVER `ConsumedQuantity` alone) + token type · access path · **API-only** estimated (`~`) cost. Subscription rows show the path but **no per-token dollar** (golden rule: subscription is not a summable bill); the header total is the API-lane estimate. A count/scope header (`scope: N records, newest first (all time)`) mirrors T12's on-screen scope label. Monochrome (`history_document_is_monochrome`; Strong model only, amber/red reserved). T11's "`push_rule` skipped in Plain mode" gotcha honored — plain delimits by labels.
- **NEW: the TUI's first scroll/viewport state (the load-bearing T13 piece).** `App` gained `scroll: u16` (top display row) + `viewport_rows: u16` (last draw's content height, for real-page `PgUp`/`PgDn`). Keys: `Up`/`Down` (1 line), `PgUp`/`PgDn` (a page), `Home`/`End` (ends) — **global** to every tab (clamp makes them inert on short tabs), inert during filter-editing, no conflict with existing keys. **`draw_app` now takes `&mut App`** (the idiomatic Ratatui pattern) and is the single clamp point: it computes the wrapped-row count (`document_display_rows` — `ceil(visible_width / area_width)` per logical line, exact for non-wrapping rows and never an over-count, so the offset can't run past the content into a blank viewport), writes `viewport_rows`, and clamps `app.scroll = min(scroll, max_scroll)`. `End` rides `u16::MAX` down to the clamped bottom; the clamp lives in the draw because only it knows the terminal size. Scroll resets to 0 on a tab switch (`App::set_screen`); each tab's viewport is independent (no per-tab scroll memory). **`Paragraph::line_count` was NOT used** — it is gated behind ratatui's unstable `rendered-line-info` feature, so the count is computed locally instead.
- **EXPORT-UX decision (the card's 📌 STOP-and-ask): took the PINNED default — NO in-TUI file write.** The tab surfaces the unchanged `costroid export --format json|csv` (in a tab line + a help line) for the complete FOCUS 1.3 record; the tab is a readable view, not a replacement. The standalone `costroid export` command + emitters (`export_focus_json`/`export_focus_csv`) are untouched (verified: still emits the `focusVersion: 1.3` envelope + the zero-row CSV header). An in-TUI export-to-file (unpinned target-path + confirmation UX) was deliberately NOT built — deferred per the card.
- **For T14+:** the scroll machinery (`App::scroll`/`viewport_rows` + the keys + the `draw_app` `&mut`/clamp + `document_display_rows`) is in place for Budget/Forecast/Anomalies; `TAB_SCREENS` now holds 5 tabs, slot `6` reserved.
- **Independent read-only review (2026-06-17): SHIP-WITH-NITS, 0 blockers.** Verified clean: the ⛔ `costroid export` surface is byte-for-byte untouched (`main.rs` + core not in the diff); the scroll state is panic-free and cannot blank the viewport on empty/short/long/wrapped content (saturating arithmetic, width-independent safety; both-ends clamp tests); render honesty (`x_ConsumedTokens`, `~`-hedge, API-only, newest-first); ASCII + monochrome; no tab regression. (Review agents were strictly read-only — the standing guardrail after the T12 review-clobber.)
- **🔎 Known limitation (accepted, documented):** `document_display_rows` counts `ceil(chars/width)` per logical line while the `Paragraph` renders with `Wrap{trim:false}` (word-wrap), so the row count is a true **lower bound** — at narrow widths the tail of a *wrapped* line can sit permanently below the fold (`End`/`Down` clamp a row or two short). It is **safe** (never over-counts → never a blank viewport, never a panic) and **latent today** (the widest line is the fixed 66-char export hint; fixture rows are ~57–63 chars; the scroll test runs at width 90 where nothing wraps). Revisit only if History ever renders lines that wrap at common terminal widths (then count word-wrapped rows, or truncate-to-width instead of wrapping). The two cosmetic nits (scroll keys global-not-History-only — intentional + self-documented; the help "export:" line reading like a keybinding) were left as-is.

**✅ T12 DONE — Models tab (2026-06-16).** The second Step 5 analytical tab, and the first to APPEND to T11's tab model (no machinery change). A new `Screen::Models` (number `4` + the Tab/BackTab cycle) renders, per API-billed model, the spend + token mix fused with the bench/frontier overlay (cost-vs-quality standing + equal-volume re-pricing). Gate green (default `cargo test --workspace` **cli 104 + core 88**, 0 failed; `--features connect-test-support` **cli 126**; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers — connect-delta unchanged, no new crate).
- **Core seam = `models_view` → `ModelsView { models: Vec<ModelRow>, no_api_usage, disclaimer, providers }` (pure-core, `crates/costroid-core/src/lib.rs`).** A pure projection over the existing snapshot — it reuses `bench_view` (the frontier overlay) and the **same resolved-key grouping `bench_view` performs** (`PricingCatalog::resolve_key` + `AggregateTotals::add_row` over the API-lane rows), adding **NO new pricing/bench math**. Each `ModelRow { model, totals: AggregateTotals, overlay: Option<OverlayModel> }` is keyed by the **resolved catalog key** and joins 1:1 to its `OverlayModel` by `model_id`, so a model's dated snapshots merge to one row and Models/Frontier always agree (this resolved-key join is the T12 review fix — see the 🔧 bullet below; spend is **lifetime-scoped**, reconciling with `trends`/`frontier`, NOT the period-scoped `now`). `Serialize`-only / not `Eq` (carries `OverlayModel`), mirroring `BenchView` — a computed view, never persisted; infallible bar `bench_view`'s bundled-data `CoreError`.
- **Honesty: API-cost rows ONLY** (the frontier is API-only, ARCHITECTURE §9.6) — a subscription-only model never appears (filtered to `CostLane::Api`); spend is **ALWAYS** `~`-hedged (`format_money(.., true)` — local cost is always an estimate); partial/unpriced coverage is surfaced via the existing `pricing_badge_plain` ("(partial pricing)"), never hidden; an un-benchmarked model renders **"not benchmarked"** — a GAP, never a guessed standing. Re-pricing reuses the frontier's `best_delta`/`delta_phrase` (cost-only, INFORM never PRESCRIBE). Monochrome (`models_document_is_monochrome` guards it; Strong/bold spend only, amber/red reserved).
- **DECISION — the per-appearance standing uses `name score% - standing` (hyphen), not `(…)`,** to avoid the double-paren `(off (dominated by X))`; the gap join degrades to "not benchmarked" rather than risking a fabricated standing when a second raw label of a merged catalog key doesn't match the overlay's first raw label (an honest understatement). T11's "`push_rule` skipped in Plain mode" gotcha honored — the plain render delimits by labels, never the `─` glyph.
- **Render = `render_models_document` (braille/ascii/plain split, `apps/cli/src/render.rs`)** mirroring `render_providers_*`; braille/ascii/plain + no-API-usage snapshots committed; `render_models` added to both `*_mode_output_is_pure_ascii` gates. TUI wiring is the exact T11 template: `Screen::Models` variant + the `TAB_SCREENS` slot-`4` entry + a `document_for_width` arm + footer/help label, no `handle_key` change. The existing nav test was extended (digit `4` now reaches Models; cycle wraps through it; `5` is the new reserved-inert digit).
- **For T13/T14+:** `TAB_SCREENS` now holds 4 tabs; slots `5`–`6` remain reserved. The `models_view` composite-view shape (join a per-group summary to an overlay, render via a `render_<tab>_document` split) is the template History/Budget follow, and serves `apps/bar` later.
- **🔧 Review fix (2026-06-16, post-build, gate re-green: default cli 104 + core 88, connect-test-support cli 126; fmt + clippy [default/`connect`/`connect-test-support`] clean).** An independent review found a **HIGH honesty bug**: `models_view` keyed per-model spend by the **raw `x_model`** (via `summarize_rows`) but joined to the bench overlay by the **resolved catalog key**. `bench_view` merges a model's dated fragments under one resolved key (keeping only the first raw label), so a second dated snapshot (e.g. `claude-opus-4-7-20251101` + `-20251201`) matched no overlay entry and rendered **"frontier: not benchmarked" for a benchmarked model — the §6 invariant inverted.** **Fix:** `models_view` now aggregates API-lane rows into a `BTreeMap<resolved_key, AggregateTotals>` using the SAME `PricingCatalog::resolve_key` + `AggregateTotals::add_row` `bench_view` uses, producing ONE row per resolved key that joins 1:1 to the overlay by `model_id` — so Models and Frontier agree row-for-row and a benchmarked model can never be a gap (spend + tokens summed across fragments; the repricing-volume skew is resolved by the same fix). No new pricing/bench math. **Regression test** (`models_view_merges_dated_fragments_so_a_benchmarked_model_is_never_a_gap`): two dated `claude-opus-4-7` fragments → one row, `overlay.is_some()` with non-empty appearances, spend = sum; plus a general invariant that NO benchmarked overlay model resolves to a gap row. **LOW:** the doc comment dropped the wrong "reconciles with `now`" claim (Models is **lifetime**-scoped like trends/bench; `now` is period-scoped), and both render docs gained an on-screen **`scope: all usage (all time)`** label (mirrors Now's `period:` line) so a user sees why Models can differ from Now — the 4 Models snapshots were regenerated for the added line (content otherwise unchanged) and reviewed. **NIT:** `tab_for_digit`'s doc comment now says `1`-`4` wired (was `1`-`3`); the duplicated 3-arm `FrontierStanding`→text mapping (frontier `point_line` + the Models standing) was extracted to a shared `frontier_standing_text` helper (the frontier's distinct plain yes/no phrasing intentionally stays separate).

**✅ T11 DONE — Providers tab + the numbered-tab model (2026-06-16).** The FIRST production consumer of the `Capability` descriptor: a new `Screen::Providers` TUI tab renders, per provider (Claude Code / Codex / Cursor), each lane's honest source + auth + quota shape + detection health — *what's available, what's unavailable, and why*. Gate green (default `cargo test --workspace` cli 97 + core 85; `--features connect-test-support` cli 117; clippy `-D warnings` on default + `connect` + `connect-test-support`; fmt; offline.rs both tiers; the connect-delta still the reviewed allowlist — no new crate). **Built by an independent fresh agent + an adversarial review workflow** (3 dimensions × verify), 0 confirmed defects.
- **Core seam = `ProviderCapabilityView` (owned), parallel to `ProviderStatus` on `EngineSnapshot`.** `Capability` is `Copy` but its `quota_kinds: &'static [LimitKind]` blocks `Deserialize` (per §11.5 ✅ T3's note), so collection projects each provider's `capability()` into an owned, `Serialize`/`Deserialize` view (`Vec<LimitKind>`), captured for EVERY provider **before** the `Box<dyn Provider>` set is consumed — present even for missing/errored providers, joined to `providers` by `ProviderId`. Infallible (no unwrap/expect/panic). `EngineSnapshot` gained the `capabilities` field (the 5 test literals updated).
- **Tab model (Q1): numbered `1`–`6` jumps + `Tab`/`BackTab` cycle**, replacing the 2-way toggle (`TAB_SCREENS` = [Now, Trends, Providers]; `cycle_tab`/`tab_for_digit`). Only `1`–`3` are wired today; `4`–`6` are reserved/inert until T12+ append to `TAB_SCREENS` (no further `handle_key` change). **Frontier stays its own `a`/`esc` overlay** (outside the cycle; Tab from Frontier returns to the first tab). Footer nav + `draw_help` enumerate the tabs.
- **Render = `render_providers_document` (braille/ascii/plain split)** with author-written DataSource/AuthMethod copy (`LocalArtifact`→"from local logs", `SanctionedHook`→"from the statusLine capture; run setup-statusline", `SanctionedOauth`/`ApiKey`→"via your connected key", `Unavailable`→"no sanctioned source"). Monochrome (Strong/bold titles only; amber/red reserved) — `providers_document_is_monochrome` guards it. Cursor renders `detected` + both unavailable lanes as "no sanctioned source", **never "coming soon"**. braille/ascii/plain snapshots committed; included in the `*_mode_output_is_pure_ascii` gates.
- **DECISION — the connect-gated connection lane is a SEPARATE `#[cfg(feature="connect")]` fn (`push_provider_connection_lane`), appended to the document by the TUI, not a parameter of `render_providers_document`.** Rationale: keeps `render_providers_document` mirroring the other `render_*_document` fns AND keeps the default build entirely free of connect types/symbols + free of dead-code (an always-compiled `ConnectionEntry`/`ConnectionState` would warn "variant never constructed" in the connect-OFF non-test build). The lane reads the EXISTING keychain/registry read-only via the dual gate (`is_connected && retrieve.is_some`), shows org label + connected/not only (**never** key material), reuses the pinned `GEMINI_UNAVAILABLE_MESSAGE` verbatim, makes **NO** network call, and degrades to empty if the keychain/registry is unreachable. The lane's `--plain`/Ascii output is folded to pure ASCII (em-dash → `-`, non-ASCII → `?`), mirroring `connect.rs::emit`. `push_rule` is skipped in Plain mode (plain docs delimit by labels, never the `─` glyph) — a reusable gotcha for T12/T13's plain renders.
- **For T12/T13:** the tab model + the `render_*_document`/snapshot/ASCII-purity pattern are in place — a new tab adds a `Screen` variant + `TAB_SCREENS` entry + a `document_for_width` arm + footer/help label + a `render_<tab>_document`. The Providers tab's Capability rendering is the template the deferred Copilot/Antigravity adapters (§8) render through by filling `Capability`.
- **Independent coordinator review + fix (2026-06-16).** A fresh-context adversarial review (6 dimensions × per-finding verify, separate from the builder's own pass) confirmed the load-bearing invariants clean — scope fence, library no-panic, Cursor/Gemini honesty, ASCII purity, no-color-alone, tab-nav reserved-slot safety, connect-gating, the core seam — and surfaced **one low finding: the connection-lane gate+label logic was uninjectable + untested** (`gather_connection_entries` hard-called `CredentialStore::new()`/`ConnectionRegistry::open()`, and `format_org_label` had no coverage). **Fixed:** extracted the testable inner `connection_entries(&store, &registry)` (the thin wrapper keeps the open-and-degrade behavior) + added 2 connect-tier tests (the dual gate incl. the registry-connected-but-no-key → `NotConnected` keychain-source-of-truth case; the verbatim Gemini message; `format_org_label` with/without id). Connect-test-support tier now **121** (was 117 + builder's render test + these 2). Gate re-run green on all tiers.

**📌 STEP 5 PINNED + CARDED (2026-06-16) — analytical tabs + alerts (T11–T17); cards at §12.17–§12.23.** Ran the §12.8 pin-then-card pass (a 6-reader + synthesis workflow mapping each tab/alert against the as-built code; canon = code). **Task split:** **T11 Providers tab** (M, Prereq T3 ✅ — the FIRST production consumer of the `Capability` descriptor; today only tests call `capability()` — and also lands the tab-model refactor the rest inherit), **T12 Models tab** (S), **T13 History tab** (M — adds TUI scroll state, which doesn't exist today), **T14 Budget** (L), **T15 Forecast** (L), **T16 Anomalies** (L–XL), **T17 Alerts** (L, ⛔). **Step 5 adds NO new network** — pure-local analytics over the existing point-in-time `EngineSnapshot`; Budget's optional invoice-true number reuses T10a's EXISTING user-initiated connect seam (`AdapterSet::cost_report`, `apps/cli/src/reconcile.rs:233`), feature-gated + off by default. **Key structural finding (code is canon):** the TUI has **no tab model** — `Screen` has 3 variants and `Tab` is a hardcoded 2-way toggle (`apps/cli/src/tui.rs:42,186-191`), `Frontier` an `a`/`esc` overlay; T11 builds the real tab bar.
- **The four product decisions (Eren-confirmed 2026-06-16):**
  - **(Q1) Tab navigation = numbered `1`–`6` direct jumps + a `Tab`/`BackTab` cycle** (Frontier stays its own `a`/`esc` overlay). Lands in T11; replaces the 2-way `match`.
  - **(Q2) First user-config file = TOML at `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`** (the documented target). Owned by `apps/cli`, **non-secret (NEVER keychain)**, atomic temp+rename + forward-compat `serde` (mirror the `connections.json` idiom but rooted at CONFIG not STATE). Adds the permissive `toml` crate (MIT/Apache, already deny-allowlist-clean) — the one dependency add, pre-approved here. Money is `rust_decimal::Decimal`, never f64. Budget compares the **API lane only**; a flat-fee subscription gets **no $ target** (§170, lanes-never-summed). Introduced by T14; extended by T17 for alert prefs.
  - **(Q3) Anomaly baseline history = re-parse local logs over the trailing window each run** (NO new persisted rolling store; pure-local, no telemetry). The daily-series helper is shared with T15.
  - **(Q4) Alerts delivery = an inline terminal banner (now/tabs) + a cron-friendly `costroid alerts --check` exit-code subcommand** — both built-in, no new dependency, no daemon. **OS desktop notifications (notify-rust) are DEFERRED behind a Cargo feature + config opt-in (off by default)** — they pull Linux D-Bus deps that re-touch the offline/forbidden-crates gates + the libdbus/precise-builds release wrinkle (T10b lesson above), so any future add takes a `CONNECT_ALLOWED`-style allowlist review. Alerts default **OFF/quiet**; thresholds reuse `WARN_FRACTION=0.80`/`CRITICAL_FRACTION=0.95` (`render.rs:33-34`), user-overridable in the TOML config.
    - **(Q4-sources) Alert sources = quota % crossings + budget $ crossings ONLY (Eren-confirmed 2026-06-17).** The closing Step-5 card stays a tight threshold-crossing surface: a quota window crossing WARN/CRITICAL (off a fresh cross-checked `Available` reading only — never Unverified/stale) and a budget crossing its monthly $ target (`BudgetView` over-state). **Forecast projected-hit + anomaly callouts are NOT alert sources in T17** — they remain advisory on their own tabs; alerting on them is a deferred fast-follow (so T17 wires neither `forecast_view` nor `anomalies_view` into the detector). The two classes (quota % vs budget $) are never mixed.
- **Pinned-technical defaults (decided in the pass; no human sign-off needed):**
  - **T15 Forecast:** $ projection = linear run-rate over the elapsed month (`spend_to_date/days_elapsed × days_in_month`) off a shared per-UTC-day API-lane series helper (generalize the `reconcile.rs` bucketing — no helper exists today); quota projection = linear burn from the current `LimitMeasure` fraction to `resets_at`. Both labeled estimates; the $ projection is suppressed below a **3-day** min-data floor; the quota ETA **degrades to unavailable** on an `Unverified`/`Estimated`/stale reading (ARCHITECTURE §9.2) — never a confident wrong ETA.
  - **T16 Anomalies:** baseline = **median + MAD** over the trailing **14 local days** of the user's own history, flag a day when `|value − median| > 3.5·MAD` (conservative → not alarmist; MAD beats mean±σ on spiky right-skewed spend). Suppress all anomalies below **7 days** of history. (`bench.rs` `BaselineUnpriced` is a $0-unpriced placeholder, NOT a statistical baseline — not reusable.)
    - **🔧 REVISED 2026-06-17 (Eren-confirmed) — ship 2 signals, defer the quota one.** The original pin named **three** signals; a coordinator data-reality check (during the T16 card refresh) found the third, **quota burn-rate jump (`LimitMeasure` delta/day), is NOT buildable from local data**: the Claude/Codex `rate_limits` caches persist a **single point-in-time** reading (one `captured_at` — `read_claude_rate_limits`/the Codex rollout path, `costroid-providers/src/lib.rs`), so there is no multi-day quota-fraction series to difference. T16 therefore ships **two** signals, both fully local-supported off `snapshot.focus_rows`: **(1) spend spike** — daily **API-lane** `billed_cost` via `reconcile::api_lane_daily_usd_series` (T15's `pub(crate)` helper); **(2) model-mix shift** — per-day share-of-tokens per model (`x_ConsumedTokens` bucketed by `(UTC day, x_Model)`). **The quota-% burn signal is DEFERRED** (returns if/when a persisted quota-reading history lands — not a new store for T16). No quota reading is consulted by T16, so the "skip Unverified/Estimated" caveat is moot here.
  - **Render obligation (all tabs):** each new `render_<tab>_document` has a braille/ascii/plain split + ASCII-purity-gate inclusion; advisory tabs (Models/Forecast/Anomalies) stay **monochrome** (amber/red reserved for the near-limit/over-budget state, which always carries a non-color cue).
- **Build order:** T11 (lands the tab model) → T12, T13 (cheap re-cuts) → T14 (config layer) → T15, T16 (analytics) → T17 (alerts, ⛔, extends T14's config). Each built in a fresh agent per §12.0 + the §11.1 review loop. **No separate proposal doc** — the pins live here + in the cards (§12.8 step 3); revisable by the build agent as it learns.

**✅ T10b DONE — v0.4.0 SHIPPED (2026-06-16). Step 4 (connections) is complete.** The release is live on all three distribution paths: **crates.io** (all 5 crates at 0.4.0, via the extended ladder `focus → providers → core → connect → cli`), the **GitHub Release** (`curl|sh` + PowerShell installers + all 6 target binaries + per-file SHA-256 + provenance attestations), and **Homebrew + npm**. `cargo install costroid` → 0.4.0. The prep was the standard lockstep bump (5 `version.workspace` members + the 4 internal `[workspace.dependencies]` constraints incl. `costroid-connect`) + `Cargo.lock` + CHANGELOG `[0.4.0]` + README/SECURITY 0.4.x + the RELEASING.md 5-crate ladder; cut by committing the prep, pushing main, tagging `v0.4.0`, then the manual crates.io ladder.
- **⚠️ Release-pipeline lesson (cost one failed Release run + a re-tag): `dist build` builds the WHOLE workspace, so on a connect-OFF release it still tried to compile `costroid-connect → keyring → libdbus-sys` and FAILED on the CI Linux runners (`Package dbus-1 was not found` — runners have no `libdbus-1-dev`).** A local `dist build` dry-run does NOT catch this (a dev box has libdbus installed). **Fix (committed, `3158c44`): `precise-builds = true` in `dist-workspace.toml`** → dist builds only `-p costroid` (connect-OFF), never compiling `costroid-connect`/`libdbus`, so the release runners need no system libs (verified: a clean rebuild compiled only `costroid`; the shipped binary stays `nm`-clean of the trio). This supersedes the old card note ("the connect-OFF release needs no libdbus") — true for the *binary*, but `dist build` needed `precise-builds` to actually avoid the workspace compile. The first (failed) v0.4.0 Release published no GitHub artifacts, so the fix was committed and the `v0.4.0` tag force-moved onto it (crates.io 0.4.0 was already live and is byte-identical — `dist-workspace.toml` is in no published crate; only the git tag ref moved). **Going forward this won't recur** (precise-builds is permanent); a future connect-ON release artifact would still need the apt deps (`libdbus-1-dev`/`libsecret-1-dev`) per the §12.10 note.
- **Next milestone: Step 5 (v0.5.0) — analytical tabs + alerts** (threshold notifications when a window crosses warning/critical, quiet/off by default, user-configurable; the analytical TUI tabs). Pin-and-card per §12.0 when starting.

**✅ CONNECTION-FLOW SAFETY REVIEW + HARDENING + ⛔ LEGAL GATE CLEARED (2026-06-16).** After T10c, a deep adversarial review of the whole connection subsystem (8 dimensions — endpoint authorization, secret boundary, transport/TLS, ToS posture, supply chain, enforcement-test adequacy, failure degradation, threat model — each finding independently verified, plus a completeness critic) found **no security-control failure**: one egress chokepoint host-bound before any I/O, HTTPS/GET-only, no tier-4 endpoint, the admin key confined stdin→keychain→redacting-header (never disk/log/argv/env/URL/error), TLS hardened, money layer unit-safe, default build links/calls none of it. Verdict: "safe — apply the hardening to reach the safest road." **Hardening landed (all 6 items, gate green — fmt + clippy [workspace/`connect`/`connect-test-support`] + `test --workspace` [cli 90 default / 109 connect-test-support, connect 65, core 85] + offline.rs both tiers + cargo-deny default & `--all-features` + `offline_acceptance.sh`):**
- **(1) Forbidden-crates `--features connect` tier → subset-allowlist.** `apps/cli/tests/offline.rs` now asserts the connect-delta (reachable(connect) − reachable(default), unioned across the 6 shipped targets) is a **subset** of a reviewed `CONNECT_ALLOWED` set, so a future dep bump that pulls a NEW socket/TLS/telemetry crate trips the gate (the prior name-denylist couldn't catch an unlisted crate). `ALWAYS_FORBIDDEN_CRATES` still independently bans the runtime/TLS/telemetry classes. An `#[ignore]` `print_connect_delta` regenerates the set. (Verified the shipped binary links only the sync `dbus-secret-service`; the async `secret-service`/zbus crates appear only in the dev/build-dep union, never `cargo tree -e normal`.)
- **(2) `reconcile` per-vendor degradation.** A hard `cost_report` fetch error (transport / unparseable body / keychain — the soft 401/403/429/5xx/4xx already degrade inside the adapter) now degrades to a new `VendorReportUnavailable::FetchFailed` (detail-free, so it can't leak) so the local estimate still shows and the OTHER connected vendors still reconcile — never aborting the whole multi-vendor view. Layer-1 test added.
- **(3) Org-label sanitization (terminal-injection guard).** Anthropic's server-controlled `me.name`/`id` is stripped of control chars at ingestion (`OrgLabel::from_server`) and the connect `emit` strips control chars always + guarantees pure-ASCII `--plain` for any label. Tests added (ESC + non-ASCII label renders safely; registry file carries no escape byte).
- **(4) `offline_acceptance.sh` assertiveness.** The netns connect-action check now requires a network-reaching diagnostic before treating exit≠0 as "fails closed" (a pre-network regression can't pass vacuously); a NOTE prints when the dynamic assertions aren't strace-observed; the `socket(AF_INET)` check is documented as the authoritative no-egress signal.
- **(5) Dormant usage endpoints flagged.** `AnthropicAdapter`/`OpenAiAdapter::usage_report` carry a DORMANT doc note (no production caller as of v0.4.0; same sanctioned platform-admin GET surface; a future caller is a deliberate reviewable step). (Chose the doc-note form over a cfg-gate to avoid a brittle dead-code cascade across both adapters for a low-value tripwire on an already-sanctioned surface.)
- **(6) Registry hardening.** `connections.json` is written `0600` (owner-only) on Unix; the no-inter-process-lock load→modify→save is documented (acceptable for interactive one-at-a-time connect/disconnect; keychain is the secret source of truth).
- **Docs trued (code is canon):** `SECURITY.md` §Scope/§Local-first/§Threat-model (the stale "nothing calls the client" claims + the no-certificate-pinning trust model), the `costroid-connect` crate doc, and `docs/proposals/T10b-LEGAL-REVIEW.md` (the cited review brief + its precision corrections). **SPKI/cert pinning: deferred + documented** (not required for 0.4.0).
- **⛔ LEGAL GATE CLEARED via maintainer self-attestation (2026-06-16, `docs/proposals/T10b-LEGAL-REVIEW.md` §10):** the maintainer reviewed the flows against the §9 checklist and accepted them for v0.4.0. This is a maintainer risk-acceptance (own-key, documented first-party endpoints, OSS), **not** counsel's opinion — counsel advisable before commercialization / 1.0 / any new credential/OAuth/endpoint. **So T10b is now unblocked — only the release mechanics remain.**

**📌 T10 PINNED + ⛔ SIGNED OFF (2026-06-13) — the `connect`/`disconnect` CLI + Connections view + reconciliation surface (the first caller of `costroid-connect`); T10a carded at §12.14, T10c at §12.15, the deferred live-confirm card T10-LIVE-ROWS at §12.16.** Full pin record + sourced endpoint research in `docs/proposals/T10-PIN-PROPOSAL.md` (PROPOSED → ⛔ SIGNED OFF 2026-06-13; produced by a 7-agent research → adversarial-doc-verify → completeness-critic workflow, every endpoint checked against live official docs). The pins:
- **CLI surface (§1.1) — APPROVED as proposed.** `costroid connect/disconnect <vendor>` + `connections [--check]` + `reconcile [--vendor] [--period]`, all `#[cfg(feature="connect")]`. Admin key read **stdin-only** (no-echo on TTY, one line on a pipe) — **NEVER argv/env**; wrong-key-class **prefix check before any network**; `gemini` → the pinned `unavailable — no sanctioned static-key usage API` line, exit 0, no key accepted; typed-failure remediation copy (individual-account / member-not-Owner / rejected).
- **Connect-time validation (§2) — the only NEW endpoint surface; neither reads spend beyond the user's own data.** **Anthropic** = `GET /v1/organizations/me` (live-confirmed; `x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01`; returns `{id,name,type}` only, **zero billing**; same Admin-API org-gate as `cost_report` → predicts the fetch; any non-200 = invalid/ineligible). **OpenAI = `GET /v1/organization/costs` over a recent COMPLETED 1-day window, `limit=1`** — the **⛔-SIGNED-OFF AMENDMENT** to proposal Option A (NOT `/usage/completions`, NOT `admin_api_keys`): probe the exact endpoint T10c depends on, so "Connected" predicts the cost fetch; `200`=success→store, `401`/`403`=failure→remediate + don't-store; probing `/costs` directly **moots the OpenAI costs-vs-usage scope question** the completeness critic flagged (the cost/usage scope is undocumented on fetchable pages). Both reuse the built `AnthropicAdapter`/`OpenAiAdapter` + `AuthorizedClient` classification (`OpenAiAdapter::fetch_cost_report` over a 1-day window IS the probe); T10a adds Anthropic's `me` `validate()` call.
- **Connections view (§4) — a `costroid connections` subcommand** (local-only by default; `--check` re-validates live). A Providers TUI tab stays Step 5. Lists Anthropic/OpenAI connected/not + Gemini unavailable; status by a **non-color text cue**; **optional non-secret org-label added to `RegistryFile`** (captured from `me`).
- **Reconciliation display (§5) — a `costroid reconcile [--vendor][--period]` subcommand** surfacing T9c `CostReconciliation`: **vendor-scoped** local estimate (`x_Tool` claude-code→Anthropic, codex→OpenAI, cursor excluded), signed variance (+over / −under), **typed vendor-absence rendered as text, NEVER `$0`**, caveats footnoted (`priority_tier_absent`, `per_model_derived_best_effort`); **dollar (cost) reconciliation ONLY** — token-side + its `responses_api_coverage_unconfirmed` caveat deferred; `--plain` + non-color cue.
- **Connect-action offline test (§7) — two layers, zero real network.** Layer 1: a `--features connect` integration test drives an **injectable** command core against the loopback `MockServer` + keyring mock (only-loopback egress; secret-to-keychain-only via a fixture-`$HOME` fingerprint; disconnect-clean). Layer 2: the strace script runs `costroid connect anthropic` with a prefix-valid-but-fake key under isolation, proving **fail-closed + no `$HOME` residue + no rogue host**. **No host-override knob** (it would weaken the authorized-host guarantee).
- **Deferred:** OAuth (tier 2, §8); API-lane rate-limit denominators (§9 — only Anthropic `/v1/organizations/rate_limits` is a clean source, OpenAI has none → render "unavailable" if ever built); token-side reconciliation.
- **Split (§10):** **T10a** (connect/disconnect/connections + validation + connect-action test + ⛔ GATE 2b) → **T10c** (reconciliation display) → **T10b** (release, §12.10). Carded at §12.14 / §12.15 (T10b already §12.10).
- **⛔ gates carried to the cards (NOT bypassed):** (1) **GATE 2b** — T10a's live-confirm with Eren's own key resolves the T9b populated-row follow-ups + the OpenAI `/costs` probe behavior; anything unconfirmable for lack of real usage is deferred to **T10-LIVE-ROWS** (§12.16) with locked criteria (Eren noted this will likely defer unless real API usage is generated first). (2) **Legal review of the connection flows** — gates the **0.4.0 release (T10b)**, reviewing the flow **T10a builds**; Eren engages it in parallel with the T10a build. Plus the standing **CLI-surface** + **secret-handling** ⛔ approvals on T10a.

**✅ T10a DONE (2026-06-15, gate green — the FIRST caller of `costroid-connect` and the FIRST real network in the product; ⛔ GATE 2b CLEARED, nothing deferred).** The `connect`/`disconnect`/`connections` CLI, connect-time key validation, the injectable command core, the two-layer connect-action test, and the GATE-2b money-shape fixes — all behind the off-by-default `connect` feature + an explicit user action. **Built as pinned (proposal §1.1/§2/§4/§6/§7):**
- **CLI surface** — `costroid connect <anthropic|openai|gemini>` · `disconnect <vendor>` · `connections [--check]` (clap, all `#[cfg(feature="connect")]`). Key entry is **stdin only** — a no-echo `rpassword` (=7.5.4, Apache-2.0 — `rpassword` + its `rtoolbox` dep both Apache-2.0; verified permissive via `cargo deny --all-features`) prompt on a TTY, one line on a pipe — never argv/env. `gemini connect` prints the pinned `GEMINI_UNAVAILABLE_MESSAGE` + a why-line and exits 0 **without** reading a key. Wrong-class is caught by the adapters' `wrong_key_class` **before any I/O**; the remediation shows only a ≤8-char non-secret class prefix (never the key body). `--plain` folds the em-dash **and** the `…` ellipsis (locked by `is_ascii()` test asserts).
- **Validation (the only new endpoint surface)** — Anthropic: a non-billing `AnthropicAdapter::validate()` → `GET /v1/organizations/me` (reuses the `x-api-key` + `anthropic-version: 2023-06-01` headers; parses `{id,name}`; captures the org label; any non-200 → the same typed `VendorReportUnavailable`). OpenAI: `OpenAiAdapter::fetch_cost_report` over `completed_day_window()` (yesterday 00:00→today 00:00 UTC) **IS** the probe — `GET /v1/organization/costs`; 200 → store, 401/403 → remediate + don't-store. New `OrgValidation` enum (re-exported); `OrgLabel` (non-secret) on `RegistryFile` + `ConnectionRegistry::{mark_connected_with_label,label}` (disconnect also drops the label).
- **Injectable command core** (`apps/cli/src/connect.rs`, `#[cfg(feature="connect")]`) — `run_connect`/`run_disconnect`/`run_connections`/`gemini_connect` write to an injected `Write`; the network half is the `AdapterSet` trait (`RealAdapters` in `main.rs`; a loopback impl in the test). `store.store` runs **only** after a successful validation (a rejected/wrong-class key is never stored).
- **GATE 2b CLEARED — nothing deferred** (the 2026-06-14/15 own-key live-confirm, APPENDIX A of the T10a build): money units **confirmed** (Anthropic `amount` = decimal-CENTS ÷100; OpenAI `amount.value` = dollars verbatim); **Responses-API/Codex coverage CONFIRMED COVERED** → `responses_api_coverage_unconfirmed` flipped **`false`** in `vendor_report.rs` (T10c carries **no** token-undercount caveat); `line_item` = `"<model>, <direction>"`, `currency` `usd`/`USD` (case-insensitive), a non-admin key on `/costs` → **401** — all confirmed; and the one real-shape finding **fixed**: `UsdAmount::from_json_dollars_str` now absorbs a **JSON-string** `amount.value`, **scientific notation** (`0E-6176`), and **>28 fractional digits** (39 dp) — strip-quotes → expand-scientific-to-plain → rounding `FromStr` (never `from_str_exact`/`from_scientific`, which **errored** on exactly these; still exact `Decimal`, never `f64`). The verbatim APPENDIX-A bodies are pinned as regression fixtures (Anthropic `cost_report`, OpenAI `/costs`, the 401). The §12.16 **T10-LIVE-ROWS** card's purpose is now fulfilled (its predicted real-shape parser change occurred).
- **Connect-action test (proposal §7)** — Layer 1: a `#[cfg(all(test, feature="connect-test-support"))]` test in `connect.rs` drives the command core against the `test_support` loopback `MockServer` + the keyring mock — connect stores the secret **only** in the mock keychain (the temp dir holds **only** the non-secret registry file), the OpenAI probe hits `/v1/organization/costs`, a rejected/wrong-class key is **not** stored, disconnect removes key+registry+label. Layer 2: `scripts/offline_acceptance.sh` runs `connect anthropic` (fake key, stdin) under a **netns** (real isolation — strace only observes and would reach the real host) → **fails closed**, no `$HOME` residue; `disconnect` makes no network call and leaves no secret residue.
- **Seams/features** — a new `test-support` feature on `costroid-connect` exposes the curated loopback harness (the `loopback_http_for_tests`/`with_client`/`RetryPolicy::test` seams stay `pub(crate)`, now `#[cfg(any(test, feature="test-support"))]`; `pub mod test_support` only under the feature). The CLI's `connect-test-support = ["connect", "costroid-connect/test-support"]` is **not** in the shipping `--features connect` build, so the loopback escape-hatch never reaches the production surface (offline.rs both tiers + the default resolved graph **unchanged**). CI runs the Layer-1 test via a dedicated step.
- **Deviations (logged):** (1) the Layer-1 test lives as a `#[cfg(test)]` module **inside `src/connect.rs`** (gated on `connect-test-support`), not under `apps/cli/tests/` — a binary crate can't expose private command-core internals to an integration test without a lib/bin split; an in-`src` unit test is the cleaner Rust idiom and keeps the scope tight. (2) the OpenAI probe reuses `fetch_cost_report`'s `limit=180` over a 1-day window (≤1 bucket either way), not a literal `limit=1` — the card pins "`fetch_cost_report` over that window IS the probe." (3) the wrong-class remediation shows a ≤8-char class prefix (not the raw "<seen>-key"), honoring the golden rule "never echo the key." (4) `ci.yml` gained a Layer-1 test step + the offline-acceptance disconnect check tolerates a keychain-unavailable nonzero exit in a headless box (it asserts no-network + no-secret, not disconnect success — which Layer-1 proves against the mock). **Scope fence held:** no reconcile DISPLAY (T10c), no rate-limit denominators, no OAuth, no TUI tab, no endpoint beyond `/me` + `/costs`. **Independently adversarially reviewed** (a fresh-context 5-dimension / 11-agent workflow: secret-handling · no-network/cfg-gating · the money parser · spec compliance · lint+test rigor): **0 critical/high/medium** (the secret boundary, no-network invariants, spec compliance, and money-parser correctness all confirmed clean), **5 low/nit fixed** — (a) `read_admin_key` now trims **in place** and moves the one buffer into `SecretString` (no separate un-zeroized plaintext copy lingers) with the doc trued; (b) two `connections --check` tests added (the Anthropic *failure* branch + the OpenAI probe path); (c) a money-rounding test pins the actual round-up-at-the-28th-digit semantics (not just trailing-zero truncation); (d)+(e) two doc/comment accuracy fixes (the parser rationale; the netns vacuous-pass note). Files: `crates/costroid-core/src/vendor_report.rs`; `crates/costroid-connect/{Cargo.toml, src/{lib.rs,anthropic.rs,openai.rs,http.rs,fetch.rs,test_support.rs}}`; `apps/cli/{Cargo.toml, src/{main.rs,connect.rs (new)}}`; `scripts/offline_acceptance.sh`; `.github/workflows/ci.yml`; README + CHANGELOG + CLAUDE.md. **Next:** T10c (`costroid reconcile`) on the stored keys + the fetch path; T10b cuts v0.4.0 once T10c + the ⛔ legal review land.

**✅ T10c DONE (2026-06-15, gate green — the `costroid reconcile` display surfaces T9c on screen; NO new ⛔ gate, NO new secret/network boundary).** The reconciliation display: `costroid reconcile [--vendor anthropic|openai] [--period day|week|month|year]` fetches a connected vendor's billed-cost report (reusing T10a's stored key + authorized client), compares it to the local API-lane estimate per UTC day + model via `reconcile_cost`, and renders the signed variance with every typed caveat/absence intact. **Built as pinned (proposal §5 / card §12.15):**
- **The subcommand + wiring** (`apps/cli/src/reconcile.rs`, new, `#[cfg(feature="connect")]`): no `--vendor` = every CONNECTED billing vendor (each its own section) + an always-present Gemini "unavailable" section (the pinned `GEMINI_UNAVAILABLE_MESSAGE`, **no fetch**); a single `--vendor` is shown even when not connected (estimate beside "connect <vendor> first"). The local rows come from the **same** `focus_records_from_local_logs` pipeline `now`/`trends`/`export` use — no re-implemented parsing — then are **vendor-scoped** (`x_Tool` `claude-code`→Anthropic, `codex`→OpenAI, `cursor`→EXCLUDED) **and window-scoped** before `LocalCostEstimate::from_focus_records`. The fetch requests a **COMPLETED-day** `DateRange` (`completed_window(period)`, today's incomplete UTC day excluded so the cost-report fetch never 400s).
- **The injectable fetch seam:** `AdapterSet` (T10a) gained `cost_report(vendor, &store, range)`; `RealAdapters` delegates to `AnthropicAdapter`/`OpenAiAdapter::cost_report` (Gemini → pinned unavailable, no network); the Layer-1 `LoopbackAdapters` serves a fixture cost body so the command core is driven against the loopback `MockServer` + keyring mock with **zero real network** (`reconcile_anthropic_fetches_loopback_scopes_rows_and_renders_no_real_network` asserts the fetch hit `/v1/organizations/cost_report`, the cursor + out-of-window rows were excluded, and the only disk artifact is the non-secret registry).
- **The honest renderer** (`apps/cli/src/render.rs`, `render_reconciliation*`): a **pure function of the core `CostReconciliation`** (no `costroid-connect` dep), so it compiles + snapshot-tests in the DEFAULT suite (gated `any(feature="connect", test)` so the default non-test bin carries no dead code). Renders: signed variance per day + model (`+$X over (+P%)` / `-$X under (-P%)` / `exact`, percentage rounded to a uniform 1 dp **at the render boundary**); **TYPED vendor-side absence as TEXT, never `$0`** (`DayNotCovered`→"report doesn't cover this day", `ModelNotInReport`→"not attributed by the vendor", `ReportUnavailable`→the typed reason incl. `NotConnected`→"connect <vendor> first" + Gemini's pinned string), with the variance cell `—` when absent; a **LOCAL `$0`** against a real billed figure rendered as a genuine "vendor billed a model Costroid never saw" row; caveats **footnoted** (`priority_tier_absent`, `per_model_derived_best_effort`) with best-effort rows `*`-marked; the local figure **always** `~`-labeled an estimate beside the hedge line. Insta snapshots in braille/ascii/plain (incl. `--plain`); over/under carried as TEXT (never color); em-dash + the absence `—` + reason em-dashes ASCII-folded at the boundary (locked by `is_ascii()` asserts).
- **DOLLAR (cost) reconciliation only** — NO token-undercount caveat (Responses-API/Codex coverage was confirmed in T10a → `responses_api_coverage_unconfirmed = false`; a token-side view + that caveat stay a deferred later card; T9c's cost-day totals are complete).
- **Deviations (logged):** (1) `run_reconcile` takes an explicit `DateRange` (main.rs computes `completed_window(period)`; the test pins a fixed window) rather than computing it internally — makes the Layer-1 row-scoping deterministic regardless of the run date. (2) `FocusRecord` is now **re-exported from `costroid-core`** (`pub use costroid_focus::FocusRecord`) so the connect-gated `run_reconcile` can name `&[FocusRecord]` without `apps/cli` taking a direct `→ focus` edge — the doc's `apps → core → {providers, focus}` arc holds; no output/export schema changed. (3) the reconcile renderer lives in `render.rs` (not behind `--features connect`) because it is pure over the core type — its snapshots run in the default suite; only the connect-gated *command* (`reconcile.rs`) is feature-fenced. **Scope fence held:** no new endpoint/parse (reused the adapters + `reconcile_cost`), no connect/disconnect/connections change, no TUI tab, no token-side reconciliation, no rate-limit denominators, no new secret/network boundary; default resolved graph unchanged. Files: `crates/costroid-core/src/lib.rs` (`FocusRecord` re-export); `apps/cli/{src/main.rs (clap Reconcile + `run_reconcile_command`), src/connect.rs (`AdapterSet::cost_report`), src/reconcile.rs (new), src/render.rs (renderer + snapshots)}`; `docs/DESIGN-SYSTEM.md` + `CHANGELOG.md` + `README.md` + `CLAUDE.md`. Gate: fmt + clippy (`--workspace`, `-p costroid --features connect`, `-p costroid --features connect-test-support`, all `-D warnings`) + `test --workspace` (**cli 90 / core 85 / connect 64**, 0 failed) + `test -p costroid --features connect-test-support` (**106 cli**, +5 reconcile.rs tests: 3 Layer-1 loopback + 2 window) + offline.rs both tiers + `cargo deny check licenses bans` (default **and** `--all-features`) + `bash scripts/offline_acceptance.sh` (default tier — the reconcile command isn't even compiled into the default build) — all green.
- **Independently adversarially reviewed** (two fresh-context reviewers — no-network/cfg-gating + secret-handling + spec-honesty + accessibility + scope; and Rust/Decimal correctness + lint rigor + test quality): **0 critical/high/medium**; the no-network/cfg-gating invariant, the secret boundary (key only READ to fetch, never logged/serialized/echoed), the honesty contract (typed absence never `$0`, signed-variance direction as text, full-precision pct rounded only at the boundary, estimate always `~`-labeled, caveats footnoted, Gemini single-sourced), and the `FocusRecord` re-export soundness all confirmed clean. **5 low/nit fixed:** (a) a genuinely non-zero **sub-cent** variance now renders `<$0.01 <dir> (<pct>)` instead of a misleading `$0.00`, and `format_signed_pct` takes its sign from the **unrounded** pct so a tiny negative that rounds to `0.0` reads `-0.0%` (sign-consistent) not `+0.0%`; (b) a `mixed_states` fixture + plain snapshot + asserts now cover the three previously-untested honest branches (`ModelNotInReport` → "not attributed by the vendor"; a `$0`-billed model → "(vs $0 billed)"; the sub-cent row); (c) the **Plain** header is de-doubled to the established `costroid reconcile` convention (not the visual builder's `costroid costroid`), for cleaner screen-reader output; (d) a footnote fires when some days are `DayNotCovered`, so the header `est / inv` pair isn't misread as a real over-estimate (the invoice total spans only covered days); (e) the window test now covers all four `day/week/month/year` mappings + `window_label`'s single-day branch. **Not changed (with rationale):** non-ASCII model names are not specially folded — that matches the existing project-wide convention (provider-supplied model/project names pass through verbatim across `now`/`trends`/`frontier` too; not a T10c regression); no dynamic `reconcile` netns check was added — the fetch reuses T10a's already-netns-proven authorized client and the Layer-1 loopback test proves the path, and the card's Done-when requires only the default-tier offline checks.
- **Deviations beyond those already listed:** none.
- **Post-build independent review pass (orchestrator, separate from the builder's in-context reviewers — the pass that gates the commit per §11.1).** Gate re-verified green from a clean tree (fmt + clippy `--workspace`/`--features connect`/`--features connect-test-support` + `test --workspace` cli 90 / core 85 / connect 64 + `--features connect-test-support` 106 + offline.rs both tiers + `cargo deny` default & `--all-features` + `offline_acceptance.sh` default tier + connect baseline + Layer-2 netns). Three fresh-context adversarial reviewers (security/invariants · honesty/correctness · docs/consistency): **0 critical/high/medium**; the no-network/cfg-gating invariant (default binary carries no reconcile symbols), the secret boundary (key only READ to build the auth header), the full honesty contract, and the `apps → core → {providers, focus}` arc (focus stays a dev-dep only) all confirmed clean. **Fixed: 1 low (code) + 3 low (docs).** Code: when a vendor's billed figure is genuinely sub-cent (so the invoice cell shows `$0.00`) and the variance is ≥ `$0.01`, the percentage exploded against an effectively-invisible denominator (e.g. `+$1.40 over (+69950.0%)`); `reconcile_variance_cell` now renders `(vs <$0.01 billed)` in that case — parallel to the existing `(vs $0 billed)`, a render-boundary-only change (no engine change), surgical so coherent small percentages (e.g. a sub-cent variance's `-50.0%`) are kept (one `mixed_states` snapshot re-recorded; ⛔-decided by Eren 2026-06-15). Docs (`docs/DESIGN-SYSTEM.md` reconciliation block only): the example block showed `×` where the renderer emits ASCII `x` in every mode incl. braille → corrected; the example omitted the second `DayNotCovered` footnote the code prints for that fixture → added (+ the "Caveats footnoted" bullet now names it); the `--plain`/Ascii bullet mislabeled the hedge `x` as a foldable `×` → corrected. **Next:** T10b (§12.10) cuts v0.4.0 once the ⛔ legal review lands.

**✅ T9c DONE (2026-06-13, gate green, 280 tests).** The estimate-vs-invoice reconciliation **engine** — pure `costroid-core`, fixture-tested, **zero network, no `costroid-connect` dependency** (it consumes the vendor-report types core itself defined in T9b; the `connect → core` direction holds by construction — core's `Cargo.toml` gained nothing). No CLI/render surface (T10 owns surfacing). Files: `crates/costroid-core/{src/reconcile.rs (new), src/lib.rs (mod + re-exports), src/vendor_report.rs (+`UsdAmount::checked_sub`)}`; docs/DATA-MODEL.md (reconciliation section → as-built shapes) + CHANGELOG.md. Gate: fmt + clippy (`--workspace -D warnings`, and `-p costroid --features connect`) + `test --workspace` (**280 tests**, +16 over T9b's 264: `reconcile` +15, `vendor_report` `checked_sub` +1) + offline.rs both tiers + `cargo deny check licenses bans` (default **and** `--all-features` — both green, graph unchanged) + `bash scripts/offline_acceptance.sh` (incl. the feature-on baseline) — all green.
- **As-built engine (the card left the shape to the builder; reconciled with DATA-MODEL "Reconciliation engine").** Module **`costroid-core::reconcile`** (re-exported from the crate root): entry point `reconcile_cost(&LocalCostEstimate, &CostReportOutcome) -> CostReconciliation`. **Input** `LocalCostEstimate` = estimated dollars per **(UTC day, model)**, **API lane only**; `from_focus_records(&[FocusRecord])` keeps API-lane rows (`CostLane::Api`), buckets by **UTC day** (`charge_period_start.date_naive()`) + `x_Model`, sums `billed_cost`. **Output** `CostReconciliation { days: Vec<DayReconciliation>, caveats: CostReportCaveats, report: ReconciledReportStatus }`; `DayReconciliation`/`ModelReconciliation` each carry `local_estimate: UsdAmount`, `vendor_billed: VendorBilled`, `variance: Option<UsdAmount>`, `variance_pct: Option<Decimal>` (+ per-model `confidence: Option<AmountConfidence>`). Added `UsdAmount::checked_sub` to `vendor_report.rs` so signed variance stays inside the unit-tagged money newtype (never raw `Decimal` escaping it).
- **Comparison semantics (pinned in the card, as built).** `variance = local_estimate − vendor_billed` (signed — **positive** = estimate exceeds the invoice, **negative** = invoice exceeds the estimate); `variance_pct = 100 × variance / vendor_billed` relative to the source of truth, at full `Decimal` precision (rounding is T10's). Days/models are the **union** of both sides; `BTreeMap`/`BTreeSet` keep day+model order deterministic. **UTC-day bucketing is deliberate** — the live-confirm finding that vendor daily buckets are UTC-midnight aligned (§11.5 ✅ T9b) means the local side must bucket by UTC day, **not** the trends view's local-tz day. The estimate is never presented as the bill and is never silently "corrected"; **calibration** (DATA-MODEL's "may calibrate") was **not** implemented — it didn't fall out naturally and would at most be a labeled output, so it's deferred (the prose was trimmed to say so).
- **Caveat survival + typed absence (the bug classes the card names).** The vendor report's `CostReportCaveats` (`priority_tier_absent`, `per_model_derived_best_effort`) are carried onto `CostReconciliation.caveats` **unchanged**, and OpenAI's per-model best-effort additionally onto each `ModelReconciliation.confidence = DerivedBestEffort` — so it survives both on the result and per row. Vendor-side absence is **typed, never `$0`**: `VendorBilled = Billed(UsdAmount) | Unavailable(BilledAbsence)`, with `BilledAbsence = ReportUnavailable(VendorReportUnavailable) | DayNotCovered | ModelNotInReport`; on absence `variance`/`variance_pct` are `None`. A whole-report `Unavailable` (Gemini/not-connected) still surfaces the local estimate day by day with every vendor figure typed-unavailable. `variance_pct` is also `None` when the vendor billed `$0` (undefined). A **local** `$0` is a genuine estimate (no observed usage) — only the vendor side is guarded against fabricated zeroes.
- **Scope held / a caveat NOT carried, on purpose.** Subscription lanes excluded (only API has an invoice). The token-usage report's `responses_api_coverage_unconfirmed` caveat is **not** consumed: it bounds a *token-side* comparison the engine doesn't do, and OpenAI's `costs` bills all traffic (incl. the Responses API Codex rides) so the dollar **day totals are complete** — a token-side reconciliation (where that caveat would live) is deferred to T10+. No FOCUS-schema change (reconciliation output is its own shape).
- **Fixtures (15 in `reconcile`, +1 `checked_sub` in `vendor_report`).** exact-match (zero delta) · over-estimate (+signed variance, +pct) · under-estimate (−signed variance, −pct) · local day outside the vendor range → `DayNotCovered` (no `$0`) · model billed but not locally estimated → real local `$0` vs billed · model estimated but not in the vendor breakdown → `ModelNotInReport` (no `$0`) · whole-report `Unavailable` → local estimate surfaced + typed reason · `priority_tier_absent` survives · OpenAI `per_model_derived_best_effort` survives on result + per-model · `Decimal` precision preserved (0.30 − 0.10 = 0.20 exact) · `variance_pct` `None` when billed `$0` · `from_focus_records` API-lane filter + UTC-day bucketing (incl. the 23:30Z/00:30Z boundary) · subscription/unknown lanes excluded · end-to-end `from_focus_records` → `reconcile_cost`.

**✅ T9b DONE (2026-06-13, gate green, 264 tests, independently adversarially reviewed; BOTH ⛔ gates cleared).** The Anthropic + OpenAI usage-API adapters + the Gemini first-class-unavailable state in `costroid-connect`, parsing into new provider-neutral vendor-report types in `costroid-core`. **⛔ Gate 1 (secret-handling) APPROVED 2026-06-13** — the adapter public API + key flow signed off as presented (key retrieved from `CredentialStore` → composed into an `AuthHeader` only, never in a URL/log/error). **⛔ Gate 2 (live-shape confirm) EXECUTED 2026-06-13** with Eren's own admin keys: the response envelopes/params/pagination/bucket+time shapes are **live-confirmed** against real API bodies, and Eren accepted finalizing on this basis because **his org has no raw-API usage to surface** (every `results` was empty across a 30-day window) — so the populated per-result-row shapes + the Responses-API coverage + a real 403 could not be live-verified and are carried as **standing follow-ups** (below), to run when real usage exists or in T9c/T10. Files: `crates/costroid-core/{src/lib.rs, src/vendor_report.rs (new)}`; `crates/costroid-connect/{Cargo.toml (+`costroid-core` dep), src/lib.rs, src/http.rs, src/fetch.rs (new), src/anthropic.rs (new), src/openai.rs (new), src/test_support.rs (new, `cfg(test)`)}`; docs/DATA-MODEL.md + ARCHITECTURE §5 + RELEASING.md + README.md + CLAUDE.md + CHANGELOG.md. Gate: fmt + clippy (`--workspace` and `-p costroid --features connect`) + `test --workspace` (**264 tests**, +36 over T9a's 228: core `vendor_report` +11, connect adapters/fetch/gemini/live-fixtures +25) + offline.rs both tiers + `cargo deny check licenses bans` (default **and** `--all-features` — both green, zero `unused-wrapper`) + `bash scripts/offline_acceptance.sh` (incl. the feature-on baseline) — all green (locally under the `unshare` netns fallback; CI's strace job is the authoritative dynamic gate).
- **Vendor-report type names + module (proposed, as built — the card left these to the builder).** All in **`costroid-core::vendor_report`** (re-exported from the crate root): `UsdAmount` (the canonical-USD money newtype, built only via the unit-tagged `from_decimal_cents_str` / `from_json_dollars_str` / `from_usd`; `MoneyParseError`), `DateRange` (+ `start_rfc3339`/`end_rfc3339`/`start_unix`/`end_unix`/`from_unix_seconds`) and the free fns `utc_date_from_rfc3339`/`utc_date_from_unix_seconds`, `VendorCostReport`→`VendorCostDay`→{`ModelCostAmount`, `CostLineItem`} + `CostReportCaveats`, `VendorUsageReport`→`VendorUsageDay`→`ModelTokenUsage` + `UsageReportCaveats`, `AmountConfidence{Exact,DerivedBestEffort}`, `CostReportOutcome`/`UsageReportOutcome` (`Available | Unavailable`), `VendorReportUnavailable` (+ `AccessForbiddenHint`) with `message()`, and `GEMINI_UNAVAILABLE_MESSAGE`. Connect adds `AnthropicAdapter`/`OpenAiAdapter` + free fns `gemini_cost_report()`/`gemini_usage_report()`. **T9c (DONE 2026-06-13) consumed these names and filled §12.13's `[fill at T9b landing: …]` slots — see §11.5 ✅ T9c.**
- **Built as pinned (§12.12 / proposal §2–§6):** endpoints, paths, params, auth headers, key-class prefixes, bucket widths, and money encodings match the ⛔-signed-off pins exactly — Anthropic `cost_report` (decimal-**cents** `÷100` exact) + `usage_report/messages`, `x-api-key` + `anthropic-version: 2023-06-01`, `sk-ant-admin`, `group_by[]=description`/`model`; OpenAI `organization/costs` (**float dollars** via the `serde_json` `raw_value` literal text, never `f64`) + `organization/usage/completions`, `Authorization: Bearer`, `sk-admin-`, `group_by=line_item`/`model`, `/v1` paths. Money is unit-tagged at the parse boundary (a core test pins that the same text differs by exactly 100× across the two encodings). Honesty caveats ride as **typed data**: Anthropic `priority_tier_absent=true`; OpenAI `per_model_derived_best_effort=true` + per-line `DerivedBestEffort` confidence; OpenAI usage `responses_api_coverage_unconfirmed=true`. Token normalization: Anthropic `cache_creation = ephemeral_5m + ephemeral_1h`; OpenAI uncached `input = input_tokens − input_cached_tokens` (their `input_tokens` includes cached). First-class unavailable states as data (never error loops): wrong-key-class (prefix-checked before any request), 401→`AuthenticationFailed`, 403→`AccessForbidden{hint}` (Anthropic individual-account matched on the documented phrase), 429/5xx/404→backoff-then-`RateLimited`/`ServerUnavailable` (the documented OpenAI `/costs` 404 outage degrades, never hard-fails). Pagination passes the opaque `next_page`/cursor back verbatim (percent-encoded for transport), stops on `has_more=false`, and is bounded by a `MAX_PAGES` ceiling.
- **Secret/key flow (⛔ GATE 1 — APPROVED 2026-06-13).** Keys live only in the OS keychain; the store-aware methods (`cost_report`/`usage_report(&CredentialStore, DateRange)`) do `CredentialStore::retrieve(ApiVendor)?` → `None` ⇒ `Unavailable(NotConnected)`, `Some(key)` ⇒ compose `AuthHeader` from the `SecretString` → use. The key reaches the wire **only** as an `AuthHeader` value (T9a redacting `Debug`): Anthropic `x-api-key`; OpenAI `Authorization: Bearer <key>` composed with an **exact-capacity** `String` so the `→ SecretString` conversion does not realloc and leave an un-zeroized heap remnant (the adversarial review flagged the original `format!` form; the value still reaches `ureq`'s non-zeroizing `HeaderValue` when sent — an inherent layer limit, same as the keychain `retrieve`). Keys are **never** placed in a URL/path/query (tests assert no secret in the request line), never logged, never serialized; `wrong_key_class` reads only the prefix via `expose_secret()` and never logs it. The lower seam `fetch_cost_report/fetch_usage_report(&SecretString, …)` takes the key directly (the testable entry; loopback fixtures inject a fake admin key).
- **Live-confirm (⛔ GATE 2 — EXECUTED 2026-06-13 with Eren's own admin keys; finalized).** **Confirmed against real `api.anthropic.com`/`api.openai.com` bodies:** both response **envelopes** match the parser structs exactly (Anthropic `{data[],has_more,next_page}`; OpenAI `{object:"page",data[],has_more,next_page}`); **bucket shapes** (Anthropic `starting_at` RFC3339-`Z`/`ending_at`/`results[]`; OpenAI `{object,start_time,end_time,results[]}` + extra `start_time_iso`/`end_time_iso` which the structs correctly ignore — a 30-day month returns one page, `limit=31`/`180`, `has_more:false`); **all query params accepted** (2xx, not 400 — `group_by[]=description`/`model`, `group_by=line_item`/`model`, `bucket_width=1d`, `limit`, RFC3339 / unix-seconds times); **OpenAI daily buckets are UTC-midnight aligned**; **pagination terminates**; and **`cost_report` 400s a current-day/future window** ("ending date must be after starting date" — it serves *completed days only*; the adapter degrades the 400 to `RequestRejected{400}`, never a crash — caller contract: request completed-day ranges). Three verbatim real bodies are pinned as regression fixtures (`parses_the_live_empty_results_envelope`, `…ignoring_unknown_fields`, `current_day_window_400_degrades_to_request_rejected`). **Could NOT be live-verified — Eren's org has no raw-API usage (every `results` empty across a 30-day window) — carried as STANDING FOLLOW-UPS** (run when real usage exists, or in T9c/T10; none change the type shapes): [ ] populated per-**result-row** field shapes (Anthropic `amount`-decimal-cents/`description`/`model`/`cost_type`/`service_tier`; OpenAI `amount.value`-float/`line_item`; token fields) — remain documented-schema-derived (the 7-agent pin research verified them vs live docs 2026-06-10); [ ] OpenAI Responses-API coverage (Codex) — `responses_api_coverage_unconfirmed` stays `true`; [ ] a real 403 body + its STATUS (401 vs 403 — classification branches on it); [ ] the `line_item` string format (grounds the best-effort per-model parse); [ ] `currency` value; [ ] deep history depth/retention.
- **⛔ GATE 2b — populated-row + live-shape finalization (MUST run before any real admin key reaches the adapters in T10).** The standing follow-ups above are not housekeeping: T9b is confirmed only at the **envelope/params/pagination/bucket+time** level, so the **per-`result`-row money-bearing parse** (Anthropic `amount`-decimal-cents/`description`/`model`/`cost_type`/`service_tier`; OpenAI `amount.value`-float/`line_item` + the token fields), the **OpenAI Responses-API/Codex coverage** (`responses_api_coverage_unconfirmed` held `true`), a **real 403 body + its STATUS** (401-vs-403, which the classifier branches on), the `line_item` format, `currency`, and history depth are **documented-schema-derived, NOT live-verified** — Eren's org had no raw-API usage (every `results` empty across 30 days). **Gate condition:** before a real admin key first reaches `AnthropicAdapter`/`OpenAiAdapter` in **T10**, EITHER (a) a live run against real usage confirms each item, OR (b) each is formally deferred to a named card with a locked completion criterion. **Asymmetric residual (why this guards T10, not T9b):** even unconfirmed, OpenAI `organization/costs` bills *all* traffic, so the **dollar day-totals are complete** regardless — the open risk is a future **TOKEN-side** undercount (e.g. Codex via the Responses API), which **T10's UI copy must surface** rather than present token sums as authoritative.
- **Deviations (logged):** (1) **`rust_decimal` and `chrono` are NOT connect deps** — all money/`Decimal` logic is encapsulated in core's `UsdAmount`, and all date formatting/parsing in core's `DateRange`/helpers, so `costroid-connect` adds **only** `costroid-core` (leaner than the card's anticipated `+ rust_decimal`; mirrors the T9a lean-deps precedent — `costroid-focus` also unneeded). (2) **`ConnectError::ClientError` gained a `body: Option<String>`** (and `get()` now reads the bounded 4xx body) so adapters can classify a 403 by its documented message; the body is a vendor error message, never a secret (the key is request-header-only) — redaction tests still hold. (3) **Added `ConnectError::ResponseFormat{detail}`** — a genuinely new failure class (a 2xx body that doesn't match the documented schema, or a malformed money amount); detail is secret-free. (4) **`AuthorizedClient` gained `pub(crate) url_for` + widened `loopback_http_for_tests` to `pub(crate)`** (both still `cfg(test)` for the latter) so adapters compose on-host URLs and the adapter tests drive the loopback path — the public prod surface is unchanged (still HTTPS-only, no `danger_*`).
- **Tests (fixtures only, zero real network — `cfg(test)` loopback `MockServer` in `test_support.rs`):** multi-page pagination with the opaque token passed back verbatim + the `has_more`+null-token termination guard; fractional-cent ÷100 + float-dollar (incl. scientific-notation) money with the 100× unit-tag test + the cents scale-overflow→`OutOfRange` arm; wrong-key-class (no request made); individual-account 403 / 401-vs-403 distinct; 429-degrade (both adapters) + 503 + the 404-outage degrade (bounded retries, zero-delay test policy); malformed-JSON→`ResponseFormat`; no-secret-in-URL + UA/auth headers asserted on the captured request; the exact Gemini string. Backoff is a pure function unit-tested directly (honors `Retry-After`, caps, exponential) so no test ever sleeps.
- **Independent adversarial review (6-agent panel, static).** Found **no blocker** and no real money-100×/secret-leak-to-logs/offline/panic/pin-fidelity gap. Nine LOW findings; the worthwhile ones were **fixed in this pass**: the OpenAI Bearer un-zeroized-remnant (exact-capacity build), the 403 phrase-matching tightened to documented-only signals (`individual account` / `owner`; dropped the `aws`/`bedrock`/`admin`/`permission` guesses → `Unknown`), explicit per-adapter `RequestLimits` (16 MiB body cap, T9a contract note 2), and +3 regression tests (null-token break, OpenAI 429-degrade, cents scale-overflow). **Documented limitations carried to the live-confirm / a follow-up** (not blockers): `Retry-After` is honored on 429 but 5xx/404 use bounded exponential backoff (threading it through `ServerError` is a T9a-shape change); OpenAI audio-token meters are not modeled (the neutral shape is the four standard meters; audio = 0 for Codex/GPT text); `currency` is not validated (the pins guarantee USD-always — confirm at Gate 2).

**✅ T9a DONE (2026-06-10, gate green, ⛔-APPROVED 2026-06-10 — human sign-off given in direct response to the ⛔ gate presentation of the three items: the redefined guarantee wording, the new crates/licenses incl. the OS-native-roots choice, and the client public API as the T9b contract) — the generic authorized-host HTTPS client built in `costroid-connect` (the crate's network half's foundation; no caller, no provider knowledge). Files: `crates/costroid-connect/{Cargo.toml,src/lib.rs,src/http.rs (new)}`; root `Cargo.toml` (`[workspace.dependencies]` exact pins); `apps/cli/Cargo.toml` (connect-feature comment currency) + `apps/cli/tests/offline.rs`; `.github/workflows/ci.yml` (license-job comment currency); `deny.toml`; `scripts/offline_acceptance.sh` (comment currency only — behavior unchanged, connect-ACTION half stays the T10 stub); SECURITY.md + CLAUDE.md + ARCHITECTURE §5/§8 (the redefined guarantee wording) + README (groundwork sentence) + RELEASING.md (`ureq`+`rustls` arrived with T9a; the `core`/`focus` deps and the publish-ladder slot defer to T9b); CHANGELOG.md. Gate: fmt + clippy (`--workspace` and `-p costroid --features connect`) + `build`/`test --workspace` (**228 tests** after the T9a review fix pass — T9a itself added 12 http tests onto a **210** pre-T9a baseline (the fix-pass entry's "209" was an off-by-one; trued by a fresh count) = 222 as built, and the review fix pass added 6 more; the offline trio expansion widened an existing test's assertions, adding no test) + `cargo deny check licenses bans` (the `--all-features` superset pass CI gates **and** the locally-run default-mode pass — both green with ZERO `unused-wrapper` warnings) + `bash scripts/offline_acceptance.sh` — all green (locally under the `unshare` netns fallback; strace not installed on the dev box — CI's strace job is the authoritative dynamic gate).**
- **Built as pinned (§12.11):** `AuthorizedClient` — blocking, HTTPS-only, GET-only, constructed over ONE bare authorized hostname; any off-host/wrong-scheme/userinfo URL is a typed error **before any I/O** (`UnauthorizedHost`/`InvalidUrl`, with `:443` default-port normalization); redirects disabled entirely (`max_redirects(0)`, any 3xx → `Redirect{status}`, never followed); caller-supplied `AuthHeader{name, value: SecretString}` with a redacting `Debug` (test-pinned: no Debug/Display/error text ever carries the value); `User-Agent: costroid/<CARGO_PKG_VERSION>` on every request (agent-level config, asserted received by a loopback test); `RequestLimits{connect_timeout: 10s, overall_timeout: 30s, max_body_bytes: 8 MiB}` defaults with a public `with_limits` constructor; classification-only error taxonomy on `ConnectError` (+11 variants: `UnauthorizedHost`/`InvalidHost`/`InvalidUrl`/`Redirect`/`Timeout`/`RateLimited{retry_after_seconds}`/`ServerError`/`ClientError`/`Transport`/`BodyTooLarge`/`NativeRoots`) — retry/backoff policy stays the caller's (T9b; the parsed `Retry-After` seconds ride the 429 variant to support it); response body returned as bounded bytes (`HttpResponse{status,body}` + a UTF-8 `text()` view; body parsing is T9b's).
- **TLS roots = OS-native trust, and webpki-roots is NOT in the graph.** `ureq = "=3.3.0"` (current stable; MSRV 1.85 ≤ our 1.88) with `default-features = false, features = ["rustls-no-provider", "_ring"]` — 3.3.0 split `rustls-webpki-roots` out of the base rustls feature, which is exactly what keeps `webpki-roots` (CDLA-Permissive-2.0 in current releases, MPL-2.0 in older ones — either way absent from the deny.toml permissive allowlist) **out of the resolved graph**; `_ring` selects rustls's `ring` provider (no `aws-lc` — whose `OpenSSL`-license component would also fail the allowlist). `_ring` is an ureq-internal feature name; the exact `=` pin keeps it stable. Roots load once at construction via `rustls-native-certs = "=0.8.4"` → `RootCerts::new_with_certs` (an empty OS store is the typed `NativeRoots` error, never a silently-untrusting client). New graph crates, all permissive: ureq 3.3.0 + ureq-proto 0.6.0 + http 1.4.2 + httparse 1.10.1 + percent-encoding 2.3.2 + rustls-pki-types 1.14.1 + utf8-zero 0.8.1 (MIT/Apache-2.0) · rustls 0.23.40 + rustls-native-certs 0.8.4 (Apache-2.0/ISC/MIT) · rustls-webpki 0.103.13 + untrusted 0.9.0 (ISC) · ring 0.17.14 (Apache-2.0 AND ISC) · openssl-probe 0.2.1 (MIT/Apache-2.0 — pure-Rust trust-store *path probing* despite the name; not OpenSSL) · schannel 0.1.29 (MIT, Windows).
- **Decision — proxies disabled.** ureq honors `HTTP(S)_PROXY` env vars by default, which would route traffic to a *non-authorized* host; the client sets `proxy(None)`. Revisit only via a carded task if users need corporate proxies.
- **Decision — loopback-TLS choice (the card's either/or): the test-only plain-HTTP loopback constructor.** `AuthorizedClient::loopback_http_for_tests` is compiled **only under `cfg(test)`** (private, unreachable from any build or dependent crate) and drives the full request/response/classification path against a hand-rolled `std::net::TcpListener` one-shot server — no TLS test server, no `rcgen`/cert-generation dev-deps in the graph, zero real network (loopback only, passes offline/strace CI). The public prod surface stays HTTPS-only with **no** `danger_*`/root-injection knob. Trade-off accepted: the TLS handshake itself is exercised only by the (root-loading) constructor test, not end-to-end — the first real-TLS exercise is T10's connect-ACTION acceptance test.
- **Deviation (expected, the card's predicted outcome) — `core`/`focus` deps DEFERRED to T9b.** The generic client API uses no internal type (hosts, URLs, headers, bytes), so the §11.4 backlog's anticipated `core`/`focus` edge was not added; the lean-deps rule beat backlog anticipation, exactly as §12.11 pinned.
- **Guards as-built (the ⛔ guarantee redefinition):** `offline.rs` connect tier now asserts **all three** of `ureq`/`rustls`/`keyring` present (T8's keyring precedent extended); default tier unchanged (trio still forbidden, `costroid-connect` unlinked — the default build's resolved graph is untouched). `deny.toml`: ureq's wrapper = `costroid-connect`; **rustls's wrapper = `ureq`** (its only real direct parent — wrappers must exactly match parents or cargo-deny emits per-entry `unused-wrapper` warnings; the chain costroid-connect → ureq → rustls is documented in the comment); keyring unchanged; both deny runs green with zero unused-wrapper warnings ("until T9" comments retired). The strace feature-on baseline still passes — the client existing ≠ a call happening (nothing calls it until T10).
- **Doc guarantee re-worded** (SECURITY.md §Security-model + scope bullet, CLAUDE.md crate bullet/build-status/Step-4 checklist/CI echo, ARCHITECTURE §5 ×3 + §8): from "costroid-connect contains no network code" to "contains the generic HTTP client but NO caller and NO provider adapter; no network call can occur without the explicit user-initiated connect action (T10)" — a property that *holds* because nothing outside the crate references the client until T10 (zero call sites), is *bounded* by the authorized-host type (where any future call may go), and is *verified* by the feature-on strace baseline (a normal connect-build run attempts zero network I/O). (The review fix pass replaced an earlier "enforced by the baseline + the type" phrasing, which overstated both mechanisms.)
- **T9b contract notes (flagged by the T9a adversarial review for the next card):** (1) **secrets must never ride in URLs** — `ureq` error text can echo the full request URI; the redaction guarantee covers `AuthHeader` values only (now rustdoc-pinned on `AuthorizedClient::get` + `AuthHeader`, with a regression test through the invalid-header-value path); (2) **`RequestLimits` accepts effectively unbounded values** — T9b sets explicit per-adapter limits rather than passing arbitrary ones through; (3) **trailing-dot FQDNs (`host.`) are treated as off-host** — fail-closed strictness; confirm that is acceptable for the pinned vendors before T9b composes URLs.

**📌 T9 PINNED + ⛔ SIGNED OFF (2026-06-10) — usage-API endpoint/auth pins accepted as proposed; T9a carded at §12.11.** Human sign-off given 2026-06-10 (the instruction to start T9a), accepting `docs/proposals/T9-PIN-PROPOSAL.md` unamended. The pins: **Anthropic** = Admin API `GET /v1/organizations/cost_report` + `GET /v1/organizations/usage_report/messages` (`x-api-key: sk-ant-admin…` + `anthropic-version: 2023-06-01`; `cost_report` amounts are **decimal-string CENTS**, fractional; individual/non-org accounts and Claude-on-AWS orgs → first-class "unavailable", never an error loop) · **OpenAI** = `GET /v1/organization/costs` + `GET /v1/organization/usage/completions` (`Authorization: Bearer sk-admin-…`; **float dollars**; costs has **no model group_by** → per-model $ is derived/best-effort only) · **Gemini = defer** — a Gemini API key authenticates inference only and the BigQuery billing export is OAuth-class, so `ApiVendor::Gemini` stays and renders **"unavailable — no sanctioned static-key usage API"**. **T9b amended to TWO adapters** (Anthropic + OpenAI) + the Gemini unavailable state — not the backlog's anticipated three. **Canon correction applied** (§5 table · §8 · the CLAUDE.md echo): the Antigravity Gemini-$ lane is **ToS-safe but NOT implementable under T9's own-key constraint** (the key reads nothing programmatically; the AI Studio cost views are browser UI, not API; the BigQuery export needs service-account JSON + an RS256 JWT-bearer OAuth exchange) — a post-T9 "Gemini (advanced)" connector at best; the docs stop promising the lane. Cross-cutting build pins (money-encoding unit-tagging at every parse boundary; classify-then-degrade on 429/5xx; `User-Agent: costroid/x.y.z`; wrong-key-class paste detection; blast-radius copy; the open empirical checks) live in the proposal §6 and bind T9a–T9c.

**✅ DOC-CURRENCY SWEEP (2026-06-10, the follow-up to the fix pass below; no task card) — the 2026-06-10 status review's remaining findings closed.** Doc-currency fixes across all md files: **8 doc-vs-code drifts, all doc-side** (the code was already correct; the docs were trued to it). Alongside the doc fixes: SECURITY.md truthed + a new **online `advisories` CI job** (`cargo deny check advisories` — the advisory-DB fetch lives in its own job, outside the offline gates); Costroid-generated text reaching `--plain` **and `RenderMode::Ascii`** output made **pure ASCII** (the em-dash provider notes; the Ascii-mode frontier header / point-note em-dashes) with a test pin; the FOCUS-conformance gate's allowlist **tightened to exact-match**; and the T9 pin proposal landed in-repo at `docs/proposals/T9-PIN-PROPOSAL.md` as **PROPOSED** (⛔ sign-off still pending — the §5/§8 classifications stay unchanged until it lands; see the §8 Antigravity research note).

**✅ FIX PASS (2026-06-10, full 12-leg gate green) — a cross-cutting 35-finding remediation from a whole-repo review (no task card; correctness + guard-hardening + doc-currency, no new features, T9 untouched).** Files: all four lib crates + `apps/cli` + `offline.rs` + the three scripts + `ci.yml` + 6 new fixtures + `scripts/focus-ruleset/` + 6 docs. **210 tests** (was 190), 23 snapshots (12 updated in place). Highlights, worst-first:
- **Quota-integrity code fixes:** `--plain`/plain-statusline now carry the warn/critical textual cue on `Available`/`Partial` (in plain, the cue is the ONLY signal — a bare "97%" was a color-alone violation); an epoch-sentinel `captured_at` renders **"capture time unknown"**, never a bogus "as of 00:00" (Claude cache missing `captured_at`, or a timestamp-less Codex entry, while the reading stays usable); Codex `used_percent` is now **raw-range-sanitized** like Claude's (out-of-range ⇒ measure `None`, never a Verified "900% !!"); `choose_limit` keeps the **latest `captured_at`**, not the last-scanned root (multi-root staleness inversion; sentinel loses to any real stamp; ties keep scan order); `parse_codex_limits` parses the two windows **independently** (a lone `primary` no longer drops); **measure-carrying `Partial` arms now carry the freshness stamp** — this *revises the T6 "no stamp on Partial" decision*: a Verified reading with an unparseable reset maps to `Partial` forever (the `resets_at` age-out can never reach it), so without a stamp an arbitrarily old % rendered with zero age signal.
- **Frontier honesty:** an overlay model with ANY unpriced row gets `fully_priced = false` → every re-pricing delta is the new `RepricingStatus::BaselineUnpriced` (a labeled gap; the line renders "spend not fully priced (frontier comparison unavailable)") — never a `Computed` dollar delta against the $0 placeholder baseline.
- **Secret/robustness hardening (⛔ surfaces, applied per the fix-pass instruction; review in this diff before commit):** `ConnectError`'s `From<keyring::Error>` **scrubs `BadEncoding`'s raw secret payload** (the one keyring variant carrying stored-secret bytes; pinned by a test); unique per-writer temp names for the cache/registry/settings atomic writes (a fixed `.tmp` sibling could publish a torn file under concurrency); `settings.json` writes are now temp+rename; `clean_window` type-checks values (only a number pct / number-or-string reset can reach the no-secret cache); the path-1 snippet wraps the original in a **`{ … }` group** (multi-line and leading-`#` originals keep their stdin; old flat-form snippets still parse); `--undo` with no backup now **restores the original parsed from the snippet** instead of deleting the user's statusLine; plain `costroid statusline` (the installed command) degrades a collect error to a blank line + exit 0.
- **Guarantee surface (⛔):** `offline_acceptance.sh` + `focus_conformance.sh` now neutralize **`CODEX_HOME`/`CURSOR_DATA_DIR`/`XDG_STATE_HOME`** too (a set override could feed REAL user logs into "fixture" gate runs); the `--live` check FAILs on any unexpected exit code (a TUI crash passed as "ok"); `ALWAYS_FORBIDDEN_CRATES` += `minreq`/`tungstenite`/`tokio-tungstenite`/`websocket`/`ssh2`/`libssh2-sys`/`russh` (≈44 total; `socket2`/`mio` deliberately excluded — `mio` rides crossterm legitimately).
- **The FOCUS-conformance gate was passing VACUOUSLY in CI.** The PyPI `focus-validator` wheel (2.1.0, latest) bundles only the 1.2.0.1 model, so `--validate-version 1.3 --block-download` crashes with `UnsupportedVersion`; the script's `|| true` swallowed it and the checker passed on zero parsed FAIL lines. Fixed three ways: the checker **hard-fails** without a results summary + rule lines; CI pins `focus-validator==2.1.0`; and the official `model-1.3.0.1.json` is **vendored at `scripts/focus-ruleset/`** (from the FOCUS_Spec release assets; CC-BY-4.0 **data artifact for the dev/CI gate only** — never compiled into or shipped with any binary, so outside the crate-license policy; see its README) via `--rule-set-path`. The gate now runs a REAL offline 1.3 validation: 764 rules, 9 failures = exactly the 3 allowlisted defects + their cascades. Also confirmed upstream issue **#144 IS filed** (open) — the allowlist's "to be reported" comment was the stale side, trued up.
- **Smaller fixes:** an `msrv` CI job (Rust 1.88 `cargo check --workspace --all-targets`, per CLAUDE.md's "test the MSRV in CI"); `RenderMode::Ascii` output is now **pure ASCII** (`-` rule, `*` insight marker, `--` for the em-dash; pinned by an `is_ascii()` test); the trends-plain snapshot is timezone-proof (the test fixture now builds **local-midnight** bucket starts exactly like production's `start_of_period_local` — the whole suite passes under `TZ=America/New_York`; the Local date *label* is documented product behavior, so the fixture, not the formatter, was the bug); provider `Unavailable` windows carry descriptive labels ("no captured reading; run `costroid setup-statusline`" / "no rate-limit data in local rollout logs" / "no sanctioned source") — the redundant "unavailable: unavailable" render is gone; a zero-row CSV export now **emits the header line** (the documented export contract; ⛔ export surface — option (a), header-always, chosen as recommended); the **unused `costroid-providers → costroid-focus` dependency was removed** (providers emits the provider-neutral types; FOCUS normalization lives in core — CLAUDE.md/ARCHITECTURE dep-direction text trued up; the RELEASING.md publish ladder order is unchanged and still valid); the stale "wired later (T4/T6)" rustdoc on `LimitAvailability::Estimated` is past-tense.
- **Doc currency (code-canon truing):** the brief's §8 "(stale)"-stamp claim (never built — the aged-out arms carry no stamp by design) and §3 `config_root` definition (shipped rule = first existing root) corrected; DATA-MODEL now records that **`ProviderName`/`PublisherName` ARE emitted** (validator-presence requirement; ARCHITECTURE §6 too) and marks its `FocusRecord` listing an explicit **abridged subset** (the struct is the authority); DESIGN-SYSTEM marks `--format`/presets **planned** (shipped flags: `--capture-only`/`--wrap` only), "configurable" → fixed consts today, and the cost-bar remaining cells as the track glyph `⣀` (shape-distinct, not color-alone "dim ⣿"); ARCHITECTURE's `NO_COLOR` claim corrected (ANSI dropped, braille stays glyph-distinct; ASCII is the braille-incapable fallback) and the statusline "side-effect-free" claim scoped to interactive stdin (piped stdin captures — the ⛔-approved T5 path 2); CLAUDE.md's TOML config is marked a **planned** convention ("config" removed from the shipped v0.1.0 list — no config system exists); §0's test count (177→210) and forbidden-crates count (~37→~44) refreshed; §3 Steps 2 and 3 now carry their ✅ done markers; §12.7's wrapper-warning count corrected to 2-since-T8.
- **New fixtures (synthetic, no real data):** `rate-limits-{poisoned-inrange,negative,string-pct,no-captured-at,malformed}.json` — incl. an **in-range poisoned-equality pin** (`used_percentage == resets_at == 50`) so the equality guard is mutation-proof independently of the `>100` range check.

**✅ T8 DONE (2026-06-09, gate green, ⛔-approved) — keychain credential store landed in `costroid-connect` (the crate's first behavior). Files: `crates/costroid-connect/{Cargo.toml,src/lib.rs}`; `deny.toml`; `apps/cli/tests/offline.rs`; `scripts/offline_acceptance.sh`; `.github/workflows/ci.yml`. Gate: fmt + clippy (`--workspace` and `-p costroid --features connect`) + `build`/`test --workspace` (11 new connect tests, 2 offline tests) + `cargo deny check licenses bans` (default **and** `--all-features`) + `bash scripts/offline_acceptance.sh` — all green.**
- **Built as pinned:** `enum ApiVendor { Anthropic, OpenAI, Gemini }` (billing-vendor axis, owned by `costroid-connect` — no `core`/`focus` dep); `CredentialStore::{new,store,retrieve,delete}` over the OS keychain (service `costroid`, account `apikey:<vendor>`); `ConnectionRegistry` (non-secret index at `${XDG_STATE_HOME:-~/.local/state}/costroid/connections.json`, atomic temp+rename); secrets wrapped in `secrecy::SecretString` (+ a redacting `Debug` on `CredentialStore`); `enum ConnectError` (thiserror, `#[from] keyring::Error`); **no `unwrap`/`expect`/`panic` in non-test code** (tests use panic-based `ok`/`some` helpers, matching the repo's deny-`unwrap_used` convention). Deps: `keyring = "=3.6.3"` (`default-features = false`, features `apple-native`/`windows-native`/`sync-secret-service`/`crypto-rust`) + `secrecy = "=0.10.3"` + workspace `serde`/`serde_json`/`thiserror`.
- **Deviation 1 — `keyring` has NO `mock` feature** (the card assumed one). `keyring::mock` is available unconditionally in 3.x; tests install it once via a `Once` (`set_default_credential_builder(mock::default_credential_builder())`). The mock persists a secret only inside its own `Entry`, so `CredentialStore` **eagerly owns one `Entry` per vendor** (an array) — that cache lets the mock round-trip and gives each store instance an isolated in-memory store (parallel-safe), while being a harmless cheap handle-reuse for the real OS backends.
- **Deviation 2 (positive) — async-io stays GLOBALLY banned.** Chose the **sync** Secret Service backend (`sync-secret-service` → `dbus-secret-service`, blocking **C libdbus**) over the `async-secret-service` (zbus) path, so NO async runtime is in any real build. `cargo tree --target all --features connect -i async-io` ⇒ nothing. So the ⛔-signed-off "permit keyring's async-io narrowly under connect" was **not needed**: `async-io`/`tokio`/`async-std`/`smol` remain in `ALWAYS_FORBIDDEN_CRATES` for **both** builds (the local-only no-async-runtime guarantee is preserved, not weakened). Cost: the C build-deps `libdbus-1-dev` (+ `libsecret-1-dev`, installed too per the card though `dbus-secret-service` needs only libdbus) — so `cargo {build,test,clippy} --workspace` now requires them (the default `costroid` binary still links none of it).
- **Deviation 3 — `offline.rs` resolves per-target.** Unfiltered `cargo metadata` is an all-targets *superset* that reported phantom `async-io`/`zbus`/`secret-service` (keyring's unused `async-secret-service` optional deps), which would have falsely tripped the forbidden-crates ban. Fixed by resolving **once per shipped triple via `--filter-platform` and unioning** (`SHIPPED_TARGETS` mirrors `deny.toml`); this applies real feature+target pruning so the phantom `async-io` is gone, while still catching a network dep gated to any single platform. `connect_feature_admits_only_the_sanctioned_trio` now also **asserts `keyring` is present**; `ureq`/`rustls` remain T9.
- **Guards/CI as-built:** `deny.toml` — keyring is now a *non-optional* dep of the `costroid-connect` member, so it is in the default workspace graph and its `wrappers` guard **fires** (comment updated); `ureq`/`rustls` wrappers stay unused (2 benign `unused-wrapper` warnings, exit 0) until T9. `apps/cli/tests/offline.rs` — per-target union + the keyring-present assertion. `scripts/offline_acceptance.sh` — the STUB's **secret-residue half** is filled at the unit level (`credential_round_trip_writes_nothing_to_disk`, mock backend) plus a **feature-on baseline** in the script (build `--features connect`; a normal run leaks no network and writes no `$HOME` residue); the connect-**action** + network half stays a T9/T10 stub. `ci.yml` — `pre-pr` and `offline-acceptance` jobs install `libdbus-1-dev`+`libsecret-1-dev`; `pre-pr` adds a `--features connect` build + clippy; the `license` job runs cargo-deny with `--all-features` (connect-on).
- **No user-facing change** (pure library, no CLI) → no README/CHANGELOG edit needed. ARCHITECTURE §5's "keyring … in T8" note trued up to "landed".
- **⛔ APPROVED 2026-06-09** (human sign-off): keyring stays **3.6.3** + the sync Secret Service backend, the credential model (service `costroid` / `apikey:<vendor>` / `ApiVendor`), and the denylist handling (async-io kept banned via the per-target resolve). **keyring 4.x evaluated and declined:** the `keyring` 4.0.1 crate is now the *CLI/sample* (it has a `[[bin]]`, depends on `clap`, and pulls **both** the dbus **and** `zbus-secret-service-keyring-store` backends *non-optionally* on Linux → `zbus → async-io`, which would break the §6 async-io ban; the backends aren't feature-gated, so it can't be excluded). The only async-io-clean 4.x path is a re-architecture onto `keyring-core` 1.0.0 + per-OS `*-keyring-store` crates (heavier deps: `regex`/`dashmap`/`ron`/`uuid`/`chrono`) — deferred, no functional gain for store/retrieve/delete. Revisit if keyring 3.x is ever deprecated.

**📌 T8 PINNED + carded (2026-06-09) — keychain credential store; pure-library scope. Carded into §12.9; not yet built. Prereq T7 ✅ met.** The first behavioral code in `costroid-connect`. Two items were ⛔ human-signed-off (marked below); the rest are agent-decidable pins logged for the build agent.
- **Pure-library scope (⛔-signed-off).** T8 ships *only* the OS-keychain credential store in `costroid-connect` — no CLI, no network. The `costroid connect`/`disconnect` command + Connections view are **T10**; the HTTP fetch + reconciliation are **T9**. "API-key entry" = the library *store* path, tested via `keyring`'s mock backend. Keeps a single public-CLI + ⛔-legal gate at T10 and T8 a self-contained secret-boundary unit.
- **Credential identity = billing vendor, not tool.** `enum ApiVendor { Anthropic, OpenAI, Gemini }` owned by `costroid-connect` — a different axis from `ProviderId` (ClaudeCode/Codex/Cursor): Cursor has no key, and Anthropic ≠ the Claude-Code *tool*. So connect stays free of `core`/`focus` deps through T8 (they land in T9 when behavior wires up — matching the Cargo.toml note).
- **Keychain naming (one-way door).** keyring service `costroid`, account `apikey:<vendor>`; `oauth:<vendor>` reserved for the deferred tier-2 OAuth (T9/T10) so a future token can't collide.
- **Non-secret connection registry.** The OS keychain isn't portably enumerable, so "what's linked" — a *non-secret* fact — lives at `${XDG_STATE_HOME:-~/.local/state}/costroid/connections.json` (atomic temp+rename), never the secret. T10's Connections view reads it.
- **In-memory hygiene.** Secrets wrapped in `secrecy::SecretString` / `zeroize` so they can't be `Debug`-logged or linger; never disk/config/logs (hard invariant §6).
- **Linux backend denylist policy (⛔-signed-off; ⚠️ superseded as-built — see ✅ T8 DONE above).** keyring's Linux Secret-Service backend may pull a transitive IPC crate currently on the forbidden-crates list (e.g. an `async-io` D-Bus executor — *local IPC, not network egress*). Resolution *as pinned:* **permit it narrowly, scoped to the `costroid-connect` subtree under `--features connect`** — the default build stays clean (keyring unlinked) and the strace offline test still proves zero outbound; install `libdbus-1-dev` + `libsecret-1-dev` in CI (Step 4). keyring uses pure-Rust crypto (`crypto-rust`), never openssl — which stays globally banned. **As built, the narrow permit was not needed:** the *sync* Secret Service backend pulls no `async-io`, so `async-io` stayed globally banned even under `--features connect` (Deviation 2 above).
- **Guard/CI consequences.** The `deny.toml` `keyring` `wrappers = ["costroid-connect"]` guard (a no-op since T7) now fires — needs a connect-on `cargo deny` pass to actually evaluate; `offline.rs`'s `connect_feature_admits_only_the_sanctioned_trio` now asserts keyring *is* linked under `--features connect` (T7 deliberately didn't — the skeleton was empty); the offline-acceptance STUB's "no secret written to disk/config/logs" half gets filled via the mock backend (the network half stays T9/T10).
- **⛔ build-time gate.** T8 is the first secret/keychain code (CLAUDE.md golden rule) and relaxes the no-network guarantee surface (like T7) — the build agent stops for approval on the keyring crate/backends, the credential model, and the narrowed denylist allowance before finalizing.

**🔒 ToS-safe rework (2026-06-06) — removed the session-reuse tier across plan + code.** The auth ladder is now **tiers 0–3 only; tier 4 = never** (no reuse of any credential/session/token against a non-sanctioned, undocumented, or internal endpoint — incl. Cursor's `api2.cursor.sh` — and no browser-cookie reading). Concretely: **`OptInSession` was removed from both `DataSource` and `AuthMethod`** in `costroid-providers`, and **Cursor's descriptor is now `auth: None`** (was `OptInSession`); the `each_provider_declares_its_capability` test was updated; full gate green. **Cursor live quota is no longer a numbered step** — it is **discovery-gated (§8)**, pursued only via a future *sanctioned* Cursor API/OAuth, never session reuse. The **egui taskbar moved Step 7 → Step 6 (v0.7.0 → v0.6.0)**, now the last numbered step; in the ledger the taskbar is **T18+** (no T19; Cursor is not a numbered T). §8 also gained verified ToS-safe discovery findings — **Copilot** (own classic-PAT / `gh` OAuth → documented `…/billing/ai_credit/usage`; user-billed only; never `copilot_internal/user`) and **Antigravity** (own-Gemini-key $ lane safe; "compute-effort" quota has no sanctioned source). The T3-DONE entry below predates this and is annotated accordingly.

**✅ T7 DONE (2026-06-06, gate green, 177 tests, ⛔-approved) — `costroid-connect` infra + the re-scoped no-network guarantee. Opens the 0.4.0 connections line. Files: new `crates/costroid-connect/` (empty leaf); `Cargo.toml` (member + `workspace.dependencies` entry); `apps/cli/Cargo.toml` (the `connect` feature + optional dep); `deny.toml`; `apps/cli/tests/offline.rs`; `scripts/offline_acceptance.sh`.**
- **The `connect` feature lives on `apps/cli`, not the root (card-deviation, the only valid home).** The card said "root `Cargo.toml` … `[features] connect = []`", but the root is a **virtual** workspace manifest (no `[package]`), so `[features]` cannot live there. Placed it on `apps/cli` as `connect = ["dep:costroid-connect"]` + `costroid-connect = { workspace = true, optional = true }` — matching CLAUDE.md / ARCHITECTURE §5 / RELEASING.md:121 (`app → costroid-connect → core`; the CLI depends on connect, connect publishes after core). `apps/cli/Cargo.toml` is a manifest, not a `.rs` source change, so it's inside the scope fence's "no source changes to … cli beyond the test". **Fixed the §2 dependency-direction drift** (it read `core → connect`; corrected to the apps-gated `app → connect → {core, focus}`). `costroid-connect` ships as an **empty leaf** (no deps) — keychain (`keyring`, T8) and HTTP (`ureq`+`rustls`, T9) land later; adding `core`/`focus` deps now would be dead weight.
- **`offline.rs` re-scoped to a two-tier check over the *resolved* graph, not `cargo metadata`'s `packages` superset.** Verified empirically that `packages` lists optional deps **regardless of feature**, so it cannot distinguish the default build from the `connect` build (and would falsely flag `ureq` in T9 even with the feature off). The new helper walks the resolved dependency graph (`resolve.nodes` `deps[].pkg`) from every workspace member **except `costroid-connect`** (the gated home — reached only as a *dependency* when `connect` is on). Two tests: (1) **default build** forbids the full network/TLS/telemetry list **including** the sanctioned trio `ureq`/`rustls`/`keyring`, **and asserts `costroid-connect` is not linked**; (2) **`--features connect`** forbids only the always-banned set (async runtimes, OpenSSL, other HTTP clients, all telemetry) — the trio is permitted — **and asserts `costroid-connect` is linked**. The trio is intentionally **not** asserted *present* (T7's crate is empty; T8/T9 add them). CI already runs both via `cargo test -p costroid --test offline`.
- **`deny.toml` scopes the trio via `wrappers` (⛔-approved: keep the guard now).** `openssl`/`openssl-sys`/`native-tls` stay banned **globally, no exception**; added `{ name = "ureq"/"rustls"/"keyring", wrappers = ["costroid-connect"] }` — allowed **only** when their direct parent is `costroid-connect`. **Known: this emits 3 benign `unused-wrapper` warnings** (2 since T8 — keyring's wrapper now fires) until the crates land (and, with `all-features = false`, even after — so T9 must add an `--all-features`/`connect`-on deny pass for the guard to actually fire; noted in the `deny.toml` comment). `cargo deny check licenses bans` still exits **0**.
- **`scripts/offline_acceptance.sh`** header re-scoped to "the **default** build, `connect` **OFF**" (it already builds `-p costroid` with default features = the local-only path); added a clearly-commented **STUB** for the feature-on network test (T8/T9: build `--features connect`, exercise an explicit `connect` action, assert outbound traffic only to the authorized host + **no secret written to disk/config/logs** + clean `disconnect`). The stub is a non-executed placeholder, so the script still PASSES.
- **No change to CI or README** (T7's code adds no user-facing behavior; SECURITY.md:54's "re-scoped … when connections land" stays accurate — the actual `connect` *action* arrives in T10/0.4.0).
- **Follow-up doc-currency audit (2026-06-07).** A full md-canon sweep (all 10 docs vs the golden rules / auth ladder / no-network / keychain / `--plain` / sequencing) then refreshed the docs for T7's as-built reality **and the v0.3.0 tag**: CLAUDE.md (workspace tree + crate bullet + dependency-direction now shows the apps→connect edge + build-status now "v0.3.0 tagged, T7 landed"), ARCHITECTURE §5 (costroid-connect moved out of "Planned crates" → exists as an off-by-default skeleton; dependency-direction + offline re-scope updated), RELEASING.md (connect is now a workspace member, unpublished; only `costroid-bar` is still absent), SECURITY.md (release line 0.2.x→0.3.x, version-agnostic signing note, `costroid-connect` added to the in-scope crates), §0 ground-truth (177 tests, the two-tier forbidden-crates test, "Not built yet" re-attributed to T8+), STATUSLINE-CAPTURE-BRIEF (offline test now two-tier; flagged the unbuilt `opus_weekly` design-intent), and **DESIGN-SYSTEM §now-mockup — fixed a canon violation: it drew Cursor with a confident `92%` amber meter, contradicting Cursor's detect-only/"unavailable" invariant; the amber+`!` near-limit illustration moved onto Claude's 5h (a real token-fraction meter) and Cursor now renders `unavailable — no sanctioned source`.** README/CHANGELOG/DATA-MODEL verified consistent, no change needed.

**✅ T6 DONE (2026-06-06, gate green, 176 tests) — render the 5 limit states + `Spend` windows. Completes the 0.3.0 milestone (T2 + T4 + T6). Files: `apps/cli/src/render.rs` (+ the authorized `captured_at` field on `LimitSummary` in `crates/costroid-core/src/lib.rs`) + 6 new snapshots.**
- **The 5 arms, measure-aware** — `render_limit_line` / `plain_limit_line` / `plain_limit_phrase` (statusline) now branch all five `LimitAvailability` arms (the T2 `LIMIT_RENDER_PENDING` placeholder is **removed**): `Available`/`Partial` read the `measure` (`TokenFraction` → meter as before; `Spend` → `"$used / $included used"`, or `"$used used"` when `included_usd` is `None` — **no meter, never a fabricated %**); `Unavailable` unchanged.
- **`Unverified` (the must-nail honest render)** — meter + `%` + the color-free cue **`" ? unverified"`** (new const `UNVERIFIED_CUE`) shown **instead of** the confident `!`/`!!` state cue, so a near-max reading renders `"96% ? unverified"`, never `"96% !!"`. **Decision: the meter is drawn NEUTRAL (`SemanticStyle::Strong`), not Warn/Critical** (helper `limit_meter_with_confidence`) — an unverified near-max must not draw as a confident red bar. Survives `--plain`/`NO_COLOR` (it's plain text).
- **`Estimated`** — **no meter**; `"usage <N> tokens (~$value, estimated) — quota % unavailable"`, or `"(estimated)"` alone when the model is unpriced (`estimated_usd: None`) — volume shown, **never a guessed price** (brief §6). Token count is thousands-grouped.
- **`captured_at` plumbing (the gap T4 flagged for T6 to close — §11.5 T4 notes).** Added `captured_at: DateTime<Utc>` to **`LimitSummary`** (the render layer only ever sees `LimitSummary`, never `LimitWindow`), set in `limit_summary` from the finalized window. The §12.6 "no new types" fence means *don't create T2's types* — threading an existing field is explicitly T6's per the T4 handoff, **not** scope creep (mirrors T3/T5's logged cross-crate touches). `LimitSummary` is internal, not export schema — no ⛔.
- **Always-on "as of HH:MM" freshness stamp (brief §8).** Shown on `Available`/`Unverified` (Codex included) once a reading is ≥ `LIMIT_FRESHNESS_STAMP_MINUTES = 10` old, gated against the summary's `generated_at` (threaded into the two now-screen renderers, mirroring the `reset_in_seconds` precedent). **Decision: formatted in UTC, not Local** — the test suite avoids env mutation (§11.5 T4), and a `Local` HH:MM would render differently per timezone (e.g. UTC+3 here vs. UTC in CI) and break snapshots; UTC is timezone-deterministic. `Partial`/`Estimated`/`Unavailable` carry **no** stamp (the brief's stale-`(stale)` refinement is deferred — `Estimated`'s `captured_at` can be the epoch sentinel for absent windows, so an unconditional stamp there would be bogus).
- **Claude chat caveat (brief §6/§8).** `"reflects Claude Code's view; claude.ai chat usage may make true usage higher."` rendered as an **indented sub-note line** under each Claude `Available`/`Unverified` **and `Estimated`** limit (decision: a continuation line, not inline — keeps the meter line readable and the caveat reachable; the compact statusline omits it per "compact presets may shorten"). `Estimated` is included because brief §6 requires the absent→estimate fallback to disclose the volume is Claude-Code-only and "excludes claude.ai chat" (surfaced by the adversarial review below).
- **Adversarial review (4 confirmed low findings, after a multi-lens verify pass).** Acted on: the Claude `Estimated` chat disclosure above (brief §6). **Deliberately deferred (documented, not silent):** (1) **statusline carries no "as of HH:MM" stamp** — kept compact; brief §8 names only the `? unverified` cue as the statusline's mandatory carry, and the fatal confident-wrong-number risk is already handled there by the cue + neutral meter. (2) **a stale aged-out `Estimated` carries no "(stale)" stamp** (brief §8 line 235) — the `Estimated` line's volume/$ are recomputed **fresh** at `generated_at` (only the discarded quota reading was stale), so there is no silently-old number to flag, and the always-on stamp is scoped to the **meter** arms; emitting a true `(stale)` variant would need a staleness flag on `LimitAvailability::Estimated`, which is a T2-type change the T6 fence forbids — leave for a future task if wanted. The "stamp should be **Local** not UTC" finding was **rejected**: Local would make snapshots timezone-dependent (UTC+3 here vs. UTC in CI) and the suite forbids env mutation — UTC is the correct deterministic choice.
- **Statusline `Unverified` selection (brief §8).** `limit_fraction` + `limit_fraction_and_reset` gained an `Unverified` arm so an unverified window **can** be the most-constrained pick — and when it is, the one-liner carries `" ? unverified"` + a neutral meter (never `"!!"`). Consequence handled: **`insight_line` flags `" (unverified)"`** when the most-constrained pick is unverified, so the now-screen insight never states an unverified reading as confident fact.
- **Tests.** `limits()` fixture gained `captured_at: now` (age 0 → no stamp → existing now/statusline snapshots **byte-identical**). New `all_arms_limits()` (8 windows: all 5 arms + both measures + a priced/unpriced `Estimated` + a Codex `Verified` window) → 4 `snapshot_now_all_arms_*` (braille+ansi / braille / ascii / plain) + 2 `snapshot_statusline_unverified_*`, plus behavior asserts (plain has no ANSI; `? unverified` not `!!`; stamp appears; caveat present; `$18.50 / $20.00 used`; Estimated volume+value+unavailable-%; Spend draws no braille meter; freshness threshold boundary). **No new dependency** (uses existing `chrono::Utc` + `rust_decimal::Decimal`); offline-acceptance + forbidden-crates still green.

**✅ T5 DONE (2026-06-06, gate green, 58 cli tests) — `setup-statusline` + `statusline --capture-only` (the cache writer). ⛔ human-gated CLI surface approved before finalizing.**
- **CLI surface (`apps/cli/src/main.rs`).** `Command::Statusline` refactored from a bare variant to `Statusline(StatuslineArgs { capture_only: bool, wrap: Option<String> })` (`--capture-only` `conflicts_with` `--wrap`); new `Command::SetupStatusline(SetupStatuslineArgs { undo: bool })`; `run_statusline` now takes `&StatuslineArgs`. New module `apps/cli/src/setup.rs` houses all of it.
- **`--capture-only` (the path-1 surface).** Reads stdin once → `build_cache_value` extracts **only** `.rate_limits.{five_hour,seven_day}.{used_percentage,resets_at}` (every other field — incl. secrets — dropped; values passed through verbatim, T4 sanitizes) → top-level `captured_at` = `Utc::now().to_rfc3339()` → **atomic write** (temp + rename) to the T4 cache path → **emits nothing, exits 0 always** (malformed/absent/no-rate_limits → writes nothing, still exit 0). Cache shape is the exact inverse of T4's `read_claude_rate_limits` (verified end-to-end: writer→reader round-trips).
- **Path-2 capture (decision, brief §2 path 2).** Plain `costroid statusline` (the string `setup-statusline` installs) now **opportunistically captures** when stdin is **piped** (`!std::io::stdin().is_terminal()`) then renders; interactive stdin (tmux / Starship) is never read, so it never blocks. This is what makes path 2 actually capture without a snippet.
- **`--wrap '<cmd>'` (decision: implemented fully, not stubbed — ⛔-approved).** The hazardous manual escape hatch (brief §2 path 3): read stdin once, tee a copy to the capture side-effect, run `sh -c '<cmd>'` on the identical bytes with stdout inherited; on spawn/exit failure print a blank line; **exit 0 always** (render-something-on-failure).
- **`setup-statusline` (idempotent, backup, undo).** Resolves a **single** config root = first **existing** of `HostEnv::claude_roots()` (so a set `CLAUDE_CONFIG_DIR` wins when it exists), printed before any write. **No existing root → stops with guidance** (lists the paths it checked, suggests running Claude Code once / setting `CLAUDE_CONFIG_DIR`), writes nothing (decision: never create config in a guessed location). Round-trips `settings.json` via `serde_json::Value` (preserves unknown keys; **malformed JSON → refuses to overwrite, errors out**; absent → fresh object). **Path 1** (existing `statusLine.command`) wraps it under the sentinel `# costroid:statusline-capture v1` (`input=$(cat); printf '%s' "$input" | costroid statusline --capture-only; printf '%s' "$input" | <ORIG>`); **path 2** (none) sets `statusLine = {type:"command", command:"costroid statusline"}`. **Idempotent:** re-run detects the sentinel or the `costroid statusline` string → no-op. **Backup** to `settings.json.costroid-bak` before the first write (only if the file existed and no backup exists yet). **`--undo`** restores the backup (then deletes it) or, for a fresh path-2 file with no backup, strips the wiring — **deleting the file entirely if that leaves it empty** (so undo of a we-created file returns to "no file", not `{}`).
- **Cross-crate change (deviation from the card's named files, logged):** `costroid-providers::claude_rate_limits_cache_path()` made **`pub`** so the writer and the T4 reader resolve the **same** path from one source (no drift); `serde_json` moved from a dev-dep to a regular dep of `apps/cli` (already in the workspace + tree — not a new/forbidden crate; offline-acceptance + forbidden-crates both still green). `std::io::IsTerminal` (stable, in-std) gates the opportunistic capture — no new dep.
- **Note for whoever runs the WSL safety:** `HostEnv::claude_roots()` scans `/mnt/c/Users/*/.claude` regardless of `HOME`, so `setup-statusline` on a WSL box **will** target a real Windows Claude config if one exists — that is correct behavior, but be aware when testing (scope tests with an explicit existing `CLAUDE_CONFIG_DIR`).
- **Fixture:** `fixtures/claude-code/statusline-stdin.json` — a raw Claude Code session object (rate_limits + extra/secret fields) to prove the writer keeps only the four allowed values. Tests cover: cache-shape + secret-drop, no-rate_limits/malformed/empty → no write, atomic write + exit-0-on-bad-input, path-1/path-2/idempotent transforms, backup+undo round-trip, malformed-settings refusal, fresh-file undo deletes.
- **For T6 (✅ now done — see the T6 DONE entry above):** with the writer live, the **flagship Claude live quota works end to end on a Pro/Max machine** — `setup-statusline` wires it, `--capture-only` captures, T4 reads/sanitizes/cross-checks, and **T6 renders** (the `Unverified`/`Estimated` arms, the "as of HH:MM" stamp via the now-closed `captured_at`→render-layer plumbing, the claude.ai caveat, the statusline `Unverified` selection arm + snapshots).

**✅ T4 DONE (2026-06-06, gate green, 154 tests) — Claude statusLine capture: cache read + sanitize + core cross-check. Defined no new types (built on T2's).**
- **Provider (`costroid-providers`).** `ClaudeCodeProvider::parse_limits` now reads the sanctioned cache and produces two windows (`five_hour`→`FiveHour`, `seven_day`→`Weekly`). New helpers: `claude_rate_limits_cache_path()` (resolves `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` — Linux-side only, the cache is Costroid's own state, no Windows-path handling), `read_claude_rate_limits(path: Option<&Path>)` (the pure seam — reads/parses or two `Unavailable`), `claude_limit_window(...)` (per-window sanitize), `parse_reset_stamp(...)` (epoch-then-RFC3339). **Sanitize order (ARCHITECTURE §9.2):** on the RAW `used_percentage` *before* ÷100 — out of `0..=100` (the 900% bug) **or** `== resets_at` (poisoned-epoch leak) → no measure, provisional `Unavailable`; else `Verified` `TokenFraction(pct/100)`. *(Range widened from the brief's bare `>100` to `!(0..=100)` — strictly safe-directional, only ever demotes; noted as a tiny defensive superset.)* `captured_at` is read from the cache's top-level field (RFC3339, epoch-sentinel fallback). Provider sets **only** the provisional `Verified`/`Unavailable` — it cannot see usage, so it never cross-checks.
- **Core (`costroid-core`).** New `window_token_volume(rows, tool, kind, now) -> TokenTotals` (pinned signature; sums FOCUS rows in the trailing window via `x_tool` + `charge_period_start`), companion `window_estimated_usd(...) -> Option<Decimal>` (sums priced rows' `effective_cost`; `None` if any contributing row is unpriced — volume shown alone, never a guessed price), and `window_duration(kind)`. The finalize pass lives in `limit_summary` (now takes `&[FocusRecord]`): `finalize_limit_status` demotes a **Claude-only** `Verified` reading to `Unverified` when `fraction ≥ HIGH_USAGE_FRACTION (0.80)` **and** window volume `< UNVERIFIED_TOKEN_FLOOR (5_000)` — Codex windows (sanctioned rollout logs) are never cross-checked. `limit_availability` gained two params (`volume`, `estimated_usd`) and now ages out a **stale** reading (`resets_at < generated_at`, any status) and a measure-less/`Unavailable` reading to `Estimated { volume_tokens, estimated_usd }` when volume > 0, else `Unavailable` — evaluated at render time so `--live` re-checks each tick. The old stale→`Partial` arm is gone; `Verified` + reset-unknown still → `Partial` ("reset time unknown").
- **Pinned constants confirmed (the §12 open item resolved):** `UNVERIFIED_TOKEN_FLOOR = 5_000` (biased low — only ever demotes, so it flags the implausible "near-max on almost no usage" and never a real heavy prompt; the live-install #31820 datapoint can tighten it later but the guard is built either way), `HIGH_USAGE_FRACTION = 0.80` (core-local mirror of render's `WARN_FRACTION` — core cannot import from `apps/cli`).
- **Testability seam (decision).** The cache is **global state, not in `DataLocation`**, and the `Provider::parse_limits` signature is fixed (no `HostEnv`). So production `parse_limits` resolves the env path, but **all tests route through `read_claude_rate_limits(path)`** with explicit fixture/None paths — no env mutation (race-free), and **no test ever reads a developer's real cache** (golden rule). Two existing tests that asserted "Claude always Unavailable" via `provider.parse_limits` (`claude_fixture_parses_usage_and_unavailable_limits`, `each_provider_emits_its_expected_window_shape`) were repointed to the pure seam. `unavailable_limit` **kept its 2-arg form** (per T2's note) — a sanitized-out window present in the cache is built inline so it can still carry the cache's `captured_at`.
- **Fixtures** under `fixtures/claude-code/`: `rate-limits-{happy,impossible-900,poisoned-epoch,false-100,absent,stale,iso-resets}.json` (valid JSON, synthetic, no secrets). `absent` = present file missing the `five_hour` key; the false-100 cross-check is proven at the **core** layer with synthetic trivial-volume rows (the provider can't see volume). Offline-acceptance still green — no new deps (`std::fs` + `serde_json` only).
- **For T6 (✅ now done):** Claude windows carry a real `captured_at` + finalized `status`, and the now-screen produces `Available`/`Unverified`/`Estimated`/`Partial`/`Unavailable` from real data. T6 shipped the real rendering of `Unverified` (`? unverified` cue, neutral meter), `Estimated` (volume + `~$value`, no meter), the always-on "as of HH:MM" stamp, the claude.ai caveat, and the statusline `limit_fraction` `Unverified` arm + snapshots — the `LIMIT_RENDER_PENDING` placeholder is removed.
  - **✅ Plumbing gap CLOSED in T6 (was: surfaced by T4):** `captured_at` is now carried by `LimitSummary` (added in T6, set in `limit_summary` from the finalized window) — the render layer consumes `LimitSummary.availability` + `LimitSummary.captured_at`, never `LimitWindow`. The "as of HH:MM" stamp (brief §8) now has its source. Internal struct, not export schema (no ⛔). As predicted, T4's scope fence forbade new types so T4 deliberately deferred this to T6; the finalize already overwrote `captured_at` per the cache, so only the wiring was missing — and T6 added it.

**✅ T3 DONE (gate green) — Capability descriptor + one out-of-named-file compile-fix.**
- **Types landed exactly as the card specs them**, placed just before the `Provider` trait in `costroid-providers/src/lib.rs`: `enum DataSource { LocalArtifact, SanctionedHook, SanctionedOauth, ApiKey, Unavailable }`, `enum AuthMethod { None, Oauth, ApiKey }` (both `#[serde(rename_all = "snake_case")]` + `Copy` — matching `AccessPath`; the snake_case wire form matches §2b's listed values, e.g. `local_artifact`/`api_key`), and `struct Capability { api_cost, subscription_quota, model_mix: DataSource, auth: AuthMethod, quota_kinds: &'static [LimitKind] }`. *(An `OptInSession` variant originally landed in both enums; the ToS-safe rework removed it — there is no session-reuse tier. See the ToS-safe-rework entry at the top of §11.5 and §5 tier 4.)*
- **`Capability` is `Copy + PartialEq + Eq` but NOT `Serialize`/`Deserialize`** — only the two enums got serde (per the card). `Deserialize` is impossible anyway: `quota_kinds: &'static [LimitKind]` is a borrowed static slice. When the Providers tab (T11) needs to serialize a descriptor it can derive `Serialize` (slices serialize fine) or project to an owned shape; deferred — no producer/consumer yet.
- **`capability()` is a REQUIRED trait method (no default).** Rationale: §2b wants each provider to *declare* its shape; a default would let a future adapter (Copilot/Antigravity) silently inherit a descriptor instead of declaring one. Consequence: the method forced a one-method compile-fix on the `FakeProvider` test double in `costroid-core` (`crates/costroid-core/src/lib.rs`, the `mod tests` import + the impl) — **one file outside the card's named providers file**, but a forced mechanical migration exactly like T2's "migrate every test," not scope creep. `FakeProvider` declares the honest conservative descriptor (all `Unavailable`, `auth: None`, empty `quota_kinds`); no test reads it. Card §12.3 Files line annotated to reflect this.
- **Tests:** providers `each_provider_declares_its_capability` pins all three descriptors (the Done-when). Full gate green — **139 tests** (was 138; +1), incl. the offline forbidden-crates acceptance test (no new crates introduced).

**T1 — Release v0.2.0 (2026-06-05): version-bump mechanics + dist host limitation.**
- **Internal dep version constraints bump in lockstep.** All four crates inherit `version.workspace = true`, so bumping `[workspace.package].version` to 0.2.0 makes every package 0.2.0 — which makes the `[workspace.dependencies]` internal constraints `costroid-core/-focus/-providers = { …, version = "0.1.0" }` (a `^0.1.0` req) **unsatisfiable** (`cargo build`/`update` fails to resolve). T1 bumped those three `version =` fields to `"0.2.0"` alongside the package version; this is part of "bump the version," **not** a code change. **Every future X.Y.Z bump must do the same** (§12.1 deliverables updated to say so).
- **`dist build --artifacts=local` is host-scoped on a non-macOS box.** Run unscoped on Linux it tries all six target triples and cargo-dist refuses to cross-compile to macOS ("a road paved with sadness"). For a local dry-run, scope to the host triple: `dist build --artifacts=local --target x86_64-unknown-linux-gnu` — builds + archives + checksums the host artifact cleanly. The real multi-target build happens per-runner in release CI. (`dist plan` is unaffected — it cleanly lists v0.2.0 across all 6 targets + 4 installers.) RELEASING.md §3 lists the unscoped command for reference; left as-is (out of T1's edit scope) — maintainer note: scope it or rely on CI.
- **CHANGELOG.md created** at repo root (the §11.4-grounding "no CHANGELOG.md yet → T1 creates it" item is now satisfied); cargo-dist auto-bundles it into every release archive + the npm package. **Verified:** full gate green; `dist plan` lists v0.2.0 across 6 targets; host `dist build` produced a working `costroid 0.2.0` binary.
- **Tagging gotcha (hit live during the ⛔ handoff).** The agent does **not** commit (card rule), so the prep sat uncommitted; tagging then put `v0.2.0` on the prior `0.1.0`-manifest commit → cargo-dist's tag==version check aborts the release CI. Lesson now baked into §12.1's ⛔ step: **commit the prep before tagging**, push `main` first, then the tag. Also: the tag triggers only the GitHub-Release/installers; **crates.io is a separate manual `cargo publish` ladder** (RELEASING.md) — `cargo install costroid` keeps serving the old version until that runs.
- **✅ RELEASED 2026-06-05 — T1 complete end to end.** Recovery worked: deleted the bad tag, committed the prep as `a2c9d11`, re-pushed `main` + `v0.2.0` (now tag==manifest, cargo-dist `plan` job green). The first corrected Release run was *manually cancelled* mid-build (no GitHub Release was created); a clean re-push of the tag then ran green in 4m31s. Live on **every** channel — GitHub Release (6 targets + checksums + attestations), Homebrew tap, npm (`0.2.0`), and the crates.io ladder (focus→providers→core→cli, all `0.2.0`); `cargo install costroid` → `costroid 0.2.0` verified. **The "tag v0.2.0 before T2+ reaches `main`" sequencing caveat is now moot** — the tag exists, so T2+ build work may merge to `main` freely.
- **README version mentions reconciled to v0.2.0 (full sweep).** Beyond the literal "Status §": the Roadmap frontier bullet ("built; lands next release"), the "Shipping today (v0.1.0):" feature-list header (→ v0.2.0, with a `frontier` bullet added), and the packaged-installers "v0.1.0 is published" note were all flipped to v0.2.0 so the release ships a self-consistent README (DoD: docs consistent). The Claude-quota "next release" claims were **left** — they're genuinely still next-release (T2–T6 / 0.3.0). *(Status-section + Roadmap done in the initial T1 prep; the feature-list header + installer note reconciled in a follow-up at the human's request.)*

**D1 — Type / behavior / render split (T2 ↔ T4 ↔ T6).** Keeps the three tasks non-overlapping so fresh agents don't collide on the same types:
- **T2 owns all TYPES + the pure map + migration** — `LimitKind`(+Daily/Monthly/BillingCycle), `LimitMeasure { TokenFraction(f64), Spend { used_usd, included_usd } }`, `LimitStatus { Verified, Unverified, Unavailable }`; `LimitWindow` gains `captured_at` + `status` and swaps `used_fraction`→`measure`. **`LimitAvailability` is reshaped so its arms carry the `LimitMeasure`, not a bare `f64`** — this is the hinge that lets T6 render a `Spend` window *without ever touching a type* (the render layer consumes `LimitSummary.availability`, never `LimitWindow`, so dollars must live in the availability arm). Target shape — **still 5 variants**: `Available { measure: LimitMeasure, resets_at, reset_in_seconds }`, `Partial { measure: Option<LimitMeasure>, resets_at, reset_in_seconds, reason }`, **new** `Unverified { measure: LimitMeasure, resets_at: Option<DateTime<Utc>>, reset_in_seconds: Option<i64> }`, **new** `Estimated { volume_tokens: u64, estimated_usd: Option<Decimal> }`, `Unavailable { reason }` (unchanged). `limit_availability()` becomes a **pure map** (status + measure + staleness → arm). Reshaping the enum makes the existing `render_limit_line`/`plain_limit_line` `match`es non-exhaustive, so **T2 also adds minimal placeholder render arms (a basic line — no `todo!()`/panic) purely to keep `cargo build`/`test` green; the real measure-aware rendering, the `Spend` formatting, and the snapshots are T6's.** Add `rust_decimal.workspace = true` to `costroid-providers/Cargo.toml` (the `Spend` measure needs `Decimal`; it is already a vetted permissive workspace dep that core uses — **not** a new-dependency "stop and ask"). Migrate Codex=`Verified` `TokenFraction`, Claude=`Unavailable`, Cursor=empty, all constructors + tests.
- **T4 owns BEHAVIOR only (defines no new types)** — Claude `parse_limits` (cache read + sanitize + provisional status); the core cross-check finalize (`window_token_volume` + demote `Verified`→`Unverified` on high-%-trivial-volume; stale age-out; estimate fallback); fixtures.
- **T6 owns RENDER only** — the 5 availability arms + `Spend` windows + the "as of HH:MM" stamp + the claude.ai caveat + snapshots.

**✅ T2 DONE (gate green) — notes for T4/T6 so they aren't surprised:**
- Types landed exactly as D1 specs them. `LimitWindow.captured_at` is **non-`Option`**: the `Unavailable` case (Claude's two windows + Codex's no-data fallback) uses a **UNIX-epoch sentinel** via `epoch_utc()` (`Utc.timestamp_nanos(0)`, infallible — no `unwrap`/`panic` in library code). The pure map ignores `captured_at` for the `Unavailable` arm, so the sentinel never surfaces. **T4** should overwrite `captured_at` with the real cache time when it wires Claude.
- **Codex** `captured_at` = the rollout line's `timestamp` (else `payload.timestamp`, else the epoch sentinel), extracted by the new `codex_entry_timestamp()`; `choose_limit` keeps the latest data-bearing entry, so the surviving window's freshness is that line's. Status is always `Verified`.
- **Refinement of the card's mapping (no behavior regression):** "Verified + measure + not-stale → Available" still **requires a present, non-stale `resets_at`**; a Verified measure with *no* reset maps to `Partial` (preserves the pre-T2 behavior — a window with no reset can't show a countdown). A measure-less window maps to `Unavailable` even if its status is `Verified` (honest: no number to show). The `LimitStatus::Unavailable` arm of the inner match is unreachable (handled by an early return) but written as a real non-panicking arm, not `unreachable!()`.
- **Placeholder rendering was intentionally minimal (✅ since replaced by T6 — see the T6 DONE entry above):** in T2, `Spend` measures and the new `Unverified`/`Estimated` availability arms all rendered the constant `"limit detail pending"` (`LIMIT_RENDER_PENDING` in render.rs) across the styled / `--plain` / statusline surfaces — ASCII, no color, no color-only cue. `TokenFraction` `Available`/`Partial` rendering is **unchanged** (existing snapshots still pass). The new `LimitKind` labels are placeholders too: `Daily`→`1d`, `Monthly`→`mo`, `BillingCycle`→`cyc`. The new arms have **no producer in T2**, so this text never reaches a user yet. `limit_fraction`/`limit_fraction_and_reset` treat `Unverified`/`Estimated` as "no fraction" (they don't feed the statusline "most constrained" pick until T6 decides).
- **Tests:** providers `each_provider_emits_its_expected_window_shape` pins each provider's window shape (the Done-when); core `limit_availability_maps_status_and_measure` pins the status+measure map incl. the `Unverified` arm, the no-measure→`Unavailable` rule, and `Spend` routing. Full gate green (138 tests, incl. the offline forbidden-crates acceptance test — `rust_decimal` is not a forbidden crate).

**Pinned defaults** (accept or override before the task): `UNVERIFIED_TOKEN_FLOOR = 5_000` · cache path `${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json` · `LIMIT_FRESHNESS_STAMP_MINUTES = 10` · setup sentinel `# costroid:statusline-capture v1`.

**Repo facts confirmed by grounding** (so an agent isn't surprised): no `CHANGELOG.md` exists yet → T1 creates it at root · `Statusline` is currently a **bare** `Command` variant with no args → T5 refactors it to `Statusline(StatuslineArgs)` · `LimitAvailability` today has exactly 3 variants (`Available`/`Partial`/`Unavailable`), each token-fraction-shaped (`used_fraction: f64`) → T2 reshapes them to carry `LimitMeasure` and adds the 2 new (`Unverified`/`Estimated`) · the render layer consumes `LimitSummary.availability` (built by `limit_summary` ~L649), **never `LimitWindow` directly** → a `Spend` window's dollars must live in the availability arm or T6 can't render them · `LimitAvailability`/`LimitSummary` are **not** emitted as any user-facing JSON/export output today (only the FOCUS cost rows are) → reshaping them is an internal change, **no export-schema ⛔ gate** · `rust_decimal` (`=1.42.0`, `serde`) is a workspace dep used by core but **not yet in costroid-providers** → T2 adds `rust_decimal.workspace = true` there · `deny.toml` bans openssl/native-tls globally and `apps/cli/tests/offline.rs` forbids ureq/rustls/keyring globally → T7 re-scopes ureq/rustls/keyring to `costroid-connect` only.

---

## 12. Ready-to-paste task prompts (T1–T8, T9a–T9c, T10a/T10c + T10b release + T10-LIVE-ROWS · T11–T17 Step 5 tabs+alerts)

*To run a task: paste **§12.0 (the header)** then that task's **body block**, into a fresh ultracode-xhigh agent. Resolve any 📌 (defaults in §11.5) first. Backlog tasks (T9+) use **§12.8**. §12 is the source of truth for task content — agents edit it (and §11.5) as they learn; those edits are tracked in `docs/` and commit with the task.*

### 12.0 — Standard header (prepend to every body)

```
You are a fresh agent implementing ONE task in the Costroid repo (/home/eren/costroid).

READ FIRST: CLAUDE.md (golden rules + decide-vs-ask), then docs/PRODUCT-PLAN.md §3 (build
steps/sequencing), §6 (hard invariants), §11.5 (decisions log — what ACTUALLY shipped; it often
supersedes older text elsewhere).

DOC MAP + CANON ORDER: apply CLAUDE.md's "Doc map & canon order" section (the single source — read
it). In short: read the ONE companion doc that owns this task's area (ARCHITECTURE = technical
canon, DATA-MODEL = data shapes, DESIGN-SYSTEM = rendering) + the task's own Spec named in its card;
don't skim every doc. For anything already built the CODE on disk is canon — when a doc disagrees
with the code, the code WINS; verify any type/field/path/flag/function in the code before relying on
it (never invent a symbol); fix any drift you find as part of KEEP THE PLAN CURRENT.

docs/ is tracked in the repo — read it on disk; edit the plan there as needed (your edits commit with the task).

Do ONLY the task below — nothing else:
[[ paste the task body block here ]]

Rules:
- Stay inside the Scope fence: no next-task work, no unrelated refactors.
- Library crates: no unwrap/expect/panic; new deps must be permissive (MIT/Apache-2.0/BSD/
  ISC/Zlib/Unicode), never copyleft; rustls not openssl.
- Any new visual needs a --plain path and must never rely on color alone.
- Finish GREEN — run and pass all four, iterating until they do:
    cargo fmt --all -- --check
    cargo clippy --workspace --all-targets -- -D warnings
    cargo build --workspace
    cargo test --workspace
  Add tests against fixtures (never real user data) that prove the Done-when.
- If a decision isn't pinned in the card, STOP and ask the human — do not guess.
- ⛔ markers = stop for human approval before finalizing.
- KEEP THE PLAN CURRENT: if you deviate from the card, affect a later task, or hit a new
  decision/limitation, EDIT the affected prompt in docs/PRODUCT-PLAN.md §12 and log it in
  §11.5 (edit on disk; the change commits with the task).
- Do NOT commit — but DO keep the plan current on disk so the next fresh agent stays on track:
  tick this task's box in §11.4 Progress, plus any §12/§11.5 edits the rules above call for.
- End by reporting: the gate output, a ≤5-line summary, the PRODUCT-PLAN.md edits you made
  (incl. the ticked box), and confirmation the card's "Next" prerequisite now holds.
```

### 12.1 — T1 · Release v0.2.0 · ⛔ · S · Prereq: none

```
**Goal:** ship the already-built cost lane (frontier, Cursor-detect, WSL fix) as v0.2.0.
**Files:** Cargo.toml ([workspace.package].version, currently 0.1.0); Cargo.lock (refresh);
  README.md (Status §); create CHANGELOG.md at repo root (none exists). RELEASING.md = runbook ref.
**Scope fence:** version bump + lockfile refresh + CHANGELOG + README status wording only. NO
  code changes in apps/ or crates/; NO edits to .github/ or dist-workspace.toml. Do NOT tag/push/publish.
**Deliverables:** bump version 0.1.0→0.2.0 — `[workspace.package].version` **and** the three
  `[workspace.dependencies]` internal constraints (`costroid-core/-focus/-providers = { …, version }`),
  which bump in lockstep because the crates inherit `version.workspace = true` (a stale `^0.1.0` req
  won't resolve against 0.2.0); `cargo update --workspace` to refresh Cargo.lock;
  create CHANGELOG.md with a 0.2.0 entry (frontier view; Cursor detect-and-defer; WSL Windows-root
  auto-detect); in README Status change "frontier … lands in the next release" → "shipped in v0.2.0"
  (also flip the Roadmap section's matching frontier "lands next release" claim, for consistency);
  run `dist plan` and `dist build --artifacts=local` (dry-run, report only — no publish; on a non-macOS
  host scope it to the host triple, e.g. `--target x86_64-unknown-linux-gnu`, since cargo-dist refuses
  to cross-compile to macOS — CI builds each target natively).
**Done when:** gate green; `dist plan` lists 0.2.0 across the 6 targets cleanly; version + lockfile
  bumped; CHANGELOG + README updated; tree otherwise clean.
**⛔ You (human) then:** review, **then COMMIT the prep first** (the agent does NOT commit) so the
  tag lands on a commit whose manifest says `0.2.0` — cargo-dist hard-requires the pushed tag to
  equal `[workspace.package].version`, so tagging a still-`0.1.0` commit makes the release CI abort.
  Then `git push origin main && git tag v0.2.0 && git push origin v0.2.0` (triggers the GitHub-Release
  CI: installers only). **The tag does NOT publish to crates.io** — run the `cargo publish` ladder in
  RELEASING.md separately (focus → providers → core → cli), or `cargo install costroid` keeps serving
  the old version. Verify `cargo install costroid` only *after* that crates.io publish.
**Sequencing caveat:** 0.2.0 = "ship only what's built." Development of T2+ can run in parallel, but
  **tag v0.2.0 from a commit that does NOT yet contain T2+ build work** — either finish + tag T1 before
  merging T2+ to `main`, or keep T2–T6 on a branch until after the tag. Otherwise the 0.2.0 release
  ships half-finished 0.3.0 quota generalization.
**Next:** independent — blocks nothing (modulo the tag-point caveat above).
```

### 12.2 — T2 · Quota data-model foundation · M · Prereq: none — *the lynchpin; do first* · ✅ **DONE (gate green, 138 tests — see §11.5)**

```
**Goal:** generalize the quota types so every later provider/feature fits ONE shape (§2a). Pure
  structural change + migration — no behavior, no rendering.
**Files:** crates/costroid-providers/src/lib.rs (LimitKind ~L150, LimitWindow ~L156, the 3
  parse_limits at ~L246/L280/L324, helpers unavailable_limit ~L930/limit_has_data ~L338);
  crates/costroid-providers/Cargo.toml (add the Decimal dep — see below);
  crates/costroid-core/src/lib.rs (LimitAvailability ~L1009, limit_availability ~L659,
  limit_summary ~L649); apps/cli/src/render.rs (compile-fix placeholder arms ONLY — see scope fence).
  (Line numbers are approximate anchors — grep by symbol; they shift as you edit.)
**Scope fence:** TYPES + the pure availability map + migration ONLY. NO cache/capture (T4), NO
  cross-check (T4), NO new providers, **NO RequestCount**. Rendering belongs to T6 — but because
  reshaping LimitAvailability makes render's `match`es non-exhaustive, you MUST add **minimal
  placeholder render arms** (a basic line, never `todo!()`/panic) so `cargo build`/`test` stay green;
  do NOT do the real Spend/Unverified/Estimated formatting or snapshots (that is T6).
**Deliverables — providers:** add `rust_decimal.workspace = true` to costroid-providers/Cargo.toml
  (the `Spend` measure needs `Decimal`; it is already a permissive workspace dep core uses — add it,
  do NOT treat it as a new-dependency stop-and-ask). `enum LimitKind { FiveHour, Weekly, Daily,
  Monthly, BillingCycle }`; `enum LimitMeasure { TokenFraction(f64), Spend { used_usd: Decimal,
  included_usd: Option<Decimal> } }`; `enum LimitStatus { Verified, Unverified, Unavailable }`; on
  `LimitWindow` add `captured_at: DateTime<Utc>` + `status: LimitStatus`, replace
  `used_fraction: Option<f64>` with `measure: Option<LimitMeasure>`; update
  `unavailable_limit`/`limit_has_data`; migrate the 3 parse_limits (Codex → `TokenFraction` +
  `status: Verified` + `captured_at` from the latest rollout entry; Claude → `Unavailable`;
  Cursor → empty).
**Deliverables — core:** **reshape `LimitAvailability` so its arms carry the measure, not a bare
  `f64`** (this is what lets T6 render Spend without touching a type — see §11.5 D1). 5 variants:
  `Available { measure: LimitMeasure, resets_at, reset_in_seconds }`, `Partial { measure:
  Option<LimitMeasure>, resets_at, reset_in_seconds, reason }`, **new** `Unverified { measure:
  LimitMeasure, resets_at: Option<DateTime<Utc>>, reset_in_seconds: Option<i64> }`, **new**
  `Estimated { volume_tokens: u64, estimated_usd: Option<Decimal> }`, `Unavailable { reason }`
  (unchanged). Make `limit_availability(&LimitWindow, generated_at) -> LimitAvailability` a PURE map
  (Verified + measure + not-stale → Available; status Unverified → Unverified; missing/stale →
  Partial; status Unavailable / no measure → Unavailable; the Estimated arm exists, wired later by
  T4/T6). Update `limit_summary` and migrate every reader of `used_fraction` to `measure`. Add the
  minimal placeholder render arms in apps/cli/src/render.rs needed to keep the build green (T6 does
  the real rendering).
**Done when:** gate green; all existing limit tests migrated and passing; a test asserts each
  provider's window shape.
**Next:** the new types exist → T3, T4, T6 build on `LimitKind`/`LimitMeasure`/`LimitStatus`/the 5
  `LimitAvailability` variants.
```

### 12.3 — T3 · Capability descriptor · S · Prereq: T2 · ✅ **DONE (gate green, 139 tests — see §11.5)**

```
**Goal:** make each provider DECLARE its data sources / auth / quota shape (§2b) so unavailability
  renders honestly and future adapters slot in by descriptor.
**Files:** crates/costroid-providers/src/lib.rs (Provider trait ~L181; the 3 adapter structs).
  NOTE (T3 done): because `capability()` is a REQUIRED trait method, the `FakeProvider` test double in
  crates/costroid-core/src/lib.rs (mod tests) also needed a one-method impl + import — a forced
  compile-fix, not scope creep (see §11.5 ✅ T3 DONE).
**Scope fence:** the enums + struct + trait method + 3 impls + 1 test ONLY. No rendering (Providers
  tab is T11). Do NOT modify LimitWindow/LimitKind/LimitStatus (T2 owns those) — only reference them.
**Deliverables:** `enum DataSource { LocalArtifact, SanctionedHook, SanctionedOauth, ApiKey,
  Unavailable }`; `enum AuthMethod { None, Oauth, ApiKey }` (both
  Serialize/Deserialize for consistency with ProviderId/AccessPath); `struct Capability { api_cost:
  DataSource, subscription_quota: DataSource, model_mix: DataSource, auth: AuthMethod, quota_kinds:
  &'static [LimitKind] }`; `fn capability(&self) -> Capability` on the Provider trait. Impls:
  Claude { api_cost: LocalArtifact, subscription_quota: SanctionedHook, model_mix: LocalArtifact,
  auth: None, quota_kinds: [FiveHour, Weekly] }; Codex { all LocalArtifact, auth: None, [FiveHour,
  Weekly] }; Cursor { api_cost/subscription_quota: Unavailable, model_mix: LocalArtifact, auth:
  None, quota_kinds: [] }.
**Done when:** gate green; a test asserts each provider's capability() values.
**Next:** the Providers tab (T11) + deferred adapters (Copilot/Antigravity) rely on Capability.
```

### 12.4 — T4 · Claude statusLine capture: cache + cross-check · L · Prereq: T2 · 📌 · ✅ **DONE (2026-06-06, gate green, 154 tests — see §11.5)**

```
**Spec:** docs/STATUSLINE-CAPTURE-BRIEF.md — read it fully; it IS the design (§4a provider / §4b
  core / §5 cross-check / §9 fixtures). **Boundary:** T2 already DEFINED the types — you define NO
  new types; you POPULATE Claude's windows and ADD the cross-check behavior.
**Files:** crates/costroid-providers/src/lib.rs (Claude parse_limits ~L246, epoch_seconds ~L947);
  crates/costroid-core/src/lib.rs (the finalize/cross-check; TokenTotals ~L894); fixtures/claude-code/.
**📌 Pin before start:** UNVERIFIED_TOKEN_FLOOR = 5_000 · cache path
  ${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json.
**Goal:** read the sanctioned rate_limits cache → sanitize → provisional status; then a core
  cross-check that demotes a high-but-untrustworthy reading to Unverified; estimate fallback.
**Scope fence:** Claude parse_limits + the core cross-check finalize + fixtures ONLY. NOT
  setup-statusline (T5), NOT rendering (T6), NO new types (T2), NO network/keychain.
**Deliverables:** (provider) read cache JSON (five_hour/seven_day → FiveHour/Weekly); sanitize raw
  used_percentage BEFORE ÷100 (`>100` or `== resets_at` → no data → status Unavailable); parse
  resets_at as epoch (reuse epoch_seconds) OR RFC3339; set captured_at from cache; provisional status
  Verified if an in-range value survived, else Unavailable; absent/unreadable cache → two Unavailable
  windows. (core) `window_token_volume(rows, tool, kind, now) -> TokenTotals` summing FOCUS rows in the
  trailing window; a finalize pass that demotes a Verified Claude window to Unverified when its % is
  high but window volume < UNVERIFIED_TOKEN_FLOOR, ages out stale windows (resets_at < generated_at),
  and sets the Estimated path when only volume is known. (fixtures) poisoned-epoch, impossible-900,
  false-100 (+ trivial transcript), absent, stale, iso-resets, happy.
**Done when:** gate green; fixtures prove every degrade path (never a confident wrong number);
  offline-acceptance still passes (no new network deps).
**Next:** Claude windows carry real captured_at + status → T6 renders them; 0.3.0 needs T6.
```

### 12.5 — T5 · `setup-statusline` + `--capture-only` · M · ⛔ (public CLI surface) · Prereq: T4 · 📌 · ✅ **DONE (2026-06-06, gate green, ⛔-approved — see §11.5)**

```
**Spec:** docs/STATUSLINE-CAPTURE-BRIEF.md (the setup-statusline section).
**Files:** apps/cli/src/main.rs (Command enum ~L26 — currently 4 variants; Statusline is BARE with
  no args); new apps/cli/src/setup.rs.
**📌 Pin:** idempotency sentinel = `# costroid:statusline-capture v1`.
**Goal:** `costroid setup-statusline` wires Claude Code's settings.json to tee rate_limits into the
  cache (snippet-into-existing, or become-the-statusline) + a `statusline --capture-only` flag.
**Scope fence:** the command + flag + idempotent settings.json editing (backup/undo) ONLY. Do NOT
  implement cache read/parse (T4 owns it) or rendering.
**Deliverables:** add `Command::SetupStatusline(SetupStatuslineArgs { undo: bool })`; refactor the
  bare `Statusline` variant to `Statusline(StatuslineArgs { capture_only: bool, wrap: Option<String> })`
  and update dispatch + run_statusline signature; create setup.rs: resolve a single Claude config root
  (HostEnv::claude_roots()), read settings.json, inject the capture snippet under the sentinel (or set
  statusLine to "costroid statusline" if none), back up to settings.json.costroid-bak, `--undo`
  restores; idempotent (detect sentinel, skip on re-run); malformed/absent settings.json handled (no
  panic). `statusline --capture-only`: read stdin once, extract .rate_limits, write cache atomically
  (temp+rename), emit nothing, **exit 0 always** (even on bad input).
**⛔ Human gate:** new public CLI surface — stop for approval on the final flags/UX before finalizing.
**Done when:** gate green; idempotent re-run is a no-op; bad/absent settings.json handled; capture
  parse-failure exits 0 with no write (test); offline-acceptance still passes.
**Next:** end-to-end Claude live quota works on a Pro/Max machine.
```

### 12.6 — T6 · Render new limit states + Spend windows · M · Prereq: T2 (+T4 for live data) · ✅ **DONE (2026-06-06, gate green, 176 tests — see §11.5)**

```
**Goal:** render the 5 LimitAvailability arms + Spend windows (dollar pool used/included, NEVER a
  fabricated %), with freshness + the claude.ai caveat. (Brief §6/§8.)
**Files:** apps/cli/src/render.rs (render_limit_line ~L1065, plain_limit_line ~L1120, state_cue
  ~L1252, plain_state_phrase ~L1261, meter helpers limit_meter_span ~L1275 / positional_meter_text
  ~L1302); apps/cli/src/snapshots/.
**Scope fence:** rendering + snapshots ONLY. Assume T2 already reshaped the 5 LimitAvailability arms
  to carry `LimitMeasure` and left **minimal placeholder render arms** to keep the build green — your
  job is to **replace those placeholders** with the real rendering. Read the fraction out of
  `measure` (TokenFraction) and the dollar pool out of `measure` (Spend); never read a removed
  `used_fraction`. Do NOT add data-model types (T2) or capture logic (T4).
**Deliverables:** branch render_limit_line + plain_limit_line on all 5 arms — Available (now reads
  `measure`: TokenFraction → meter as before, Spend → dollar line), Partial (same, measure-aware),
  Unavailable (unchanged), **Unverified** (meter + % + a non-color cue like
  " ? unverified" that survives --plain/NO_COLOR), **Estimated** (no meter; show window token volume +
  estimated $ value, labeled unavailable-%). For TokenFraction reuse the meter primitives; for Spend
  render "<tool> <kind>: $<used> / $<included> used" (no meter). Always-on "as of HH:MM" stamp from
  captured_at (LIMIT_FRESHNESS_STAMP_MINUTES = 10); Codex gets the same stamp. Claude Available/Unverified
  carry the caveat: "reflects Claude Code's view; claude.ai chat usage may make true usage higher."
  Update limit_fraction() so Unverified windows can be selected as most-constrained in the statusline
  (carry the cue). Snapshot tests for all 5 arms × braille/ASCII/plain; plain asserts no ANSI.
**Done when:** gate green; snapshots cover all 5 arms in 3 modes; plain has no ANSI; Spend formats
  without a meter; the stamp appears.
**Next:** the **0.3.0 milestone** (Claude live quota + generalized model) is complete (T2+T4+T6 green).
```

### 12.7 — T7 · `costroid-connect` infra + CI re-scope · L · ⛔ · Prereq: T3 · ✅ **DONE (2026-06-06, gate green, 177 tests, ⛔-approved — see §11.5)**

> **As-built deviations (the card prompt below predates them):** the `connect` feature lives on **`apps/cli`**, not the root `Cargo.toml` (a virtual workspace has no `[package]`, so no `[features]`) — `app → costroid-connect → core`, per ARCHITECTURE §5 / RELEASING.md; `offline.rs` walks the **resolved** dependency graph, not the `packages` superset (the only way to tell the default build from the `connect` build); `deny.toml` uses `wrappers` (benign `unused-wrapper` warnings until T9 — 3 at T7, **2 since T8** wired keyring's, check still exits 0). Full detail in §11.5 ✅ T7 DONE.

```
**Goal:** create the feature-gated network/credential crate with NO behavior yet, and re-scope the
  no-network guarantees so the default build still PROVES zero network.
**Files:** new crates/costroid-connect/ (Cargo.toml + src/lib.rs skeleton); root Cargo.toml (member +
  `connect` feature); deny.toml; apps/cli/tests/offline.rs (FORBIDDEN_CRATES); scripts/offline_acceptance.sh.
**Scope fence:** crate skeleton + feature gate + test re-scoping ONLY. **NO keychain, NO HTTP** (T8/T9).
  No new CLI commands, no source changes to core/providers/cli beyond the test.
**⛔ Human gate:** changing offline-acceptance + forbidden-crates redefines a safety guarantee — stop
  for approval on the re-scoped assertions before finalizing.
**Deliverables:** crates/costroid-connect/ (empty lib, documented as the future keychain/HTTP home);
  root Cargo.toml adds the member + `[features] connect = []` (off by default); deny.toml scopes
  ureq/rustls/keyring as allowed only when costroid-connect is in the tree (per cargo-deny syntax);
  offline.rs splits into (1) the default-build check that still forbids ureq/rustls/keyring, (2) a
  feature-aware check; scripts/offline_acceptance.sh tests the default (feature-off) build offline and
  adds a clearly-commented STUB section for the feature-on network test (T8/T9 fill it).
**Done when:** `cargo build/test --workspace` green with feature OFF (default); `--features connect`
  builds; offline.rs default-build test passes; `bash scripts/offline_acceptance.sh` passes;
  `cargo deny check licenses bans` passes.
**Next:** T8 (keychain) and T9 (HTTP clients) have a home → 0.4.0 connections can proceed.
```

### 12.8 — Backlog tasks (T9+): the pin-then-card prompt

*T11–T18 aren't carded — they have open 📌 that must be pinned first. (**T10 has fully run this prompt** — T10a §12.14, T10c §12.15, T10-LIVE-ROWS §12.16, T10b §12.10, all carded + none built; pins in `docs/proposals/T10-PIN-PROPOSAL.md` + §11.5 📌 T10.) Paste §12.0 + this body, with `<ID>` filled, to turn a backlog task into a real card (don't build it yet):*

> **T9 status — this prompt has fully RUN for T9; do not re-run it.** The T9 pins were proposed + ⛔-signed-off 2026-06-10 (`docs/proposals/T9-PIN-PROPOSAL.md`, logged in §11.5), T9a is built (§12.11 ✅), and T9b/T9c are built (§12.12/§12.13 ✅ DONE 2026-06-13 — T9 complete).
> **T10 status — this prompt has fully RUN for T10; do not re-run it.** The T10 pins were proposed + ⛔-signed-off 2026-06-13 (`docs/proposals/T10-PIN-PROPOSAL.md`, logged in §11.5 📌 T10), and T10a/T10c/T10-LIVE-ROWS are carded (§12.14/§12.15/§12.16; T10b release at §12.10) — **none built yet**; build each in a fresh agent per §12.0 when its Prereq holds.
> **Step 5 status — this prompt has RUN for T11–T17; do not re-run it.** The Step 5 pins were proposed (a 6-reader + synthesis workflow) + Eren-confirmed 2026-06-16 (logged in §11.5 "📌 STEP 5 PINNED"), and T11–T17 are carded (§12.17–§12.23) — **none built yet**; build each in a fresh agent per §12.0 when its Prereq holds (**T11 first — it lands the tab model T12/T13 inherit**).

```
Backlog task <ID> (see §11.4) is NOT carded — it has open 📌 decisions a build agent can't guess.
Your job is to PIN + CARD it, not to build it:
1. Read CLAUDE.md, docs/PRODUCT-PLAN.md §3/§5/§8/§11.5, and the relevant code/specs.
2. Propose concrete answers to this task's 📌 (e.g. which provider usage endpoints + auth schemes;
   the forecast algorithm; the anomaly baseline; the GUI design). For anything that's an external/ToS
   or product call, present options + a recommendation and STOP for the human. **Never** propose a
   session-reuse or undocumented-endpoint path (e.g. Cursor's `api2.cursor.sh`) — that is the ToS line
   (§5 tier 4); a provider with no sanctioned source stays detect-only / "unavailable" (§8).
3. Once pinned (with human sign-off where flagged), write a full T1–T7-style body for <ID> into §12
   and log the pinned decisions in §11.5.
4. Do NOT implement or commit. Output the proposed card + decisions for review.
```

### 12.9 — T8 · Keychain credential store (`costroid-connect`) · L · ⛔ · Prereq: T7 · ✅ **DONE (2026-06-09, gate green, ⛔-approved — see §11.5)**

> **As-built (2026-06-09; gate green, ⛔-approved — full detail in §11.5 ✅ T8).** Built as pinned, with three deviations from the card, all logged: (1) **`keyring` has no `mock` feature** — `keyring::mock` is always available; tests install it once via a `Once`, and each `CredentialStore` owns its entries (the mock persists per-store → isolated, parallel-safe). (2) **Backend = sync Secret Service** (`dbus-secret-service`, C libdbus) over the async/zbus path, so **`async-io` is pulled by NO real build and stays GLOBALLY banned even under `--features connect`** — the card's anticipated "allow async-io narrowly" was *not needed* (a stronger result). (3) **`offline.rs` now resolves per shipped target** (`--filter-platform`, unioned) because unfiltered `cargo metadata` is an all-targets superset that reported phantom `async-io`/`zbus` from keyring's unused async path (cargo tree confirms no async runtime in any real build).

> **As-pinned (2026-06-09; two items ⛔ human-signed-off below).** T8 is **pure-library**: it ships *only* the OS-keychain credential store inside `costroid-connect` — **no new CLI, no network** (the `costroid connect`/`disconnect` UX + Connections view are **T10**; the HTTP fetch + reconciliation are **T9**). "API-key entry" here = the library *store* path, tested via `keyring`'s **mock** backend. Secrets are keyed by **billing vendor** (`Anthropic`/`OpenAI`/`Gemini`) — a different axis from `ProviderId` (the *tool*: ClaudeCode/Codex/Cursor) — via a small owned `enum ApiVendor`, so `costroid-connect` stays free of `core`/`focus` deps through T8 (those land in T9). **⛔-signed-off:** (1) **Linux backend** — the pin anticipated keyring's Linux Secret-Service backend pulling a transitive IPC crate (e.g. an `async-io` D-Bus executor; *local IPC, not network egress*) and authorized permitting it **narrowly, scoped to the `costroid-connect` subtree under `--features connect`**. **Superseded by the As-built result above:** choosing the *sync* Secret Service backend (`dbus-secret-service`, C libdbus) means `async-io` is pulled by **no** real build, so it was kept **globally banned even under `--features connect`** — the narrow permit was never needed (a stronger guarantee). `libdbus-1-dev`/`libsecret-1-dev` install in CI (Step 4); the default build stays clean (keyring unlinked) and the strace test still proves zero outbound. (2) **Scope** — pure-library; CLI entry deferred to T10. Full detail in §11.5 *✅ T8 DONE* (Deviation 2 records the supersession).

```
**Goal:** give costroid-connect its FIRST behavior — a keychain-backed credential store for the user's
  own usage/billing API keys (Anthropic/OpenAI/Gemini), so T9 can read a key and T10 can wire
  connect/disconnect on top. KEYCHAIN ONLY — no network, no CLI in this task.
**Files:** crates/costroid-connect/Cargo.toml (its FIRST deps — see Deliverables);
  crates/costroid-connect/src/lib.rs (the store + ApiVendor + registry + error — replaces the empty
  skeleton); deny.toml (the keyring `wrappers` guard now fires + a connect-on pass);
  apps/cli/tests/offline.rs (flip the connect-on test to assert keyring PRESENT; permit keyring's IPC
  subtree narrowly); scripts/offline_acceptance.sh (fill the secret-residue half of the feature-on STUB
  via the mock backend); .github/workflows/ci.yml (libdbus-1-dev + libsecret-1-dev; a `--features connect`
  build/test; a connect-on `cargo deny` pass).
**📌 Pinned (2026-06-09 — accept as-is; (a)/(b) are ⛔-signed-off, do not re-litigate):**
  · keychain service = `costroid`; account = `apikey:<vendor>` (reserve `oauth:<vendor>` for T9/T10).
  · credential identity = owned `enum ApiVendor { Anthropic, OpenAI, Gemini }` (NOT ProviderId; no core/focus dep yet).
  · secrets wrapped in `secrecy::SecretString` (no Debug/serde leak); never disk/config/logs.
  · connection registry = a NON-secret index at ${XDG_STATE_HOME:-~/.local/state}/costroid/connections.json
    (atomic temp+rename; lists connected vendors only — zero secret material).
  · (a) ⛔ Linux backend: allow keyring's transitive IPC deps ONLY inside the costroid-connect subtree under
    --features connect; default build unaffected; strace still proves 0 outbound; libdbus/libsecret in CI.
  · (b) ⛔ scope: pure library; the `costroid connect`/`disconnect` CLI + Connections view are T10.
**Scope fence:** the costroid-connect credential store + ApiVendor + ConnectionRegistry + error type +
  the dep/guard/CI wiring ONLY. NO HTTP / usage-API calls (T9). NO `costroid connect` command or any
  apps/cli source change beyond apps/cli/tests/offline.rs (T10 owns the CLI). NO core/focus dep on
  costroid-connect yet. NO OAuth (tier 2, deferred §8). NO RequestCount / no new providers.
**Deliverables — crate:** add to costroid-connect/Cargo.toml (still gated by the consumer's `connect`
  feature, so off by default): `keyring = "3"` with the native backends (macOS `apple-native`, Windows
  `windows-native`, Linux Secret Service) + its PURE-RUST crypto (`crypto-rust`, NEVER `crypto-openssl` —
  openssl stays globally banned) + a mock/test path; `secrecy` (or `zeroize`) for in-memory secrets; verify
  every new transitive license is permitted (cargo-deny). `enum ApiVendor { Anthropic, OpenAI, Gemini }`
  (Display + FromStr). A `CredentialStore` exposing `store(ApiVendor, SecretString) -> Result<(), ConnectError>`,
  `retrieve(ApiVendor) -> Result<Option<SecretString>, ConnectError>`, `delete(ApiVendor) -> Result<(), ConnectError>`
  — keyring service `costroid`, account `apikey:<vendor>`. `struct ConnectionRegistry` over connections.json:
  `mark_connected` / `mark_disconnected` / `list() -> Vec<ApiVendor>`, atomic write (temp+rename), stores NO
  secret. `enum ConnectError` (thiserror; `#[from] keyring::Error`, IO, etc.) — NO unwrap/expect/panic (library crate).
**Deliverables — guards/CI:** deny.toml — the `keyring` `wrappers = ["costroid-connect"]` entry now fires; add a
  connect-on deny pass (a `--features connect` / `--all-features` graph) so the wrapper guard actually evaluates
  (T7's deny.toml comment flagged this no-op); narrowly allow keyring's transitive IPC crate(s) ONLY under the
  connect subtree; openssl/openssl-sys/native-tls stay banned GLOBALLY (no exception). apps/cli/tests/offline.rs —
  `connect_feature_admits_only_the_sanctioned_trio` now asserts `keyring` IS linked under --features connect (it was
  deliberately NOT asserted present in T7's empty skeleton); the default-build test still forbids keyring; document
  exactly which keyring-transitive crate names are permitted under connect and WHY (local IPC, not egress).
  scripts/offline_acceptance.sh — fill the secret-residue half of the STUB: with the MOCK backend a
  store→retrieve→delete round-trip writes nothing to $HOME/disk/config/logs (diff a fixture HOME before/after);
  the network half stays a T9/T10 stub. ci.yml — install libdbus-1-dev + libsecret-1-dev; add a `--features connect`
  build+test job; run the connect-on `cargo deny` pass.
**⛔ Human gate:** FIRST secret-handling/keychain code (CLAUDE.md golden rule + working-style: ask before anything
  touching authentication, secrets, or the keychain) AND it relaxes the forbidden-crates/deny guarantee surface
  (like T7). Stop for approval on the as-built specifics — the keyring crate + chosen backends/features, the
  credential model (service/account scheme + ApiVendor), and the narrowed denylist allowance — before finalizing.
  (Decisions (a)/(b) above are already signed off; do not re-open them.)
**Done when:** default `cargo build/test --workspace` green with connect OFF — keyring NOT linked, offline.rs
  default test + scripts/offline_acceptance.sh still pass (zero outbound); `--features connect` builds and the
  credential-store + registry tests pass via the keyring MOCK backend; `cargo deny check licenses bans` passes both
  default AND connect-on (keyring/wrappers guard fires, all licenses permissive, no openssl); offline.rs connect-on
  test asserts keyring present; a mock store→retrieve→delete round-trip writes nothing outside the keychain
  (asserted against a fixture HOME). No real developer keychain is ever touched by a test (mock only).
**Next:** T9 (ureq+rustls usage-API clients + reconciliation) reads stored keys via `CredentialStore`; T10 wires the
  `costroid connect`/`disconnect` CLI + the Connections view on top of the store + `ConnectionRegistry`.
```

### 12.10 — T10b · Release v0.4.0 (connections) · ⛔ · S · Prereq: T9 + ⛔ GATE 2b (§11.5 ✅ T9b) + T10 done + ⛔ legal review cleared

> **✅ DONE — v0.4.0 SHIPPED 2026-06-16 (§11.5). Step 4 complete.** Live on crates.io (5-crate ladder), the GitHub Release (installers + 6-target binaries + checksums + attestations), and Homebrew + npm. The mechanics below ran as carded; the one wrinkle was a `dist build` libdbus-on-runner failure on the first Release run, fixed by `precise-builds = true` in `dist-workspace.toml` + a `v0.4.0` re-tag (see §11.5 lesson). The pre-ship prereqs are retained below for the record.
>
> **✅ All prereqs met (2026-06-16).** T9 + GATE 2b + T10a + T10c are DONE, and the ⛔ legal review of the connection flows is CLEARED via maintainer self-attestation (`docs/proposals/T10b-LEGAL-REVIEW.md` §10; §11.5 2026-06-16). The connection-flow safety review + hardening (§11.5 2026-06-16) also landed. **Only the release mechanics below remain** — and they are a ⛔ human action (the maintainer commits the prep, then tags + publishes; the agent prepares but does not tag/push/publish).
>
> **The release-mechanics cap on Step 4** — the connections analogue of T1 (which cut v0.2.0). It does NOT build features (T9 = HTTP clients + reconciliation, T10 = the connect/disconnect CLI + Connections view); it cuts and publishes the release. 0.4.0 is the FIRST release to change the crates.io publish ladder (`costroid-connect` joins) and the FIRST lockstep version bump across FIVE workspace crates — so it warrants its own card (v0.3.0 was a chore-cut because it added zero new release mechanics; this adds several). Carded now because the mechanics are knowable (unlike T9's endpoints); runs only once its prereqs land.

```
**Goal:** ship the connections line as v0.4.0 — the first network release (opt-in, off by default).
**Files:** Cargo.toml ([workspace.package].version + the FOUR [workspace.dependencies] internal
  constraints, now incl. costroid-connect — the `costroid` CLI has no entry); Cargo.lock (refresh); CHANGELOG.md (0.4.0 entry);
  README.md + SECURITY.md (release-line wording 0.3.x → 0.4.x); RELEASING.md (extend the crates.io
  ladder). Do NOT tag/push/publish.
**Scope fence:** version bump + lockfile + CHANGELOG + README/SECURITY release wording + the
  RELEASING.md ladder line ONLY. NO code changes in apps/ or crates/; NO edits to .github/ or
  dist-workspace.toml (but DO verify them — see Deliverables). Do NOT tag/push/publish.
**Deliverables:** bump 0.3.0→0.4.0 — `[workspace.package].version` **and** the FOUR
  `[workspace.dependencies]` internal constraints (`costroid-core/-focus/-providers/-connect =
  { …, version }` — the new `costroid-connect` entry now rides the same lockstep bump the §11.5 T1
  lesson logged for the original three; the `costroid` CLI has no constraints entry, though all FIVE
  `version.workspace` members move together; a stale `^0.3.0` won't resolve against 0.4.0);
  `cargo update --workspace`;
  CHANGELOG 0.4.0 entry (connections: opt-in own-key usage-API reconciliation + connect/disconnect +
  the Connections view; off by default, the default build still makes zero network calls); flip
  README/SECURITY release line to 0.4.x; extend the RELEASING.md crates.io ladder to
  `focus → providers → core → connect → cli`; run `dist plan` + host-scoped
  `dist build --artifacts=local` (dry-run, report only). **Verify the release pipeline:** confirm the
  dist RELEASE workflow still ships the **connect-OFF default** `costroid` binary (so release.yml needs
  no libdbus/libsecret) — if a connect-on artifact is ever added, first add `[dist].dependencies`
  (apt = libdbus-1-dev, libsecret-1-dev) to dist-workspace.toml so the Linux runners install them.
**⛔ Human gates:** (1) the **legal review of the connection flows** (Step 4) — ✅ **CLEARED 2026-06-16**
  via maintainer self-attestation (`docs/proposals/T10b-LEGAL-REVIEW.md` §10; §11.5 2026-06-16), so this no
  longer blocks; (2) public release — review, then **COMMIT the prep first** (the agent does NOT commit) so
  the tag lands on a 0.4.0-manifest commit (cargo-dist requires tag == `[workspace.package].version` —
  the §11.5 T1 gotcha that aborted a run), then `git push origin main && git tag v0.4.0 && git push
  origin v0.4.0` (triggers the GitHub-Release CI: installers only). **The tag does NOT publish to
  crates.io** — run the extended `cargo publish` ladder (focus → providers → core → **connect** → cli)
  per RELEASING.md, or `cargo install costroid` keeps serving 0.3.0.
**Done when:** gate green; `dist plan` lists 0.4.0 across the 6 targets; version + lockfile + CHANGELOG +
  README/SECURITY updated; the RELEASING.md crates.io ladder includes `costroid-connect`; (after your
  tag + the crates.io ladder) release CI succeeds and `cargo install costroid` → 0.4.0.
**Next:** 0.4.0 is live; the connections line is shipped → Step 5 (analytical tabs + alerts, T11–T17).
  (0.5.0 / 0.6.0 can be chore-cut like 0.3.0 — they add no new publish-ladder/CI mechanics — unless a
  later release again changes the published crate set.)
```

### 12.11 — T9a · `costroid-connect` HTTP infra: the generic authorized-host client · L · ⛔ (guarantee redefinition) · Prereq: T7 ✅, T8 ✅, T9 pins ⛔-signed-off (§11.5 2026-06-10) ✅ · ✅ **DONE 2026-06-10, ⛔-approved (see §11.5 ✅ T9a)**

```
**Goal:** give `costroid-connect` its network half's foundation: a small, generic, provider-agnostic
  BLOCKING HTTPS client on `ureq`+`rustls` that can only talk to an explicitly authorized host —
  the HTTP layer the T9b adapters will call and the T10 offline-acceptance connect-ACTION test will
  exercise. NO provider knowledge (no endpoint, no param, no response shape — all of that is T9b).
**Spec:** docs/proposals/T9-PIN-PROPOSAL.md §6 (cross-cutting pins: User-Agent, backoff/degrade
  split, secret redaction; the wrong-key-class copy is T10's); ARCHITECTURE §8 (credential boundary).
**Files:** crates/costroid-connect/{Cargo.toml,src/}; root Cargo.toml ([workspace.dependencies]
  pins for the new crates, exact `=` versions); apps/cli/tests/offline.rs (presence assertions);
  deny.toml (comment currency — the ureq/rustls wrappers now fire); SECURITY.md + CLAUDE.md +
  ARCHITECTURE §5/§8 (the guarantee re-wording below); CHANGELOG.md [Unreleased].
**Scope fence:** no provider adapters/endpoints (T9b); no reconciliation (T9c); no CLI surface, no
  connect/disconnect, no key-paste UX (T10); the DEFAULT build's resolved graph is unchanged (trio
  still forbidden + connect unlinked); scripts/offline_acceptance.sh's connect-ACTION half STAYS the
  T9/T10 stub (T10 finishes it); tests must NEVER touch the real network (loopback only — the suite
  must pass inside the strace harness and offline CI).
**Pins (decisions already made — do not re-open):**
  - HTTPS-only, GET-only (both pinned vendor APIs are GET; widen only when a carded task needs it).
  - Authorized-host enforcement IN the type: a client value is constructed over ONE allowlisted host;
    any request whose URL is not on that host is a typed error BEFORE any I/O. Redirects: disabled
    entirely — a redirect response is a typed error (neither pinned API needs them; following one
    could leave the authorized host).
  - Auth: caller-supplied header name/value pairs with `secrecy::SecretString` values (T9b composes
    `x-api-key` + `anthropic-version`, or `Authorization: Bearer …`). The client NEVER logs, echoes,
    or Debug-prints a secret (redacting Debug — the CredentialStore precedent; pin it with a test).
  - `User-Agent: costroid/<CARGO_PKG_VERSION>` on every request (proposal §6 — Anthropic asks for it).
  - Timeouts (connect + overall) and a bounded response-body size — typed errors, never a hang.
  - Error taxonomy: the client CLASSIFIES (unauthorized-host / redirect / timeout / status 429 / 5xx
    / other-4xx / transport / body-too-large); retry/backoff POLICY belongs to the caller (T9b) —
    keep the client small. Everything degrades to a typed error (`ConnectError` grows variants);
    no `unwrap`/`expect`/`panic!`.
  - TLS roots: **OS-native trust** (ureq's native-certs path / rustls-native-certs) — NOT
    `webpki-roots` (current releases are CDLA-Permissive-2.0, older were MPL-2.0 — either way
    absent from deny.toml's permissive allowlist). ⛔ if native roots prove
    infeasible on a tier-1 platform and a non-allowlisted license would be the only path.
  - `ureq`: current stable, pinned exact (`=`), default-features off + only the needed features
    (rustls TLS + native certs; json OFF — body parsing is T9b's; gzip only if a pinned API needs
    it). The resolved graph must stay clean across the six shipped triples (offline.rs proves it).
  - `core`/`focus` deps: the §11.4 backlog line anticipated them landing here — add ONLY if the
    client API actually uses their types; a generic client should not need them, so the expected
    outcome is DEFER to T9b and log the deviation (the lean-deps rule beats backlog anticipation).
  - Response body: returned as bytes/string (the caller parses; typed shapes are T9b).
**Guards (the ⛔ guarantee redefinition — the T7/T8 precedent):**
  - offline.rs connect tier: assert `ureq` AND `rustls` present (keyring's T8 precedent); default
    tier unchanged. deny.toml: the wrappers finally fire → `cargo deny check licenses bans` (the
    `--all-features` superset pass CI gates — features are additive — plus the locally-run
    default-mode pass) green with ZERO unused-wrapper warnings; update the
    deny.toml comments that said "until T9".
  - The strace feature-on baseline must STILL pass: the client existing ≠ a call happening; a normal
    `--features connect` run performs zero network I/O (nothing calls the client until T10).
  - Doc guarantee re-wording (SECURITY.md's interim-posture note, the CLAUDE.md echo, ARCHITECTURE
    §5/§8): from "costroid-connect contains no network code" to "contains the generic HTTP client
    but NO caller and NO provider adapter; no network call can occur without the explicit
    user-initiated connect action (T10) — enforced by the feature-on strace baseline + the
    authorized-host type."
**Tests that prove Done-when (loopback only):** authorized-host refusal (off-host URL → typed error,
  no I/O); redirect → typed error; secrets absent from Debug/Display/error text; 429/5xx/timeout
  classification; the UA header actually sent; a success path returning the body. How to serve
  loopback TLS is the agent's choice — a self-signed rustls test server with an injectable test
  root, or a test-only plain-HTTP loopback constructor — pick one, NEVER weaken the public prod API
  (no `danger_*` knobs in the public surface), and log the choice in §11.5.
**Done when:** four-command gate green; offline.rs green on BOTH tiers (default: trio forbidden,
  connect unlinked; connect: trio present); cargo-deny green with zero unused-wrapper warnings on
  the `--all-features` (superset) pass CI gates AND the locally-run default-mode pass (CI's license
  job runs only `--all-features` — additive features make it a strict superset, no coverage hole);
  `bash scripts/offline_acceptance.sh` green (incl. the feature-on
  baseline); the new client tests green with zero real-network I/O; docs + CHANGELOG updated;
  §11.4 box ticked; §11.5 as-built entry written.
**⛔ Human gate (stop before finalizing):** present (1) the redefined guarantee wording, (2) the
  exact new crates/versions/features + their licenses (incl. the TLS-roots choice), (3) the client's
  public API surface — it becomes the contract T9b builds on.
**Next:** T9b (TWO adapters — Anthropic + OpenAI — plus the Gemini first-class-unavailable state) —
  now carded at §12.12, against the client API as built.
```

### 12.12 — T9b · The two usage-API adapters (Anthropic + OpenAI) + the Gemini first-class-unavailable state · L · ⛔📌 · Prereq: T9a ✅

> **As-pinned (2026-06-10).** The 📌 is already resolved: every endpoint, parameter, auth, and money detail below is sourced from the ⛔-signed-off `docs/proposals/T9-PIN-PROPOSAL.md` (§2 Anthropic · §3 OpenAI · §4 Gemini · §6 cross-cutting) — read it fully; it IS the API design, and where this card is silent the proposal governs. **Never invent an endpoint/param/field beyond it** (that is the whole point of pin-then-card). The two ⛔ below (secret-handling approval + the §8-style live-shape confirm) still gate.

```
**Goal:** give costroid-connect's network half its first real behavior: TWO adapter modules
  (Anthropic, OpenAI) that turn a stored admin key (`CredentialStore::retrieve(ApiVendor)`) + a
  date range into `AuthorizedClient::get` calls against the four pinned endpoints, parse the
  documented response shapes, and produce typed VENDOR-REPORT values (billed-$ per day; tokens by
  model) for T9c's reconciliation — plus the Gemini first-class-unavailable state (NO adapter: a
  typed unavailable carrying the exact reason string already shipped in the docs:
  "unavailable — no sanctioned static-key usage API").
**Spec:** docs/proposals/T9-PIN-PROPOSAL.md (the API design — §2/§3/§4/§6); §11.5 ✅ T9a (the
  as-built client contract + its three T9b contract notes); DATA-MODEL "Estimate vs. invoice
  reconciliation" (design-intent prose only — it specs NO vendor-report shapes, so the builder
  PROPOSES the type names and logs them in §11.5).
**Files:** crates/costroid-connect/{Cargo.toml, src/lib.rs, src/anthropic.rs + src/openai.rs (new;
  exact module layout = builder's choice)}; crates/costroid-core/src/ (a NEW vendor-report types
  module — see Type placement); hand-built fixture response bodies for the adapter tests;
  docs/DATA-MODEL.md (record the as-built vendor-report shapes next to the reconciliation section);
  RELEASING.md (the §11.5 ✅ T9a entry deferred the publish-ladder note here: costroid-connect now
  depends on costroid-core → publishes after core); CHANGELOG.md [Unreleased].
**Type placement (pinned):** the parsed vendor-report data shapes live in **costroid-core**
  (provider-neutral), so T9c stays pure-core/fixture-tested with NO connect dependency — the
  dependency direction is connect → core, NEVER core → connect. T9b therefore adds
  costroid-connect's FIRST internal dep (`costroid-core.workspace = true` — the
  [workspace.dependencies] entry already exists) + `rust_decimal.workspace = true` for the money
  types (`costroid-focus` only if a shape genuinely needs it; expected NOT — log if added). The
  honesty caveats ride IN the types as data, never doc-only: the Anthropic Priority-Tier-absent
  footnote (Priority Tier dollars are ABSENT from cost_report — cost totals understate the bill
  for priority users; track Priority Tier via usage_report `service_tier=priority`; conversely
  code-execution costs appear ONLY in cost_report) and the OpenAI per-model-$ derived/best-effort
  label — T9c/T10 must be unable to lose them.
**Endpoints — Anthropic** (host `api.anthropic.com`; auth **`x-api-key: <admin key>` +
  `anthropic-version: 2023-06-01`**, NOT Bearer; key class **`sk-ant-admin…`** — standard
  `sk-ant-api03…` keys are explicitly rejected on these endpoints; pin code + tests to the API
  paths + version header, NOT to doc URLs — the docs are mid-host-migration):
  · `GET /v1/organizations/cost_report` — actual billed cost in USD per day. Params: `starting_at`
    (required, RFC 3339; buckets snapped to UTC day start, inclusive), `ending_at` (exclusive),
    `bucket_width` (**`1d` only** — the sole granularity), **always pass `group_by[]=description`**
    (it is what unlocks the parsed `model`/`token_type`/`service_tier`/`context_window`/
    `inference_geo` fields), `limit`, `page`. Response: `data[]` of buckets `{starting_at,
    ending_at, results[]}`; each result carries `amount`, `currency` (always "USD"), `description`,
    `cost_type` (`tokens|web_search|code_execution|session_usage`), `model`, `token_type`,
    `service_tier`, `context_window`, `inference_geo`, `workspace_id` (null = default workspace).
    Data appears ~5 minutes after request completion.
  · `GET /v1/organizations/usage_report/messages` — token counts by model per bucket. Params:
    `starting_at` (required), `ending_at` (exclusive), `bucket_width` (`1m|1h|1d`),
    **use `group_by[]=model`**, `limit`, `page`. Response per result: `uncached_input_tokens`,
    `cache_read_input_tokens`, `cache_creation{ephemeral_5m_input_tokens,
    ephemeral_1h_input_tokens}`, `output_tokens`, `server_tool_use{web_search_requests}` —
    **NO cost field**, tokens only. Buckets-per-page caps: `1d` default 7 / max 31 ·
    `1h` 24/168 · `1m` 60/1440 — paginate for longer ranges.
  · Pagination: `has_more` + `next_page`, passed back as `?page=…` — the token is OPAQUE (doc
    samples look timestamp-like; pass it back verbatim, never parse it). (The bucket caps above
    are usage_report's; cost_report's per-page cap is undocumented — probe empirically.)
**Endpoints — OpenAI** (host `api.openai.com`; auth **`Authorization: Bearer <key>`**, no version
  header; key class **`sk-admin-…`** — project/standard keys cannot call these endpoints, and
  admin keys cannot call non-administration endpoints; use the **`/v1`** path form — reference
  tables omit it but every official curl example uses it):
  · `GET /v1/organization/costs` — actual billed spend in USD per day, whole org. Params:
    `start_time` (required, Unix seconds, inclusive), `end_time` (exclusive), `bucket_width`
    (**`1d` only**), `limit` (1–180 buckets, default 7), `group_by` of
    `project_id | line_item | api_key_id`, `page` cursor. Response: `object="page"` → `data[]`
    buckets `{start_time, end_time, results[]}`; each result `object="organization.costs.result"`
    with `amount: {value: number, currency: "usd"}` (**float dollars**). **`group_by` has NO
    `model` option** → per-model $ is only derivable from the undocumented, uncontracted
    `line_item` strings → label any per-model $ **derived/best-effort** (typed), or compute
    per-model estimates from documented token counts × prices.
  · `GET /v1/organization/usage/completions` — token counts by model per bucket. Params:
    `start_time` (required), `end_time`, `bucket_width` (`1m|1h|1d`, default `1d`),
    **`group_by=model`**, the same per-page bucket caps (`1d` 7/31 · `1h` 24/168 · `1m` 60/1440),
    `page` cursor. Response per result: `input_tokens` (INCLUDES cached), `output_tokens`,
    `input_cached_tokens`, `input_audio_tokens`, `output_audio_tokens`, `num_model_requests` —
    no cost field.
  · Pagination: `page` = previous `next_page`; stop on `has_more=false`.
**Money pins (proposal §6 — unit-tag at EVERY parse boundary; mixing encodings silently is a 100×
  error):** Anthropic cost_report `amount` = **decimal-string CENTS, fractional possible**
  ("123.78912" = $1.2378912) — parse arbitrary-precision via `rust_decimal`, divide by 100; never
  integer-cents, never float-and-round. OpenAI `amount.value` = **float dollars** — convert via
  the JSON number's literal text (serde_json's `raw_value` feature is already on workspace-wide)
  into `Decimal`; NEVER through f64 arithmetic. **Do NOT validate parsers against Anthropic's doc
  examples — they are internally inconsistent** (proposal §2.4: the prose money math is wrong, and
  a sample pairs a Sonnet description with an opus model id) — hand-build fixtures from the
  documented SCHEMA instead. Separate `service_tier` types per endpoint (cost_report's enum is
  only `standard|batch` — narrower than usage_report's six values; don't share an enum);
  cost_report's `inference_geo` = best-effort nullable (its doc text is self-contradictory).
**Defensive pins:** backoff POLICY lives in the adapters (T9a classifies, T9b decides): honor
  `ConnectError::RateLimited { retry_after_seconds }`; respect Anthropic's ≤1 req/min sustained
  guidance (bursts for paginated downloads OK); 429/5xx → bounded backoff → degrade to a typed
  unavailable outcome, NEVER a hard failure (OpenAI's costs endpoint had a real ~1-day 404
  outage). First-class unavailable states modeled AS DATA, never error loops: Anthropic
  individual/non-org account ("The Admin API is unavailable for individual accounts") +
  Claude-on-AWS orgs; OpenAI member-not-Owner (cannot mint an admin key). 401 vs 403 (both arrive
  as `ConnectError::ClientError { status }`) classify to DISTINCT adapter outcomes — the
  wrong-key-class connect-time copy itself is T10's. **Secrets NEVER in URLs** (§11.5 T9a contract
  note 1: ureq error text can echo the full request URI; the redaction guarantee covers
  `AuthHeader` values only) — URLs compose from non-secret parts exclusively; keys ride only in
  `AuthHeader`. Each adapter sets its OWN explicit `RequestLimits` (never passes arbitrary caller
  values through — T9a contract note 2). Hosts are bare `api.anthropic.com` / `api.openai.com`
  (trailing-dot FQDNs are off-host by design — T9a contract note 3; fine for both vendors).
**Scope fence:** NO CLI surface / connect / disconnect / key-paste UX / Connections view (T10).
  NO reconciliation math (T9c). NO `GET /v1/organizations/me` (the sanctioned connect-time key
  validation) and NO `GET /v1/organizations/rate_limits` (the documented API-lane denominators) —
  both documented with the same key, noted for T10/later carding per proposal §2.5, NOT called
  here. NO Gemini adapter — and NEVER the legacy OpenAI `GET /v1/usage` /
  `GET /v1/dashboard/billing/usage` (confirmed undocumented/internal — the tier-4 line). NO OAuth
  (tier 2, deferred). No rendering. The DEFAULT build's resolved graph stays unchanged;
  scripts/offline_acceptance.sh's connect-ACTION half STAYS the T10 stub; tests NEVER touch the
  real network.
**Guards:** NO guarantee redefinition this time — the sanctioned trio is unchanged and the new
  internal dep (costroid-core) is already in every build's graph. offline.rs (both tiers),
  cargo-deny (both passes), and the feature-on strace baseline must all stay green AS-IS
  (adapters existing ≠ a call happening; nothing calls them until T10).
**Tests (fixtures only, zero real network):** drive the adapters through the `cfg(test)` loopback
  constructor — widen `AuthorizedClient::loopback_http_for_tests` from module-private to
  `pub(crate)` (STILL `cfg(test)`-only: invisible to builds, dependent crates, and tests/
  integration tests — so adapter tests are in-crate unit tests). Fixtures are hand-built per the
  documented response schemas (NOT copied from Anthropic's broken doc examples). Cover: multi-page
  pagination (opaque token passed back verbatim; the bucket caps); money parsing (fractional-cent
  string ÷ 100; float-dollar via literal text; unit tags asserted); every unavailable state
  (individual-account / member-not-Owner shaped responses + the Gemini reason string) as data,
  never loops; 429-degrade (Retry-After honored, bounded backoff, typed unavailable — never a
  hard-fail); 401 vs 403 distinct; no secret in any composed URL.
**⛔ Human gates (TWO):**
  (1) Secret-handling: this task READS stored keys via `CredentialStore::retrieve(ApiVendor)`
      (CLAUDE.md decide-vs-ask: anything touching authentication/secret handling stops first).
      Stop for approval on the adapter public API + the key flow before finalizing.
  (2) The §8-style LIVE-SHAPE CONFIRM — the FINAL gate before T9b is DONE. The build proceeds
      fixtures-first; before the parsers are TRUSTED, the human runs the open empirical checks
      with their OWN admin keys (proposal §6), when ready, and the results are logged in §11.5:
      [ ] Responses-API coverage: does `usage/completions` include Responses-API traffic? (Codex
          rides the Responses API; there is no usage/responses endpoint and no doc says
          completions covers it — fire a known Responses-API call and watch the usage endpoint,
          §3.3. If NOT covered, token-side reconciliation silently undercounts exactly the Codex
          traffic while /costs still bills it — the report types must label that lane.)
      [ ] the OpenAI `line_item` string format (undocumented; grounds the derived/best-effort label)
      [ ] history depth/retention on all four pinned endpoints (undocumented at both vendors)
      [ ] OpenAI UTC-alignment + invoice-exactness of daily buckets
      [ ] Anthropic org-creator-gets-admin-role (§2.3 — informs T10's connect copy; record only)
**Done when:** four-command gate green; offline.rs both tiers + cargo-deny both passes +
  `bash scripts/offline_acceptance.sh` (incl. the feature-on baseline) all still green; adapter
  fixture tests green with zero network I/O; the vendor-report types live in costroid-core with
  no core→connect edge; both ⛔ gates passed (the live-shape confirm executed + logged);
  DATA-MODEL + RELEASING.md + CHANGELOG updated; §11.4 box ticked; §11.5 as-built entry written
  (incl. the proposed vendor-report type names + the live-confirm findings).
**Next:** T9c consumes the core vendor-report types (fill §12.13's `[fill at T9b landing: …]`
  slots from the as-built names); T10 wires connect/disconnect + the `GET /v1/organizations/me`
  connect-time validation + the wrong-key-class copy on top.
```

### 12.13 — T9c · Estimate-vs-invoice reconciliation engine · M · Prereq: T9b ✅ · ✅ **DONE 2026-06-13 (gate green, 280 tests — see §11.5 ✅ T9c)**

> **Carded 2026-06-10 with the T9b-dependent slots marked `[fill at T9b landing: …]`; BUILT 2026-06-13** with those slots filled in place from the as-built `costroid-core::vendor_report` types. The as-built record is §11.5 ✅ T9c; the body below is the prompt as run.

```
**Goal:** the estimate-vs-invoice reconciliation ENGINE: pure costroid-core logic that compares
  Costroid's local estimated cost (Σ tokens × bundled prices — ALWAYS the estimate) against the
  vendor-billed report values T9b's adapters produce (the invoice side — the SOURCE OF TRUTH),
  per day and, where honestly supported, per model, and surfaces the variance with typed, honest
  labels. ENGINE ONLY — display/render is T10's "reconciliation display" 📌.
**Spec:** DATA-MODEL "Estimate vs. invoice reconciliation" — currently design-intent PROSE, not
  shapes (it pins: the local figure is the estimate; the provider invoice is the source of truth;
  reconciliation aggregates estimated cost per billing period and service, compares it to the
  invoiced cost, surfaces the variance, and may calibrate the estimate). This card defers shape
  names to the builder, who reconciles them with DATA-MODEL and updates that section to as-built.
  Proposal §6's money pin (unit-tagged money at every boundary) binds here too.
**Files:** crates/costroid-core/src/ — the engine lives beside the vendor-report types
  [BUILT: crates/costroid-core/src/reconcile.rs (new module, beside src/vendor_report.rs;
  re-exported from src/lib.rs)]; fixtures are in-module `#[cfg(test)]` builders (hand-built
  vendor-report + local-estimate pairs — no separate fixture files needed); docs/DATA-MODEL.md
  (reconciliation section → as-built shapes); CHANGELOG.md [Unreleased].
**Pinned now:**
  · Pure costroid-core: NO connect dependency (core can NEVER depend on connect — the dependency
    direction is connect → core; T9c consumes the vendor-report types core itself defined in
    T9b). Fixture-tested, ZERO network — the engine never fetches; callers (T10) hand it values.
  · Comparison semantics: local estimate per day/model (from the FOCUS rows core already
    computes) vs vendor-billed per day; deltas labeled honestly — never present the estimate as
    the bill, never silently "correct" it. Calibration (DATA-MODEL's "may calibrate") is AT MOST
    a labeled output value — never a mutation of FOCUS rows or the pricing table; defer it
    entirely if it doesn't fall out naturally.
  · The typed caveats T9b carried in MUST survive into the engine's output types: the OpenAI
    per-model-$ **derived/best-effort** label (per-model $ comparison for OpenAI is best-effort
    or token-side only) and the Anthropic **Priority-Tier-absent** footnote (vendor cost totals
    understate the bill for priority users) — flattening either away is a bug.
  · Vendor-side absence is TYPED absence, never a zero: a day/lane the vendor report doesn't
    cover (history depth, data latency, Gemini's first-class unavailable) reconciles to
    "unavailable", never to a fabricated $0 delta.
  · Money is Decimal end to end; subscription lanes are NOT in scope (limits are not summable
    dollars; DATA-MODEL's effective-estimate-vs-flat-fee "is the plan worth it?" comparison is a
    future view, not T9c).
  · [BUILT: the engine consumes `CostReportOutcome` (`Available(VendorCostReport{days,caveats})`
    | `Unavailable(VendorReportUnavailable)`), `VendorCostDay{date,total,by_model,line_items}`,
    `ModelCostAmount{model,amount,confidence}`, `UsdAmount`, `AmountConfidence`, `CostReportCaveats`.
    Output types (new, in `reconcile`): `LocalCostEstimate` (the local API-lane estimate per
    UTC-day/model; `from_focus_records`), `reconcile_cost(...) -> CostReconciliation{days,caveats,
    report}`, `DayReconciliation`/`ModelReconciliation` (`local_estimate`,`vendor_billed`,`variance`,
    `variance_pct`,+per-model `confidence`), `VendorBilled = Billed(UsdAmount) | Unavailable(BilledAbsence)`,
    `BilledAbsence = ReportUnavailable(VendorReportUnavailable)|DayNotCovered|ModelNotInReport`,
    `ReconciledReportStatus = Available | Unavailable(VendorReportUnavailable)`.]
  · [BUILT: caveat/unavailability representations — `CostReportCaveats` (`priority_tier_absent`,
    `per_model_derived_best_effort`) carried unchanged onto `CostReconciliation.caveats`, plus the
    OpenAI per-model label on each `ModelReconciliation.confidence = DerivedBestEffort`. Vendor-side
    absence is the typed `VendorBilled::Unavailable(BilledAbsence)` above — never a fabricated $0;
    a local $0 stays a genuine estimate.]
  · [BUILT: live-confirm findings that bound comparability — the vendor daily buckets are
    UTC-midnight aligned (T9b live-confirm), so the local estimate is bucketed by **UTC day**, not
    the trends view's local-tz day. The OpenAI `responses_api_coverage_unconfirmed` caveat bounds a
    **token-side** comparison, which this **cost** engine does NOT perform — OpenAI `costs` bills all
    traffic (incl. the Responses API Codex rides), so the dollar day totals are complete and that
    caveat does not apply; a token-side reconciliation (where it would live) is deferred to T10+.]
**Scope fence:** engine only — NO network, NO connect dep, NO CLI command/flag, NO rendering or
  TUI/statusline surface (T10's reconciliation-display 📌 owns surfacing), NO subscription
  plan-worth-it view, NO change to the FOCUS export schema (reconciliation output is its own
  shape, not new FOCUS columns — `x_Estimated` etc. stay as DATA-MODEL specs them).
**Tests (fixtures, no network):** fixture pairs prove: an exact-match day (zero delta, labeled
  estimate-vs-billed); under- and over-estimate days (signed variance); vendor-absent days
  (typed absence, never $0); the derived/best-effort and Priority-Tier caveats present on the
  relevant outputs; Decimal precision preserved (no float drift); [BUILT: fixtures mirror the
  as-built vendor-report types — `VendorCostDay::from_line_items` with `CostLineItem`s, plus
  `LocalCostEstimate::from_focus_records` over hand-built `FocusRecord`s; 15 reconcile fixtures +
  1 `UsdAmount::checked_sub`, listed in §11.5 ✅ T9c].
**Done when:** four-command gate green; the engine compiles with no connect edge (core's
  Cargo.toml gains nothing — the dependency direction holds by construction); fixtures cover
  every path above; DATA-MODEL's reconciliation section updated to as-built; CHANGELOG updated;
  §11.4 box ticked; §11.5 as-built entry written.
**Next:** T10 surfaces reconciliation (the display 📌) + wires connect/disconnect; then T10b cuts
  v0.4.0.
```

### 12.14 — T10a · `connect`/`disconnect`/`connections` CLI + key validation + connect-action test · XL · ⛔📌 · Prereq: T9 ✅ + ⛔ GATE 2b resolution agreed (§11.5 ✅ T9b / 📌 T10)

> **As-pinned (2026-06-13).** The 📌 is resolved: every CLI/endpoint/validation detail below is from the ⛔-signed-off `docs/proposals/T10-PIN-PROPOSAL.md` (§1.1 CLI · §2 validation · §4 connections · §6 GATE 2b · §7 the connect-action test) — read it fully; it IS the design, and where this card is silent the proposal governs. **Never invent an endpoint/param/field beyond it.** The OpenAI validation was signed off with an amendment: probe **`GET /v1/organization/costs`** (a recent COMPLETED 1-day window, `limit=1`), NOT `/usage/completions`. The three ⛔ below (CLI surface + secret-handling approval + the GATE-2b live-confirm) still gate; the ⛔ legal review of the flow this card builds gates T10b (the release), not this card's build.

```
**Goal:** wire the FIRST caller of costroid-connect — the connect/disconnect/connections CLI — so a
  user pastes their own admin key (Anthropic sk-ant-admin / OpenAI sk-admin-), it is validated WITHOUT
  reading spend beyond their own data, stored ONLY in the OS keychain, and listed; disconnect revokes
  instantly. This is the FIRST real network in the product (opt-in, behind --features connect + an
  explicit user action).
**Spec:** docs/proposals/T10-PIN-PROPOSAL.md §1.1 (CLI), §2 (validation — incl. the OpenAI /costs
  amendment in §2.2), §4 (connections), §6 (GATE 2b), §7 (the connect-action test); §11.5 ✅ T8/T9a/T9b
  + 📌 T10 (CredentialStore/ConnectionRegistry/AuthorizedClient/AnthropicAdapter/OpenAiAdapter);
  CLAUDE.md golden rules + decide-vs-ask.
**Files:** apps/cli/src/main.rs (clap: add Connect/Disconnect/Connections, all #[cfg(feature="connect")]);
  apps/cli/src/ (a NEW connect command module — the INJECTABLE command core, so the Layer-1 test can drive
  it with a loopback client + mock keychain); apps/cli/Cargo.toml (the `connect` feature already gates
  costroid-connect); crates/costroid-connect/src/anthropic.rs (add the non-billing `validate()` →
  GET /v1/organizations/me) + src/openai.rs (the validation probe = fetch_cost_report over a 1-day
  COMPLETED window, limit=1 — no new endpoint); crates/costroid-connect/src/lib.rs (optional NON-secret
  org-label on RegistryFile, captured from `me`); apps/cli/tests/ (the Layer-1 connect-action integration
  test, #[cfg(feature="connect")]); scripts/offline_acceptance.sh (replace the T9/T10 STUB with the
  Layer-2 fail-closed check); README.md + CHANGELOG.md.
**Pinned (proposal §1.1/§2 — signed off 2026-06-13):**
  · vendor = anthropic|openai|gemini; `gemini` connect prints the pinned `unavailable — no sanctioned
    static-key usage API` line (GEMINI_UNAVAILABLE_MESSAGE; ASCII-folded in --plain) + exits 0 with NO
    key prompt/accept.
  · Key entry = STDIN ONLY — NEVER argv/env (argv leaks to `ps`; env leaks to children/history). No-echo
    prompt on a TTY; read one line on a pipe (so `echo "$KEY" | costroid connect anthropic` works).
    Build agent picks a lean PERMISSIVE no-echo crate (e.g. rpassword, Apache-2.0) or a small termios shim.
  · Wrong-key-class PREFIX check BEFORE any network (reuse the adapters' wrong_key_class): sk-ant-admin /
    sk-admin-; on mismatch print "<seen>-key; <vendor> needs a <expected>… admin key", do NOT store,
    exit nonzero. Only the prefix is inspected (expose_secret()); the key is never echoed.
  · Anthropic validation = GET /v1/organizations/me (x-api-key + anthropic-version: 2023-06-01; returns
    {id,name,type}; ZERO billing; same Admin-API org-gate as cost_report → a 200 predicts the fetch; any
    non-200 = invalid/ineligible — treat generically, capture the real status at GATE 2b).
  · OpenAI validation = GET /v1/organization/costs over a recent COMPLETED 1-day window, limit=1 (the
    SIGNED-OFF AMENDMENT — NOT /usage/completions, NOT admin_api_keys): probe the exact endpoint T10c
    depends on, so "Connected" predicts the cost fetch and the costs-vs-usage scope question is moot.
    OpenAiAdapter::fetch_cost_report over that window IS the probe; 200=success, 401/403=failure, and a
    post-auth 400 on the window (cf. Anthropic completed-day 400 → RequestRejected{400}) means "request
    completed-day ranges" — confirm at GATE 2b.
  · On success: CredentialStore::store(vendor, secret) (keychain only) → ConnectionRegistry::mark_connected
    + capture the non-secret org label; print "Connected <vendor> — organization <name> (<id>). Key stored
    in your OS keychain." On failure: typed VendorReportUnavailable reason + remediation (AuthenticationFailed
    → "key was rejected"; AccessForbidden{IndividualAccount} → "create an org first"; {MemberNotOwner} →
    "use an Owner-created admin key"), do NOT store, exit nonzero. Never echo the key.
  · disconnect = CredentialStore::delete (idempotent) + ConnectionRegistry::mark_disconnected (idempotent);
    print "Disconnected <vendor>." exit 0 even if nothing was stored. No network.
  · connections = LOCAL-ONLY list by default (registry + keychain presence): anthropic/openai connected|not
    + the org label when present; gemini always "unavailable". `--check` re-runs the validation call per
    connected vendor (network). Status by a NON-COLOR text cue; --plain + em-dash ASCII-fold (as
    cursor_detected_message).
**Scope fence:** connect/disconnect/connections + the two validation calls + the connect-action test ONLY.
  NO reconciliation display (T10c — `costroid reconcile`). NO rate-limit denominators (deferred — proposal
  §9). NO OAuth (deferred — proposal §8; the Anthropic `me` OAuth-Bearer alt is NOT used). NO TUI tab
  (Step 5). NO new endpoint beyond the §2 validation calls. The DEFAULT build's resolved graph + the
  strace DEFAULT tier stay UNCHANGED (zero network); only the explicit connect/connections --check actions
  reach the network, behind --features connect.
**Connect-action test (proposal §7 — two layers, zero real network):**
  · Layer 1 — a #[cfg(feature="connect")] integration test drives the INJECTABLE command core against the
    cfg(test) loopback MockServer (the T9b test_support pattern) + the keyring MOCK backend, asserting:
    (a) the only network egress is to the loopback authorized host (off-host refused by the type before
    I/O — already T9a-tested); (b) connect writes the secret ONLY to the mock keychain (a fixture $HOME
    fingerprint is UNCHANGED across connect — extends T8's credential_round_trip_writes_nothing_to_disk to
    the full command); (c) disconnect removes the key + registry entry, no residue.
  · Layer 2 — replace the script STUB: run `costroid connect anthropic` (--features connect build) under
    strace/unshare with a PREFIX-VALID-BUT-FAKE key on stdin (e.g. sk-ant-admin-FAKE, so it passes the
    prefix check and ATTEMPTS the validation call) + a fixture $HOME, asserting (i) no non-loopback AF_INET
    connect escapes isolation (the real-host attempt FAILS CLOSED), (ii) the fixture $HOME fingerprint is
    unchanged (no secret/file residue on the failure path), (iii) disconnect likewise leaves no residue.
  · NO host-override knob is added (it would weaken the authorized-host guarantee). The CI strace gate
    treats both layers like the rest (assert_no_inet already allows 127.0.0.1/::1).
**⛔ Human gates (THREE on this card):**
  (1) CLI surface — the subcommand shapes + stdin key entry + copy (CLAUDE.md ask-first; §1.1 APPROVED —
      confirm the as-built matches).
  (2) Secret-handling — connect reads/stores keys (CLAUDE.md ask-first); approve the validate() API + the
      key flow (stdin → prefix-check → validate → keychain; never argv/env/disk/log; AuthHeader-only wire)
      before finalizing.
  (3) ⛔ GATE 2b — the live-confirm with the human's OWN admin key runs HERE (proposal §6): the connect
      validation + a first real cost fetch confirm the T9b populated-row shapes, the OpenAI /costs probe
      behavior (200/401/403 + any window 400), and a real 401/403 status. Any item NOT confirmable for
      lack of real usage is formally DEFERRED to the named T10-LIVE-ROWS card (§12.16) with its locked
      criterion. This card's §11.5 entry MUST record GATE 2b as CLEARED or each item deferred. (Eren noted
      it will likely defer unless real API usage is generated first.)
  (NOTE — not a gate on THIS card: the ⛔ legal review of the connection flows gates T10b/0.4.0 ship, not
   the T10a build; Eren engages it in parallel with this build.)
**Done when:** four-command gate green; --features connect builds; connect/disconnect/connections work
  against the keyring MOCK + loopback MockServer (ZERO real network in tests); Layer-1 test + Layer-2 script
  prove the three properties (only-loopback host, secret-keychain-only, disconnect-clean); offline.rs both
  tiers + cargo-deny both passes + the DEFAULT strace tier stay green; GATE 2b cleared OR each item deferred
  to T10-LIVE-ROWS; README/CHANGELOG updated; §11.4 box ticked; §11.5 as-built entry written.
**Next:** T10c (§12.15) surfaces reconciliation (`costroid reconcile`) on the stored keys + the fetch path;
  T10b (§12.10) cuts v0.4.0 once T10c + the ⛔ legal review land.
```

### 12.15 — T10c · reconciliation display (`costroid reconcile`) · L · 📌 · Prereq: T10a ✅ (§12.14)

> **✅ DONE 2026-06-15 (gate green; no new ⛔ gate). Full record: §11.5 ✅ T10c.** Built as pinned below — the `costroid reconcile [--vendor anthropic|openai] [--period …]` subcommand on the injectable `AdapterSet::cost_report` seam (loopback-tested, zero real network), vendor-+window-scoped local rows from the existing `focus_records_from_local_logs` pipeline, the honest pure-core renderer (signed variance, typed absence never `$0`, footnoted caveats, estimate-labeled) snapshot-tested in braille/ascii/plain. Deviations: `run_reconcile` takes an explicit `DateRange`; `FocusRecord` re-exported from `costroid-core`; the renderer lives in `render.rs` (pure over the core type) — see §11.5.
>
> **As-pinned (2026-06-13).** From the ⛔-signed-off `docs/proposals/T10-PIN-PROPOSAL.md` §5. Surfaces the T9c engine (`costroid-core::reconcile`, §11.5 ✅ T9c) — no new secret/network boundary beyond T10a's (it reuses the connected key + the authorized client). The 📌 is resolved; no ⛔ gate new to this card.

```
**Goal:** surface T9c's estimate-vs-invoice reconciliation HONESTLY — a `costroid reconcile` subcommand
  that fetches a connected vendor's cost report, compares it to the local API-lane estimate per UTC day +
  model (via reconcile_cost), and renders signed variance with every typed caveat/absence intact.
**Spec:** docs/proposals/T10-PIN-PROPOSAL.md §5; DATA-MODEL "Reconciliation engine"; §11.5 ✅ T9c
  (reconcile_cost / LocalCostEstimate / CostReconciliation / DayReconciliation / ModelReconciliation /
  VendorBilled / BilledAbsence / ReconciledReportStatus); DESIGN-SYSTEM (reconciliation rendering,
  --plain, non-color cue).
**Files:** apps/cli/src/main.rs (clap: add Reconcile, #[cfg(feature="connect")]); apps/cli/src/render.rs
  (the reconciliation renderer + --plain/Ascii variants + insta snapshots); apps/cli/src/ (the reconcile
  command wiring: fetch via the adapter → vendor-scope the FOCUS rows → reconcile_cost → render);
  docs/DESIGN-SYSTEM.md (the as-built reconciliation component) + CHANGELOG.md.
**Pinned (proposal §5):**
  · `costroid reconcile [--vendor anthropic|openai] [--period day|week|month|year]`; no --vendor = every
    connected vendor (each section; gemini = "unavailable — no sanctioned static-key usage API").
  · VENDOR-SCOPE the local estimate (the T9c "scope to one vendor before building" rule): x_Tool
    claude-code → Anthropic, codex → OpenAI, cursor → EXCLUDED (no admin key, no invoice). Build
    LocalCostEstimate::from_focus_records over only that vendor's rows; reconcile against that vendor's
    CostReportOutcome (fetched via the adapter + the stored key; request COMPLETED-day ranges).
  · Render HONESTLY: signed variance (variance = local − billed → "+$X over" / "−$X under"; variance_pct
    ROUNDED at the render boundary — full Decimal precision is preserved upstream); TYPED vendor-absence as
    TEXT, NEVER $0 (DayNotCovered → "report doesn't cover this day"; ModelNotInReport → "not attributed by
    the vendor"; ReportUnavailable(reason) → the typed reason incl. NotConnected → "connect <vendor> first"
    + Gemini's pinned string); when absent, variance/variance_pct render "—". A LOCAL $0 against a real
    billed figure is a genuine "vendor billed a model Costroid never saw" signal — render it as real.
  · Caveats FOOTNOTED (survive on CostReconciliation.caveats + per-model confidence): priority_tier_absent
    → "Anthropic Priority-Tier spend isn't in this report — the bill may be higher"; per_model_derived_
    best_effort → "OpenAI per-model figures are best-effort (derived from line items)" + mark those rows.
    report = Unavailable(reason) → surface the local estimate day-by-day beside "vendor invoice
    unavailable: <reason>", never a fabricated delta.
  · DOLLAR (cost) reconciliation ONLY for T10c. If any TOKEN view is ever shown it MUST carry
    responses_api_coverage_unconfirmed ("OpenAI token counts may undercount Codex/Responses-API traffic") —
    but DEFER the token-side view (and that caveat) to a later card; T9c's cost-day totals are complete.
  · Local figure ALWAYS labeled an estimate (x_Estimated); never presented as the bill; hedged per the
    DESIGN-SYSTEM voice.
**Scope fence:** the reconcile subcommand + its renderer ONLY. NO new endpoint/parse (reuse the adapters +
  reconcile_cost). NO connect/disconnect/connections changes (T10a). NO TUI History/Models tab (Step 5).
  NO token-side reconciliation. NO rate-limit denominators. NO new secret/network boundary (reuses T10a's).
**Accessibility:** full --plain rendering; NON-COLOR cue for over/under + absence + caveat states; em-dash
  ASCII-fold at the render boundary; insta snapshots incl. --plain.
**⛔ Human gate:** none new beyond T10a's (no new secret/network boundary). (The ⛔ legal review + the
  release are T10b/§12.10.)
**Done when:** four-command gate green; `costroid reconcile` renders a fixture reconciliation in all render
  modes (driven by fixtures/loopback, ZERO real network — the adapter path is loopback-tested as in T9b);
  typed absence never shows $0; caveats survive on screen; --plain snapshot pinned; offline.rs both tiers +
  the DEFAULT strace tier stay green; DESIGN-SYSTEM + CHANGELOG updated; §11.4 box ticked; §11.5 entry
  written.
**Next:** T10b (§12.10) cuts v0.4.0 (after the ⛔ legal review) — the connections line ships.
```

### 12.16 — T10-LIVE-ROWS · deferred populated-row / probe-behavior live-confirm · S · ⛔📌 · Prereq: real API usage on Eren's connected org

> **A DEFERRED card, created 2026-06-13 by the T10 pin pass** to hold any ⛔ GATE 2b item that T10a's live-confirm could NOT verify for lack of real raw-API usage (the same reason T9b's populated-row checks were deferred — §11.5 ✅ T9b: Eren's org had no raw-API usage across a 30-day window). It is NOT a build task in the usual sense — it is a locked-criterion live-verification checklist, run by the human with their own admin key once real usage exists, that then flips/confirms the items and pins real-body regression fixtures. **It does not block T10c, but it (or its formal closure) is part of clearing ⛔ GATE 2b before T10b/0.4.0 ships.**

> **✅ COMPLETE — ⛔ GATE 2b CLEARED in T10a (2026-06-15); this card's purpose is fulfilled.** The 2026-06-14 partial run plus the 2026-06-15 completed-day capture closed every load-bearing item (boxes below all ticked): money units confirmed (Anthropic CENTS ÷100; OpenAI dollars verbatim), Responses-API/Codex coverage confirmed → `responses_api_coverage_unconfirmed = false`, the populated cost ($) rows parse with the verbatim APPENDIX-A bodies pinned as regression fixtures, the `/costs` probe `200`/`401` behavior confirmed, `line_item`/`currency` confirmed — and the **one real-shape finding** (OpenAI `amount.value` is a JSON string with scientific notation `0E-6176` and >28 fractional digits) was **fixed** in `UsdAmount::from_json_dollars_str` (the parser change this card explicitly predicted). The only items NOT separately captured — a real **403** body and history-depth/retention — are non-blocking (the 403 classifier branch + remediation copy stay unit-tested; dollar/token totals are unaffected), so nothing is re-deferred. Full record: §11.5 ✅ T10a.
>
> _(Historical: the 2026-06-14 partial — Anthropic Haiku + OpenAI `gpt-4o-mini` Chat + a Responses call read via the Org usage API — first confirmed the usage-side Responses coverage; the 2026-06-15 run captured the completed-2026-06-14-day cost bodies once the cost reports rolled over.)_

```
**Goal:** finalize the T9b/T10a live-confirm items that needed REAL raw-API usage to verify — the
  populated per-result-row parses, the OpenAI /costs probe behavior, and a real 401/403 — and pin verbatim
  real bodies as regression fixtures, so no money-bearing parse or auth classification ships
  documented-schema-derived-only.
**Spec:** §11.5 ✅ T9b (the standing follow-ups) + ✅ T9b ⛔ GATE 2b block; docs/proposals/T10-PIN-PROPOSAL.md
  §6 (the GATE-2b resolution table) + §2.2 (the OpenAI /costs probe).
**Files:** crates/costroid-connect/src/anthropic.rs + src/openai.rs (real-body regression fixtures, like
  T9b's parses_the_live_empty_results_envelope; flip caveats if confirmed); crates/costroid-core/src/
  vendor_report.rs (only if a real field shape forces a parser change — NOT expected); §11.5 (log the
  results); CHANGELOG.md if a caveat flips.
**Locked completion criteria (each: confirm with a real admin key against real usage, then pin a verbatim
  body fixture; NO change to the type shapes is expected — if one IS forced, that is a finding to log):**
  · [x] **Populated per-RESULT-row shapes — CONFIRMED 2026-06-15 (T10a) + real bodies pinned.** Anthropic
    cost_report `amount` (decimal-CENTS ÷100; `0.0045`+`0.047` cents = $0.000515) / `description` / `model` /
    `cost_type` / `service_tier`, and OpenAI `/costs` `amount.value` / `line_item` all parse on real bodies. The
    verbatim APPENDIX-A bodies are pinned as regression fixtures (`anthropic.rs::parses_the_live_gate2b_cost_rows`,
    `openai.rs::parses_the_live_gate2b_costs_rows_with_string_scientific_and_overlong_amounts`). **The predicted
    "parser change if a real field shape forces it" DID occur:** OpenAI `amount.value` is a JSON **string** with
    scientific notation (`0E-6176`) and >28 fractional digits (39 dp) — `UsdAmount::from_json_dollars_str` was
    hardened (strip-quotes → expand-scientific → rounding `FromStr`; never `f64`). Anthropic's cents path is unchanged.
  · [x] **OpenAI Responses-API coverage (Codex) — CONFIRMED COVERED 2026-06-14:** a Responses call (15 in / 27 out)
    surfaced in `usage/completions` in the same bucket as the Chat call (total `num_model_requests: 2`, 30 in / 57 out)
    → `responses_api_coverage_unconfirmed` set to **false** (flipped in T10a) + T10c's token-undercount caveat **dropped**.
    (Cost day-totals were complete regardless; this confirms the token side is complete too.)
  · [x] **401 — CONFIRMED 2026-06-15:** a non-admin key on `/costs` → **HTTP 401** (the 401→`AuthenticationFailed`
    classifier branch validated on real data; `openai.rs::costs_401_from_non_admin_key_is_authentication_failed`).
    The 403 branch stays documented-schema-derived (no under-scoped key was available to elicit a real 403) — its
    classifier branch + remediation copy are unit-tested (Anthropic individual-account 403; OpenAI member-not-owner
    403); a real 403 body is a non-blocking observation (dollar/token totals are unaffected).
  · [x] **OpenAI `/costs` PROBE behavior — CONFIRMED 2026-06-15:** a completed 1-day window returns **200** on a good
    admin key (the populated rows above), **401** on a non-admin key. The connect-time probe (`fetch_cost_report` over
    `completed_day_window()`) requests a completed day, so a post-auth `400` on a current/future window does not arise
    in practice (the "request completed-day ranges" contract holds); the 400→`RequestRejected{400}` branch remains
    unit-tested (Anthropic's documented completed-day 400).
  · [x] **`line_item` / `currency` — CONFIRMED 2026-06-15:** `line_item` = `"<model-id>, <direction>"` (e.g.
    `"gpt-4o-mini-2024-07-18, input"` / `", output"` / `", cached input"`) — the "text before the first comma"
    best-effort per-model parse yields the model id. `currency` = OpenAI `"usd"` (lowercase) / Anthropic `"USD"`
    (uppercase); the adapters ignore the case (they don't assert on currency). History depth/retention is a
    non-blocking observation, not measured.
**Scope fence:** live-verification + real-body regression fixtures + caveat flips ONLY. NO new endpoints,
  NO new features, NO CLI/render change beyond a copy tweak if a caveat flips. Tests still NEVER hit the
  real network (the live run is the human's one-off; only its captured bodies become committed fixtures).
**⛔ Human gate:** this IS the human-run live-confirm (the one allowed use of a real admin key + real data,
  per §11.5 ✅ T9b) — not an automated test. Each item is checked off only on a real confirmation.
**Done when:** every box above is either confirmed (with a pinned real-body fixture + any flipped caveat)
  or, if real usage for that lane still doesn't exist, explicitly re-deferred with the reason logged in
  §11.5 — and ⛔ GATE 2b is recorded as fully cleared. (Partial closure is allowed: confirmed items ship;
  unconfirmable-for-no-usage items stay deferred here, which is itself the §6 "formally deferred" path.)
**Next:** with GATE 2b fully cleared, T10b/0.4.0 has no outstanding live-confirm debt.
```

---

## Step 5 cards (T11–T17) — analytical tabs + alerts · PINNED + carded 2026-06-16 (pins in §11.5 "📌 STEP 5 PINNED")

> **Shared structural note for T11–T16 (do not restate per card).** The TUI has **no tab model** today — `Screen` has 3 variants and `Tab` is a hardcoded 2-way `match` (`apps/cli/src/tui.rs:42,186-191`), with `Frontier` an `a`/`esc` overlay (tui.rs:224-241). **T11 lands the tab-model refactor** the rest inherit: replace the 2-way `match` with **numbered `1`–`6` direct jumps + a `Tab`/`BackTab` cycle** (Q1-confirmed; Frontier stays its own overlay), and update the footer nav (tui.rs:318-340) + `draw_help` (tui.rs:456-475) to enumerate tabs. Each later tab then adds: a `Screen` variant (tui.rs:42), a `document_for_width` arm (tui.rs:273-316), a footer label, a help line, and a `render_<tab>_document` (braille/ascii/plain split per `render.rs:1138-1153`) included in `plain_mode_output_is_pure_ascii` / `ascii_mode_output_is_pure_ascii` (render.rs:3240,3310). **Step 5 adds NO new network** — pure-local analytics over the cached point-in-time `EngineSnapshot`; the only optional connect path is Budget's invoice-true enrichment via the EXISTING user-initiated `AdapterSet::cost_report` seam (`apps/cli/src/reconcile.rs:233`), feature-gated + off by default. Advisory tabs (Models/Forecast/Anomalies) stay **monochrome** (amber/red reserved for the near-limit/over-budget state, which always carries a non-color cue).

### 12.17 — T11 · Providers tab · M · Prereq: T3 ✅ (Capability shipped)

```
**Goal:** ship the Providers analytical tab — the FIRST production consumer of the `Capability`
  descriptor (today only tests call `capability()` — providers/src/lib.rs:276, core/src/lib.rs:3116).
  Renders, per provider (Claude Code / Codex / Cursor), each data lane's honest source + auth + quota
  shape + detection health, and (under --features connect only) connection state — i.e. *what's
  available, what's unavailable, and why*. Also lands the tab-model refactor T12/T13 inherit.
**Spec:** docs/PRODUCT-PLAN.md §2b + §11.5 "📌 STEP 5 PINNED"; DESIGN-SYSTEM §Accessibility (--plain +
  no-color-alone); honesty invariant — a lane with no clean source declares DataSource::Unavailable,
  never fabricated (providers/src/lib.rs:237-241). Cursor = auth:None, detect-only, permanently
  unavailable (NOT "coming soon"); map it to ProviderStatusKind::Detected, not Missing (core/src/
  lib.rs:1024-1029).
**Files:** apps/cli/src/tui.rs (Screen::Providers variant + tab-model refactor of the 2-way Tab match at
  186-191 → numbered 1–6 jumps + Tab/BackTab cycle; footer + draw_help enumeration); apps/cli/src/
  render.rs (new render_providers_document, braille/ascii/plain — mirror the render_*_document split);
  crates/costroid-core/src/lib.rs (capture capability() into a NEW owned per-provider view alongside
  ProviderStatus BEFORE the Box<dyn Provider> set is consumed at :178/:183 — Capability is Copy but its
  &'static quota_kinds blocks Deserialize, so project to an owned ProviderCapabilityView); apps/cli/src/
  connect.rs reuse (under #[cfg(feature="connect")]: the is_connected && store.retrieve dual gate at
  :205, OrgLabel, the pinned GEMINI_UNAVAILABLE_MESSAGE).
**Scope fence:** the Providers tab + its render fn + the minimal core seam to surface capability() +
  the numbered-tab refactor ONLY. NO new core analytics. NO config file (T14). NO new network — the
  connect lane is read-only over the EXISTING keychain/registry (ConnectionRegistry/CredentialStore),
  and the default build (connect off) must render local Capability/ProviderStatus alone and degrade
  gracefully (mirror connect.rs being entirely #[cfg(feature="connect")]). NO Cursor live-quota work
  (discovery-gated §8). NO scroll state (T13).
**Deliverables:** Screen::Providers + the numbered-tab refactor (replace tui.rs:186-191 with 1–6 jumps +
  Tab/BackTab; footer label tui.rs:318-340; help line tui.rs:456-475); render_providers_document with
  author-written human copy for DataSource (LocalArtifact→"from local logs", SanctionedHook→"from the
  statusLine capture; run setup-statusline", SanctionedOauth/ApiKey→"via your connected key",
  Unavailable→"no sanctioned source") + AuthMethod, phrased to match cursor_detected_message (core/src/
  lib.rs:288-303); the owned ProviderCapabilityView core seam (infallible — capability() is infallible;
  no unwrap/expect/panic); the connect-gated connection lane (org label + connected/not only — NEVER key
  material; Gemini reuses the pinned string verbatim, not paraphrased); braille + ascii + plain snapshots
  + inclusion in plain_mode_output_is_pure_ascii / ascii_mode_output_is_pure_ascii (render.rs:3240,3310).
**Done when:** `cargo build/test --workspace` green (connect OFF default — tab renders local Capability/
  ProviderStatus, no connect symbols linked); `--features connect` builds and the connection lane renders
  org label + connected state via the dual gate; clippy/fmt clean; no unwrap/expect/panic in library
  code; Cursor renders Detected + "no sanctioned source" (both Unavailable lanes), never "coming soon";
  braille/ascii/plain snapshots committed and the ASCII-purity gates pass; the new tab is reachable via
  its number key + Tab cycle + appears in help + footer.
**Next:** the tab-model + render seam are in place → T12 (Models) and T13 (History) slot in cheaply; the
  deferred Copilot/Antigravity adapters (§8) render through this same tab by filling Capability.
```

### 12.18 — T12 · Models tab · S · Prereq: T11 (tab model)

```
**Goal:** a per-model tab fusing API spend + token mix with the bench/frontier overlay (cost-vs-quality
  standing + repricing), un-benchmarked models shown as gaps never guessed.
**Spec:** §11.5 "📌 STEP 5 PINNED" + the as-built **§11.5 ✅ T11** entry (the landed tab template — read
  it: a new tab = a `Screen` variant + a `TAB_SCREENS` entry filling a reserved `4`–`6` slot + a
  `document_for_width` arm + footer/help label + a `render_<tab>_document`, with NO further `handle_key`
  change); the shared structural note above §12.17; advisory rows are API-cost ONLY (frontier is
  API-only, ARCHITECTURE §9.6). Verify every symbol in code (canon) before relying on it.
**Files:** apps/cli/src/tui.rs (`Screen::Models` variant + a `TAB_SCREENS` entry (slot 4) + a
  `document_for_width` arm + footer/help label — the exact T11 pattern); apps/cli/src/render.rs
  (`render_models_document` braille/ascii/plain split + a `models_document_is_monochrome` guard — mirror
  T11's `providers_document_is_monochrome`, the closest landed template, or `frontier_document_is_monochrome`;
  mind T11's "push_rule is skipped in Plain mode" gotcha so the plain render delimits by labels, not the
  `─` glyph); crates/costroid-core/src/lib.rs (a small pure composite view fn that keys API-lane spend by the
  RESOLVED catalog key — the SAME `resolve_key` + `AggregateTotals::add_row` grouping `bench_view` uses, so a
  model's dated fragments merge to one row — and joins 1:1 to the bench OverlayModel by `model_id`; reuse
  `bench_view` + `resolve_key`, NO new pricing/bench math; bench.rs:278-355).
**Scope fence:** the tab + its render fn + the per-model composite view ONLY. NO new pricing/bench math
  (reuse bench_view). NO network. NO change to the tab-model machinery T11 landed (only APPEND a
  TAB_SCREENS entry). Stay monochrome (amber/red reserved for limits).
**Deliverables:** `Screen::Models` arm + the appended `TAB_SCREENS` entry (so it's reachable by `4` + the
  Tab cycle); the composite per-model view fn; `render_models_document` with the format_money `~` estimate
  hedge; the monochrome guard; braille/ascii/plain snapshots + inclusion in the `*_mode_output_is_pure_ascii`
  gates.
**Done when:** workspace green; clippy/fmt clean; no unwrap/expect/panic; per-model rows show spend +
  tokens + frontier standing; un-benchmarked models are gaps not guesses; the tab is monochrome;
  snapshots committed; ASCII-purity passes; the tab is reachable by number + cycle.
**Next:** the cheapest re-cut done → T13 History; the same composite-view pattern serves apps/bar later.
```

### 12.19 — T13 · History tab · M · Prereq: T11 ✅ (tab model) + T12 ✅ (render/scope-label precedents)

```
**Goal:** a scrollable per-turn FocusRecord history list — "the full record" — with FOCUS export
  surfaced from the tab. Lands the TUI's FIRST scroll/viewport state (the analytics tabs reuse it).
**Spec:** §11.5 "📌 STEP 5 PINNED" + the as-built §11.5 ✅ T11 (the landed tab template) + ✅ T12 (the
  render_<tab>_document braille/ascii/plain split, the on-screen scope/count header label, the
  "push_rule is skipped in Plain mode" gotcha, the monochrome guard); DATA-MODEL §210
  (FocusExportEnvelope) + the stable-header-even-for-zero-rows export contract (costroid-focus). Verify
  every symbol/line in code (canon) — T11/T12 shifted line numbers, so don't trust the refs below blindly.
**Files:** apps/cli/src/tui.rs (`Screen::History` variant + a `TAB_SCREENS` entry filling **slot 5**
  (T12 took slot 4) + a `document_for_width` arm + footer/help label — the exact T11 template; PLUS the
  NEW piece: a scroll/viewport **offset field on `App`** + PgUp/PgDn/Up/Down (+ Home/End) keys + a
  `draw_app` viewport clamp — there is NO scroll offset today, `draw_app` renders the whole
  `StyledDocument` as one wrapped `Paragraph`, so add a real clamped offset); apps/cli/src/render.rs
  (`render_history_document` over `snapshot.focus_rows`, braille/ascii/plain split, a count/scope header
  like T12's, monochrome); reuse the FOCUS emitters `export_focus_json`/`export_focus_csv`
  (costroid-core — verify the exact paths) — do NOT re-implement parsing/serialization.
**Scope fence:** the History tab + the new scroll state + surfacing export ONLY. NO new export schema
  (reuse `FocusExportEnvelope`). NO network. Read `x_ConsumedTokens` for usage, NEVER `ConsumedQuantity`
  alone. Rows come from the SAME `focus_rows` pipeline now/trends/export use (no re-parse). **The
  standalone `costroid export` command STAYS UNCHANGED** — it is a shipped public surface: do NOT
  move/remove/alter its flags or behavior; the tab REUSES the same emitters, it does not replace the
  command (touching the `export` CLI surface is a ⛔ — don't). Stay monochrome (History is informational;
  amber/red reserved for limits). NO period-key globalization — `d`/`w`/`m`/`y` stay Trends-gated; if you
  think History needs time-windowing, STOP and ask, don't repurpose the Trends keys.
**📌 STOP-and-ask:** an IN-TAB export-to-FILE (writing a file from inside the live TUI) needs an
  unpinned target-path + confirmation UX. Default deliverable = the read-only scrollable list + surface
  the existing `costroid export` in the tab's help/footer. Implement an in-tab export keybinding ONLY if
  it writes to an obvious CWD file with an on-screen status line AND is non-surprising — otherwise STOP
  and ask the human for the export-from-TUI UX. Either way `costroid export` keeps working unchanged.
**Deliverables:** `Screen::History` arm (reachable by `5` + the Tab/BackTab cycle) + the `App`
  scroll-offset field + keys + the `draw_app` viewport clamp; `render_history_document` (newest-first
  rows, a count/scope header, the shared `filter` applied, --plain ASCII + non-color cue); braille/ascii/
  plain snapshots + inclusion in the `*_mode_output_is_pure_ascii` gates; the FOCUS export surfaced per
  the STOP-and-ask above.
**Done when:** workspace green (default + `--features connect-test-support`); clippy/fmt clean; no
  unwrap/expect/panic; rows scroll (PgUp/PgDn/arrows), clamped at both ends with NO panic on an empty or
  short list; **`costroid export` STILL emits valid FOCUS 1.3 unchanged** (incl. the zero-row header);
  usage never dropped; the tab is monochrome; snapshots committed; ASCII-purity passes; the tab is
  reachable by number `5` + the cycle + appears in help/footer.
**Next:** the three cheap re-cuts (T11–T13) ship → the analytics tabs (T14 Budget → T15 Forecast → T16
  Anomalies) build on the tab model + the scroll machinery this lands; T17 Alerts closes Step 5.
```

### 12.20 — T14 · Budget · L · 📌 RESOLVED (§11.5: TOML config) · Prereq: T11 ✅ (+ T12/T13 templates) · **use workflows (L)**

> **📌 RESOLVED (Eren-confirmed 2026-06-16, §11.5):** the FIRST user-config file = **TOML** at
> `${XDG_CONFIG_HOME:-$HOME/.config}/costroid/config.toml`, owned by `apps/cli`, non-secret (never
> keychain), forward-compat serde. Schema: `[budget] total_monthly_usd` (optional) + `[budget.per_tool]`
> keyed by **tool** (the `x_Tool` ids `claude-code`/`codex`/`cursor`), money as `rust_decimal::Decimal`.
> Adds the permissive `toml` crate (pre-approved here; deny-allowlist-clean). **API-lane only — NEVER a $
> target for a flat-fee subscription** (§170). Introducing the first config file + the `toml` dep is the
> only build-time heads-up (already human-approved — keep the schema EXACTLY as pinned; if you must
> deviate, STOP and ask).
>
> **Slot + scoping (read before building):** Budget fills **`TAB_SCREENS` slot 6** — the LAST
> digit-reachable slot (`handle_key` already matches `'1'..='6'`, so NO `handle_key` change; T15/T16 at
> slots 7/8 will extend that range, not you). **T14 config is READ-ONLY:** load the TOML the user
> hand-edits; build NO writer/saver and NO `budget set` command (that's a CLI-surface ⛔ + unpinned UX —
> not T14); config absent ⇒ today's zero-config behavior + an honest "no budget set" state. **NO network /
> NO invoice enrichment:** the Budget tab compares against the **local API-lane estimate** (always
> `~`-hedged) — the invoice-true comparison already lives in `costroid reconcile` (T10c); putting a
> connect fetch in the local-only TUI render loop is out of scope (deferred, not dropped).

```
**Goal:** a Budget tab comparing user-set monthly $ target(s) against ACTUAL API-lane spend (the local
  `~`-estimate), with a fill bar + pace cue + an honest over-budget state. Introduces the FIRST
  user-config file (read-only TOML).
**Spec:** §11.5 "📌 STEP 5 PINNED" (Q2) + the as-built §11.5 ✅ T11/T12/T13 (the landed tab template, the
  on-screen scope/count header, the monochrome-EXCEPT-this-tab note below, the render_<tab>_document
  split, the "push_rule skipped in Plain" gotcha); §170 (never a $ budget for a flat-fee subscription);
  the connections.json forward-compat/atomic idiom (costroid-connect) as the persistence shape reference
  (but CONFIG dir, non-secret, read-only here). Verify every symbol/line in code (canon) — T11–T13
  shifted line numbers.
**Files:** apps/cli (NEW config module, e.g. `apps/cli/src/config.rs` — the XDG_CONFIG_HOME path resolver
  + the serde config struct + a LOADER (absent file ⇒ default, malformed ⇒ a clear non-crash error);
  `toml` dep in apps/cli/Cargo.toml); crates/costroid-core (a pure, **config-neutral** budget-vs-actual
  compute that takes the targets as INPUT — core never reads a file: a `BudgetTargets` input + a
  `budget_view(snapshot, &targets) -> BudgetView` over the `CostLane::Api` per-tool/total spend);
  apps/cli/src/render.rs (`render_budget_document` braille/ascii/plain; reuse `meter_segments`
  (render.rs:2656) / `cost_bar_span` (:2605) / `positional_meter_text` (:2622) + `WARN_FRACTION 0.80` /
  `CRITICAL_FRACTION 0.95` (:31-32); add an over-cap textual cue — `meter_segments` clamps to 1.0, so
  >100% needs a spelled-out `OVER` cue); apps/cli/src/tui.rs (`Screen::Budget` + a `TAB_SCREENS` slot-6
  entry + a `document_for_width` arm + footer/help label — NO `handle_key` change, `'6'` already maps).
**Scope fence:** the read-only budget config + the config-neutral core compute + the tab ONLY. API/overage
  lane ONLY (never subscription $; a flat-fee subscription gets NO $ target). NO writer/saver, NO
  `budget set` command (CLI-surface ⛔). NO network / NO invoice enrichment in the tab (use the LOCAL
  estimate; `costroid reconcile` already does invoice-true). NO `handle_key`/tab-machinery change beyond
  appending slot 6. Decimal money, never f64. Config absent ⇒ zero-config default.
**Deliverables:** the config path resolver + serde struct + LOADER (forward-compat `#[serde(default)]`,
  absent ⇒ default, malformed ⇒ a typed non-panic error surfaced as a status line, never a crash); the
  config-neutral `BudgetTargets`/`BudgetView` + `budget_view` core fn over the API lane (per-tool keyed
  by `x_Tool`, + an optional total), every figure `~`-hedged; the flat-fee guard (a subscription-only
  tool never gets a $ comparison); `render_budget_document` — a fill bar per budget (used vs target),
  a pace cue, and the over-cap cue: **amber/red IS allowed on this tab (the near/over-budget state) but
  ONLY paired with a non-color textual cue** (`!`/`!!`/`OVER`, spelled out in `--plain`), with a --plain
  `$X / $Y budget (over by $Z)` line; the honest "no budget set — set targets in
  ~/.config/costroid/config.toml" empty state; braille/ascii/plain snapshots (incl. a no-budget and an
  over-budget snapshot) + inclusion in the `*_mode_output_is_pure_ascii` gates; `cargo deny` green with
  `toml` added.
**Done when:** workspace green (default + `--features connect-test-support`); clippy/fmt clean; no
  unwrap/expect/panic; budget compares the API lane ONLY and never assigns a $ target to a flat-fee
  subscription; the config loads (absent ⇒ zero-config default, malformed ⇒ a clear non-crash status);
  over-budget renders the non-color `OVER` cue; the default build adds NO network call (strace/offline
  unaffected — `toml` is parse-only); `cargo deny check` passes (toml allowlist-clean); snapshots +
  ASCII-purity pass; Budget reachable by `6` + the Tab cycle + in help/footer.
**Next:** the config layer exists → T15 Forecast / T16 Anomalies (the next analytics tabs — they'll need
  to extend the `'1'..='6'` digit range to reach slots 7/8) → T17 Alerts EXTENDS this config for
  thresholds/opt-in and closes Step 5.
```

### 12.21 — T15 · Forecast · L · 📌 RESOLVED (§11.5: linear run-rate) · Prereq: T11 ✅ (+ T12/T13/T14 templates) · **use workflows (L)** · *slot/line audit 2026-06-17*

> **📌 RESOLVED (technical, §11.5):** $ projection = linear run-rate over the elapsed month
> (`spend_to_date / days_elapsed × days_in_month`) off a shared per-UTC-day API-lane $ series helper
> (generalize the bucketing in `LocalCostEstimate::from_focus_records`, `crates/costroid-core/src/reconcile.rs:106`);
> quota projection = linear burn from the current `LimitMeasure::TokenFraction` to `resets_at`. Both labeled
> estimates; the $ projection is suppressed below a 3-day min-data floor; the quota ETA degrades to
> unavailable on an Unverified/Estimated/stale reading (ARCHITECTURE §9.2). Revisit only if Eren wants a
> different method.
>
> **Slot + scoping (read before building — verified in code 2026-06-17, canon = code):** Forecast is
> `TAB_SCREENS` **slot 7** — the FIRST tab past the original digit range, so T15 (unlike T14) **MUST extend
> `handle_key`'s `KeyCode::Char(ch @ '1'..='6')` → `'1'..='7'`** (`apps/cli/src/tui.rs:270`) and bump the
> footer hint string `"1-6/tab switch | a frontier"` → `"1-7…"` (`tui.rs:495`); also true the `TAB_SCREENS`
> doc comment (`tui.rs:56`) that today says "tabs (T15/T16 at slots 7/8) extend…". **Consistency trap:** the
> reconcile series buckets by **UTC day** (`charge_period_start.date_naive()`), but `period_range_for(Period::Month, …)`
> /budget bucket by **LOCAL** month boundaries — the $ run-rate's spend-to-date numerator AND its
> days-elapsed/days-in-month denominator MUST come from the SAME calendar (don't mix a UTC-day sum with a
> local-month elapsed fraction). Pick one calendar; it's a `~`-hedged estimate either way. **Quota ETA source:**
> ride `now_summary(snapshot, NowOptions::default()).limits` (each `LimitSummary` carries `kind: LimitKind` +
> `availability: LimitAvailability`) — that path already runs the sanitize/cross-check/stale-age-out ladder
> (`limit_summary`/`limit_availability`, `lib.rs:937/1061`); do NOT re-derive off raw `LimitWindow`. Project an
> ETA ONLY off `LimitAvailability::Available { measure: LimitMeasure::TokenFraction(f), resets_at, reset_in_seconds }`;
> every other arm (`Unverified`/`Estimated`/`Partial`/`Unavailable`, and any `Spend` measure) → "ETA unavailable"
> (stale is already aged-out to `Estimated` upstream, so "stale → unavailable" falls out for free). Window length
> for the burn math is `window_duration(kind)` (`lib.rs:1043`, same-crate).

```
**Goal:** a Forecast tab projecting "~$X projected API spend this month" + "hit your weekly Claude limit
  ~Friday", both hedged as estimates.
**Spec:** §11.5 "📌 STEP 5 PINNED" (T15 default) + the 📌 block above (slot 7, the consistency trap, the
  quota-ETA source + degrade rules); ARCHITECTURE §9.2 (degrade, never a confident wrong number); mirror the
  as-built §11.5 ✅ T14 (the `budget_view`/`ModelsView` view-shape: `Serialize`-only, not `Eq`, computed-
  never-persisted; the `render_<tab>_document` braille/ascii/plain split; the "push_rule skipped in Plain"
  gotcha). Verify every symbol/line in code (canon) — T11–T14 shifted line numbers.
**Files:** crates/costroid-core (a NEW pure-core forecast view — mirror `budget_view`'s config-neutral
  `pub fn forecast_view(snapshot: &EngineSnapshot) -> ForecastView` (lib.rs:253) over a NEW shared per-UTC-day
  API-lane $ series helper; the helper is `pub(crate)` so **T16 Anomalies reuses it** — generalize, don't
  duplicate, `LocalCostEstimate::from_focus_records`'s lane+date bucketing (reconcile.rs:106), and don't break
  reconcile's tests; keep the month math on the UTC calendar (`today.day()` + a `days_in_month_utc` helper) —
  do NOT reuse `period_range_for`/`period_elapsed_fraction` here, they are LOCAL-month and mixing a UTC-day sum
  with a local-month fraction is exactly the consistency trap above; reuse `window_duration` (core, same-crate)
  for the quota-burn window length); apps/cli/src/render.rs (`render_forecast_document` braille/ascii/plain split mirroring
  `render_budget_document` (render.rs:1782); reuse `sparkline` (render.rs:2962) / `braille_sparkline`
  (render.rs:2988) / `braille_scatter` (render.rs:1003) for the actual-vs-projected series — distinguish by
  glyph/dim style, NEVER color); apps/cli/src/tui.rs (`Screen::Forecast` enum variant tui.rs:43 + a
  `TAB_SCREENS` slot-7 entry tui.rs:58 + the `'1'..='7'` digit-range extension tui.rs:270 + a
  `document_for_width` arm calling `forecast_view` + screen_name/footer/help labels + the `tui.rs:56`/`:495`
  trueing above).
**Scope fence:** the shared daily-series helper + the projection algorithm + the tab ONLY. Pure-local, NO
  network, NO new dependency (so strace/offline + `cargo deny` are unaffected). Monochrome (advisory tab;
  amber/red reserved for the near/over-limit state — the forecast is advisory text + a sparkline, NOT a
  meter, so it needs none). Always hedged (`~`/estimated). The quota ETA rides `now_summary(...).limits` and
  degrades to unavailable per the 📌 rules, never a confident wrong ETA. NO change to scroll/tab machinery
  beyond appending slot 7 + extending the digit range. Decimal money, never f64.
**Deliverables:** the shared per-UTC-day API-lane $ series helper (`pub(crate)`, reused by T16); a
  `ForecastView` (Serialize-only, not Eq, computed-never-persisted) carrying an explicit estimate marker, a
  $-projection variant (projected month total + spend-to-date + days elapsed/in-month) vs an honest
  insufficient-data state below the 3-day floor, the per-day actuals for the sparkline, and per-window quota
  ETAs (a projected-at-instant variant vs a "resets before you hit it" variant vs a typed "ETA unavailable
  (reason)"); `forecast_view`; `render_forecast_document` — an actual-vs-projected distinction surviving
  `--plain` (a `projected ~$X by <date> (estimated)` line + per-window `<window>: projected to hit ~<weekday>
  (estimated)` / `<window>: ETA unavailable (<reason>)` lines) + the monochrome guard; braille/ascii/plain
  snapshots (incl. an insufficient-data snapshot AND an ETA-unavailable snapshot) + inclusion in the
  `*_mode_output_is_pure_ascii` gates; Forecast reachable by `7` + the Tab cycle + in help/footer.
**Done when:** workspace green (default + `--features connect-test-support`); clippy/fmt clean; no
  unwrap/expect/panic; the $ projection's numerator+denominator share one calendar and it is suppressed below
  the 3-day floor; every figure is a labeled estimate; the quota ETA projects ONLY off a Verified/fresh
  `TokenFraction` `Available` reading and is "unavailable" on every other arm (incl. Unverified/Estimated/
  stale/Spend); the tab is monochrome; the default build adds NO network call (`cargo deny`/strace/offline
  unaffected — no new dep); snapshots + ASCII purity pass; Forecast reachable by `7` + Tab + help/footer;
  tested on fixtures only.
**Next:** the per-UTC-day series helper is shared by T16 Anomalies (slot 8 — it extends the digit range to
  `'8'`); forecast signals feed T17 Alerts.
```

### 12.22 — T16 · Anomalies · L · ✅ **DONE 2026-06-17** (§11.5 ✅ T16 — built + independent-review-fixed; gate green, NO new dep) · 📌 RESOLVED (median+MAD; **2 signals — quota deferred**) · Prereq: T11 ✅ + T15 helper ✅

> **📌 RESOLVED (technical, §11.5):** baseline = median + MAD over the trailing **14 local days** of the
> user's OWN history, flag when `|value − median| > 3.5·MAD`; suppress below **7 days** of history;
> **history derived from the snapshot's already-parsed `focus_rows` each run (Q3 — NO new persisted
> store; `focus_rows` IS the parsed local logs).**
>
> **🔧 SHIP 2 SIGNALS — the quota one is DEFERRED (Eren-confirmed 2026-06-17, §11.5).** The original pin
> named three signals; the third — **quota burn-rate jump (`LimitMeasure` delta/day)** — is **NOT
> buildable from local data**: the Claude/Codex `rate_limits` caches persist a **single point-in-time**
> reading (one `captured_at`), so there is no multi-day quota-fraction series to difference. **Build
> exactly two**, both off `snapshot.focus_rows`: **(1) spend spike** (daily API-lane `billed_cost`) and
> **(2) model-mix shift** (per-day share-of-tokens per model). **Do NOT consult any quota reading** and
> do NOT invent a quota series — note the deferral in the empty-state/footnote honesty, not as a fake
> signal. (So the old "quota signals skip Unverified/Estimated" caveat is moot here.)
>
> **Slot + data reality (read before building — verified in code 2026-06-17, canon = code):** Anomalies is
> `TAB_SCREENS` **slot 8** — the LAST numbered slot, so it **MUST extend `handle_key`'s digit match
> `'1'..='7'` → `'1'..='8'`** (`apps/cli/src/tui.rs:275`), bump the footer hint `"1-7…"` → `"1-8…"`
> (`tui.rs:509`), and true the `TAB_SCREENS` doc comment (`tui.rs:56-58`, today reads "T16 (Anomalies)
> will take…"). The **two daily series both come from `snapshot.focus_rows`** (the full parsed local
> history, each row carrying `charge_period_start` + `x_consumed_tokens` + `x_model` + `x_access_path`):
> reuse `reconcile::api_lane_daily_usd_series(&focus_rows) -> BTreeMap<NaiveDate, Decimal>` (T15's
> `pub(crate)` helper, `reconcile.rs:168`) for the **$ spend** series, and add a **token-mix** per-day
> series bucketed by `(charge_period_start.date_naive(), x_model)` summing `x_consumed_tokens` (one new
> bucketer — keep ONE definition; `api_lane_day_rows` at `reconcile.rs:145` already yields `(date, model,
> $)` and is the generalization point). **MAD = 0 pitfall:** when the trailing values are near-identical
> the MAD is 0 — guard the `/MAD` (skip / require an absolute floor) so it neither divides by zero nor
> flags every day.

```
**Goal:** an Anomalies tab surfacing proactive, non-alarmist callouts vs the user's OWN recent history —
  TWO signals: a daily spend spike + a model-mix shift — each with magnitude + the compared window. (The
  quota-burn signal is DEFERRED — local data has no multi-day quota history; see the 📌 above.)
**Spec:** §11.5 "📌 STEP 5 PINNED" (T16 default + the 🔧 2-signal revision + Q3); ARCHITECTURE §9.2
  (confidence-respecting honesty); mirror the as-built §11.5 ✅ T15 (the pure config-neutral
  `forecast_view`/`ForecastView` view-shape: `Serialize`-only, not `Eq`, computed-never-persisted; the
  `render_<tab>_document` braille/ascii/plain split; the "push_rule skipped in Plain" gotcha). Verify
  every symbol/line in code (canon) — T11–T15 shifted line numbers.
**Files:** crates/costroid-core (a NEW pure-core anomaly engine — `median`/`MAD` helpers + a config-neutral
  `pub fn anomalies_view(&EngineSnapshot) -> AnomaliesView`, mirroring `forecast_view`; `bench.rs`'s
  `BaselineUnpriced` is a $0-unpriced placeholder, NOT a statistical baseline — not reusable; reuse
  `reconcile::api_lane_daily_usd_series` (reconcile.rs:168) for the $ series + add ONE token-per-(day,model)
  bucketer — don't duplicate the lane/date logic in `api_lane_day_rows` (reconcile.rs:145)); apps/cli/src/render.rs
  (`render_anomalies_document` braille/ascii/plain split mirroring `render_forecast_document` (render.rs:2034);
  a non-color marker surviving `--plain` — `*`/`!`/text, NEVER color; the proactive voice mirrors
  `insight_line` (render.rs:3493)); apps/cli/src/tui.rs (`Screen::Anomalies` + a `TAB_SCREENS` slot-8 entry +
  the `'1'..='8'` digit-range extension (tui.rs:275) + a `document_for_width` arm calling `anomalies_view` +
  screen_name/footer/help labels + the tui.rs:56-58/:509 trueing above).
**Scope fence:** the median+MAD baseline + the TWO signal detectors + the tab ONLY. Baseline is vs the
  user's OWN local history (`snapshot.focus_rows`), pure-local, NO network/external baseline, NO new
  persisted store. **Exactly two signals — NO quota signal, consult no `LimitWindow`/quota reading.**
  Proactive, never alarmist (conservative k=3.5). Monochrome. Decimal money, never f64. Fixtures only, no
  real user data. NO tab-machinery change beyond appending slot 8 + extending the digit range.
**Deliverables:** pure `median`/`MAD` helpers (no-panic on empty/odd/even lengths, MAD=0 guarded); the
  trailing-14-day baseline over BOTH series (from `focus_rows`); the two detectors — spend spike (a UTC day
  whose API `billed_cost` is `>3.5·MAD` off the median) + model-mix shift (a model whose today's
  token-share is `>3.5·MAD` off its 14-day median share); an `Anomaly` event struct carrying the kind, the
  value, the baseline median, the deviation magnitude (e.g. `~3.2× your 14-day median`, hedged), and the
  compared window; an `AnomaliesView` (Serialize-only, not Eq, computed-never-persisted) with the 7-day
  min-history suppression (an honest "not enough history yet — N of 7 days" state) and an honest
  "no anomalies — usage in line with your 14-day norm" state; `render_anomalies_document` with `--plain`
  labeled lines + a non-color marker + the monochrome guard; braille/ascii/plain snapshots (incl. a
  below-floor AND a no-anomalies state) + inclusion in the `*_mode_output_is_pure_ascii` gates; Anomalies
  reachable by `8` + the Tab cycle + in help/footer.
**Done when:** workspace green (default + `--features connect-test-support`); clippy/fmt clean; no
  unwrap/expect/panic; anomalies are vs the user's OWN history, k=3.5 conservative, suppressed below the
  7-day floor; the median/MAD math is panic-free incl. MAD=0; **exactly two signals, no quota reading
  consulted**; every magnitude is a hedged estimate; tone is proactive not alarmist; the tab is monochrome;
  the default build adds NO network call (no new dep; strace/offline + `cargo deny` unaffected); snapshots +
  ASCII purity pass; Anomalies reachable by `8` + Tab + help/footer; tested on fixtures only.
**Next:** anomaly signals (the `Anomaly` events) feed T17 Alerts — the LAST Step 5 card, which closes 0.5.0.
```

### 12.23 — T17 · Alerts · L · ⛔📌 RESOLVED (posture + sources) · Prereq: T14 config · *symbol-audited 2026-06-17* · ✅ **BUILT 2026-06-17 — ⛔ AWAITING SIGN-OFF (see §11.5 ✅ T17 for the as-built + the sign-off package)**

> **⛔📌 Posture RESOLVED (Eren-confirmed 2026-06-16, §11.5 Q4):** delivery = an inline terminal banner
> (now/tabs) + a cron-friendly `costroid alerts --check` exit-code subcommand — both built-in, NO new
> dependency, NO daemon. **OS desktop notifications (notify-rust) DEFERRED behind a Cargo feature +
> config opt-in (off)** — they pull Linux D-Bus deps that re-touch the offline/forbidden-crates gates +
> the libdbus/precise-builds release wrinkle, so a future add takes a CONNECT_ALLOWED-style allowlist
> review. Default OFF/quiet; thresholds reuse `WARN_FRACTION=0.80`/`CRITICAL_FRACTION=0.95` (today at
> apps/cli **render.rs:33-34** — define the alert thresholds in CORE for the pure detector, numerically
> identical).
>
> **⛔📌 Sources RESOLVED (Eren-confirmed 2026-06-17):** alerts fire on **quota % crossings + budget $
> crossings ONLY**. Forecast projected-hit + anomaly callouts stay advisory on their own tabs — alerting
> on them is a **deferred fast-follow**, explicitly OUT of this card (so the build agent does not wire
> `forecast_view`/`anomalies_view` into the detector). **The ⛔ stays for the standard build-time review
> of the final CLI surface (`costroid alerts` + `--check`) + the alert copy strings.**

```
**Goal:** opt-in threshold alerts — a quota window crossing WARN/CRITICAL + a budget crossing its monthly
  $ target — surfaced as an inline banner + a cron-friendly `costroid alerts --check`. Quiet/OFF by
  default, no daemon, no telemetry, no new dependency. **Sources = quota % + budget $ ONLY (Eren-confirmed
  2026-06-17); forecast-hit + anomaly alerting are a DEFERRED fast-follow, NOT this card.**
**Spec:** §11.5 "📌 STEP 5 PINNED" (Q4 delivery + the copy rules) + the 2026-06-17 sources decision;
  §173-174 (alerts default off); DESIGN-SYSTEM §Voice; mirror the as-built §11.5 ✅ T14 (the config layer
  + the config-neutral core seam — core takes thresholds as INPUT, never reads a file, like
  `budget_view(snapshot, &BudgetTargets)`) and the connections.json atomic/0600 idiom (costroid-connect).
  Verify every symbol/line in code (canon) — T11–T16 shifted line numbers.
**Files:** crates/costroid-core (a NEW pure, **config-neutral** crossing-detector: an `AlertThresholds`
  INPUT + e.g. `active_alerts(&NowSummary, &BudgetView, &AlertThresholds) -> Vec<Alert>`. **Reuse the
  0.80/0.95 WARN/CRITICAL values** (today `WARN_FRACTION`/`CRITICAL_FRACTION` at apps/cli render.rs:33-34)
  — define the alert thresholds in CORE (e.g. `AlertThresholds` defaults) so the detector is pure-core;
  keep them numerically identical, don't fork a third set. The two classes **quota-% vs budget-$ are NEVER
  mixed**; quota alerts consume ONLY a fresh cross-checked reading — `LimitAvailability::Available` with a
  `TokenFraction`/`Spend` measure — NEVER `Unverified`/`Estimated`/`Partial`/stale (the T15 discipline);
  budget alerts ride `BudgetView`'s over-target state (`over_by_usd`/`pace`)); apps/cli (EXTEND the T14
  config — `apps/cli/src/config.rs` `Config` gains an `alerts: AlertsConfig` section, `enabled`
  default-FALSE, forward-compat `#[serde(default)]`, absent ⇒ off / malformed ⇒ the existing typed
  non-crash `ConfigError`; map it into the core `AlertThresholds`; a NEW `Command::Alerts(AlertsArgs)` clap
  subcommand in `apps/cli/src/main.rs` — the `Command` enum + a `match` arm; an inline-banner render
  component shown atop `render_now_document` (render.rs:1149) when enabled + crossing).
**Scope fence:** the crossing-detector + the `[alerts]` config + the `alerts`/`--check` command + the
  inline banner ONLY. **Sources = quota % + budget $ ONLY — NO forecast/anomaly alerting** (those stay
  advisory on their own tabs; deferred fast-follow). Default OFF. NO daemon. NO network/telemetry (inline +
  exit-code are inherently local). NO desktop notifications in the baseline (notify-rust deferred behind a
  Cargo feature + config opt-in). The two alert classes NEVER mixed. NEVER fire off an
  Unverified/Estimated/stale reading. Decimal money, never f64. NO new dependency.
**Deliverables:** the core crossing-detector (quota WARN/CRITICAL + budget over-target, config-neutral
  thresholds as input, the two classes never mixed); the `[alerts]` config schema (`enabled` default-off,
  optional per-class threshold overrides, forward-compat serde, extending T14's `Config`); `costroid
  alerts` (a human list with an honest "alerts off"/"no active alerts" state) + `costroid alerts --check`
  (exit 0 = clear, nonzero = a crossing, one printed line — cron-friendly); the inline banner (--plain
  ASCII + a non-color cue — amber/red allowed for the crossing state but ALWAYS paired with a textual cue,
  exactly like Budget); the copy strings per the voice rules (quota = quota-extension framing, NEVER "save
  money"; budget = dollars; sentence case, no emoji, one insight at a time); snapshots/tests for the banner
  (all 3 modes + an off/clear state) + the --check exit-code behavior.
**⛔ Human gate:** the new CLI surface (`costroid alerts` + `--check` — final flags + exit-code semantics) +
  the alert copy strings + the default-off behavior/threshold defaults — the build agent presents the FINAL
  flags, exit-code semantics, default-off proof, and the copy strings for sign-off before finalizing (the
  plan's only ⛔ Step-5 card; the delivery posture + the quota+budget sources are pre-approved per §11.5 Q4
  + the 2026-06-17 sources decision).
**Done when:** workspace green (default + `--features connect-test-support`); clippy/fmt; no
  unwrap/expect/panic; alerts default OFF and emit nothing networked (strace/offline still pass, NO new
  dep); `alerts --check` sets correct exit codes; the banner has a --plain + non-color form; quota copy
  never uses money-framing; NO alert fires off an Unverified/stale reading; **sources are quota+budget only
  (no forecast/anomaly firing)**; the two classes never mixed; snapshots + ASCII purity pass; tested on
  fixtures only.
**Next:** Step 5 COMPLETE (analytical tabs + alerts) → the v0.5.0 release cut (a chore-cut like 0.3.0
  unless the published crate set changes) → Step 6 = the egui taskbar (apps/bar) mirrors these tabs from
  core. (Deferred fast-follows: T16b all-lane model-mix; forecast/anomaly alerting.)
```
