# Changelog

All notable changes to Costroid are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and Costroid adheres to
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

Costroid is local-first: every release in this log reads only the logs your AI coding
tools already write to disk, makes no network calls in the default build, and sends no
telemetry. All cost figures are estimates (your tokens × current prices), reconcilable
against your provider invoice, which is the source of truth.

## [Unreleased]

The 0.3.0 milestone: the generalized quota model plus Claude Code's live 5h/7d quota —
**captured** from the `statusLine` hook (T5), **read + sanitized + cross-checked** from a
no-secret local cache (T4), and **rendered** on the `now` screen and status line (T6), all
on top of the cost lane — no new network code, no telemetry, still zero network calls in
the default build.

### Added

- **`costroid setup-statusline`** (and **`--undo`**) — wires Claude Code's `statusLine` so
  each assistant turn tees its `rate_limits` into a no-secret local cache: it injects a
  capture snippet into an existing `statusLine`, or makes Costroid the status line if you
  have none. Idempotent, backs up `settings.json` first, and fully reversible.
- **`costroid statusline --capture-only`** — the internal capture step the snippet calls:
  reads the `statusLine` JSON on stdin and writes only two percentages, two reset stamps,
  and a capture time to the cache — never a token, prompt, or credential. Emits nothing,
  exits 0 always, and never fails your prompt.
- **`costroid statusline --wrap '<command>'`** — a manual escape hatch that captures and
  then runs an existing status-line command on the same input.
- **Live Claude 5h/7d quota on the `now` screen and status line** — the captured
  `rate_limits` cache is read, **sanitized and cross-checked**, then rendered: a meter for
  token-fraction limits, `$used / $included used` (no fabricated %) for dollar limits, and
  an estimate fallback. Readings degrade to a color-free " ? unverified" cue or to
  "unavailable" rather than ever showing a confident wrong number, carry an always-on
  "as of HH:MM" freshness stamp, and note that claude.ai chat usage may make true usage
  higher. Still no network calls in the default build.
- **Generalized quota model** — limit windows, measures (token-fraction vs. dollar spend),
  kinds (5-hour / weekly / daily / monthly / billing-cycle) and availability states are
  normalized across providers, so each provider's quota maps onto one shared shape.

## [0.2.0] - 2026-06-05

Ships the already-built local **cost lane** on top of v0.1.0 — no new network code, no
telemetry, still zero network calls in the default build.

### Added

- **`costroid frontier`** — the cost-vs-quality frontier view. Plots the bundled, dated,
  sourced cost-vs-quality frontier (DeepSWE + CursorBench) and where your own spend sits
  on it. Advisory and sourced, **API-cost rows only**; un-benchmarked models are shown as
  gaps, never guessed. Renders in braille, ASCII, and `--plain` modes.

### Changed

- **Cursor: detect-and-defer (beta).** Cursor is detected when installed, but its live
  subscription quota is deferred to a later release (Cursor keeps no local quota log).
  The quota lane degrades to "unavailable" rather than presenting a guessed number.
- **WSL Windows-root auto-detection.** Log discovery now auto-detects the Windows user
  root from inside WSL, so logs that AI tools write under `/mnt/c/Users/<user>/...` (when
  the tools run on Windows) are found alongside logs written inside WSL under `~`.

## [0.1.0] - 2026-06-03

First public release — the local cost lane's foundation.

### Added

- **`costroid` (now)** — current API spend by model, plus Codex 5-hour and weekly
  subscription limits with reset countdowns, from local data with no network calls.
  (Live Claude subscription quota is not yet wired; Cursor quota is detect-and-defer.)
- **`costroid trends`** — spend over time with `--period day|week|month|year` and
  `--group model|app|total`.
- **`costroid statusline`** — a compact one-line status for a shell prompt, tmux, or
  Starship.
- **`costroid export`** — FOCUS 1.3-conformant records (`--format json|csv`).
- **`--live`** auto-refreshing interactive view and **`--plain`** ASCII mode
  (screen-reader- and pipe-friendly; never relies on color alone).
- WSL-aware multi-root log discovery for Claude Code, Codex, and Cursor; degrades
  gracefully when a provider is absent.
- Exact-`Decimal` `tokens × price` cost from bundled, dated pricing; estimates verified
  to the cent against ccusage.
- Packaged releases via cargo-dist (shell, PowerShell, Homebrew, npm) plus crates.io
  (`cargo install costroid` / `cargo binstall costroid`), each artifact SHA-256-checksummed
  and build-provenance-attested.

[Unreleased]: https://github.com/Costroid/costroid/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/Costroid/costroid/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/Costroid/costroid/releases/tag/v0.1.0
