# Costroid agent operating manual

Costroid is a secure, open-source, FOCUS-native developer tool that shows what your AI coding tools cost — both subscription limits (Claude Code and Codex 5-hour and weekly caps, with reset countdowns) and real API-bill dollars by model — by default entirely from local data, with nothing leaving the machine. It is a Rust Cargo workspace. This file is the operating manual for any coding agent (and human contributor) working in this repo: read it before doing anything. Scope and build sequencing are governed by `docs/PRODUCT-PLAN.md` — the step-by-step production plan and going-forward source of truth for what to build and in what order; `docs/ARCHITECTURE.md` remains the technical source of truth but defers scope/sequencing to PRODUCT-PLAN. See **[Doc map & canon order](#doc-map--canon-order)** below for which doc owns what, and how conflicts resolve — **when a doc disagrees with the code, the code wins.** Read the relevant `docs/` file before implementing the area it covers. These `docs/` specs are tracked in the repository — read them on disk.

---

## Golden rules — read first, non-negotiable

These are hard constraints. If a task seems to require breaking one, **stop and ask the human** instead.

- **Follow the build steps in `docs/PRODUCT-PLAN.md`.** Surfaces ship in sequence — cost lane, then Claude `statusLine` capture, then the generalized quota model, then connections, then analytical tabs/alerts, then the egui taskbar (the last surface). (Cursor live quota is **not** in this sequence — it is discovery-gated, PRODUCT-PLAN §8.) Don't jump ahead of the step you're on, and don't build a later step's adapter or surface speculatively.
- **Three providers ship today: Claude Code, Codex, Cursor.** GitHub Copilot and Antigravity CLI are *planned* additions via the `Capability` descriptor on the `Provider` trait — but only after a live-install discovery confirms each one's real data/auth/quota shape. Never build either adapter speculatively. (The provider layer is pluggable so adding one is *easy*; that is not permission to guess at one's shape.)
- **Never build the web platform here.** It is a separate, separately-licensed repo. This repo is the local developer tool only.
- **No chat / LLM-chat interface.** Costroid surfaces proactive, plain-language insight; it is not a chatbot and embeds no conversational LLM UI.
- **The default / local-only build makes no network calls** — still *enforced* by the strace offline-acceptance test plus the forbidden-crates test. Network happens **only** through the `costroid-connect` crate, behind a Cargo feature **and** an explicit, user-initiated `connect` action to a provider endpoint the user authorized.
- **No telemetry.** Ever, by default. Any update check must be opt-in, clearly disclosed, individually disableable, and off by default.
- **Secrets live only in the OS keychain** (via the `keyring` crate). Never read passwords. Never write tokens or credentials to disk, config files, or logs. Never route credentials through any server.
- **Local cost is always an estimate** (your tokens × current prices). Never present it as the authoritative bill; design for reconciliation against the provider invoice, which is the source of truth.
- **Keep the core permissive.** This repo is Apache-2.0. Do not add any copyleft (GPL / AGPL / LGPL / SSPL) dependency. Verify a dependency's license is permissive (MIT / Apache-2.0 / BSD / ISC / Zlib / Unicode) before adding it.
- **Accessibility is required, not optional.** Every visual has a `--plain` ASCII equivalent; never rely on color alone (the amber warning state needs a second, non-color cue); `--plain` output must be screen-reader-friendly.
- **No `unwrap()`, `expect()`, or `panic!` in library crates.** Propagate errors. (Tests may use them.)

---

## Doc map & canon order

The single source for navigating these docs and resolving conflicts between them. **PRODUCT-PLAN.md §12.0 (the per-task header) references this block — keep the two in sync by editing only here.**

**Doc map — which doc is authoritative for what.** Read the *one* that owns the area you're touching, not all of them: the docs carry planned + historical content, and reading the wrong section breeds wrong assumptions.

- **`docs/PRODUCT-PLAN.md`** — scope, sequencing, the build steps (§3), the hard invariants (§6), the per-task cards (§12), and the decisions/limitations log (§11.5). Owns *what* to build and *in what order*; **§11.5 is the freshest "what actually shipped"** and where new decisions get logged.
- **`docs/ARCHITECTURE.md`** — the technical canon: crate boundaries + dependency direction (§5), the security/credential boundary + auth ladder (§8), the degrade-never-crash + Claude `rate_limits` sanitize/cross-check rules (§9.2), data flow, render mechanics (§7).
- **`docs/DATA-MODEL.md`** — the data shapes: FOCUS columns, the Rust structs (`UsageEvent` / `FocusRecord` / `LimitWindow` / `LimitMeasure` / `LimitStatus` / core `LimitAvailability`), per-provider field paths, the bundled pricing JSON schema, export shapes.
- **`docs/DESIGN-SYSTEM.md`** — rendering/UX detail: braille dot math, the meter/bar/sparkline components, the ASCII/`--plain` substitutes, the always-on non-color cue, voice.
- **A task's Spec** (named in its §12 card, e.g. `docs/STATUSLINE-CAPTURE-BRIEF.md`) — read it fully when the card says so; it IS that task's design.
- **User-facing docs** (`README.md`, `SECURITY.md`, `CHANGELOG.md`) are *downstream*, not input canon — update them only when a change shifts user-facing behavior/interface (Definition of Done).

**Canon order — how to resolve any conflict, and the #1 way to avoid hallucinating.**

- For anything **already built, the CODE on disk is canon.** When a doc disagrees with the code, the **code wins**; PRODUCT-PLAN §11.5 records what actually shipped (newer than ARCHITECTURE / DATA-MODEL / the brief).
- For anything **not yet built, design intent lives in the docs:** PRODUCT-PLAN §3/§6 + the §12 card own scope/sequencing/invariants; ARCHITECTURE owns the technical design; the per-task Spec owns that task's design.
- Either way, before relying on **any** doc statement about *current* behavior ("X returns unavailable", "Y is not built", or that a type/field/path/flag/function exists), **verify it in the code first** (grep/read it) — never invent or assume a symbol. Fix any drift you find as part of keeping the plan current.

---

## Environment & setup

**Prerequisites (local-only build):** Rust via `rustup` (with `clippy` and `rustfmt` components), plus `build-essential`, `pkg-config`, and `git`. The keyring deps (`libdbus-1-dev`, `libsecret-1-dev`) land **with the connections step** (Step 4 / v0.4.0, see `docs/PRODUCT-PLAN.md` §2c) — don't install them before then. The egui taskbar (Step 6) is built on `eframe`/`egui` + the `tray-icon` crate (no Tauri, no webview); its deps land with that step.

**WSL:**
- Work on the **Linux filesystem** (`~/costroid`), never under `/mnt/c` — cross-mount builds are slow and file-watching is flaky.
- Path discovery must be **WSL-aware**: when the AI tools run on Windows, their logs live under `/mnt/c/Users/<user>/...` as seen from WSL; when they run inside WSL, under `~`. Handle both.
- The CLI and TUI develop and run fine in WSL.

**Canonical commands** — run these; do not invent variants:

```bash
# Build
cargo build                              # debug
cargo build --release                    # release

# Test
cargo test --workspace

# Lint (warnings are errors)
cargo clippy --workspace --all-targets -- -D warnings

# Format
cargo fmt --all -- --check               # check (use in CI / before commit)
cargo fmt --all                          # apply

# Run the CLI during development (apps/cli is the `costroid` package)
cargo run -p costroid -- <args>

# Pre-PR gate — all three must pass
cargo fmt --all -- --check && cargo clippy --workspace --all-targets -- -D warnings && cargo test --workspace
```

**Release** uses cargo-dist (the installed binary is `dist`; invoke as `dist …` or `cargo dist …`):

```bash
cargo install cargo-dist                 # provides the `dist` binary
dist init                                 # one-time: writes dist config + the GitHub Actions release workflow
dist build                                # build installers/archives locally to verify
# Releases are then cut by pushing a version tag; CI builds and publishes the installers,
# the Homebrew tap, and the npm wrapper, each artifact checksummed + build-provenance-attested.
# The crates are published to crates.io separately (`cargo publish`, in dependency order — see
# RELEASING.md). (Scoop is not supported by cargo-dist.)
```

CI runs the pre-PR gate (fmt + clippy + test) on every push and PR.

---

## Repo conventions

**Workspace layout:**

```
costroid/
├─ Cargo.toml              workspace
├─ crates/
│  ├─ costroid-core/       engine: orchestration, cost calc, bundled pricing, bench/recommend (frontier)
│  ├─ costroid-focus/      FOCUS schema types + serde — no business logic
│  ├─ costroid-providers/  Provider trait + Claude Code/Codex/Cursor adapters + WSL-aware log discovery
│  └─ costroid-connect/    ALL network + credential code; feature-gated, OFF by default (Step 4 / v0.4.0)
├─ apps/
│  ├─ cli/                 package `costroid`, binary `costroid` — CLI + Ratatui TUI + statusline + --live (`setup-statusline`: planned, Step 2/5)
│  └─ bar/                 binary `costroid-bar` — egui/eframe + `tray-icon` taskbar app (Step 6 / v0.6.0); depends only on `costroid-core`
└─ .github/workflows/      CI + cargo-dist release pipeline
```

No `costroid-mcp` (name intentionally unclaimed). `costroid-connect` lands at Step 4 and `apps/bar` at Step 6 — see `docs/PRODUCT-PLAN.md` §2c/§2d and ARCHITECTURE §5.

**What belongs where:**
- `costroid-core` — the engine. Orchestrates providers, normalizes to FOCUS via `costroid-focus`, computes estimated cost, and houses the `bench`/`recommend` (frontier) module. No terminal/UI code.
- `costroid-focus` — FOCUS record types and (de)serialization only. Pure data; depends on nothing internal.
- `costroid-providers` — the `Provider` trait (plus the `Capability` descriptor — landed in T3: the `DataSource`/`AuthMethod` enums + the `Capability` struct + a required `capability()` trait method, declared by all three adapters), the three adapters that ship today, and WSL-aware log discovery. Depends only on `costroid-focus`.
- `costroid-connect` — **all** network + credential code; feature-gated and **off by default**. HTTP via `ureq` + `rustls` (no async runtime); secrets via `keyring` (OS keychain only). Lands at Step 4 (v0.4.0). Depends on `costroid-core`/`costroid-focus`.
- `apps/cli` — argument parsing (`clap`), the Ratatui TUI, the statusline emitter, `--live`, and all rendering (`setup-statusline` is planned — Step 2/5). Depends on `costroid-core`.
- `apps/bar` — binary `costroid-bar`: the egui/eframe + `tray-icon` taskbar app (Step 6); accessibility via AccessKit, never color-alone. Depends only on `costroid-core`.

**Dependency direction:** `apps → core → {providers, focus}`; `providers → focus`; `connect → {core, focus}`. No cycles. `costroid-focus` has no internal dependencies.

**Errors:** `thiserror` for typed errors in library crates; `anyhow` only in the binaries (`apps/`). No `unwrap`/`expect`/`panic!` in library code.

**Logging:** `tracing` for local diagnostics only — never networked, never telemetry. Quiet by default; `-v`/`-vv` raise verbosity.

**Edition & MSRV:** Rust edition 2021. Track the latest stable Rust; document and test the MSRV in CI.

**Lockfile:** commit `Cargo.lock` — Costroid is an application (it ships the `costroid` binary), so the lockfile is tracked for reproducible, verifiable builds; ensure `.gitignore` ignores `/target` but not `Cargo.lock`.

**Dependencies:** prefer lean, well-maintained, permissively-licensed crates. Use `rustls`, not OpenSSL, for TLS. Recommended (not required): `cargo deny` for license + advisory checks in CI.

**Config:** a TOML config under the XDG config dir (e.g. `~/.config/costroid/config.toml`), with sensible zero-config defaults. **Secrets never go in config** — keychain only.

**Commits:** small and focused; conventional-commit style preferred.

---

## Definition of Done (apply to every change)

- [ ] `cargo build` clean and `cargo test --workspace` passes.
- [ ] `cargo clippy --workspace --all-targets -- -D warnings` is clean.
- [ ] `cargo fmt --all -- --check` is clean.
- [ ] No `unwrap`/`expect`/`panic!` introduced in library code.
- [ ] New behavior is covered by tests (use fixture logs, never real user data).
- [ ] Docs (`README.md` / `CLAUDE.md` / `docs/*`) updated if behavior or interface changed.
- [ ] No new copyleft dependency; new dependency licenses verified permissive.
- [ ] Any new visual has a `--plain` ASCII equivalent and does not rely on color alone.
- [ ] No telemetry; the default/local-only build introduces no network call (any network stays inside `costroid-connect`, feature-gated and behind an explicit user-initiated `connect`).
- [ ] Change stays on the current build step (`docs/PRODUCT-PLAN.md` §3).

---

## Build status & scope (the build steps)

Scope and sequencing are governed by `docs/PRODUCT-PLAN.md` §3 — the step-by-step production plan. Build the step you're on; don't jump ahead, and don't build a later step's adapter or surface speculatively. Steps below are tagged CURRENT (built, v0.1.0) vs PLANNED (roadmap).

### Built today (v0.1.0)

1. **Core + workspace.** `costroid-core` / `costroid-focus` / `costroid-providers` (Claude + Codex), verified to the cent vs ccusage. *Shipped (v0.1.0).*
2. **TUI + full cost picture.** `now` / `trends`, subscription + API, filter, per-lane totals, export, config; Cursor **detection only** (beta); subscription quota graceful. *Shipped (v0.1.0).*
3. **Frontier / recommendation view (`costroid frontier`).** The `bench` module: the cost-vs-quality frontier + the user's position, scoped to what the data honestly supports; advisory, sourced, **API-cost rows only**. *Built and gate-green.*
4. **Status-line emitter.** `costroid statusline` (tmux / Starship / Claude Code `statusLine`). *Emitter shipped (v0.1.0).*

### Planned — the spine

The full step sequence (goals, deliverables, acceptance, and the generalized-quota + `Capability` design) is owned by `docs/PRODUCT-PLAN.md` §3 — read it there rather than restating it here (a duplicated list drifts). The arc by release: **0.2.0** ship the built cost lane → **0.3.0** Claude `statusLine` capture (flagship) + the generalized quota model → **0.4.0** connections (`costroid-connect`, first network code) → **0.5.0** analytical tabs + alerts → **0.6.0** the egui taskbar (`apps/bar`, the last surface). (Cursor live quota is discovery-gated — PRODUCT-PLAN §8 — not a numbered release.)

### Acceptance criteria (the local cost + quota product)

- [ ] Workspace builds; `cargo install --path apps/cli` installs a working `costroid` binary.
- [ ] Detects installed providers (Claude Code, Codex, Cursor) by locating their local data, including WSL→Windows paths; degrades gracefully when a provider is absent.
- [ ] `costroid` (the **now** screen): shows current API spend by model **and** 5-hour + weekly subscription limits with reset countdowns, from local data, with **no network calls** (Claude's 5h/7d via the `statusLine` cache — T4 landed the *reader* (sanitize + cross-check); the *writer* (`setup-statusline`, T5) and the *render* (T6) are still pending, so no Claude quota flows to the screen yet; Codex's from local windows today; Cursor quota is detect-and-defer).
- [ ] `costroid trends`: `--period day|week|month|year` and `--group model|app|total` both work.
- [ ] `costroid --live`: refreshes in place; `q`/Ctrl-C exits cleanly; works over SSH and inside tmux.
- [ ] `costroid statusline`: emits a compact one-line status suitable for a shell prompt, tmux, or Starship; `costroid setup-statusline` wires Claude Code's `statusLine` for live quota.
- [ ] `costroid frontier`: shows the cost-vs-quality frontier and the user's position; advisory, sourced, **API-cost rows only**; un-benchmarked models shown as gaps, never guessed.
- [ ] `costroid export`: emits FOCUS 1.3-conformant records (`--format json|csv`) that validate against the schema in `docs/DATA-MODEL.md`.
- [ ] `--plain`: every screen renders in ASCII, no color, no braille, identical data, screen-reader-friendly.
- [ ] Pricing comes from bundled curated JSON; the tool works fully offline; all cost figures are labeled estimates.
- [ ] Subscription limits and API costs are modeled separately (limits are not summable dollars); a model used both ways appears in both, marked by access path.
- [ ] Live Claude quota from the `statusLine` `rate_limits` field is **sanitized + cross-checked** and degrades to "unavailable"/"unverified", never a confident wrong number (ARCHITECTURE §9.2).
- [ ] Zero telemetry; zero unauthorized network calls.
- [ ] Ships via cargo-dist (shell + PowerShell installers + Homebrew tap + npm wrapper, each artifact checksummed and build-provenance-attested; tag-triggered release in CI) and crates.io (`cargo install costroid` / `cargo binstall costroid`). (Scoop unsupported by cargo-dist — see RELEASING.md.)
- [ ] CI green: fmt + clippy + test + FOCUS-conformance + `cargo deny` + the strace offline-acceptance test (which proves the default/local-only build makes zero network calls — re-scoped to the local path once `costroid-connect` lands at Step 4) + the forbidden-crates test.

**Acceptance test:** on a machine with real Claude Code / Codex / Cursor logs and **networking disabled**, `costroid`, `costroid trends --period month --group model`, `costroid frontier`, `costroid export --format json`, and `costroid --plain` all produce correct output.

### Connections & live quota (Step 4, opt-in) — the auth source ladder

Network + credential code lives **only** in `costroid-connect` (feature-gated, off by default), and every source is chosen by descending an explicit ladder, most-sanctioned first — **only tiers 0–3 are ever built; tier 4 is the ToS line** (see `docs/PRODUCT-PLAN.md` §5):

0. **Local artifacts** — provider logs on disk (today's default path).
1. **Sanctioned push / hook** — Claude's `statusLine` `rate_limits` capture.
2. **Sanctioned OAuth** (GitHub; deferred) — first-party, system browser + loopback redirect, PKCE.
3. **Your own API key** — Anthropic/OpenAI/Gemini *usage* APIs, the user's own key.
4. **Never** reuse any credential, session, or token against a non-sanctioned, undocumented, or internal endpoint (this includes reusing a local Cursor session against `api2.cursor.sh`), and never read browser cookies — that's an account-ban path and a ToS violation; that datum stays "unavailable," never fetched.

**Step 4 (v0.4.0) — Connections.**

- [ ] `costroid connect/disconnect <provider>` plus a Connections view that lists what is linked and supports instant disconnect/revoke; nothing is stored outside the keychain.
- [ ] Tokens/keys stored **only** in the OS keychain (`keyring`); HTTP via `ureq` + `rustls`, strictly device↔provider, never via a server.
- [ ] All network calls limited to provider endpoints the user authorized; still no telemetry.

**Step 5 (v0.5.0) — alerts.**

- [ ] Threshold notifications fire when a window crosses warning/critical; quiet or off by default; user-configurable.

**Acceptance test (Step 4):** a user with no prior session can connect via the browser/own key, see live quotas, revoke access, and confirm (e.g. by inspecting the keychain and the filesystem) that no secret was written to disk, config, or logs.

### Deferred — discovery-gated provider adapters (PRODUCT-PLAN §8)

Planned, but only after a live-install discovery confirms each one's real data/auth/quota shape — never built speculatively, each added via the `Capability` descriptor on the `Provider` trait:

- **Cursor live quota** — Cursor serves usage/quota server-side only. It has a sanctioned `cursor-agent /statusline` hook and a documented Admin/Analytics usage API, but **neither carries an individual's quota** (statusline = session metadata only; Admin API = team-admin/enterprise-only) — so Cursor stays detect-only and its cost/quota are "unavailable." A live fetch is pursued **only if** Cursor publishes a documented per-user API/OAuth — or adds a quota field to its existing `/statusline` (the unlock to watch) — **never** by reusing a local Cursor session against its undocumented `api2.cursor.sh` RPC (a ToS violation). Quota shape (monthly $-credit pool + overage; daily token rate-limit on free tier) already maps to the generalized model; only a sanctioned source is missing.
- **GitHub Copilot** — as of 2026-06-01, **AI Credits** (dollar-denominated monthly pool + overage) **replaced** premium requests; request-count is the **legacy** model. **A completely ToS-safe path is identified** (verified 2026-06-05): the user's **own classic PAT** (fine-grained PATs are unsupported on the billing endpoints) or `gh` OAuth → the documented `GET /users/{username}/settings/billing/ai_credit/usage` → AI-credit consumption + $-by-model; the Copilot CLI `statusLine` hook adds session cost. **User-billed only** (enterprise-billed → "unavailable"). **Never** the internal `api.github.com/copilot_internal/user`. Not promised until a live-install check confirms the endpoint on a personal plan.
- **Antigravity CLI** — split (verified 2026-06-05): the **Gemini-API $ lane is ToS-safe** (the user's own Gemini key → AI Studio dashboards + Cloud Billing BigQuery export); the **"compute-effort" subscription quota has no sanctioned source** (Hooks aren't fed quota; transcripts are content only; IDE `.pb` is keychain-encrypted; the only quota source is the internal `GetUserStatus` RPC via a reused token = ban path) → quota stays "unavailable."

### Speculative / unbuilt

- **MCP server** (`costroid-mcp`) — speculative; the crates.io name is intentionally left unclaimed (no placeholder). Not built. The recommendation engine it would have exposed is built into the frontier view.

---

## Working style — decide vs. ask

**Decide on your own:** implementation details, internal refactors, test design, formatting, module structure, and choosing among permissive, well-maintained crates.

**Ask the human first before:**
- adding any dependency that is non-permissive, copyleft, or unusual in license;
- changing the public CLI surface or any export/output schema;
- anything touching authentication, secret handling, or the keychain;
- making the default/local-only build perform a network call, adding network code outside `costroid-connect`, or adding anything that could phone home;
- expanding scope beyond the build steps in `docs/PRODUCT-PLAN.md` §3, or building a deferred provider adapter (Copilot, Antigravity) before its discovery lands.

**Always:** keep commits small, update docs in the same change when behavior shifts, write tests against fixture logs (never real user data), provide a `--plain` path for every visual, never rely on color alone, and source pricing/model data at build time rather than hardcoding figures that drift.

---

## Quick reference

```bash
cargo build                                          # build
cargo test --workspace                               # test
cargo clippy --workspace --all-targets -- -D warnings  # lint (deny warnings)
cargo fmt --all -- --check                           # format check
cargo run -p costroid -- <args>                      # run the CLI
```

follow the PRODUCT-PLAN steps · three providers today (Copilot + Antigravity discovery-gated later) · local logs + sanctioned `statusLine` push · network only via `costroid-connect` (feature-gated, off by default, user-initiated) · the default/local-only build makes no network calls · no telemetry · secrets in keychain · `--plain` for everything · cost is an estimate.