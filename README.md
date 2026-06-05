# Costroid

`C ⠉` — a pixel **C** beside the braille cell ⠉ (dots 1 and 4, the two top dots: a meter at full).

> Local-first, FOCUS-native cost and limit visibility for your AI coding tools.

![status](https://img.shields.io/badge/status-early_development-orange)
![license](https://img.shields.io/badge/license-Apache--2.0-blue)

Costroid shows you — right in your terminal — what your AI coding tools actually cost. It tracks both the subscription limits you're burning through (your Claude Code and Codex 5-hour and weekly caps, with reset countdowns) and the real dollars on your API bill, broken down by model. Everything runs locally: it reads the logs those tools already write to your machine, sends nothing anywhere, and normalizes the data into the open [FOCUS](https://focus.finops.org) standard so your cost data is portable, auditable, and ready for whatever you plug it into next.

It's the kind of tool that should be free and open, so it is.

## Status

**Early development.** Costroid's first release, **v0.1.0**, is published — the `now`, `trends`, `statusline`, and `export` commands. Install it via the packaged installers below (shell, PowerShell, Homebrew, npm), `cargo install costroid`, or `cargo binstall costroid` — or build from source (see [Quickstart](#quickstart)). The cost-vs-quality `frontier` view is built and lands in the next release; live Claude subscription quota (via Claude Code's `statusLine`) is in progress. Commands and flags may still evolve.

## What Costroid does

Shipping today (v0.1.0):

- **Two views in one tool.**
  - `now` — your Codex 5-hour and weekly limits with reset countdowns today (live Claude quota lands next release), plus your current API spend by model.
  - `trends` — spend over day / week / month / year, grouped or filtered by model or app.
- **Local logs only.** Reads what Claude Code, Codex, and Cursor already write to disk. Today's release needs no API keys and no login, and nothing leaves your machine. (Optional, opt-in connections — your own API key, or a sanctioned login — are on the roadmap; the local-only path always stays the default.)
- **FOCUS-conformant export** (JSON / CSV) so your cost data is standard and portable.
- **Statusline mode** for your shell, tmux, or Starship.
- **`--live`** auto-refreshing view and a **`--plain`** ASCII mode for accessibility and pipes.
- **Braille rendering.** Charts, meters, and bars are drawn in braille dots — dense, distinctive, and terminal-native.

A note on the two views: subscription limits and API costs are deliberately separate, because they're different things. A subscription is a flat monthly fee, so it has a quota percentage and a reset timer but no per-use dollar amount. An API key is pay-as-you-go, so it has real, summable dollars per model. Costroid shows both, and a model you use both ways appears in each view, marked by access path.

## Roadmap

Where Costroid is headed:

- **Live Claude quota (next release).** Claude Code's `statusLine` hook hands Costroid your real 5-hour and weekly limits locally — no login, no token reuse. A `costroid setup-statusline` command wires it up.
- **Cost-vs-quality frontier** (`costroid frontier`) — built; lands next release. Plots the published cost-vs-quality frontier and where your own spend sits on it; advisory and sourced, never "just use the cheapest."
- **Connections (your own key, opt-in).** Optional, default-off, feature-gated connections fetch live numbers no local log carries — your own Anthropic / OpenAI / Gemini usage-API key first, a sanctioned OAuth login where one exists, and (for Cursor, which keeps no local data) opt-in reuse of your existing session. Tokens live only in your OS keychain and are used strictly between your device and the provider. `costroid connect`/`disconnect` plus a revocable Connections view manage it. Cursor's live quota and threshold alerts ride on this.
- **Taskbar / menu-bar app** — a planned `costroid-bar` surface built in egui (no webview), the richest and last surface; everything it shows the core already computes.
- **Maybe later** — an MCP server (query your costs from inside your AI agent) remains a speculative, uncommitted future surface.
- A separate, team-oriented **web platform** for company-wide cost management is planned as its own project.

See [CLAUDE.md](CLAUDE.md) for the build scope and the rules that AI coding agents follow. (Costroid's detailed design specs — architecture, data model, product plan — live in [docs/](docs/).)

## Quickstart

### Build from source (works today)

```bash
git clone https://github.com/Costroid/costroid
cd costroid
cargo install --path apps/cli
```

### Packaged installers

> **v0.1.0 is published** — all commands below work today. Built and released by [cargo-dist](https://github.com/axodotdev/cargo-dist) (binary `dist`). Release binaries carry build-provenance attestations + checksums but are not yet OS-code-signed, so on first run macOS may show an "unidentified developer" prompt and Windows a SmartScreen prompt — see [Security & privacy](#security--privacy).

macOS / Linux (shell):

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

npm:

```bash
npx costroid
```

Prebuilt binary via [cargo-binstall](https://github.com/cargo-bins/cargo-binstall) (downloads the attested release binary from GitHub, no compile):

```bash
cargo binstall costroid
```

From crates.io (compiles from source):

```bash
cargo install costroid
```

### Usage

```bash
costroid                         # interactive "now": live limits + current API costs
costroid trends                  # interactive spend-over-time view
costroid trends --period week    # day | week | month | year
costroid trends --group model    # model | app | total
costroid --live                  # auto-refresh the interactive view
costroid statusline              # compact one-line status for shell / tmux / Starship
costroid export --format json    # FOCUS export (--format json | csv)
costroid --plain                 # one-shot ASCII, no color (screen-reader & pipe friendly)
```

On an interactive terminal, `costroid` and `costroid trends` open a navigable view (press `?` for keys, `q` to quit); when the output is piped or `--plain` is set, they render once and exit. `statusline` and `export` are always one-shot.

## Security & privacy

- **No telemetry, by default.** Any update check is opt-in and clearly disclosed.
- **Your data stays on your machine.** The default, local-only build reads local logs only and makes no network calls — enforced by an offline-acceptance test. Network access happens only through the opt-in, feature-gated connections path, and only to a provider endpoint you explicitly authorized; nothing is uploaded to Costroid.
- **Secrets live in your OS keychain.** When optional login arrives, tokens are stored via the system keychain and used only between your device and the provider — never routed through any Costroid server.
- **Attested releases.** Release binaries carry keyless [GitHub build-provenance attestations](https://docs.github.com/en/actions/security-guides/using-artifact-attestations-to-establish-provenance-for-builds) and SHA-256 checksums — verify with `gh attestation verify <file> --repo Costroid/costroid`. OS code-signing (macOS notarization, Windows Authenticode) is not yet in place, so first run may show an unidentified-developer / SmartScreen prompt.
- Local cost figures are **estimates** (your tokens × current prices). Costroid is built to reconcile them against your actual provider invoices, which are the source of truth.

## Standards

Costroid follows [FinOps Foundation](https://www.finops.org) practice and emits [FOCUS](https://focus.finops.org) 1.3-conformant records — the open specification that now covers AI billing data — so your cost data is portable and vendor-neutral from the start.

## Project status & contributing

- Detailed design specs (architecture, data model, product plan): [docs/](docs/)
- Building Costroid, and the rules that AI coding agents must follow: [CLAUDE.md](CLAUDE.md)
- Contributions are welcome — please read [CLAUDE.md](CLAUDE.md) first.

## License

Licensed under the Apache License, Version 2.0. See [LICENSE](LICENSE).