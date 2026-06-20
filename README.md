# Costroid

> Local-first, FOCUS-native cost and limit visibility for your AI coding tools — right in your terminal.

![license](https://img.shields.io/badge/license-Apache--2.0-blue)

Costroid shows what your AI coding tools actually cost — both the subscription limits you're burning through (Claude Code and Codex 5-hour and weekly caps, with reset countdowns) and the real dollars on your API bill, by model. By default it runs entirely from the local logs those tools already write, sends nothing anywhere, and normalizes everything into the open [FOCUS](https://focus.finops.org) 1.3 standard. Subscription limits and API costs are modeled separately, because they're different things: a subscription has a quota % and a reset timer; an API key has summable per-model dollars.

**Feature-complete at v0.6.0.** Edition 2021, Apache-2.0. MSRV 1.88 (libraries + CLI), 1.92 (the taskbar).

## Install

**CLI (`costroid`):**

```bash
# macOS / Linux (shell)
curl --proto '=https' --tlsv1.2 -LsSf https://github.com/Costroid/costroid/releases/latest/download/costroid-installer.sh | sh
# Windows (PowerShell)
powershell -ExecutionPolicy Bypass -c "irm https://github.com/Costroid/costroid/releases/latest/download/costroid-installer.ps1 | iex"
brew install Costroid/tap/costroid     # Homebrew
npx costroid                           # npm
cargo binstall costroid                # prebuilt binary, no compile
cargo install costroid                 # from crates.io (compiles)
```

**Taskbar (`costroid-bar`):** downloadable binary archives, or `cargo install costroid-bar`. (No npm/Homebrew this cut.)

Build from source: `git clone https://github.com/Costroid/costroid && cd costroid && cargo install --path apps/cli`.

## Commands

| Command | What it does |
|---|---|
| `costroid` / `now` | Live Claude + Codex 5h/weekly limits with reset countdowns, plus current API spend by model |
| `costroid trends` | Spend over time — `--period day\|week\|month\|year`, `--group model\|app\|total` |
| `costroid frontier` | Cost-vs-quality frontier and where your spend sits; advisory, sourced, API-cost rows only |
| `costroid statusline` | Compact one-line status for shell / tmux / Starship (`--wrap '<cmd>'` escape hatch) |
| `costroid setup-statusline` | Wire Claude Code's `statusLine` to capture live 5h/7d quota (`--undo` to restore) |
| `costroid export --format json\|csv` | FOCUS 1.3-conformant export |
| `costroid import <file>` | Import a foreign FOCUS v1.2 export (incl. AWS Data Exports / Bedrock; multi-currency) → Costroid FOCUS 1.3 cloud lane (`--format focus-csv\|focus-json`, `--version auto\|1.2`, `--out json\|csv`, `--pricing-override <file>` to layer a user price file over the bundled catalog); pure local parse, no network |
| `costroid alerts` / `--check` | Opt-in threshold alerts (default off); `--check` is cron-friendly (exit 0/1/2) |
| `costroid --live` | Auto-refreshing interactive view |
| `costroid --plain` | One-shot ASCII, no color — screen-reader & pipe friendly |

**Opt-in connections** (behind `--features connect`, off by default): `costroid connect` / `disconnect` / `connections [--check]` link your own Anthropic/OpenAI usage-API key (stdin-only entry, instant revoke), and `costroid reconcile` puts your local estimate side by side with the vendor's billed invoice.

## Surfaces

- **TUI** — 9 numbered tabs (`1` now, `2` trends, `3` providers, `4` models, `5` history, `6` budget, `7` forecast, `8` anomalies, `9` activity) plus an `a`/`esc` frontier overlay. Charts/meters/bars draw in braille dots, painted in Costroid's palette; `--plain`/`NO_COLOR` strip all color with byte-identical output.
- **Taskbar** (`costroid-bar`) — always-on tray glance (your most-constrained quota meter, in the dot-density warning language) plus a live cockpit (Overview, opt-in alert banner, Budget / Forecast / Anomalies / Providers). egui/eframe + tray-icon, AccessKit on, a pure consumer of the same local data. macOS/Windows tray paths compile but are not yet field-verified.

## Providers

Claude Code and Codex (full cost + quota); Cursor (detect-only — cost/quota "unavailable"). Cursor live quota, GitHub Copilot, Antigravity, and Gemini own-key are discovery-gated and never built speculatively.

## Guarantees

- **Local-first, zero-network default.** The default build reads local logs only and makes no network calls (enforced by a strace offline-acceptance test + a two-tier forbidden-crates test). Network happens only under `--features connect`, on an explicit `connect` / `connections --check` / `reconcile` action, as an HTTPS GET to the one provider host you authorized.
- **No telemetry, ever.** Any update check is opt-in and off by default.
- **Secrets live only in your OS keychain** (`keyring`) — read from stdin, never written to disk, config, or logs, never routed through any server. A usage-API key is organization-wide; `connect` warns at paste time and recommends a dedicated, instantly-revocable key. TLS validates against your OS trust store (no cert pinning) — see [SECURITY.md](SECURITY.md).
- **Cost is always an estimate** (your tokens × current prices), built to reconcile against the provider invoice, which is the source of truth.
- **`--plain` everywhere**, never color alone (the dot-density grid is the cue); permissive licenses only.

## More

- Standards: emits [FOCUS](https://focus.finops.org) 1.3-conformant records — portable and vendor-neutral.
- Roadmap: [docs/ROADMAP.md](docs/ROADMAP.md). Release history: [CHANGELOG.md](CHANGELOG.md). Architecture: [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md). Build rules for AI coding agents: [CLAUDE.md](CLAUDE.md).

## License

Apache-2.0. See [LICENSE](LICENSE). Costroid uses only local and provider-sanctioned data sources and never reuses a credential or session against an undocumented or internal endpoint; if you connect your own key, you remain responsible for complying with each provider's terms of service.
