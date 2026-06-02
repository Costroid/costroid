# Architecture

This document describes how Costroid is built: the workspace, how data flows through it, the provider abstraction, the security boundary, the rendering layer, the MCP server, the release pipeline, and cross-cutting concerns. It is implementation-oriented. For the cost data shapes see [DATA-MODEL.md](DATA-MODEL.md); for the dot-rendering rules see [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md); for build rules and per-phase acceptance criteria see [../AGENTS.md](../AGENTS.md).

Code sketches below illustrate intent and naming; they are not frozen signatures.

## Workspace layout

Costroid is a single Cargo workspace (Rust edition 2021; track the latest stable, document the MSRV in CI).

```
costroid/
├─ Cargo.toml              # [workspace] — shared deps, lints, profiles
├─ crates/
│  ├─ costroid-core/       # Engine: orchestration, cost calc, recommendations (Phase 4).
│  ├─ costroid-focus/      # FOCUS schema types + serde. No business logic.
│  ├─ costroid-providers/  # Provider trait + Claude Code/Codex/Cursor adapters + log discovery.
│  └─ costroid-mcp/        # MCP server exposing core data + recommendations (Phase 4).
├─ apps/
│  ├─ cli/                 # package `costroid`, binary `costroid`: clap CLI + Ratatui TUI + statusline + --live
│  └─ bar/                 # Tauri 2 tray app (Phase 3)
├─ pricing/                # bundled curated pricing JSON (embedded at build; see DATA-MODEL.md)
├─ fixtures/               # sample provider logs for tests — never real user data
└─ .github/workflows/      # CI (fmt + clippy + test) + cargo-dist release
```

Responsibilities:

- **`costroid-core`** — the engine. Orchestrates providers, normalizes their output into FOCUS records via `costroid-focus`, computes estimated cost from the bundled pricing table, aggregates by period and group, and (Phase 4) houses the recommendation engine. No terminal/UI code.
- **`costroid-focus`** — the FOCUS record types and their (de)serialization to JSON and CSV. Pure data; no internal dependencies. This is the crate most likely to be extracted later (as `focus-rs`) and reused by the platform.
- **`costroid-providers`** — the `Provider` trait, one adapter per provider, and WSL-aware discovery of each provider's local data. Depends only on `costroid-focus`.
- **`costroid-mcp`** — wraps `costroid-core` as an MCP server (Phase 4).
- **`apps/cli`** — the `costroid` binary: argument parsing, the TUI, the statusline emitter, `--live`, and all rendering, including the `--plain` path. Depends on `costroid-core`.
- **`apps/bar`** — the tray app (Phase 3). Depends on `costroid-core`.

**Dependency direction (no cycles):**

```
apps/cli ─┐
          ├─► costroid-core ─► costroid-providers ─► costroid-focus
apps/bar ─┘                └─► costroid-focus
costroid-mcp ─► costroid-core
```

`costroid-focus` sits at the bottom and depends on nothing internal. Nothing depends on the apps.

## Data flow

The Phase 1 pipeline is entirely local and makes no network calls:

```
local logs ─► provider.discover() ─► provider.parse_usage()/parse_limits()
           ─► core: normalize to FOCUS (costroid-focus) + cost calc (pricing JSON)
           ─► core: aggregate (period, group)
           ─► render (TUI / statusline) ── or ── export (JSON / CSV)
```

1. **Discover.** Each provider locates its local data (WSL-aware; see below). Missing providers are skipped, not fatal.
2. **Parse.** Adapters read the raw logs and emit normalized intermediate records — usage events (tokens, model, timestamp, project) and limit windows (quota %, window kind, reset time).
3. **Normalize + cost.** `costroid-core` maps usage events into FOCUS records via `costroid-focus`, attaching an **estimated** cost computed from the embedded pricing table. Subscription limits are kept as a **separate** quota-window type — they are not FOCUS cost rows and are never summed into dollars (see DATA-MODEL.md).
4. **Aggregate.** The engine rolls records up by period (`day`/`week`/`month`/`year`) and group (`model`/`app`/`total`).
5. **Sink.** The aggregated data is handed to a renderer (the now/trends screens, the statusline) or to an exporter (FOCUS JSON/CSV). Renderers and exporters are pure consumers of the same in-memory model.

## Provider abstraction

Providers are pluggable from day one so adding one later is mechanical — but the set is fixed at three (Claude Code, Codex, Cursor) unless explicitly expanded.

```rust
/// A source of local AI-tool usage data. One implementation per provider.
pub trait Provider: Send + Sync {
    /// Stable id, e.g. "claude-code", "codex", "cursor".
    fn id(&self) -> &'static str;

    /// Locate this provider's local data, honoring WSL→Windows paths.
    /// Ok(None) means "not installed / no data" — not an error.
    fn discover(&self, env: &HostEnv) -> Result<Option<DataLocation>, ProviderError>;

    /// Parse local logs into normalized usage events (Phase 1).
    fn parse_usage(&self, loc: &DataLocation) -> Result<Vec<UsageEvent>, ProviderError>;

    /// Parse subscription limit windows from local data, where available (Phase 1).
    fn parse_limits(&self, loc: &DataLocation) -> Result<Vec<LimitWindow>, ProviderError>;

    /// Phase 2: live quota via an existing local session — no new login.
    fn live_limits(&self, _session: &Session) -> Result<Vec<LimitWindow>, ProviderError> {
        Ok(Vec::new())
    }
}
```

**Registry.** `costroid-core` holds a registry of boxed `Provider`s and iterates them; the CLI selects all detected providers or a subset via flags.

**Adding a provider** (when authorized): implement `Provider` in `costroid-providers`, add discovery paths, map its log format to `UsageEvent`/`LimitWindow`, register it, and add a fixture under `fixtures/`. No changes outside `costroid-providers` should be required — that is the test of whether the abstraction is right.

**WSL-aware path discovery.** `HostEnv` captures whether we're under WSL and resolves candidate roots accordingly:

- Native (Linux/macOS/Windows): the provider's standard config/data dirs (XDG / `~/Library` / `%APPDATA%`).
- Under WSL: check both the WSL home (`~`) **and** the Windows user profile as mounted (`/mnt/c/Users/<user>/...`), because the AI tools may run on Windows while Costroid runs in WSL. Detect WSL via `/proc/sys/kernel/osrelease` (contains `microsoft`/`WSL`) and resolve the Windows user via the mounted profile.

Discovery returns a `DataLocation` describing the files found; parsing never assumes a single fixed path.

## Authentication tiers and the security boundary

The hard rule: **secrets live only in the OS keychain, and credentials flow only between the device and the provider — never through any Costroid server.** There is no Costroid backend in this product.

- **Tier 1 — local logs (Phase 1, default).** No login. Read what the tools already wrote to disk. This is the entire Phase 1 surface and involves no credentials and no network.
- **Tier 2 — reuse existing session (Phase 2).** Detect the token/session the official CLI already stored and use it to query the provider's own quota endpoint for live limits. No new login.
- **Tier 3 — OAuth login (Phase 2).** Optional. System browser + loopback redirect with PKCE (`oauth2` crate + a short-lived local listener). The resulting token is stored **only** in the OS keychain via the `keyring` crate (macOS Keychain, Windows Credential Manager, Linux Secret Service / libsecret).

Browser-cookie reading, if ever implemented, is a clearly-disclosed, off-by-default last resort. Tokens, sessions, and cookies are **never** written to disk, config, or logs. A connections view (Phase 2) lists what is linked and supports instant disconnect/revoke. TLS uses `rustls` (no OpenSSL). All network access is confined to provider endpoints the user explicitly authorized.

## Rendering layer

The renderer lives in `apps/cli` on top of **Ratatui** (v0.30) with the **crossterm** backend. Braille is the rendering primitive; the exact dot math, thresholds, and component specs are in [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md). This section covers the mechanics.

**Render mode.** A single enum threads through all drawing:

```rust
enum RenderMode {
    Braille, // default: dot-rendered charts/meters, color
    Ascii,   // braille unsupported by term/font: ASCII markers, color
    Plain,   // --plain: no TUI chrome, plain text, no color, screen-reader friendly
}
```

Mode is chosen from: the explicit `--plain` flag, TTY detection (non-tty ⇒ Plain, for pipes/CI), the `NO_COLOR` env var (disables color), and a terminal/font braille-capability check that automatically selects `Ascii` when braille isn't supported. `Ascii` is an internal fallback, not a user-facing flag — the only mode flag is `--plain`. Color is never the only signal — the amber warning state always carries a second cue (a marker/word), so `NO_COLOR` and `--plain` lose no information.

**Output mode (interactive vs one-shot).** Separate from `RenderMode`, the binary chooses between the interactive TUI and a single printed render. On an interactive TTY, `costroid` and `costroid trends` launch the navigable Ratatui TUI; when stdout is piped/redirected, when `--plain` is passed, or for `costroid statusline` and `costroid export`, the output is **one-shot** (render once and exit). `--plain` always bypasses the TUI — an alternate-screen application is unusable for a screen reader.

**One source of visual truth.** The renderer produces a neutral **styled document** — lines of styled spans carrying *semantic* styles (strong, dim, warn, critical, plain), not raw escape codes. Two adapters consume the same document: the one-shot path serializes it to an ANSI (or plain) `String`, and the TUI path maps it to Ratatui `Text`/`Style`. This keeps the one-shot output and the interactive TUI identical in content and look, and lets the one-shot serializer be snapshot-tested as the compatibility contract.

**Braille is computed, not borrowed.** Meters, cost bars, and the sparkline are all drawn by computing braille codepoints directly (`U+2800` + an 8-dot bitmask), *not* via Ratatui's `symbols::braille` constants or its `Canvas` widget — those constants changed across versions, so computing from the codepoint is stable and keeps the look identical on both the one-shot and TUI paths. Fill is **positional** — the glyph itself distinguishes used / partial-boundary / remaining cells — with color/intensity only a secondary cue, so meters stay legible under `NO_COLOR`, color-blindness, and `--plain`. In `RenderMode::Ascii` the runs fall back to ASCII markers (`[####--]`, an ASCII height ramp); in `RenderMode::Plain` charts degrade to labeled numeric rows. See [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md) for the exact dot math and the per-component rules.

**Screens.** Two top-level views, selected by subcommand/state:

- **now** — live 5-hour and weekly limit meters with reset countdowns, plus current API spend by model. Driven by `parse_limits` (Phase 1) / `live_limits` (Phase 2) and the cost aggregation.
- **trends** — a spend chart over `--period day|week|month|year`, grouped by `--group model|app|total`, with a breakdown list.

**Interactive loop.** On a TTY the now/trends screens run as a navigable Ratatui app (keybindings per DESIGN-SYSTEM: `tab`, `d`/`w`/`m`/`y`, `g`, `f` or `/`, `r`, `?`, `q`; `a` is a Phase-4 stub). It enters raw mode + the alternate screen and runs a crossterm event loop (`poll` with a tick deadline, so it never busy-spins) that redraws on input, on resize (regenerating the styled document at the new width), and on a tick. **`--live`** turns the tick into a periodic data re-collect (about every 2s); without it the screen is a snapshot refreshed manually with `r`. The terminal is **always restored** — raw mode off, alternate screen left, cursor shown — on `q`/`Ctrl-C`, on error, and on panic, via a restore guard plus a panic hook that leaves the alternate screen, chains the default panic handler so the message stays visible, and exits with status 101 (so a panic can leave neither the terminal wedged nor the process hung). Works over SSH and inside tmux.

**Statusline (`costroid statusline`).** A non-interactive subcommand that prints a single compact line to stdout and exits — for shell prompts, tmux, and Starship. It consumes the same core data, renders a braille glyph plus key figures, honors `RenderMode` (ASCII/plain variants), and supports a configurable format string. It must be fast and side-effect-free.

## MCP server and recommendation engine (Phase 4)

**Recommendation engine.** Lives in `costroid-core` as a `recommend` module that consumes the FOCUS records plus bundled, build-time-sourced pricing and benchmark data. It is vendor-neutral and multi-benchmark; **CursorBench is one clearly-labeled vendor input, never the sole basis**. Recommendations apply **only** to API-cost rows (where model choice changes the bill), never to subscription rows, and each carries its reasoning, its sources, and a projected dollar delta. All output is advisory.

**MCP server.** `costroid-mcp` wraps the engine and aggregation using the `rmcp` crate, exposed via `costroid mcp` over stdio so an MCP client (e.g. a coding agent) can call it. It surfaces read-only tools/resources such as querying spend (by period/group), reading current limits, and requesting a recommendation. It honors the same security rules — local data only, no telemetry.

## Distribution and release

Releases are produced by **cargo-dist** (the installed binary is `dist`; invoke as `dist …` or `cargo dist …`). `dist init` writes the dist config and the GitHub Actions release workflow; tagging a version triggers CI to build and publish.

Outputs and channels:

- **Shell + PowerShell installers** hosted on GitHub Releases (the `releases/latest/download/costroid-installer.sh | sh` and `… .ps1 | iex` pattern).
- **Homebrew formula** published to `Costroid/homebrew-tap`, and a **Scoop manifest** published to `Costroid/scoop-bucket` — both generated by dist, not hand-maintained.
- **npm wrapper** so `npx costroid` runs the native binary (no JS runtime dependency at runtime).
- **crates.io**: `cargo install costroid`.
- **Checksums and attestations** on each release artifact.

**Signing.** macOS builds are signed with an Apple Developer ID and notarized; Windows builds use Authenticode (an EV cert avoids SmartScreen reputation prompts). Signing is non-negotiable for a security-branded tool — unsigned binaries trigger OS warnings that undercut trust.

**Tray app (Phase 3).** Built with the Tauri 2 bundler (`.dmg`/`.app`, `.msi`, `.AppImage`/`.deb`) and updated via `tauri-plugin-updater`, which serves a static signed update JSON from GitHub Releases (the updater **requires** a signature — keep the private signing key safe and out of the repo). The tray uses `tauri::tray::TrayIconBuilder` (with the `tray-icon` feature) and `WebviewWindow` for any popover. **Build and test the tray on the host OS, not in WSL** — WSL has no real system tray and GUI apps need WSLg. The CLI/TUI build and run fine in WSL.

A tooling caveat: cargo-dist is now community-maintained and its binary was renamed to `dist`; it still works and receives fixes. If it stalls, fall back to hand-written installers plus `cargo-binstall`, or `release-plz` for release automation.

## Cross-cutting concerns

- **Errors.** `thiserror` for typed errors in library crates (`ProviderError`, `FocusError`, `CoreError`); `anyhow` only in the binaries. **No `unwrap`/`expect`/`panic!` in library code** — propagate. The TUI additionally wraps execution in a terminal-restoring guard so even an unexpected panic restores the terminal.
- **Config.** A TOML file under the XDG config dir (e.g. `~/.config/costroid/config.toml`), with sensible zero-config defaults. **Secrets never go in config** — keychain only.
- **No telemetry.** Phase 1 makes no network calls at all. Any update check (later) is opt-in, disclosed, and individually disableable; nothing else phones home.
- **Tracing.** `tracing` with an env filter for local diagnostics only — never networked. Quiet by default; `-v`/`-vv` raise verbosity to stderr.
- **Pricing data.** A curated JSON table embedded at build time (see DATA-MODEL.md). It must be **sourced current at build time**, never hardcoded with figures that drift, and the tool works fully offline against it. Cost is always labeled an estimate.
- **Testing.** Unit tests per crate; integration tests in `apps/cli` driven by committed `fixtures/` logs (never real user data); snapshot tests (e.g. `insta`) for rendered output, especially the `--plain` path; and a Phase 1 acceptance test run with networking disabled to enforce the no-network rule. CI gate: `cargo fmt --all -- --check` + `cargo clippy --workspace --all-targets -- -D warnings` + `cargo test --workspace`.