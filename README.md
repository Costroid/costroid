# Costroid

`C ⠉` — a pixel **C** beside the braille cell ⠉ (dots 1 and 4, the two top dots: a meter at full).

> Local-first, FOCUS-native cost and limit visibility for your AI coding tools.

![status](https://img.shields.io/badge/status-early_development-orange)
![license](https://img.shields.io/badge/license-Apache--2.0-blue)

Costroid shows you — right in your terminal — what your AI coding tools actually cost. It tracks both the subscription limits you're burning through (Claude Code, Codex, and Cursor session and weekly caps, with reset countdowns) and the real dollars on your API bill, broken down by model. Everything runs locally: it reads the logs those tools already write to your machine, sends nothing anywhere, and normalizes the data into the open [FOCUS](https://focus.finops.org) standard so your cost data is portable, auditable, and ready for whatever you plug it into next.

It's the kind of tool that should be free and open, so it is.

## Status

**Early development — Phase 1.** Costroid is being built in the open and has **not shipped a release yet**. You can build it from source today (see [Quickstart](#quickstart)); the packaged installers listed below will work once the first release is tagged. The commands and flags shown here reflect the planned v1 interface and may change during development. Nothing in the [Roadmap](#roadmap) section exists yet.

## What Costroid does

Phase 1 (v1) scope:

- **Two views in one tool.**
  - `now` — live 5-hour and weekly subscription limits with reset countdowns, plus your current API spend by model.
  - `trends` — spend over day / week / month / year, grouped or filtered by model or app.
- **Local logs only.** Reads what Claude Code, Codex, and Cursor already write to disk. No API keys, no login, nothing leaves your machine.
- **FOCUS-conformant export** (JSON / CSV) so your cost data is standard and portable.
- **Statusline mode** for your shell, tmux, or Starship.
- **`--live`** auto-refreshing view and a **`--plain`** ASCII mode for accessibility and pipes.
- **Braille rendering.** Charts, meters, and bars are drawn in braille dots — dense, distinctive, and terminal-native.

A note on the two views: subscription limits and API costs are deliberately separate, because they're different things. A subscription is a flat monthly fee, so it has a quota percentage and a reset timer but no per-use dollar amount. An API key is pay-as-you-go, so it has real, summable dollars per model. Costroid shows both, and a model you use both ways appears in each view, marked by access path.

## Roadmap

Not yet built — here's where Costroid is headed:

- **Phase 2** — live quotas via your existing tool sessions, plus optional OAuth login (tokens stored only in your OS keychain, used strictly between your device and the provider). Threshold alerts.
- **Phase 3** — a cross-platform tray / menu-bar app for Windows, macOS, GNOME, and KDE.
- **Phase 4** — an MCP server (query your costs from inside your AI agent) and quality-per-dollar model recommendations.
- A separate, team-oriented **web platform** for company-wide cost management is planned as its own project.

See [HANDOFF.md](HANDOFF.md) for the full plan and phase-by-phase acceptance criteria.

## Quickstart

### Build from source (works today)

```bash
git clone https://github.com/Costroid/costroid
cd costroid
cargo install --path apps/cli
```

### Packaged installers (once the first release ships)

> ⚠ **Not yet published.** These are the planned install commands and will resolve only after the first release is tagged. The release pipeline uses [cargo-dist](https://github.com/axodotdev/cargo-dist); installer asset names follow its defaults and may be adjusted.

macOS / Linux:

```bash
curl --proto '=https' --tlsv1.2 -LsSf https://github.com/Costroid/costroid/releases/latest/download/costroid-installer.sh | sh
```

Windows (PowerShell):

```powershell
powershell -ExecutionPolicy Bypass -c "irm https://github.com/Costroid/costroid/releases/latest/download/costroid-installer.ps1 | iex"
```

Homebrew:

```bash
brew install Costroid/tap/costroid
```

Scoop (Windows):

```bash
scoop bucket add costroid https://github.com/Costroid/scoop-bucket
scoop install costroid
```

crates.io:

```bash
cargo install costroid
```

npm:

```bash
npx costroid
```

### Usage

```bash
costroid                         # "now": live limits + current API costs
costroid trends                  # spend over time
costroid trends --period week    # day | week | month | year
costroid trends --group model    # model | app | total
costroid --live                  # auto-refreshing view
costroid statusline              # compact one-line status for shell / tmux / Starship
costroid export --format json    # FOCUS export (--format json | csv)
costroid --plain                 # ASCII fallback, no color (accessible, pipe-friendly)
```

## Security & privacy

- **No telemetry, by default.** Any update check is opt-in and clearly disclosed.
- **Your data stays on your machine.** Phase 1 reads local logs only; nothing is uploaded.
- **Secrets live in your OS keychain.** When optional login arrives (Phase 2), tokens are stored via the system keychain and used only between your device and the provider — never routed through any Costroid server.
- **Signed releases.**
- Local cost figures are **estimates** (your tokens × current prices). Costroid is built to reconcile them against your actual provider invoices, which are the source of truth.

## Standards

Costroid follows [FinOps Foundation](https://www.finops.org) practice and emits [FOCUS](https://focus.finops.org) 1.3-conformant records — the open specification that now covers AI billing data — so your cost data is portable and vendor-neutral from the start.

## Project status & contributing

- Plan and architecture: [HANDOFF.md](HANDOFF.md)
- Building Costroid, and the rules that AI coding agents must follow: [AGENTS.md](AGENTS.md)
- Contributions are welcome — please read [AGENTS.md](AGENTS.md) first.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).