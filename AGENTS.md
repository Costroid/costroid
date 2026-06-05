# Costroid agent operating manual

Costroid is a secure, open-source, FOCUS-native developer tool that shows what your AI coding tools cost — both subscription limits (Claude Code, Codex, Cursor session and weekly caps, with reset countdowns) and real API-bill dollars by model — entirely from local data, with nothing leaving the machine. It is a Rust Cargo workspace. This file is the operating manual for any coding agent (and human contributor) working in this repo: read it before doing anything. The canonical build spec and phase sequencing live in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — the source of truth that governs this file; for technical detail see [docs/DATA-MODEL.md](docs/DATA-MODEL.md) and [docs/DESIGN-SYSTEM.md](docs/DESIGN-SYSTEM.md). Read the relevant `docs/` file before implementing the area it covers.

---

## Golden rules — read first, non-negotiable

These are hard constraints. If a task seems to require breaking one, **stop and ask the human** instead.

- **Stay within v1 scope** (the build steps in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §10). **"Phase" denotes a data tier, not a feature roadmap:** Phase 1 = local logs + sanctioned push (the v1 product — includes Claude's live 5h/7d via the `statusLine` hook and Codex's local windows); Phase 2 = the disfavored credential-reuse / OAuth path, needed only for **Cursor's** live quota. Do not implement Phase-2 features speculatively.
- **Providers are exactly three: Claude Code, Codex, Cursor.** Never add a fourth without explicit instruction. (The provider layer is pluggable so adding one is *easy* later — that is not permission to do it now.)
- **Never build the web platform here.** It is a separate, separately-licensed repo. This repo is the local developer tool only.
- **No chat / LLM-chat interface.** Costroid surfaces proactive, plain-language insight; it is not a chatbot and embeds no conversational LLM UI.
- **No network calls except provider endpoints the user has explicitly authorized.** **Phase 1 makes no network calls at all** — it reads local data only (provider logs plus Claude's sanctioned `statusLine` cache).
- **No telemetry.** Ever, by default. Any update check must be opt-in, clearly disclosed, individually disableable, and off by default.
- **Secrets live only in the OS keychain** (via the `keyring` crate). Never read passwords. Never write tokens or credentials to disk, config files, or logs. Never route credentials through any server.
- **Local cost is always an estimate** (your tokens × current prices). Never present it as the authoritative bill; design for reconciliation against the provider invoice, which is the source of truth.
- **Keep the core permissive.** This repo is Apache-2.0. Do not add any copyleft (GPL / AGPL / LGPL / SSPL) dependency. Verify a dependency's license is permissive (MIT / Apache-2.0 / BSD / ISC / Zlib / Unicode) before adding it.
- **Accessibility is required, not optional.** Every visual has a `--plain` ASCII equivalent; never rely on color alone (the amber warning state needs a second, non-color cue); `--plain` output must be screen-reader-friendly.
- **No `unwrap()`, `expect()`, or `panic!` in library crates.** Propagate errors. (Tests may use them.)

---

## Environment & setup

**Prerequisites (Phase 1):** Rust via `rustup` (with `clippy` and `rustfmt` components), plus `build-essential`, `pkg-config`, and `git`. Defer the keyring/OAuth deps (`libdbus-1-dev`, `libsecret-1-dev`) to Phase 2 (the Cursor credential-reuse path) — do not install them early. (No Tauri/GTK deps: the tray is cut — see ARCHITECTURE §10.)

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
│  └─ costroid-providers/  Provider trait + Claude Code/Codex/Cursor adapters + WSL-aware log discovery
├─ apps/
│  └─ cli/                 package `costroid`, binary `costroid` — CLI + Ratatui TUI + statusline + setup-statusline + --live
└─ .github/workflows/      CI + cargo-dist release pipeline
```

No `costroid-mcp` (name intentionally unclaimed) and no `apps/bar` tray (cut) — see ARCHITECTURE §5/§10.

**What belongs where:**
- `costroid-core` — the engine. Orchestrates providers, normalizes to FOCUS via `costroid-focus`, computes estimated cost, and houses the `bench`/`recommend` (frontier) module. No terminal/UI code.
- `costroid-focus` — FOCUS record types and (de)serialization only. Pure data; depends on nothing internal.
- `costroid-providers` — the `Provider` trait, the three adapters, and WSL-aware log discovery. Depends only on `costroid-focus`.
- `apps/cli` — argument parsing (`clap`), the Ratatui TUI, the statusline emitter, `setup-statusline`, `--live`, and all rendering. Depends on `costroid-core`.

**Dependency direction:** `apps → core → {providers, focus}`; `providers → focus`. No cycles. `costroid-focus` has no internal dependencies.

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
- [ ] Docs (`README.md` / `AGENTS.md` / `docs/*`) updated if behavior or interface changed.
- [ ] No new copyleft dependency; new dependency licenses verified permissive.
- [ ] Any new visual has a `--plain` ASCII equivalent and does not rely on color alone.
- [ ] No telemetry and no unauthorized network call introduced.
- [ ] Change stays within v1 scope (ARCHITECTURE §10).

---

## Build status & scope (data tiers, not a feature roadmap)

**"Phase" denotes a data tier, not a feature ladder** (see [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) §10). **Phase 1** = local logs + sanctioned push — *the v1 product*. **Phase 2** = the disfavored, opt-in credential-reuse / OAuth path, needed only for **Cursor's** live quota. Build within the v1 steps below; do not start Phase-2 work speculatively.

### v1 build steps (the product — steps 1–4)

1. **Core + workspace.** `costroid-core` / `costroid-focus` / `costroid-providers` (Claude + Codex), verified to the cent vs ccusage. *Shipped (v0.1.0).*
2. **TUI + full cost picture.** `now` / `trends`, subscription + API, filter, per-lane totals, export, config; Cursor **detection only** (beta); subscription quota graceful. *Shipped (v0.1.0).*
3. **Status-line mode.** `costroid statusline` (tmux / Starship / Claude Code `statusLine`). Live Claude 5h/7d quota rides on this surface via the `rate_limits` capture, so **`costroid setup-statusline`** (wires `settings.json`, handles an existing statusline) is load-bearing — see ARCHITECTURE §8. *Emitter shipped (v0.1.0); the `rate_limits` capture + `setup-statusline` is the next deliverable.*
4. **Frontier / recommendation view (`costroid frontier`).** The `bench` module: the cost-vs-quality frontier + the user's position, scoped to what the data honestly supports; advisory, sourced, **API-cost rows only**. *Built and gate-green.*

### v1 acceptance criteria

- [ ] Workspace builds; `cargo install --path apps/cli` installs a working `costroid` binary.
- [ ] Detects installed providers (Claude Code, Codex, Cursor) by locating their local data, including WSL→Windows paths; degrades gracefully when a provider is absent.
- [ ] `costroid` (the **now** screen): shows current API spend by model **and** 5-hour + weekly subscription limits with reset countdowns, from local data, with **no network calls** (Claude's 5h/7d via the `statusLine` capture, Codex's from local windows; Cursor quota is detect-and-defer).
- [ ] `costroid trends`: `--period day|week|month|year` and `--group model|app|total` both work.
- [ ] `costroid --live`: refreshes in place; `q`/Ctrl-C exits cleanly; works over SSH and inside tmux.
- [ ] `costroid statusline`: emits a compact one-line status suitable for a shell prompt, tmux, or Starship; `costroid setup-statusline` wires Claude Code's `statusLine` for live quota.
- [ ] `costroid frontier`: shows the cost-vs-quality frontier and the user's position; advisory, sourced, **API-cost rows only**; un-benchmarked models shown as gaps, never guessed.
- [ ] `costroid export`: emits FOCUS 1.3-conformant records (`--format json|csv`) that validate against the schema in [docs/DATA-MODEL.md](docs/DATA-MODEL.md).
- [ ] `--plain`: every screen renders in ASCII, no color, no braille, identical data, screen-reader-friendly.
- [ ] Pricing comes from bundled curated JSON; the tool works fully offline; all cost figures are labeled estimates.
- [ ] Subscription limits and API costs are modeled separately (limits are not summable dollars); a model used both ways appears in both, marked by access path.
- [ ] Live Claude quota from the `statusLine` `rate_limits` field is **sanitized + cross-checked** and degrades to "unavailable"/"unverified", never a confident wrong number (ARCHITECTURE §9.2).
- [ ] Zero telemetry; zero unauthorized network calls.
- [ ] Ships via cargo-dist (shell + PowerShell installers + Homebrew tap + npm wrapper, each artifact checksummed and build-provenance-attested; tag-triggered release in CI) and crates.io (`cargo install costroid` / `cargo binstall costroid`). (Scoop unsupported by cargo-dist — see RELEASING.md.)
- [ ] CI green: fmt + clippy + test + FOCUS-conformance + `cargo deny` + offline-acceptance.

**Acceptance test:** on a machine with real Claude Code / Codex / Cursor logs and **networking disabled**, `costroid`, `costroid trends --period month --group model`, `costroid frontier`, `costroid export --format json`, and `costroid --plain` all produce correct output.

### Phase 2 — Cursor live quota, optional login, alerts (later, opt-in)

Only **Cursor** needs Phase 2 — no sanctioned/local source exists (its usage/quota are live RPC to `api2.cursor.sh`). Claude and Codex already have their limits in Phase 1 (Claude via the `statusLine` hook, Codex via local windows), so they need no credential reuse.

- [ ] Reuse an existing local Cursor session to fetch live quota/cost — **opt-in, default-off**, behind a one-time disclosure naming the host and the undocumented/ToS risk; always degrades to "unavailable."
- [ ] Optional first-party OAuth (system browser + loopback redirect, PKCE); tokens stored **only** in the OS keychain (`keyring`); strictly device↔provider, never via a server.
- [ ] A connections view lists what is linked and supports instant disconnect/revoke; nothing is stored outside the keychain.
- [ ] Browser-cookie reading, if implemented at all, is a clearly-disclosed last-resort fallback, off by default.
- [ ] Threshold notifications fire when a window crosses warning/critical; quiet or off by default; user-configurable.
- [ ] All network calls are limited to provider endpoints the user authorized; still no telemetry.

**Acceptance test:** a user with no prior session can log in via the browser, see live Cursor quotas, revoke access, and confirm (e.g. by inspecting the keychain and the filesystem) that no secret was written to disk, config, or logs.

### Cut / speculative (not v1, not a committed phase)

- **Tray / menu-bar app** (`apps/bar`, Tauri) — **cut.** It only shows a number the TUI and status-line already show, and is the most expensive surface; revisit only on real demand.
- **MCP server** (`costroid-mcp`) — speculative; the crates.io name is intentionally left unclaimed (no placeholder). Not built. The recommendation engine it would have exposed is built into v1 step 4.

---

## Working style — decide vs. ask

**Decide on your own:** implementation details, internal refactors, test design, formatting, module structure, and choosing among permissive, well-maintained crates.

**Ask the human first before:**
- adding any dependency that is non-permissive, copyleft, or unusual in license;
- changing the public CLI surface or any export/output schema;
- anything touching authentication, secret handling, or the keychain;
- making Costroid perform a network call, or adding anything that could phone home;
- expanding scope beyond v1 (ARCHITECTURE §10), or beyond the three providers.

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

v1 scope · three providers · local logs + sanctioned `statusLine` push · no network in Phase 1 · no credential reuse (Cursor-only, later) · no telemetry · secrets in keychain · `--plain` for everything · cost is an estimate.