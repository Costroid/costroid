# Costroid — Production Plan

*The single, executable build plan for Costroid as a cross-tool cost-and-quota cockpit — a terminal tool **and** an egui taskbar app, built on one shared core. Hand it to a build agent: it carries the current status (§0), the step-by-step sequence (§3), the auth model (§5), and the hard invariants (§6).*

> **Authority & relationship to canon.** This plan is the going-forward source of truth for *scope and sequencing*, and the canon is reconciled to it: [ARCHITECTURE.md](ARCHITECTURE.md) is the **technical** source of truth, and it plus [../CLAUDE.md](../CLAUDE.md) (the operating manual) defer scope/sequencing here. The **hard invariants in §6 are not superseded by anything** — they bind every step.
>
> **GUI choice:** the taskbar app is built in **egui / eframe (+ `tray-icon`)**, *not* Tauri — Rust-native, no webview, permissive licenses, shares `costroid-core` directly.
>
> **Deferred by instruction:** the Antigravity and GitHub Copilot **discovery checks and adapters are explicitly later** (§8) — the data model is generalized to *fit* them, but no adapter is built until a live-install discovery confirms its real shape.

---

## 0. Where we are today (v0.3.0 tagged; T8 landed, T9a built) — ground truth

Verified against the code, not the docs. (v0.2.0 — the cost lane: frontier + Cursor-detect + WSL fix — shipped 2026-06-05 across GitHub Release, Homebrew, npm, and crates.io; **v0.3.0 — Claude live quota end to end + the generalized quota model — tagged 2026-06-06**; T1–T8 done (T8 = the keychain credential store, 2026-06-09); T9a DONE 2026-06-10, ⛔-approved — see §11.5.)

**By lane** (the §1 spine — three lanes, never summed):

| Lane | State today | Evidence |
|---|---|---|
| **API cost ($) by model** | ✅ **Done** | FOCUS 1.3-conformant records, exact-Decimal `tokens × price`, bundled dated pricing (6 models), dedup verified to the cent vs ccusage |
| **Subscription quota (windows)** | ✅ **Done (Claude live quota end to end)** | Codex 5h + weekly parsed for real; **Claude end-to-end works** — T5's `setup-statusline` writer + `--capture-only` feed the T4 reader's sanitize + cross-check, and **T6 renders all 5 states on screen** (Available/Partial/Unverified/Estimated/Unavailable + `Spend` dollar pools, the `? unverified` cue, the "as of HH:MM" freshness stamp, the claude.ai caveat); **Cursor returns empty** by design |
| **Model quality (frontier)** | ✅ **Done** | `bench.rs`: DeepSWE + CursorBench, Pareto dominance, API-cost-only re-pricing overlay |

**Solid foundation the rest builds on:** three-crate engine (`apps → core → {providers, focus}`, no cycles, no `unwrap`/`expect`/`panic!` in libs); a working 5-method `Provider` trait (`id` / `capability` / `discover` / `parse_usage` / `parse_limits`); WSL-aware multi-root discovery; three render modes (braille / ASCII / **plain**) with non-color cues; `--live`; the statusline emitter; FOCUS export; and **enforced** invariants — a strace-based offline-acceptance CI job, a two-tier resolved-graph forbidden-crates test (since T7: the default build forbids ~44 networking/TLS/telemetry crates incl. the gated `ureq`/`rustls`/`keyring` trio; `--features connect` admits only that trio), `cargo-deny` (no copyleft, openssl banned), attested releases. **228 tests, 23 render snapshots, green CI gate** (counts as of the T9a review fix pass, 2026-06-10 — see §11.5). The cost lane is `cargo install`-able and correct today.

**Not built yet:** the *provider-facing* network half of connections (T9b — no usage-API adapter, no fetch; T9c — no reconciliation; no OAuth) and the `costroid connect`/`disconnect` CLI + Connections view (T10). **T8 built the keychain credential store** in `costroid-connect` (`CredentialStore`/`ConnectionRegistry`/`ApiVendor`, `keyring` sync Secret Service — DONE 2026-06-09, ⛔-approved) and **T9a built the generic authorized-host HTTP client** (`AuthorizedClient` on `ureq`+`rustls`, OS-native roots — DONE 2026-06-10, ⛔-approved), so secrets have a home and the HTTP layer exists — but the client has **no caller and no provider knowledge**, so nothing fetches and no build performs a network call until T10's explicit connect action; 6 of 8 tabs (Providers, Budget, Forecast, Anomalies, + Models/History as dedicated tabs — only Overview/now and Trends exist today); alerts; the taskbar; Antigravity & Copilot. *(Claude live quota **on screen** is now **done** — T4 landed the reader, T5 the writer, and **T6 the render**, so the captured quota surfaces end to end on a Pro/Max machine; see §11.5 ✅ T6 DONE. The generalized quota **shape** — `LimitKind`×5, `LimitMeasure`, `LimitStatus`, the reshaped `LimitAvailability` — landed in T2; its live **producers** in T4 (cross-check demotion + stale age-out + the `Estimated` fallback); its **rendering** in T6 (all 5 arms + `Spend`). The **`Capability` descriptor** — `DataSource`/`AuthMethod`/`Capability` + the required `capability()` trait method, declared by all three adapters — **landed in T3**; its consumer, the Providers tab (T11), is still future.)*
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
- **⛔ Legal review (before connections ship):** this is the step where the liability surface grows. Before 0.4.0 ships, get a quick **legal review of the connection flows** (own-key + sanctioned OAuth only) confirming they hit only provider endpoints the user authorized, store nothing outside the keychain, and induce no ToS violation. Not a code task — a human gate.

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

*Step 0 (canon reconcile) and Step 1 (v0.2.0, shipped 2026-06-05) are done, and the Claude `statusLine` capture is built end to end (T2–T5 ✅). **The 0.3.0 milestone is complete — T6 (render the new limit states + Spend windows) landed, so T2 + T4 + T6 are all green.** Claude live quota now surfaces on screen. **T7 is also done** — the feature-gated `costroid-connect` crate + the re-scoped no-network guarantee — so the 0.4.0 connections line is unblocked. **T8 (keychain credential store) is done** — gate green 2026-06-09, ⛔-approved (§11.5 ✅ T8); the next build is **T9 (HTTP usage-API clients + reconciliation)**.*

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

*Dependency-ordered. ⛔ = human gate · 📌 = pin before starting · S/M/L/XL = size. **T1 is independent; T2 is the lynchpin for all build work.** Cards **T1–T8 are all DONE** (T8: §12.9 / §11.5, gate green 2026-06-09, ⛔-approved); **T9a is DONE** (§12.11 / §11.5, gate green 2026-06-10, ⛔-approved); **T9b/T9c are now carded** (§12.12/§12.13 — T9b build-ready against the T9a client API as built; T9c real but with its T9b-dependent slots explicitly marked `[fill at T9b landing: …]` rather than fabricated); **T10+ remains a backlog** that gets expanded into full cards when its Prereq lands (except T10b, whose release mechanics are knowable; carded at §12.10) — its detail depends on decisions not yet made, and speccing it now would fabricate.*

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
- [ ] **T9b** the two usage-API adapters (Anthropic + OpenAI) + the Gemini first-class-unavailable state — **carded at §12.12** (2026-06-10, against the T9a client API as built; the next build task)
- [ ] **T9c** estimate-vs-invoice reconciliation engine (pure core) — **carded at §12.13** (T9b-dependent slots marked `[fill at T9b landing: …]`; fill them when T9b lands)
- T10+ — backlog (carded when its Prereq lands; T10b already carded at §12.10)

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
- **T10 — connect/disconnect CLI + Connections view** · ⛔📌 · Prereq T8,T9 — 📌 connect UX, reconciliation display · ⛔ **legal review of the connection flows before this ships** (own-key + sanctioned OAuth only — see Step 4). **Also finishes** the `scripts/offline_acceptance.sh` feature-on connect-ACTION network test (the connect action reaches only the authorized host · the secret lands only in the keychain · disconnect leaves no residue — replaces the `T9/T10` STUB at the script's tail). The 0.4.0 release itself is cut by **T10b**.
- **T10b — Release v0.4.0 (connections)** · ⛔ · S · Prereq T9, T10 + the ⛔ legal review signed off → **cuts v0.4.0** — the release-mechanics cap on Step 4 (the connections analogue of T1, which cut v0.2.0). Version bump 0.3.0→0.4.0 across the **four** `[workspace.dependencies]` constraints (now incl. `costroid-connect`; the CLI has no entry — the §11.5 T1 lockstep gotcha, with all **5** `version.workspace` members bumping together) + `Cargo.lock` + CHANGELOG + README/SECURITY release line; `dist plan` / host `dist build` dry-run; then the human tags + runs the **extended** crates.io ladder `focus → providers → core → connect → cli`. **Carded at §12.10.**
- **T11 Providers tab** (Prereq T3) · **T12 Models tab** · **T13 History tab** — cheap re-cuts
- **T14 Budget 📌 · T15 Forecast 📌 · T16 Anomalies 📌 · T17 Alerts ⛔📌** — 📌 budget persistence schema · forecast algorithm · anomaly baseline · alert thresholds + copy → **0.5.0**
- **T18+ — egui taskbar** · ⛔ · Prereq T2–T6 (CLI feature-complete) — greenfield: needs a GUI design first, then per-tab fan-out → **0.6.0**
- **Cursor live quota — discovery-gated (§8), not a numbered build task.** Pursued only if Cursor publishes a sanctioned/documented API or first-party OAuth (never session reuse against `api2.cursor.sh`); until then Cursor stays detect-only / "unavailable." Card it (like Copilot/Antigravity) only after that discovery lands.

When you reach a backlogged task, pin its 📌 and have a planning agent expand it into a full T1–T7-style card before you hand it to a build agent.

### 11.5 Decisions & limitations (living log)

*New decisions/constraints land here as tasks run — agents append (newest first), dated by the task that surfaced them. This is where "a new decision/limitation" goes.*

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

**✅ FIX PASS (2026-06-10, full 12-leg gate green) — a cross-cutting 35-finding remediation from a whole-repo review (no task card; correctness + guard-hardening + doc-currency, no new features, T9 untouched).** Files: all four lib crates + `apps/cli` + `offline.rs` + the three scripts + `ci.yml` + 6 new fixtures + `scripts/focus-ruleset/` + 6 docs. **209 tests** (was 190), 23 snapshots (12 updated in place). Highlights, worst-first:
- **Quota-integrity code fixes:** `--plain`/plain-statusline now carry the warn/critical textual cue on `Available`/`Partial` (in plain, the cue is the ONLY signal — a bare "97%" was a color-alone violation); an epoch-sentinel `captured_at` renders **"capture time unknown"**, never a bogus "as of 00:00" (Claude cache missing `captured_at`, or a timestamp-less Codex entry, while the reading stays usable); Codex `used_percent` is now **raw-range-sanitized** like Claude's (out-of-range ⇒ measure `None`, never a Verified "900% !!"); `choose_limit` keeps the **latest `captured_at`**, not the last-scanned root (multi-root staleness inversion; sentinel loses to any real stamp; ties keep scan order); `parse_codex_limits` parses the two windows **independently** (a lone `primary` no longer drops); **measure-carrying `Partial` arms now carry the freshness stamp** — this *revises the T6 "no stamp on Partial" decision*: a Verified reading with an unparseable reset maps to `Partial` forever (the `resets_at` age-out can never reach it), so without a stamp an arbitrarily old % rendered with zero age signal.
- **Frontier honesty:** an overlay model with ANY unpriced row gets `fully_priced = false` → every re-pricing delta is the new `RepricingStatus::BaselineUnpriced` (a labeled gap; the line renders "spend not fully priced (frontier comparison unavailable)") — never a `Computed` dollar delta against the $0 placeholder baseline.
- **Secret/robustness hardening (⛔ surfaces, applied per the fix-pass instruction; review in this diff before commit):** `ConnectError`'s `From<keyring::Error>` **scrubs `BadEncoding`'s raw secret payload** (the one keyring variant carrying stored-secret bytes; pinned by a test); unique per-writer temp names for the cache/registry/settings atomic writes (a fixed `.tmp` sibling could publish a torn file under concurrency); `settings.json` writes are now temp+rename; `clean_window` type-checks values (only a number pct / number-or-string reset can reach the no-secret cache); the path-1 snippet wraps the original in a **`{ … }` group** (multi-line and leading-`#` originals keep their stdin; old flat-form snippets still parse); `--undo` with no backup now **restores the original parsed from the snippet** instead of deleting the user's statusLine; plain `costroid statusline` (the installed command) degrades a collect error to a blank line + exit 0.
- **Guarantee surface (⛔):** `offline_acceptance.sh` + `focus_conformance.sh` now neutralize **`CODEX_HOME`/`CURSOR_DATA_DIR`/`XDG_STATE_HOME`** too (a set override could feed REAL user logs into "fixture" gate runs); the `--live` check FAILs on any unexpected exit code (a TUI crash passed as "ok"); `ALWAYS_FORBIDDEN_CRATES` += `minreq`/`tungstenite`/`tokio-tungstenite`/`websocket`/`ssh2`/`libssh2-sys`/`russh` (≈44 total; `socket2`/`mio` deliberately excluded — `mio` rides crossterm legitimately).
- **The FOCUS-conformance gate was passing VACUOUSLY in CI.** The PyPI `focus-validator` wheel (2.1.0, latest) bundles only the 1.2.0.1 model, so `--validate-version 1.3 --block-download` crashes with `UnsupportedVersion`; the script's `|| true` swallowed it and the checker passed on zero parsed FAIL lines. Fixed three ways: the checker **hard-fails** without a results summary + rule lines; CI pins `focus-validator==2.1.0`; and the official `model-1.3.0.1.json` is **vendored at `scripts/focus-ruleset/`** (from the FOCUS_Spec release assets; CC-BY-4.0 **data artifact for the dev/CI gate only** — never compiled into or shipped with any binary, so outside the crate-license policy; see its README) via `--rule-set-path`. The gate now runs a REAL offline 1.3 validation: 764 rules, 9 failures = exactly the 3 allowlisted defects + their cascades. Also confirmed upstream issue **#144 IS filed** (open) — the allowlist's "to be reported" comment was the stale side, trued up.
- **Smaller fixes:** an `msrv` CI job (Rust 1.88 `cargo check --workspace --all-targets`, per CLAUDE.md's "test the MSRV in CI"); `RenderMode::Ascii` output is now **pure ASCII** (`-` rule, `*` insight marker, `--` for the em-dash; pinned by an `is_ascii()` test); the trends-plain snapshot is timezone-proof (the test fixture now builds **local-midnight** bucket starts exactly like production's `start_of_period_local` — the whole suite passes under `TZ=America/New_York`; the Local date *label* is documented product behavior, so the fixture, not the formatter, was the bug); provider `Unavailable` windows carry descriptive labels ("no captured reading; run `costroid setup-statusline`" / "no rate-limit data in local rollout logs" / "no sanctioned source") — the redundant "unavailable: unavailable" render is gone; a zero-row CSV export now **emits the header line** (the documented export contract; ⛔ export surface — option (a), header-always, chosen as recommended); the **unused `costroid-providers → costroid-focus` dependency was removed** (providers emits the provider-neutral types; FOCUS normalization lives in core — CLAUDE.md/ARCHITECTURE dep-direction text trued up; the RELEASING.md publish ladder order is unchanged and still valid); the stale "wired later (T4/T6)" rustdoc on `LimitAvailability::Estimated` is past-tense.
- **Doc currency (code-canon truing):** the brief's §8 "(stale)"-stamp claim (never built — the aged-out arms carry no stamp by design) and §3 `config_root` definition (shipped rule = first existing root) corrected; DATA-MODEL now records that **`ProviderName`/`PublisherName` ARE emitted** (validator-presence requirement; ARCHITECTURE §6 too) and marks its `FocusRecord` listing an explicit **abridged subset** (the struct is the authority); DESIGN-SYSTEM marks `--format`/presets **planned** (shipped flags: `--capture-only`/`--wrap` only), "configurable" → fixed consts today, and the cost-bar remaining cells as the track glyph `⣀` (shape-distinct, not color-alone "dim ⣿"); ARCHITECTURE's `NO_COLOR` claim corrected (ANSI dropped, braille stays glyph-distinct; ASCII is the braille-incapable fallback) and the statusline "side-effect-free" claim scoped to interactive stdin (piped stdin captures — the ⛔-approved T5 path 2); CLAUDE.md's TOML config is marked a **planned** convention ("config" removed from the shipped v0.1.0 list — no config system exists); §0's test count (177→209) and forbidden-crates count (~37→~44) refreshed; §3 Steps 2 and 3 now carry their ✅ done markers; §12.7's wrapper-warning count corrected to 2-since-T8.
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
- **Linux backend denylist policy (⛔-signed-off; ⚠️ superseded as-built — see ✅ T8 DONE above).** keyring's Linux Secret-Service backend may pull a transitive IPC crate currently on the forbidden-crates list (e.g. an `async-io` D-Bus executor — *local IPC, not network egress*). Resolution *as pinned:* **permit it narrowly, scoped to the `costroid-connect` subtree under `--features connect`** — the default build stays clean (keyring unlinked) and the strace offline test still proves zero outbound; install `libdbus-1-dev` + `libsecret-1-dev` in CI (Step 4 §161). keyring uses pure-Rust crypto (`crypto-rust`), never openssl — which stays globally banned. **As built, the narrow permit was not needed:** the *sync* Secret Service backend pulls no `async-io`, so `async-io` stayed globally banned even under `--features connect` (Deviation 2 above).
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

## 12. Ready-to-paste task prompts (T1–T8, T9a–T9c + T10b release)

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

*T10–T18 aren't carded (except T10b, whose release mechanics are knowable; carded at §12.10) — they have open 📌 that must be pinned first. Paste §12.0 + this body, with `<ID>` filled, to turn a backlog task into a real card (don't build it yet):*

> **T9 status — this prompt has fully RUN for T9; do not re-run it.** The T9 pins were proposed + ⛔-signed-off 2026-06-10 (`docs/proposals/T9-PIN-PROPOSAL.md`, logged in §11.5), T9a is built (§12.11 ✅), and T9b/T9c are carded (§12.12/§12.13).

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

> **As-pinned (2026-06-09; two items ⛔ human-signed-off below).** T8 is **pure-library**: it ships *only* the OS-keychain credential store inside `costroid-connect` — **no new CLI, no network** (the `costroid connect`/`disconnect` UX + Connections view are **T10**; the HTTP fetch + reconciliation are **T9**). "API-key entry" here = the library *store* path, tested via `keyring`'s **mock** backend. Secrets are keyed by **billing vendor** (`Anthropic`/`OpenAI`/`Gemini`) — a different axis from `ProviderId` (the *tool*: ClaudeCode/Codex/Cursor) — via a small owned `enum ApiVendor`, so `costroid-connect` stays free of `core`/`focus` deps through T8 (those land in T9). **⛔-signed-off:** (1) **Linux backend** — the pin anticipated keyring's Linux Secret-Service backend pulling a transitive IPC crate (e.g. an `async-io` D-Bus executor; *local IPC, not network egress*) and authorized permitting it **narrowly, scoped to the `costroid-connect` subtree under `--features connect`**. **Superseded by the As-built result above:** choosing the *sync* Secret Service backend (`dbus-secret-service`, C libdbus) means `async-io` is pulled by **no** real build, so it was kept **globally banned even under `--features connect`** — the narrow permit was never needed (a stronger guarantee). `libdbus-1-dev`/`libsecret-1-dev` install in CI (Step 4 §161); the default build stays clean (keyring unlinked) and the strace test still proves zero outbound. (2) **Scope** — pure-library; CLI entry deferred to T10. Full detail in §11.5 *✅ T8 DONE* (Deviation 2 records the supersession).

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

### 12.10 — T10b · Release v0.4.0 (connections) · ⛔ · S · Prereq: T9 + T10 done + ⛔ legal review cleared

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
**⛔ Human gates:** (1) the **legal review of the connection flows** (Step 4) must be signed off before
  this ships; (2) public release — review, then **COMMIT the prep first** (the agent does NOT commit) so
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

### 12.13 — T9c · Estimate-vs-invoice reconciliation engine · M · Prereq: T9b (carded §12.12, not yet built)

> **Carded 2026-06-10 with the T9b-dependent slots explicitly marked `[fill at T9b landing: …]`** rather than fabricated — fill them (from the §11.5 ✅ T9b as-built entry) before handing this card to a build agent; everything else below is pinned now and stands.

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
  [fill at T9b landing: the module name/path T9b created]; fixture files (hand-built vendor-report
  + local-estimate pairs); docs/DATA-MODEL.md (reconciliation section → as-built shapes);
  CHANGELOG.md [Unreleased].
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
  · [fill at T9b landing: the vendor-report type names/fields the engine consumes];
    [fill at T9b landing: the caveat/unavailability representations as built];
    [fill at T9b landing: any live-confirm findings that bound comparability — e.g. OpenAI
    daily-bucket UTC alignment, and whether usage/completions covers Responses-API traffic
    (if NOT, the OpenAI token-side lane is labeled partial for exactly the Codex traffic)].
**Scope fence:** engine only — NO network, NO connect dep, NO CLI command/flag, NO rendering or
  TUI/statusline surface (T10's reconciliation-display 📌 owns surfacing), NO subscription
  plan-worth-it view, NO change to the FOCUS export schema (reconciliation output is its own
  shape, not new FOCUS columns — `x_Estimated` etc. stay as DATA-MODEL specs them).
**Tests (fixtures, no network):** fixture pairs prove: an exact-match day (zero delta, labeled
  estimate-vs-billed); under- and over-estimate days (signed variance); vendor-absent days
  (typed absence, never $0); the derived/best-effort and Priority-Tier caveats present on the
  relevant outputs; Decimal precision preserved (no float drift); [fill at T9b landing: fixture
  shapes mirror the as-built vendor-report types].
**Done when:** four-command gate green; the engine compiles with no connect edge (core's
  Cargo.toml gains nothing — the dependency direction holds by construction); fixtures cover
  every path above; DATA-MODEL's reconciliation section updated to as-built; CHANGELOG updated;
  §11.4 box ticked; §11.5 as-built entry written.
**Next:** T10 surfaces reconciliation (the display 📌) + wires connect/disconnect; then T10b cuts
  v0.4.0.
```
