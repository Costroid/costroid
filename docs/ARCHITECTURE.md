# Costroid — Architecture & Build Spec (canonical)

The single source of truth for the build. This lives in the repo as `ARCHITECTURE.md` and governs every decision below it. It supersedes the earlier strategy docs for build purposes; launch/marketing material (the validation post) stays separate. Code-shaped notes describe intent and naming, not frozen signatures. Detailed companion specs (DATA-MODEL.md, DESIGN-SYSTEM.md) are referenced where they own the granular truth.

---

## 1. What Costroid is (and is not)

**Position:** the local, private FinOps tool for AI coding assistants — the one that *also* sees the subscription limits the platforms can't.

**It is NOT:** a platform, observability, enterprise FinOps, a proxy/gateway, a chatbot or conversational-LLM interface, or anything that processes prompt/completion content. **Metadata only.** (Any web platform is a separate, separately-licensed repo — never built here.)

**Competitive frame:** measured against **ccusage** and the status-bar tools — not Finout, OpenCost, or Vantage. The two load-bearing differentiators, because no billing-based competitor can do them: **subscription-quota awareness** (no invoice exists for it) and **local-first / no content / zero integration**.

**Audience:** individual developers, for now.

## 2. The intellectual core: the cost–quality frontier

Price and quality don't track each other, and that gap is why this product exists.

- DeepSWE: gpt-5.5 = 70% @ $6.61/task vs claude-opus-4.8 = 58% @ $12.58 (cheaper *and* better); claude-sonnet-4.6 = 32% @ $5.52 (far below Opus on hard tasks).
- CursorBench: Composer 2.5 = 63.2% @ $0.55 vs Opus 4.7 Max = 64.8% @ $11.02 (matched at ~1/20th the cost); gpt-5.5 = 64.3% @ $4.37.

The frontier is jagged: the cheapest model is sometimes a trap, the priciest often isn't best. So the product **shows the frontier, plots where the user's spend sits on it, and lets them judge** — never "use the cheapest." It stays honest that it sees spend and benchmarks but *not* task difficulty, so it informs rather than prescribes. That honesty is what makes it trustable, and this principle governs the whole UI — not just one screen.

## 3. Scope (v1)

- **Providers:** Claude Code + Codex (validated), Cursor (v1: **detection only** — present + selected model, labeled beta; its usage/quota are live-only, so Cursor cost/quota land in Phase 2).
- **Cost:** subscription quota (%) *and* API cost ($); filter by provider. Cross-provider totals are **per-lane** (see §6 — API spend, subscription-equivalent value, and quota % shown distinctly), never one merged number.
- **Trends dimensions:** aggregate by period (`day` / `week` / `month` / `year`) and group (`model` / `app` / `total`).
- **Export:** FOCUS records to JSON and CSV.
- **Surfaces:** the TUI (`now` / `trends`, with the recommendation/frontier surface reached via the `a` ask key + the insight line) and a `costroid statusline` mode (tmux, Starship, Claude Code's `statusLine`). The cross-platform taskbar/tray is **deferred/cut**.
- **Recommendation:** a frontier view from **DeepSWE (primary)** + **CursorBench (corroborating)**, as a bundled snapshot.

## 4. Technical canon (constraints the build inherits)

- Rust; single Cargo workspace; edition 2021; **MSRV 1.88** (documented in CI); workspace crates versioned **in lockstep** (`[workspace.package].version`); commit `Cargo.lock`. **CI gate (green = releasable):** `rustfmt` + `clippy -D warnings` + `cargo test` + FOCUS-conformance + `cargo deny` (license + advisories) + offline-acceptance (no-network).
- `thiserror` in libs (`ProviderError`/`FocusError`/`CoreError`), `anyhow` in the binary. **No `unwrap`/`expect`/`panic` in libs** (tests use the `match … panic!` idiom).
- `tracing` with an env filter, **local diagnostics only, never networked**; quiet by default, `-v`/`-vv` to stderr. **No telemetry.** License **Apache-2.0**; dependencies must be **permissively licensed** (MIT / Apache-2.0 / BSD / ISC / Zlib / Unicode) — **no copyleft** (GPL / AGPL / LGPL / SSPL); verify before adding (the CI gate runs `cargo deny`).
- Standards: FinOps Foundation + **FOCUS 1.3** (validator `finopsfoundation/focus_validator`, run offline against the bundled ruleset; 3 known validator-ruleset defects allowlisted with upstream refs — only the rules that fire, never their passing siblings). Treat the FOCUS spec, not the docs, as the authority on column semantics.
- Pricing **bundled inside `costroid-core` via `include_str!`** at `crates/costroid-core/pricing/pricing.vN.json` — **cargo packages only files under the crate dir, so all bundled data must live inside its crate** (a workspace-root location breaks `cargo publish`). Sourced current at build time and refreshed deliberately — never hardcoded figures that drift; works fully offline. Records `as_of` + `sources`; rates per meter (input/output/cache_read/cache_write) **per 1M tokens**; the calculator derives the **per-token** unit price (rate ÷ 1e6). Catalog key = model id (e.g. `claude-sonnet-4-6`), not display name. Cost is always labeled an **estimate**; reconciliation against the real invoice is Phase 2+.
- **Config:** TOML at the XDG path (`~/.config/costroid/config.toml`) with sensible zero-config defaults. **Secrets never go in config — keychain only.**
- crates.io: **iterate the existing names** (`costroid`, `costroid-core`, `costroid-focus`, `costroid-providers`) to 0.2.0 — don't orphan or rename them. npm: `costroid`.
- **Brand:** monochrome (black/grey/white, adapting to the terminal theme) + a **single amber accent reserved for the warning/near-limit state** (red only as an intensification, always with a non-color cue); JetBrains Mono; braille glyphs (U+2800 + bitmask); **mandatory `--plain` ASCII fallback**. Mark `C⠉` (a pixel `C` beside the braille cell `⠉`); wordmark `costroid` with `cost` in the strong weight and `roid` muted — carry the split into the UI: **dollar figures and the active metric use the strong weight; labels and context are muted.** (Dot math + component specs in DESIGN-SYSTEM.)
- **Log discovery (WSL-aware):** Claude Code = `~/.claude/projects/**/*.jsonl` + `~/.config/claude/projects/**/*.jsonl`; Codex = `~/.codex/sessions/**/*.jsonl`; Cursor = **no local usage/quota** — the CLI fetches both live from `api2.cursor.sh`, so Phase 1 only **detects presence + selected model** from the `~/.cursor` config (honor `CURSOR_DATA_DIR`), never chat content. **Honor `CLAUDE_CONFIG_DIR` (comma-separated roots) and `CODEX_HOME` before the defaults; merge all roots.** Detect WSL via `/proc/sys/kernel/osrelease` (contains `microsoft`/`WSL`); under WSL, **scan `/mnt/c/Users/*` for the profile(s) that actually hold the logs** (so a WSL user ≠ Windows profile, e.g. `eren`/`ereno`, still resolves), with `USERPROFILE` (if set) and the legacy `$USER` path as fallbacks. Native roots are XDG / `~/Library` / `%APPDATA%`. Never assume a single fixed path. Claude Code has **no quota in local logs** → its subscription limits are *unavailable* in Phase 1 (they arrive via Phase 2 live data); Codex exposes rate-limit windows locally (5h = 300 min, weekly = 10080 min).

## 5. Workspace architecture

- **`costroid-core`** — cost calc + FOCUS normalization orchestration + bundled pricing + a **`bench`/`recommend` module** (the DeepSWE/CursorBench snapshot, the reconciliation rule, the frontier computation). No terminal/UI code. *Ported and test-guarded.* The trust foundation. Promote `bench` to its own crate only if it grows.
- **`costroid-focus`** — the FOCUS record types and their (de)serialization (JSON/CSV). Pure data, **no internal dependencies**; the crate most likely extracted/reused later. *Ported.*
- **`costroid-providers`** — the provider trait + one adapter per provider + WSL-aware discovery. Depends only on `costroid-focus`. Claude + Codex *ported/validated*; Cursor *new (beta)*; subscription-quota fetchers *new* (reuse stored credentials, codexbar-style, always graceful).
- The binary crate is **`costroid`** (the `cargo install costroid` / `npx costroid` name) — clap CLI + the Ratatui TUI + the statusline + `--live` + the `--plain` path. Its directory location is a layout detail.
- `fixtures/` holds committed sample provider logs for tests — **never real user data**.
- No `costroid-mcp`, no tray app in this scope.

**Dependency direction (no cycles):** `costroid-focus` sits at the bottom and depends on nothing internal; `costroid-providers → costroid-focus`; `costroid-core → providers + focus`; the `costroid` binary → core; nothing depends on the binary.

**Data flow (entirely local, no network in Phase 1):** `discover()` → `parse` usage + limits → normalize to FOCUS + estimate cost → aggregate (period, group) → render (now/trends/statusline) **or** export (JSON/CSV). Renderers and exporters are pure consumers of the same in-memory model.

**Provider trait semantics:** `discover()` returns "no data" (not an error) when a provider isn't installed — missing providers are skipped, not fatal. `costroid-core` holds a registry of providers; the CLI runs all detected or a subset via flags. Adding a provider should require **no changes outside `costroid-providers`** (the test of the abstraction). Limits come from local data in Phase 1 and from a live, already-stored session in Phase 2.

## 6. Data model (summary — full spec in DATA-MODEL.md)

- **FOCUS output.** Each unit of API usage is emitted as FOCUS 1.3 Cost & Usage rows with `ChargeCategory = "Usage"`. Input / output / cache-read / cache-write each become a **separate row** (distinct `SkuId`/`SkuPriceId` + `x_TokenType`) so quantities and unit prices stay coherent. Use the active 1.3 participating-entity columns **`ServiceProviderName` / `HostProviderName` / `InvoiceIssuerName`**; do **not** emit the deprecated `ProviderName` / `PublisherName` (removed in 1.4). `ServiceCategory = "AI and Machine Learning"`. Unit prices are **per token**; `PricingUnit = "tokens"`.
- **Custom `x_` columns:** `x_Model`, `x_TokenType` (input|output|cache_read|cache_write), `x_AccessPath` (api|subscription|unknown), `x_Estimated`, `x_PricingStatus` (priced|missing_price|unknown_model), `x_Tool` (claude-code|codex|cursor), `x_Project`, and **`x_ConsumedTokens`** — the raw token count, **always populated** (the aggregation engine totals from this).
- **Subscription limits are a separate type, not FOCUS rows.** A `LimitWindow` carries `tool`, optional `plan`, `kind` (`FiveHour` | `Weekly`), `used_fraction` (0–1), and `resets_at` — **no dollars**, never summed into a bill.
- **Three lanes, never summed across each other:** (1) **API usage** → FOCUS rows, real cost estimate, `x_AccessPath = "api"`; (2) **subscription usage** → FOCUS rows valued at **API-equivalent** (`x_Estimated = true`, `x_AccessPath = "subscription"`) — labeled estimated value, **never a bill**; (3) **subscription quota** → `LimitWindow`s (%). The cross-provider "total" is therefore **per-lane** (API spend, subscription-equivalent value, quota % shown distinctly). Recommendations attach **only** to `x_AccessPath = "api"` rows.
- **Access path is detected from evidence, never guessed.** Codex: `rate_limits` windows present ⇒ subscription. Claude Code: from auth mode via non-secret **presence** signals only (never read credential values). `unknown` when there's no signal.
- **Unpriced rows** (no matching rate, or empty pricing table): the four cost columns stay present and `0`, `SkuPriceId` is null, and the FOCUS-required pricing/consumption columns go null accordingly — but `x_ConsumedTokens` still carries the count, so unpriced usage is **never dropped** from totals; flag `x_PricingStatus`. **Never substitute a guessed price.**
- **Export contract:** JSON is a wrapper `{ "focusVersion": "1.3", "rows": [...] }` (never a bare array); CSV uses the exact FOCUS PascalCase header (`x_` columns appended). `LimitWindow`s export **separately**, never mixed into the cost data.
- **Grouping:** by `x_Model`, by `x_Project` (bucket `"unknown"` when undeterminable), or `total`; period buckets by `ChargePeriodStart` in the user's local time zone. Never sum `LimitWindow` data.

DATA-MODEL.md remains authoritative for the column-by-column mapping, the `UsageEvent`/`FocusRecord`/`LimitWindow` Rust shapes, per-provider field paths, the pricing JSON schema, and the FOCUS-validator nullability/defect mechanics.

## 7. Rendering & UX (mechanics here; dot math + mockups in DESIGN-SYSTEM)

- **Ratatui + crossterm.** Braille is computed directly from `U+2800` + an 8-dot bitmask — **not** Ratatui's braille constants or `Canvas` (those drift across versions, and `Canvas` is TUI-only). The **sparkline is hand-rasterized to braille** like every other component, so the one-shot and TUI paths stay identical (see the styled-document rule below).
- **Fill = bright vs dim cells.** A terminal cell has a single foreground color, so braille gives sub-cell *shape* but not sub-cell *color*: used vs remaining is **bright vs dim cells** at cell granularity (the solid-dot/hollow-ring look from the mockups is raster-only — website/marketing). **When color is unavailable (`NO_COLOR`), meters and bars fall back to the ASCII `[####--]` rendering** so used/remaining stay distinguishable.
- **Warning thresholds:** `warn = 0.80`, `critical = 0.95` (defaults, configurable). The warning state turns amber (red at critical/over) **and always carries a textual cue** — `!`, `!!`, `OVER`, or "near limit" — so it survives `NO_COLOR`, color-blindness, and `--plain`. Cost bars never go amber (amber is for limits, not spend).
- **`RenderMode`:** `Braille` (default) / `Ascii` (automatic fallback when braille is unsupported — internal, not a user flag) / `Plain` (`--plain`: no TUI chrome, screen-reader friendly). Mode is chosen from `--plain`, TTY detection (non-tty ⇒ Plain), `NO_COLOR`, and a braille-capability check. The only user-facing mode flag is `--plain`.
- **Interactive vs one-shot:** on a TTY, `now`/`trends` run as a navigable TUI; piped output, `--plain`, `statusline`, and `export` render once and exit.
- **One styled document, two adapters:** the renderer emits a neutral styled document (semantic styles: strong/dim/warn/critical/plain). A one-shot adapter serializes it to an ANSI/plain string; the TUI adapter maps it to Ratatui — keeping both identical. The one-shot serializer is snapshot-tested as the compatibility contract.
- **Terminal is always restored** (raw mode off, alternate screen left, cursor shown) on quit, error, and panic — via a restore guard plus a panic hook that leaves the alternate screen, chains the default handler, and exits 101. Works over SSH and inside tmux.
- **`--live`** re-collects data periodically (~2s); without it the screen is a snapshot refreshed manually (`r`).
- **`costroid statusline`** prints a single compact line (shell prompts, tmux, Starship) from the same core data, honors `RenderMode`, takes a configurable format string (presets: default / compact / minimal), and is fast and side-effect-free.
- **Screens.** `now` = stacked 5-hour + weekly **limit meters with reset countdowns**, then **API spend by model** (cost bars), then one insight line; limits and costs are visually parallel but clearly separate (limits carry no dollars). `trends` = a period **sparkline**, then the **breakdown cost bars**, then one insight line.
- **Keybindings:** `d`/`w`/`m`/`y` set period; `g` cycles group; `tab` switches screen; `f` or `/` filters (fuzzy model/app); `a` asks (hands context to the recommendation view, §10 step 4); `r` refreshes; `q`/`Ctrl-C` quits (always restoring the terminal); `?` help.
- **States:** *loading* = braille spinner + short label; *empty* = plain-language note on what Costroid looked for and how to point it at the data (incl. the WSL/Windows path note), never an error; *partial* = show what's available and **label the gap explicitly, never fabricate** (e.g. Cursor); *per-provider error* = inline, non-fatal, the rest still renders.

## 8. Security boundary & credentials

The whole trust story depends on this.

- **Secrets live only in the OS keychain** (`keyring` crate: macOS Keychain / Windows Credential Manager / Linux Secret Service). **Never written to disk, config, or logs.**
- **Credentials flow only between the device and the provider** — there is no Costroid backend or server.
- **Three tiers:** (T1) **local logs** — Phase 1 default, no login, no credentials, no network; (T2) **reuse the existing local session** — Phase 2, query the provider's own quota endpoint for live limits, no new login; (T3) **optional OAuth** — Phase 2, system browser + loopback redirect with PKCE (`oauth2`), token stored to the keychain only. **No browser-cookie reading** — dropped for v1; T2/T3 cover live quota.
- TLS via **rustls** (no OpenSSL). Network access is confined to provider endpoints the user explicitly authorized.
- **Provider logs are untrusted input** — parsed defensively; malformed data yields a clean error or "unavailable", never a crash, and the parser never executes or evaluates anything from log content.
- A **connections view** (Phase 2) lists what's linked and supports instant disconnect/revoke.

## 9. Non-negotiable principles (trust + don't over-engineer)

1. **The cost core is sacred.** The committed **fixture** golden tests vs. ccusage are the hard gate — if a refactor moves a number, the build fails. Real-log ccusage parity is a one-off confidence check, not a bit-identical-forever mandate (ccusage can legitimately differ).
2. **Fragile parts degrade, never crash.** Cursor's format and the subscription endpoints are undocumented and will break on vendor updates. Each returns data *or* a clean "unavailable" state. Showing nothing is correct; showing a wrong or stale number is fatal.
3. **Benchmarks are a bundled, dated, cited snapshot — not a live fetch.** DeepSWE primary (a neutral data company), CursorBench corroborating (Cursor's own, showcases its Composer model). Where they disagree or lack a model, show both and recommend only what's supported — never invent a number. Surface the source and date in the UI.
4. **Quota (%) and dollars ($) are different types.** The cross-provider total sums dollars only, per lane; quota shows alongside.
5. **Local, no content, no telemetry.** The brand and the trust both depend on it.
6. **The recommendation is a frontier view, not a prescriber.** It plots the published cost-vs-quality frontier and overlays the user's actual model mix and spend; it never claims "this task should have used X." It applies **only to API-cost rows** (model choice only changes the API bill), never subscription rows, and each recommendation carries its reasoning, its sources, and a projected dollar delta. All output is advisory.
7. **Tested against fixtures, not the network.** Unit tests per crate; integration tests driven by committed `fixtures/` logs (never real user data); `insta` snapshot tests for rendered output, especially `--plain`; and a no-network acceptance test that enforces the no-network rule. (CI gate is in §4.)
8. **Speak like a colleague.** The insight line states the fact, then the so-what, then optionally a next step — proactive, plain, specific, brief. **Hedge estimated costs** (`~`, "estimated", "about") — never false precision on inferred money. One insight at a time, quiet by default, never blocking. Sentence case; no emoji, no fake urgency, no greetings, no chatbot register.

## 10. Build sequence (let shipping be the validation)

*Throughout, **Phase 1 / Phase 2** denote data tiers — Phase 1 = local-only (the v1 product, built in the steps below); Phase 2 = live quota via session reuse / OAuth, a later capability — not these build steps.*

*Status (post-`v0.1.0`): steps 1–3 are shipped. **Step 1's cost core is verified to the cent against ccusage on real logs**, and its one gap (Codex `CODEX_HOME`) is closed. Step 4 is the remaining build — and per its own note it waits until the shipped core has users. Near-term polish around it: the WSL Windows-root auto-detect (§12) and the Cursor detect-and-defer (§12).*

1. **Core + workspace.** Port `costroid-core`/`-focus`/`-providers` (Claude + Codex) into the clean structure; **re-verify to the cent vs ccusage.** Fast — it's porting, not inventing. The trust foundation.
2. **TUI + full cost picture.** now/trends, subscription + API, filter, the per-lane totals, export, config; Cursor detection (beta) and subscription quota (graceful). Ship it — **getting this adopted is the validation.**
3. **Status-line mode.** `costroid statusline` → tmux / Starship / Claude Code `statusLine`. Cheap table-stakes + a distribution channel; ship as its own small post. Call it a status-line integration, not a "cross-platform terminal toolbar" — Windows Terminal, Ghostty, and Apple Terminal have no toolbar to host.
4. **Frontier / recommendation view.** The `bench` module: frontier + your-position, scoped to what the data honestly supports. Added once the core has users, kept small, cut if it doesn't land.

**Deferred or cut:** the cross-platform taskbar/tray — the most expensive surface, already shipped by codexbar and remigius42, and still only shows a number the TUI and status-line already show. Revisit only on real demand.

## 11. Distribution & release

- **cargo-dist** (the installed binary is `dist`; invoke as `dist …` / `cargo dist …`); `dist init` writes the config + GitHub Actions workflow. **Nothing publishes on a normal push** — only a pushed `vX.Y.Z` tag (which must equal `[workspace.package].version`) triggers CI to build and publish; PRs run `dist plan` only. **Targets (6):** Linux gnu x86_64/aarch64, Linux musl x86_64 (static), macOS x86_64/aarch64, Windows x86_64. (cargo-dist is actively maintained — v0.32.0 — by axodotdev; if it ever stalls, fall back to hand-written installers + `cargo-binstall`, or `release-plz`.)
- Channels: shell + PowerShell installers on GitHub Releases (`releases/latest/download/costroid-installer.sh | sh`); the Homebrew tap (`Costroid/homebrew-tap`, auto-generated by cargo-dist); the npm wrapper (`npx costroid` runs the native binary, no JS runtime); crates.io — **published separately via `cargo publish` in dependency order: `costroid-focus` → `costroid-providers` → `costroid-core` → `costroid`** (see RELEASING.md), `cargo install costroid` / `cargo binstall costroid`. The `costroid-mcp` name is intentionally left unclaimed (no placeholder). **No Scoop bucket** (cargo-dist doesn't support it) — Windows users use the PowerShell installer or `cargo binstall`; a hand-maintained bucket is a later, only-on-demand item.
- **Signing:** releases are **not OS-code-signed**; each artifact ships **keyless GitHub build-provenance attestations + SHA-256 checksums** instead (verify with `gh attestation verify <file> --repo Costroid/costroid`). The trade-off: first run shows a macOS "unidentified developer" / Windows SmartScreen prompt. To enable signing later (paid, and just config toggles on the existing pipeline): macOS notarization (Apple Developer ID, ~$99/yr) + Windows Authenticode (EV cert with HSM/cloud signing, ~$200–300/yr), then cargo-dist's signing config + the matching secrets. Revisit only if those prompts become a real adoption problem.

## 12. The honest flags, in one place

- Don't literally rewrite from zero — **port** the validated cost core; it's the trust asset and the bug-trap you already escaped.
- Tray is cut for v1; "cross-platform terminal toolbar" means the status-line integration (most terminals have no toolbar). Revisit the tray only on real demand.
- Pricing lives inside `costroid-core`, never a top-level directory — cargo won't package it otherwise.
- The subscription endpoints (and Cursor's Phase-2 live fetch) are your fragility risks; isolate behind the trait with graceful "unavailable."
- Recommendation = frontier + your position, not per-task prescriptions; DeepSWE over CursorBench on neutrality.
- The "bigger picture total" is per-lane (API $ / subscription-equivalent $ / quota %), never one merged number.
- Sparkline is hand-rasterized (no Ratatui `Canvas`) — one styling path feeds both the TUI and `--plain`; `NO_COLOR` falls back to ASCII meters/bars (never color alone). Both **settled** (mechanics in §7).
- Iterate the existing crates (0.2.0); don't orphan the claimed names.
- **Opus real-log quirk:** costroid runs ~0.08% under ccusage on opus totals — isolated to re-logged sub-agent (sidechain) cache-read de-dup; mainline matches to the cent. Benign methodology difference, Claude parser unchanged (§9.1); the invoice is ground truth (Phase 2+).
- **WSL Windows-root discovery (fixed):** under WSL with `USERPROFILE` unset, costroid scans `/mnt/c/Users/*` for profiles holding `.claude`/`.config/claude`/`.codex` and merges them — and Codex discovery now merges all roots (was first-root-wins), with session-level cross-root dedup. Residual behaviors to know: a *set* `USERPROFILE` (even empty) is explicit → no scan; the scan is `/mnt/c` only (other drives need the env knobs); it's evidence-based, so it includes *any* Windows profile with logs (e.g. a sandbox profile).
- **Cursor is detection-only in v1:** its CLI keeps no usage/quota on disk (both are live RPC to `api2.cursor.sh`), so Phase 1 shows Cursor present + selected model, **beta**, with usage/quota *"unavailable — live (Phase 2)."* Phase-2 items: live Cursor cost/quota, and a `LimitKind::Daily` (Cursor's quota is daily; the enum has only `FiveHour`/`Weekly` today).

## 13. Document status

- **This doc** = canonical build spec / source of truth (ships in the repo as `ARCHITECTURE.md`, replacing the old one).
- **Companion specs (kept):** DATA-MODEL.md (full FOCUS column mapping, Rust shapes, per-provider field paths, pricing JSON, validator mechanics — summarized in §6) and DESIGN-SYSTEM.md (component dot math, screen mockups, ASCII substitutes, spinner, voice examples — summarized in §4/§7/§9; drop its tray-icon section).
- **Agent operating manual (kept):** AGENTS.md / CLAUDE.md — golden rules, canonical commands, Definition of Done, decide-vs-ask. Distinct from this spec; **true it up to current decisions** (drop the tray and MCP phases, align its phase plan with §10, remove `costroid-mcp`/`apps/bar` from its tree) and point its `docs/` references at this file.
- **HANDOFF.md (superseded — safe to delete):** its vision, scope, and product model are folded into §§1–7, its signing/Scoop facts into §11, and its phase plan is replaced by §10; `ARCHITECTURE.md` + README.md now serve the "start here" role.
- **The validation/launch post** (`costroid-recommendation-validation.md`) = marketing, kept separate; reframe it as the launch post for the shipped core.
- **The earlier validation kit and next-move plan** = superseded for build purposes; their useful discipline (commit-what's-done first, the honest framing, the sequencing) is folded in here.