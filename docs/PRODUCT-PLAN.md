# Costroid — Production Plan

*The single, executable build plan for Costroid as a cross-tool cost-and-quota cockpit — a terminal tool **and** an egui taskbar app, built on one shared core. Hand it to a build agent: it carries the current status (§0), the step-by-step sequence (§3), the auth model (§5), and the hard invariants (§6).*

> **Authority & relationship to canon.** This plan is the going-forward source of truth for *scope and sequencing*, and the canon is reconciled to it: [ARCHITECTURE.md](ARCHITECTURE.md) is the **technical** source of truth, and it plus [../CLAUDE.md](../CLAUDE.md) (the operating manual) defer scope/sequencing here. The **hard invariants in §6 are not superseded by anything** — they bind every step.
>
> **GUI choice:** the taskbar app is built in **egui / eframe (+ `tray-icon`)**, *not* Tauri — Rust-native, no webview, permissive licenses, shares `costroid-core` directly.
>
> **Deferred by instruction:** the Antigravity and GitHub Copilot **discovery checks and adapters are explicitly later** (§8) — the data model is generalized to *fit* them, but no adapter is built until a live-install discovery confirms its real shape.

---

## 0. Where we are today (v0.2.0) — ground truth

Verified against the v0.2.0 code, not the docs. (v0.2.0 — the cost lane: frontier + Cursor-detect + WSL fix — shipped 2026-06-05 across GitHub Release, Homebrew, npm, and crates.io; T1 done — see §11.5.)

**By lane** (the §1 spine — three lanes, never summed):

| Lane | State today | Evidence |
|---|---|---|
| **API cost ($) by model** | ✅ **Done** | FOCUS 1.3-conformant records, exact-Decimal `tokens × price`, bundled dated pricing (6 models), dedup verified to the cent vs ccusage |
| **Subscription quota (windows)** | 🟡 **Claude capture complete; render pending** | Codex 5h + weekly parsed for real; **Claude end-to-end capture works** — T5 landed the `setup-statusline` writer + `--capture-only`, feeding the T4 reader's sanitize + cross-check; only **T6 render** remains before the numbers reach the screen; **Cursor returns empty** by design |
| **Model quality (frontier)** | ✅ **Done** | `bench.rs`: DeepSWE + CursorBench, Pareto dominance, API-cost-only re-pricing overlay |

**Solid foundation the rest builds on:** three-crate engine (`apps → core → {providers, focus}`, no cycles, no `unwrap`/`expect`/`panic!` in libs); a working 5-method `Provider` trait (`id` / `capability` / `discover` / `parse_usage` / `parse_limits`); WSL-aware multi-root discovery; three render modes (braille / ASCII / **plain**) with non-color cues; `--live`; the statusline emitter; FOCUS export; and **enforced** invariants — a strace-based offline-acceptance CI job, a 36-crate forbidden-crates test, `cargo-deny` (no copyleft, openssl banned), attested releases. **164 tests, 17 render snapshots, green CI gate.** The cost lane is `cargo install`-able and correct today.

**Not built yet:** Claude live quota *on screen* — **T4 landed the reader** and **T5 landed the writer** (`setup-statusline` + `--capture-only`; capture now works end to end on a Pro/Max machine — see §11.5 ✅ T5 DONE), but the **T6 render** is still pending, so the captured quota does not yet surface to a user; any auth/connections (T7 — no keychain, no API-key entry, no OAuth); 5 of 8 tabs (Providers, Budget, Forecast, Anomalies, + Models/History as dedicated tabs); alerts; the taskbar; Antigravity & Copilot. *(The generalized quota **shape** — `LimitKind`×5, `LimitMeasure`, `LimitStatus`, the reshaped `LimitAvailability` — is **done**: T2 landed the types + pure availability map + migration; see §3 T2 / §11.5. Its live **producers landed in T4** (cross-check demotion + stale age-out + the `Estimated` fallback); **rendering is still T6**. The **`Capability` descriptor** — `DataSource`/`AuthMethod`/`Capability` + the required `capability()` trait method, declared by all three adapters — **landed in T3**; its consumer, the Providers tab (T11), is still future.)*
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

Dependency direction stays acyclic: `apps/cli → core → {providers, focus}`; `apps/bar → core → …`; `core → connect` (optional, feature-gated); `connect → focus`. **No crate except `costroid-connect` ever links a network or keychain dependency.**

### 2a. Generalize the quota window — the hard prerequisite (Step 3)

> ✅ **Landed in T2 (0.3.0 line).** The target shapes below are now the **shipped** types in `costroid-providers`/`costroid-core` (the "Today …" sentence is the pre-T2 motivation, kept for context). What remains is wiring producers (Claude live capture = T4) and the real rendering of `Spend`/`Unverified`/`Estimated` (T6); the legacy request-count measure stays cut. See §3 T2 and §11.5 (✅ T2 DONE).

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

### Step 2 — **0.3.0**: Claude statusLine capture *(flagship)* · *quota half becomes real*
- **Goal:** Claude live 5h/7d quota from the sanctioned `statusLine` `rate_limits` push — zero token reuse, zero API tokens, local only.
- **Deliverables:** implement [STATUSLINE-CAPTURE-BRIEF.md](STATUSLINE-CAPTURE-BRIEF.md) end to end — the `setup-statusline` command, `statusline --capture-only`, the no-secret cache, sanitize + cross-check + age-out, `captured_at` + `LimitStatus` on `LimitWindow`, the always-on "as of HH:MM" freshness stamp + claude.ai-chat under-report caveat. Codex windows adopt the same `captured_at`/age-out (status always `Verified`).
- **Open item (T4 resolved the floor; live-install check still open):** the cross-check guard is **built** with `UNVERIFIED_TOKEN_FLOOR = 5_000` (biased low — only ever demotes). Still worth confirming against a live install whether the false-100% bug (#31820) fires on the shipped binary — that datapoint can *tighten* the floor, but the guard ships either way.
- **Acceptance:** on a Pro/Max machine, `costroid` shows real Claude 5h + 7d with reset countdowns; degrades to "unavailable"/"unverified" never a confident wrong number; **still no network calls** (offline-acceptance test unchanged and green).
- **Invariants:** the cache holds only two percentages + two reset stamps + a capture time — no token/prompt/credential.

### Step 3 — Generalize the quota model · *folds partly into 0.3.0*
- **Goal:** the data-model prerequisite for every remaining provider (§2a, §2b).
- **Deliverables:** extend `LimitKind`, introduce `LimitMeasure` (token-fraction + spend-$/credits; no request-count), add the `Capability` descriptor, add the `Estimated` availability variant at the core layer. Migrate Claude/Codex to the new shape (still token-fraction). Render `Spend`-measure windows in the limit-line + statusline (dollar pool used/included, not a fabricated %).
- **Acceptance:** a synthetic Cursor/Copilot-shaped `Spend` window renders correctly in all three render modes; existing token-fraction windows unchanged; full gate green.

### Step 4 — **0.4.0**: Connections — the safe, friendly login · *first network code*
- **Goal:** the API-cost half users connect — paste-your-key for the official usage/billing APIs — built on the §2c isolation.
- **Deliverables:** `costroid-connect` crate (ureq + rustls + keyring, feature-gated); `costroid connect <anthropic|openai|gemini>` (paste key → keychain → pull real API usage/cost to reconcile against the local estimate); a **Connections view** listing what's linked; `costroid disconnect <provider>` with instant revoke. The auth source-ladder (§5) enforced in code: a datum with no clean source is **unavailable, never fetched**.
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
| **Claude Code** | local transcripts (+ optional Anthropic usage API w/ key) | **statusLine sanctioned push** | none (`setup-statusline`) | 5h + 7d, token-% | cost ✅ · quota = Step 2 |
| **Codex** | local rollout logs (+ optional OpenAI usage API w/ key) | local rollout logs | none | 5h + weekly, token-% | ✅ both |
| **Cursor** | unavailable (no sanctioned source) | unavailable (no sanctioned source) | none (local model-mix only) | **monthly billing-cycle $-credit pool + overage; daily token rate-limit on free tier** | detect-only; live quota **discovery-gated (§8)** |
| **GitHub Copilot** | own token → billing API ($ by model) | **own classic PAT / `gh` OAuth → documented `…/billing/ai_credit/usage`** (tier 3/2) | own classic PAT (fine-grained unsupported) or `gh` OAuth | **monthly AI-credit pool ($) + overage** *(premium-requests is the legacy pre-June-2026 model)* | **discovery-gated (§8) — ToS-safe path identified; needs live-install check** |
| **Antigravity CLI** | **own Gemini API key → AI Studio / Cloud Billing ($ lane, ToS-safe)** | **unavailable — compute-effort quota has no sanctioned source** (hooks not fed quota; only the internal `GetUserStatus` RPC = ban path) | own Gemini API key (for the $ lane) | **5h + weekly, metered in "compute effort"** (+ credit overage) | **discovery-gated (§8) — $ lane safe; quota unavailable** |

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
- **Antigravity CLI adapter** — *findings landed (2026-06-05); it splits into two lanes.* **$ lane — ToS-safe, build when carded:** Gemini-API cost via the user's own key (AI Studio cost/usage dashboards + Cloud Billing BigQuery export). **Compute-effort subscription quota — no sanctioned source:** its documented Hooks are *not* fed quota (only `conversationId`/`workspacePaths`/`transcriptPath`/tool fields), local transcripts are conversation content only, the IDE `.pb` files are keychain-encrypted, and the only live quota source is the internal Language-Server `GetUserStatus` RPC via a reused token (ban path) → quota stays "unavailable." Remaining discovery: how "compute effort" is denominated (community-sourced only — not officially published) and model-mix attribution (it routes to Gemini *and* Claude). Unlock to watch: Google feeding a documented quota payload into a Hook/status bar, or a consumer usage API.
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

*Step 0 (canon reconcile) and Step 1 (v0.2.0, shipped 2026-06-05) are done, and the Claude `statusLine` capture is built end to end (T2–T5 ✅). The only build left for the **0.3.0 milestone** is **T6 — render the new limit states + Spend windows** (T2 + T4 + T6 = 0.3.0).*

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

**Bottom line:** the model executes successfully if you (a) go in dependency order with **3 riding alongside 2**, (b) accept the human gates on 1 / 4 / 5 / 6 / 7 as checkpoints rather than fighting them, and (c) split the L/XL steps into the sub-units above. **Start with Step 2 + §3's data-model half** — the highest-fit, highest-value first handoff.

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

*Dependency-ordered. ⛔ = human gate · 📌 = pin before starting · S/M/L/XL = size. **T1 is independent; T2 is the lynchpin for all build work.** Cards **T1–T7 are turnkey now**; **T8+ are a backlog** that gets expanded into full cards when its Prereq lands — their detail depends on decisions not yet made, and speccing them now would fabricate.*

> These cards are the at-a-glance **map**. The full, **paste-ready prompts live in §12** and are the source of truth — when a build agent revises a task it edits §12 + logs in §11.5, not these cards. The T2/T4/T6 boundary (types vs behavior vs render) is settled in **§11.5 D1**.

**Progress — the version-controlled "where are we"** (every fresh agent and you read this; the finishing agent ticks its own box as part of its doc edits, you confirm on commit):
- [x] **T1** Release v0.2.0 — ✅ **shipped 2026-06-05** (GitHub Release + Homebrew + npm + crates.io all at 0.2.0; `cargo install costroid` → 0.2.0 verified)
- [x] **T2** Quota data-model foundation *(lynchpin)* — ✅ types + pure map + migration landed, gate green (see §11.5)
- [x] **T3** Capability descriptor — ✅ `DataSource`/`AuthMethod` enums + `Capability` struct + `capability()` trait method + 3 impls + test landed, gate green (see §11.5)
- [x] **T4** Claude statusLine capture — cache + cross-check — ✅ `parse_limits` reads/sanitizes the rate_limits cache; core `window_token_volume` + cross-check finalize (demote → `Unverified`, stale age-out, estimate fallback); 7 bad-data fixtures; gate green, 154 tests (see §11.5)
- [x] **T5** `setup-statusline` + `--capture-only` — ✅ command + `StatuslineArgs {capture_only, wrap}` + `setup.rs` (idempotent settings.json wiring, backup/undo, atomic cache writer); gate green, 58 cli tests (see §11.5)
- [ ] **T6** Render new limit states + Spend windows
- [ ] **T7** `costroid-connect` infra + CI re-scope
- T8+ — backlog (carded when its Prereq lands)

**T2 + T4 + T6 ticked = the 0.3.0 milestone** (Claude live quota + generalized model).

**T1 — Release v0.2.0** · ⛔ · S · Prereq: none (independent of the build tasks)
- **Goal:** ship the already-built cost lane (frontier, Cursor-detect, WSL fix).
- **Agent does:** confirm the gate is green on `main`; run `dist plan` + `dist build --artifacts=local` (dry-run, report only); draft the version bump in `Cargo.toml` + refresh `Cargo.lock`; add a CHANGELOG entry; flip README "next release" → "shipped." Report; **do not tag/push.**
- **⛔ You do:** review, then `git tag v0.2.0 && git push origin v0.2.0`; verify `cargo install costroid` post-release.
- **Done when:** gate green; `dist plan` clean; version + lockfile bumped; (after your tag) release CI succeeds.
- **Next:** independent — blocks nothing, but **tag v0.2.0 before T2+ work reaches `main`** (or branch T2–T6) so 0.2.0 ships only the built cost lane, not half-finished 0.3.0 quota work.

**T2 — Quota data-model foundation** · M · Prereq: none — *do this first of the build work*
- **Files:** `crates/costroid-providers/src/lib.rs` (`LimitKind`, `LimitWindow`, the 3 `parse_limits`); `crates/costroid-core/src/lib.rs` (`LimitAvailability`, `limit_availability`).
- **Goal:** generalize the quota types so every later provider/feature fits one shape (§2a).
- **Scope fence:** types + migration only. No statusline capture, no rendering beyond compiling, no new providers, **no `RequestCount`**.
- **Deliverables:** `LimitKind += Daily, Monthly, BillingCycle`; `enum LimitMeasure { TokenFraction(f64), Spend { used_usd: Decimal, included_usd: Option<Decimal> } }`; `enum LimitStatus { Verified, Unverified, Unavailable }`; on `LimitWindow` add `captured_at: DateTime<Utc>` + `status: LimitStatus` and replace `used_fraction` with `measure: Option<LimitMeasure>`; reshape core `LimitAvailability` so its arms carry the `measure` and add `Unverified` + `Estimated` (5 variants, availability layer only — never on `LimitWindow`); add minimal placeholder render arms to stay green (T6 does the real rendering). Migrate Codex (stays `Verified` `TokenFraction`), Claude (`Unavailable` for now), Cursor (empty) + every constructor + every existing test. (Full shape in §11.5 D1 / §12.2.)
- **Done when:** gate green; existing limit tests updated and passing.
- **Next:** the new types exist → T3, T4, T6 (and later the Providers tab) build on them.

**T3 — Capability descriptor** · S · Prereq: T2
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

**T6 — Render new limit states + Spend windows** · M · Prereq: T2 (+ T4 for live data)
- **Files:** `apps/cli/src/render.rs` (`render_limit_line` / `plain_limit_line` / `state_cue`) + snapshots; **and `costroid-core`** to plumb `captured_at` (see ⚠️ below).
- **Goal:** render `Available / Partial / Unavailable / Unverified / Estimated` and `Spend` windows (dollar pool used/included, never a fabricated %), with the always-on "as of HH:MM" stamp + the claude.ai-chat under-report caveat (brief §8).
- **⚠️ Do first (T4 handoff, §11.5):** `captured_at` does **not** reach the render layer yet — thread it onto `LimitSummary` (or the `Available`/`Unverified` arms) in `limit_summary`, or the "as of HH:MM" stamp has no source. Internal struct only, no export-schema gate.
- **Scope fence:** rendering + snapshots only.
- **Done when:** gate green; snapshot tests cover every availability arm in braille/ASCII/plain; plain asserts no ANSI.
- **Next:** the **0.3.0 milestone** (Claude live quota + generalized model) is complete.

**T7 — `costroid-connect` infra + CI re-scope** · L · ⛔ · Prereq: T3
- **Files:** new `crates/costroid-connect/`; root `Cargo.toml` (member + feature); `deny.toml`; `apps/cli/tests/offline.rs`; `scripts/offline_acceptance.sh`.
- **Goal:** create the feature-gated network/credential crate **with no behavior yet**, and re-scope the no-network guarantees so the default build still *proves* zero network.
- **Scope fence:** crate skeleton + feature gate + test re-scoping only. **No keychain, no HTTP yet** (T8/T9).
- **⛔ Human gate:** changing offline-acceptance + forbidden-crates is a guarantee redefinition — stop for approval on the re-scoped assertions.
- **Deliverables:** `costroid-connect` behind feature `connect` (off by default); `deny.toml` + forbidden-crates scoped so `ureq`/`rustls`/`keyring` are allowed only in `costroid-connect`; offline-acceptance asserts the default build makes zero calls; a new test asserts network happens only with the feature on + an explicit action (stub).
- **Done when:** default `cargo build`/`test` green AND offline-acceptance still passes; `--features connect` builds.
- **Next:** keychain (T8) and HTTP (T9) have a home.

**Backlog — carded when its Prereq lands (📌 must be pinned first):**
- **T8 — keychain + API-key entry** · ⛔📌 · Prereq T7
- **T9 — usage-API clients + reconciliation** · ⛔📌 · Prereq T7,T8 — 📌 which provider endpoints + auth schemes
- **T10 — connect/disconnect CLI + Connections view** · ⛔📌 · Prereq T8,T9 — 📌 connect UX, reconciliation display → **0.4.0** · ⛔ **legal review of the connection flows before this ships** (own-key + sanctioned OAuth only — see Step 4)
- **T11 Providers tab** (Prereq T3) · **T12 Models tab** · **T13 History tab** — cheap re-cuts
- **T14 Budget 📌 · T15 Forecast 📌 · T16 Anomalies 📌 · T17 Alerts ⛔📌** — 📌 budget persistence schema · forecast algorithm · anomaly baseline · alert thresholds + copy → **0.5.0**
- **T18+ — egui taskbar** · ⛔ · Prereq T2–T6 (CLI feature-complete) — greenfield: needs a GUI design first, then per-tab fan-out → **0.6.0**
- **Cursor live quota — discovery-gated (§8), not a numbered build task.** Pursued only if Cursor publishes a sanctioned/documented API or first-party OAuth (never session reuse against `api2.cursor.sh`); until then Cursor stays detect-only / "unavailable." Card it (like Copilot/Antigravity) only after that discovery lands.

When you reach a backlogged task, pin its 📌 and have a planning agent expand it into a full T1–T7-style card before you hand it to a build agent.

### 11.5 Decisions & limitations (living log)

*New decisions/constraints land here as tasks run — agents append (newest first), dated by the task that surfaced them. This is where "a new decision/limitation" goes.*

**🔒 ToS-safe rework (2026-06-06) — removed the session-reuse tier across plan + code.** The auth ladder is now **tiers 0–3 only; tier 4 = never** (no reuse of any credential/session/token against a non-sanctioned, undocumented, or internal endpoint — incl. Cursor's `api2.cursor.sh` — and no browser-cookie reading). Concretely: **`OptInSession` was removed from both `DataSource` and `AuthMethod`** in `costroid-providers`, and **Cursor's descriptor is now `auth: None`** (was `OptInSession`); the `each_provider_declares_its_capability` test was updated; full gate green. **Cursor live quota is no longer a numbered step** — it is **discovery-gated (§8)**, pursued only via a future *sanctioned* Cursor API/OAuth, never session reuse. The **egui taskbar moved Step 7 → Step 6 (v0.7.0 → v0.6.0)**, now the last numbered step; in the ledger the taskbar is **T18+** (no T19; Cursor is not a numbered T). §8 also gained verified ToS-safe discovery findings — **Copilot** (own classic-PAT / `gh` OAuth → documented `…/billing/ai_credit/usage`; user-billed only; never `copilot_internal/user`) and **Antigravity** (own-Gemini-key $ lane safe; "compute-effort" quota has no sanctioned source). The T3-DONE entry below predates this and is annotated accordingly.

**✅ T5 DONE (2026-06-06, gate green, 58 cli tests) — `setup-statusline` + `statusline --capture-only` (the cache writer). ⛔ human-gated CLI surface approved before finalizing.**
- **CLI surface (`apps/cli/src/main.rs`).** `Command::Statusline` refactored from a bare variant to `Statusline(StatuslineArgs { capture_only: bool, wrap: Option<String> })` (`--capture-only` `conflicts_with` `--wrap`); new `Command::SetupStatusline(SetupStatuslineArgs { undo: bool })`; `run_statusline` now takes `&StatuslineArgs`. New module `apps/cli/src/setup.rs` houses all of it.
- **`--capture-only` (the path-1 surface).** Reads stdin once → `build_cache_value` extracts **only** `.rate_limits.{five_hour,seven_day}.{used_percentage,resets_at}` (every other field — incl. secrets — dropped; values passed through verbatim, T4 sanitizes) → top-level `captured_at` = `Utc::now().to_rfc3339()` → **atomic write** (temp + rename) to the T4 cache path → **emits nothing, exits 0 always** (malformed/absent/no-rate_limits → writes nothing, still exit 0). Cache shape is the exact inverse of T4's `read_claude_rate_limits` (verified end-to-end: writer→reader round-trips).
- **Path-2 capture (decision, brief §2 path 2).** Plain `costroid statusline` (the string `setup-statusline` installs) now **opportunistically captures** when stdin is **piped** (`!std::io::stdin().is_terminal()`) then renders; interactive stdin (tmux / Starship) is never read, so it never blocks. This is what makes path 2 actually capture without a snippet.
- **`--wrap '<cmd>'` (decision: implemented fully, not stubbed — ⛔-approved).** The hazardous manual escape hatch (brief §2 path 3): read stdin once, tee a copy to the capture side-effect, run `sh -c '<cmd>'` on the identical bytes with stdout inherited; on spawn/exit failure print a blank line; **exit 0 always** (render-something-on-failure).
- **`setup-statusline` (idempotent, backup, undo).** Resolves a **single** config root = first **existing** of `HostEnv::claude_roots()` (so a set `CLAUDE_CONFIG_DIR` wins when it exists), printed before any write. **No existing root → stops with guidance** (lists the paths it checked, suggests running Claude Code once / setting `CLAUDE_CONFIG_DIR`), writes nothing (decision: never create config in a guessed location). Round-trips `settings.json` via `serde_json::Value` (preserves unknown keys; **malformed JSON → refuses to overwrite, errors out**; absent → fresh object). **Path 1** (existing `statusLine.command`) wraps it under the sentinel `# costroid:statusline-capture v1` (`input=$(cat); printf '%s' "$input" | costroid statusline --capture-only; printf '%s' "$input" | <ORIG>`); **path 2** (none) sets `statusLine = {type:"command", command:"costroid statusline"}`. **Idempotent:** re-run detects the sentinel or the `costroid statusline` string → no-op. **Backup** to `settings.json.costroid-bak` before the first write (only if the file existed and no backup exists yet). **`--undo`** restores the backup (then deletes it) or, for a fresh path-2 file with no backup, strips the wiring — **deleting the file entirely if that leaves it empty** (so undo of a we-created file returns to "no file", not `{}`).
- **Cross-crate change (deviation from the card's named files, logged):** `costroid-providers::claude_rate_limits_cache_path()` made **`pub`** so the writer and the T4 reader resolve the **same** path from one source (no drift); `serde_json` moved from a dev-dep to a regular dep of `apps/cli` (already in the workspace + tree — not a new/forbidden crate; offline-acceptance + forbidden-crates both still green). `std::io::IsTerminal` (stable, in-std) gates the opportunistic capture — no new dep.
- **Note for whoever runs the WSL safety:** `HostEnv::claude_roots()` scans `/mnt/c/Users/*/.claude` regardless of `HOME`, so `setup-statusline` on a WSL box **will** target a real Windows Claude config if one exists — that is correct behavior, but be aware when testing (scope tests with an explicit existing `CLAUDE_CONFIG_DIR`).
- **Fixture:** `fixtures/claude-code/statusline-stdin.json` — a raw Claude Code session object (rate_limits + extra/secret fields) to prove the writer keeps only the four allowed values. Tests cover: cache-shape + secret-drop, no-rate_limits/malformed/empty → no write, atomic write + exit-0-on-bad-input, path-1/path-2/idempotent transforms, backup+undo round-trip, malformed-settings refusal, fresh-file undo deletes.
- **For T6 (still pending, unchanged):** with the writer live, the **flagship Claude live quota now works end to end on a Pro/Max machine** — `setup-statusline` wires it, `--capture-only` captures, T4 reads/sanitizes/cross-checks. T6's remaining job is the *render* (the `Unverified`/`Estimated` arms, the "as of HH:MM" stamp + the §11.5/T4 `captured_at`→render-layer plumbing gap, the claude.ai caveat, the statusline `Unverified` selection arm + snapshots).

**✅ T4 DONE (2026-06-06, gate green, 154 tests) — Claude statusLine capture: cache read + sanitize + core cross-check. Defined no new types (built on T2's).**
- **Provider (`costroid-providers`).** `ClaudeCodeProvider::parse_limits` now reads the sanctioned cache and produces two windows (`five_hour`→`FiveHour`, `seven_day`→`Weekly`). New helpers: `claude_rate_limits_cache_path()` (resolves `${XDG_STATE_HOME:-$HOME/.local/state}/costroid/claude-rate-limits.json` — Linux-side only, the cache is Costroid's own state, no Windows-path handling), `read_claude_rate_limits(path: Option<&Path>)` (the pure seam — reads/parses or two `Unavailable`), `claude_limit_window(...)` (per-window sanitize), `parse_reset_stamp(...)` (epoch-then-RFC3339). **Sanitize order (ARCHITECTURE §9.2):** on the RAW `used_percentage` *before* ÷100 — out of `0..=100` (the 900% bug) **or** `== resets_at` (poisoned-epoch leak) → no measure, provisional `Unavailable`; else `Verified` `TokenFraction(pct/100)`. *(Range widened from the brief's bare `>100` to `!(0..=100)` — strictly safe-directional, only ever demotes; noted as a tiny defensive superset.)* `captured_at` is read from the cache's top-level field (RFC3339, epoch-sentinel fallback). Provider sets **only** the provisional `Verified`/`Unavailable` — it cannot see usage, so it never cross-checks.
- **Core (`costroid-core`).** New `window_token_volume(rows, tool, kind, now) -> TokenTotals` (pinned signature; sums FOCUS rows in the trailing window via `x_tool` + `charge_period_start`), companion `window_estimated_usd(...) -> Option<Decimal>` (sums priced rows' `effective_cost`; `None` if any contributing row is unpriced — volume shown alone, never a guessed price), and `window_duration(kind)`. The finalize pass lives in `limit_summary` (now takes `&[FocusRecord]`): `finalize_limit_status` demotes a **Claude-only** `Verified` reading to `Unverified` when `fraction ≥ HIGH_USAGE_FRACTION (0.80)` **and** window volume `< UNVERIFIED_TOKEN_FLOOR (5_000)` — Codex windows (sanctioned rollout logs) are never cross-checked. `limit_availability` gained two params (`volume`, `estimated_usd`) and now ages out a **stale** reading (`resets_at < generated_at`, any status) and a measure-less/`Unavailable` reading to `Estimated { volume_tokens, estimated_usd }` when volume > 0, else `Unavailable` — evaluated at render time so `--live` re-checks each tick. The old stale→`Partial` arm is gone; `Verified` + reset-unknown still → `Partial` ("reset time unknown").
- **Pinned constants confirmed (the §12 open item resolved):** `UNVERIFIED_TOKEN_FLOOR = 5_000` (biased low — only ever demotes, so it flags the implausible "near-max on almost no usage" and never a real heavy prompt; the live-install #31820 datapoint can tighten it later but the guard is built either way), `HIGH_USAGE_FRACTION = 0.80` (core-local mirror of render's `WARN_FRACTION` — core cannot import from `apps/cli`).
- **Testability seam (decision).** The cache is **global state, not in `DataLocation`**, and the `Provider::parse_limits` signature is fixed (no `HostEnv`). So production `parse_limits` resolves the env path, but **all tests route through `read_claude_rate_limits(path)`** with explicit fixture/None paths — no env mutation (race-free), and **no test ever reads a developer's real cache** (golden rule). Two existing tests that asserted "Claude always Unavailable" via `provider.parse_limits` (`claude_fixture_parses_usage_and_unavailable_limits`, `each_provider_emits_its_expected_window_shape`) were repointed to the pure seam. `unavailable_limit` **kept its 2-arg form** (per T2's note) — a sanitized-out window present in the cache is built inline so it can still carry the cache's `captured_at`.
- **Fixtures** under `fixtures/claude-code/`: `rate-limits-{happy,impossible-900,poisoned-epoch,false-100,absent,stale,iso-resets}.json` (valid JSON, synthetic, no secrets). `absent` = present file missing the `five_hour` key; the false-100 cross-check is proven at the **core** layer with synthetic trivial-volume rows (the provider can't see volume). Offline-acceptance still green — no new deps (`std::fs` + `serde_json` only).
- **For T6 (render):** Claude windows now carry a real `captured_at` + finalized `status`, and the now-screen can produce `Available`/`Unverified`/`Estimated`/`Partial`/`Unavailable` from real data. T6 still owns the real rendering of `Unverified` (`? unverified` cue), `Estimated` (volume + `~$value`, no meter), the always-on "as of HH:MM" stamp, the claude.ai caveat, and the statusline `limit_fraction` `Unverified` arm + snapshots — all still placeholder (`LIMIT_RENDER_PENDING`) today.
  - **⚠️ Plumbing gap T6 must close first (new limitation surfaced by T4):** `captured_at` lives on `LimitWindow` but is **not** carried by `LimitSummary` or any `LimitAvailability` arm — and the render layer consumes `LimitSummary.availability`, **never `LimitWindow`** (the §11.5 repo-facts invariant). So the "as of HH:MM" stamp (brief §8) is **structurally unreachable until T6 threads `captured_at` to the render layer** — add `captured_at: DateTime<Utc>` to `LimitSummary` (smallest change; `limit_summary` already has the finalized window in hand) or carry it on the `Available`/`Unverified` arms. This is an internal struct, **not** export schema (no ⛔). It is **T6's** to add — T4's scope fence forbids new types, so T4 deliberately did not. The finalize already overwrites `captured_at` per the cache, so the value is correct and ready; only the wiring is missing.

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
- **Placeholder rendering is intentionally minimal (T6 replaces it):** `Spend` measures and the new `Unverified`/`Estimated` availability arms all render the constant `"limit detail pending"` (`LIMIT_RENDER_PENDING` in render.rs) across the styled / `--plain` / statusline surfaces — ASCII, no color, no color-only cue. `TokenFraction` `Available`/`Partial` rendering is **unchanged** (existing snapshots still pass). The new `LimitKind` labels are placeholders too: `Daily`→`1d`, `Monthly`→`mo`, `BillingCycle`→`cyc`. The new arms have **no producer in T2**, so this text never reaches a user yet. `limit_fraction`/`limit_fraction_and_reset` treat `Unverified`/`Estimated` as "no fraction" (they don't feed the statusline "most constrained" pick until T6 decides).
- **Tests:** providers `each_provider_emits_its_expected_window_shape` pins each provider's window shape (the Done-when); core `limit_availability_maps_status_and_measure` pins the status+measure map incl. the `Unverified` arm, the no-measure→`Unavailable` rule, and `Spend` routing. Full gate green (138 tests, incl. the offline forbidden-crates acceptance test — `rust_decimal` is not a forbidden crate).

**Pinned defaults** (accept or override before the task): `UNVERIFIED_TOKEN_FLOOR = 5_000` · cache path `${XDG_STATE_HOME:-~/.local/state}/costroid/claude-rate-limits.json` · `LIMIT_FRESHNESS_STAMP_MINUTES = 10` · setup sentinel `# costroid:statusline-capture v1`.

**Repo facts confirmed by grounding** (so an agent isn't surprised): no `CHANGELOG.md` exists yet → T1 creates it at root · `Statusline` is currently a **bare** `Command` variant with no args → T5 refactors it to `Statusline(StatuslineArgs)` · `LimitAvailability` today has exactly 3 variants (`Available`/`Partial`/`Unavailable`), each token-fraction-shaped (`used_fraction: f64`) → T2 reshapes them to carry `LimitMeasure` and adds the 2 new (`Unverified`/`Estimated`) · the render layer consumes `LimitSummary.availability` (built by `limit_summary` ~L649), **never `LimitWindow` directly** → a `Spend` window's dollars must live in the availability arm or T6 can't render them · `LimitAvailability`/`LimitSummary` are **not** emitted as any user-facing JSON/export output today (only the FOCUS cost rows are) → reshaping them is an internal change, **no export-schema ⛔ gate** · `rust_decimal` (`=1.42.0`, `serde`) is a workspace dep used by core but **not yet in costroid-providers** → T2 adds `rust_decimal.workspace = true` there · `deny.toml` bans openssl/native-tls globally and `apps/cli/tests/offline.rs` forbids ureq/rustls/keyring globally → T7 re-scopes ureq/rustls/keyring to `costroid-connect` only.

---

## 12. Ready-to-paste task prompts (T1–T7)

*To run a task: paste **§12.0 (the header)** then that task's **body block**, into a fresh ultracode-xhigh agent. Resolve any 📌 (defaults in §11.5) first. Backlog tasks (T8+) use **§12.8**. §12 is the source of truth for task content — agents edit it (and §11.5) as they learn; those edits are tracked in `docs/` and commit with the task.*

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

### 12.2 — T2 · Quota data-model foundation · M · Prereq: none — *the lynchpin; do first*

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

### 12.3 — T3 · Capability descriptor · S · Prereq: T2

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

### 12.4 — T4 · Claude statusLine capture: cache + cross-check · L · Prereq: T2 · 📌

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

### 12.5 — T5 · `setup-statusline` + `--capture-only` · M · ⛔ (public CLI surface) · Prereq: T4 · 📌

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

### 12.6 — T6 · Render new limit states + Spend windows · M · Prereq: T2 (+T4 for live data)

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

### 12.7 — T7 · `costroid-connect` infra + CI re-scope · L · ⛔ · Prereq: T3

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

### 12.8 — Backlog tasks (T8+): the pin-then-card prompt

*T8–T18 aren't carded — they have open 📌 that must be pinned first. Paste §12.0 + this body, with `<ID>` filled, to turn a backlog task into a real card (don't build it yet):*

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
