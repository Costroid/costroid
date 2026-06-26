# Costroid agent operating manual

Costroid is a secure, open-source, FOCUS-native developer tool that shows what your AI coding tools cost — subscription limits (Claude Code / Codex 5-hour + weekly caps with reset countdowns), real API-bill dollars by model, and the measured/estimated cost of running open-weights models on your own hardware — by default entirely from local data, with nothing leaving the machine. It is a Rust Cargo workspace (Apache-2.0, edition 2021), **feature-complete at v0.7.0**: the "Costroid-Next" arc (three-lane FOCUS ledger, cloud/API cost lane, local-inference economics, break-even, loopback web UI) shipped M1–M6 plus the first M3b wall-meter measurement (`gemma-4-31b-dense`; every other local model stays *estimated — pending M3b measurement*). This file is the operating manual: read it before doing anything. Scope + deferred adapters live in [`docs/ROADMAP.md`](docs/ROADMAP.md); release history in [`CHANGELOG.md`](CHANGELOG.md); the technical canon is [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) + [`docs/DESIGN-SYSTEM.md`](docs/DESIGN-SYSTEM.md); measurement honesty in [`docs/methodology.md`](docs/methodology.md) + [`docs/limitations.md`](docs/limitations.md). **When a doc disagrees with the code, the code wins** — verify any symbol/path/flag in the code before relying on it; never invent one.

---

## Golden rules — non-negotiable

If a task seems to require breaking one, **stop and ask the human**.

- **Default build makes ZERO network calls.** Enforced by the strace offline-acceptance test + a two-tier forbidden-crates test. All network + credential code lives **only** in `costroid-connect`, feature-gated (`--features connect`) and **off by default**; a call happens **only** on an explicit user-initiated `connect` / `connections --check` / `reconcile` action, as an HTTPS-only GET to the one authorized provider host (`ureq`+`rustls`, OS-native trust roots, no cert pinning). Never add network code outside `costroid-connect`.
- **No telemetry, ever, by default.** Any update check must be opt-in, disclosed, individually disableable, off by default.
- **Secrets live only in the OS keychain** (`keyring`). Never write tokens/credentials to disk, config, or logs; never read passwords; never route credentials through a server.
- **Cost is always an estimate** (your tokens × current prices) — never the authoritative bill; design for reconciliation against the provider invoice.
- **Permissive licenses only.** No copyleft (GPL / AGPL / LGPL / SSPL). Verify a new dep is MIT / Apache-2.0 / BSD / ISC / Zlib / Unicode before adding it.
- **Accessibility is required.** Every visual has a `--plain` ASCII (screen-reader-friendly) equivalent; **never rely on color alone** — every color carries a shape/text cue (the 0–8 dot-density warning ramp is the non-color cue).
- **No `unwrap()` / `expect()` / `panic!` in library crates.** Propagate errors. (Tests may `panic!`/assert but not `unwrap`/`expect` — clippy denies those even in test code.)
- **No web platform, no chat/LLM-chat interface** here — this repo is the local developer tool only.

---

## Workspace layout

```
costroid/
├─ crates/
│  ├─ costroid-focus/      FOCUS schema types + serde — pure data, no business logic
│  ├─ costroid-providers/  Provider trait + Claude/Codex/Cursor adapters + WSL-aware log discovery + FOCUS v1.2 import
│  ├─ costroid-core/       engine: orchestration, cost calc, bundled pricing, bench/frontier, breakeven, vendor_report, reconcile, display helpers
│  ├─ costroid-config/     shared read-only [budget]/[alerts] TOML schema + loader (both apps; no writer)
│  ├─ costroid-connect/    ALL network + credential code; feature-gated, OFF by default
│  ├─ costroid-store/      SQLite (rusqlite, bundled) metadata-only FOCUS ledger; off-by-default `store` feature; leaf
│  └─ costroid-power/      local-inference economics engine (PowerSampler + subprocess runner + harness); off-by-default `power` feature; leaf
├─ apps/
│  ├─ cli/                 package `costroid` — CLI + Ratatui TUI + statusline + --live + alerts + connect/reconcile + (power) bench/breakeven + import
│  ├─ bar/                 binary `costroid-bar` — egui/eframe + tray-icon taskbar
│  └─ server/              binary `costroid-server` — loopback-only (127.0.0.1) HTTP API + 3-view embedded web UI
└─ .github/workflows/      CI + cargo-dist release pipeline
```

(No `costroid-mcp` — name intentionally unclaimed.)

**Per-crate responsibility:**
- `costroid-focus` — FOCUS record types + (de)serialization only (v1.2-in / v1.3-out); no internal deps.
- `costroid-providers` — the `Provider` trait + `Capability` descriptor, the three adapters, WSL-aware log discovery, the FOCUS v1.2 importer; emits provider-neutral `UsageEvent`/`LimitWindow`; no internal deps.
- `costroid-core` — the engine: orchestrates providers, normalizes to FOCUS, computes estimated cost, houses bench/frontier, the pure `breakeven` engine (no `core→power` edge), the provider-neutral `vendor_report` types, the pure `reconcile` estimate-vs-invoice engine, and money/share display helpers. No UI code, no `costroid-connect`/`costroid-power` dep.
- `costroid-config` — pure local-only leaf: the `[budget]`/`[alerts]` TOML schema + loader so both apps share one schema; no network/keychain/writer (missing file = zero-config default).
- `costroid-connect` — all network + credential code, feature-gated off by default: the OS-keychain `CredentialStore`, the non-secret `ConnectionRegistry`, the authorized-host HTTPS client (`AuthorizedClient`, HTTPS-only/GET-only, redirects+proxies disabled, off-host refused before I/O), and the Anthropic + OpenAI usage-API adapters parsing into `costroid-core::vendor_report` (Gemini = first-class unavailable). Secrets wrapped in `secrecy::SecretString`.
- `costroid-store` — the metadata-only (R4 — no prompt/response content) SQLite ledger of FOCUS rows via `rusqlite` (bundled); fail-closed whitelist schema. Reachable behind the CLI's `store` feature and linked unconditionally by `costroid-server`; never in the default CLI/bar graph.
- `costroid-power` — the local-inference engine: the four-source wall-meter-led `PowerSampler`, the subprocess llama.cpp/Ollama runner + benchmark harness, the dated/stamped hardware+electricity profile + Gemma 4 manifest. Off-by-default `power` feature; a leaf (no `core` edge — the CLI orchestrates and hands `core` a pre-computed `LocalRunEvent`). Never asserts a real power number in CI (synthetic fixtures only); `MEASURED_MODELS` is the human-gated measured allowlist.
- `apps/cli` — `clap` parsing, the Ratatui TUI, the statusline emitter (`--capture-only`/`--wrap`), `setup-statusline` (`--undo`), `--live`, the opt-in `alerts`/`alerts --check`, the feature-gated `connect`/`disconnect`/`connections` + `reconcile`, the `power`-gated `bench`/`breakeven`, and `import`.
- `apps/bar` — `costroid-bar`: a pure core consumer (no new data path/compute/telemetry). Tray glance + live cockpit; the Providers lane is display-only + zero-network. AccessKit on; never color-alone.
- `apps/server` — `costroid-server`: a separate binary, **never linked into `costroid`/`costroid-bar`**. A blocking `tiny_http` server bound to `127.0.0.1` by construction; three server-rendered views (timeline / comparison / break-even) + `?plain` + a JSON API, all assets embedded, zero external requests. Reviewed `SERVER_ALLOWED` allowlist + a runtime loopback-only proof.

**TUI:** 9 numbered tabs — 1 now, 2 trends, 3 providers, 4 models, 5 history, 6 budget, 7 forecast, 8 anomalies, 9 activity — plus the `a`/`esc` Frontier overlay. Colorful via the `SemanticStyle` palette; `--plain`/`NO_COLOR` strip all color.

**Taskbar:** tray glance (most-constrained quota meter in the dot-density warning language) + a live cockpit window (Overview meters, opt-in alert banner, Budget/Forecast/Anomalies/Providers panels). Ships as binary archives + `cargo install costroid-bar` (no npm/Homebrew/musl); macOS/Windows tray paths compile but are NOT field-verified.

---

## Conventions

- **Dependency direction:** `apps → core → {providers, focus}`; `apps → config → core`; `connect → core` (behind the apps' off-by-default `connect` feature). No cycles. `costroid-focus`/`costroid-providers` have no internal deps.
- **crates.io publish ladder:** focus → providers → core → config → connect → store → power → costroid → costroid-server → costroid-bar (see [`RELEASING.md`](RELEASING.md); a topo-sort test guards the order).
- **Errors:** `thiserror` for typed errors in library crates; `anyhow` only in the binaries (`apps/`).
- **MSRV:** 1.88 (libs + CLI), 1.92 (the bar). Tracked + tested in CI.
- **Lockfile:** commit `Cargo.lock` (Costroid ships a binary). `.gitignore` ignores `/target`, not `Cargo.lock`.
- **Dependencies:** lean, well-maintained, permissive; `rustls` not OpenSSL. `cargo deny` is a required CI gate (`deny.toml`): licenses + bans offline; advisories online.
- **Config:** TOML under XDG (e.g. `~/.config/costroid/config.toml`), zero-config defaults; **secrets never go in config** — keychain only.
- **Commits:** small, focused, conventional-commit style.

**Auth source ladder** (only tiers 0–3 are ever built; tier 4 is the ToS line):
0. Local artifacts — provider logs on disk (the default path).
1. Sanctioned push/hook — Claude Code `statusLine` `rate_limits` capture.
2. Sanctioned OAuth (e.g. GitHub; deferred) — system browser + loopback redirect, PKCE.
3. Your own API key — Anthropic/OpenAI usage APIs (user's own admin-class key).
4. **Never** reuse a credential/session/cookie against an undocumented/internal endpoint — account-ban path + ToS violation; that datum stays "unavailable."

Providers today: Claude Code + Codex (full), Cursor (detect-only; cost/quota "unavailable"). Deferred/discovery-gated (never built speculatively): Cursor live quota, GitHub Copilot, Antigravity, Gemini own-key — see [`docs/ROADMAP.md`](docs/ROADMAP.md).

---

## Canonical commands

```bash
cargo build                                              # debug
cargo build --release                                   # release
cargo test --workspace                                  # test
cargo clippy --workspace --all-targets -- -D warnings   # lint (warnings = errors)
cargo fmt --all -- --check                              # format check (cargo fmt --all to apply)
cargo run -p costroid -- <args>                         # run the CLI in dev

# Pre-PR gate — all three must pass
cargo fmt --all -- --check && cargo clippy --workspace --all-targets -- -D warnings && cargo test --workspace
```

A full `cargo test`/`clippy --workspace` (and any `--features connect` build) needs `libdbus-1-dev` + `libsecret-1-dev` for the keychain backend; CI installs them. Releases use cargo-dist (tag-triggered) + crates.io — see [`RELEASING.md`](RELEASING.md).

---

## Definition of Done (every change)

- [ ] `cargo build` clean; `cargo test --workspace` passes.
- [ ] `cargo clippy --workspace --all-targets -- -D warnings` clean.
- [ ] `cargo fmt --all -- --check` clean.
- [ ] No `unwrap`/`expect`/`panic!` introduced in library code.
- [ ] New behavior covered by tests (fixture logs, never real user data).
- [ ] Docs (`README.md` / `CLAUDE.md` / `docs/*`) updated if behavior or interface changed.
- [ ] No new copyleft dependency; new dep licenses verified permissive.
- [ ] Any new visual follows [`docs/DESIGN-SYSTEM.md`](docs/DESIGN-SYSTEM.md): color via the `SemanticStyle` palette (never raw ANSI/`ratatui::Color`), reusing the shared compose helpers; has a `--plain` ASCII equivalent; never relies on color alone.
- [ ] No telemetry; the default/local-only build introduces no network call (network stays in `costroid-connect`, feature-gated, behind explicit user-initiated `connect`).

---

## Decide vs. ask

**Decide on your own:** implementation details, internal refactors, test design, formatting, module structure, and choosing among permissive well-maintained crates.

**Ask the human first before:**
- adding any non-permissive/copyleft/unusual-license dependency;
- changing the public CLI surface or any export/output schema;
- anything touching authentication, secret handling, or the keychain;
- making the default/local-only build perform a network call, adding network code outside `costroid-connect`, or anything that could phone home;
- building a deferred provider adapter (Copilot, Antigravity, Cursor/Gemini live) before its discovery lands.

**Always:** keep commits small; update docs in the same change when behavior shifts; write tests against fixture logs (never real user data); build every visual in the [`docs/DESIGN-SYSTEM.md`](docs/DESIGN-SYSTEM.md) language; provide a `--plain` path for every visual; never rely on color alone; source pricing/model data at build time rather than hardcoding figures that drift.
