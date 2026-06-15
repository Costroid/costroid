# Costroid

`C ⠉` — a pixel **C** beside the braille cell ⠉ (dots 1 and 4, the two top dots: a meter at full).

> Local-first, FOCUS-native cost and limit visibility for your AI coding tools.

![status](https://img.shields.io/badge/status-early_development-orange)
![license](https://img.shields.io/badge/license-Apache--2.0-blue)

Costroid shows you — right in your terminal — what your AI coding tools actually cost. It tracks both the subscription limits you're burning through (your Claude Code and Codex 5-hour and weekly caps, with reset countdowns) and the real dollars on your API bill, broken down by model. Everything runs locally: it reads the logs those tools already write to your machine, sends nothing anywhere, and normalizes the data into the open [FOCUS](https://focus.finops.org) standard so your cost data is portable, auditable, and ready for whatever you plug it into next.

It's the kind of tool that should be free and open, so it is.

## Status

**Early development.** Costroid's **v0.3.0** release adds live Claude subscription quota (5-hour + weekly, via Claude Code's `statusLine`) on top of the local cost lane — local read + sanitize + cross-check, the `costroid setup-statusline` capture wiring, and on-screen rendering on the now-screen and statusline (token-fraction meters, dollar `Spend`, the color-free `? unverified` cue, and an "as of HH:MM" freshness stamp), all local with no login and no token reuse. The cost lane it builds on (the `now`, `trends`, `statusline`, and `export` commands, the cost-vs-quality `frontier` view, Cursor detect-and-defer, and WSL Windows-root auto-detection) shipped in v0.2.0. Install it via the packaged installers below (shell, PowerShell, Homebrew, npm), `cargo install costroid`, or `cargo binstall costroid` — or build from source (see [Quickstart](#quickstart)). Commands and flags may still evolve.

## What Costroid does

Shipping today (v0.3.0):

- **Two views in one tool.**
  - `now` — your Codex **and live Claude** 5-hour and weekly limits with reset countdowns (Claude via its `statusLine`), plus your current API spend by model.
  - `trends` — spend over day / week / month / year, grouped or filtered by model or app.
- **Cost-vs-quality frontier** (`frontier`) — the published cost-vs-quality frontier (DeepSWE + CursorBench) and where your own spend sits on it; advisory, sourced, **API-cost rows only**.
- **Local logs only.** Reads what Claude Code, Codex, and Cursor already write to disk. Today's release needs no API keys and no login, and nothing leaves your machine. (Optional, opt-in connections — your own API key, or a sanctioned login — are in progress for v0.4.0; the local-only path always stays the default.)
- **FOCUS-conformant export** (JSON / CSV) so your cost data is standard and portable.
- **Statusline mode** for your shell, tmux, or Starship.
- **`--live`** auto-refreshing view and a **`--plain`** ASCII mode for accessibility and pipes.
- **Braille rendering.** Charts, meters, and bars are drawn in braille dots — dense, distinctive, and terminal-native.

A note on the two views: subscription limits and API costs are deliberately separate, because they're different things. A subscription is a flat monthly fee, so it has a quota percentage and a reset timer but no per-use dollar amount. An API key is pay-as-you-go, so it has real, summable dollars per model. Costroid shows both, and a model you use both ways appears in each view, marked by access path.

## Roadmap

Where Costroid is headed:

- **Live Claude quota** — **shipped in v0.3.0.** Claude Code's `statusLine` hook hands Costroid your real 5-hour and weekly limits locally — no login, no token reuse. `costroid setup-statusline` wires it up and captures the data, and the now-screen and statusline render those limits (with a color-free `? unverified` cue and a labeled estimate fallback when a reading can't be trusted, never a confident wrong number).
- **Cost-vs-quality frontier** (`costroid frontier`) — **shipped in v0.2.0.** Plots the published cost-vs-quality frontier and where your own spend sits on it; advisory and sourced, never "just use the cheapest."
- **Connections (your own key, opt-in) — in progress for v0.4.0.** Optional, default-off, feature-gated connections fetch live numbers no local log carries — your own Anthropic or OpenAI usage-API key, and a sanctioned OAuth login where the provider offers one. Costroid never reuses a session or token against an undocumented endpoint, so a provider with no sanctioned source — Cursor today, and Gemini, which exposes no static-key usage API — stays detect-only and shows "unavailable" until an official API exists. Tokens live only in your OS keychain and are used strictly between your device and the provider. Most of it is already on `main` — the feature-gated `costroid-connect` crate, its OS-keychain credential store, the generic authorized-host HTTPS client, the Anthropic + OpenAI usage-API adapters, the estimate-vs-invoice reconciliation engine, the **`costroid connect`/`disconnect`/`connections` CLI** (the first opt-in connection, stdin-only key entry, instant revoke), and now the **`costroid reconcile` view** that puts your local estimate side by side with the vendor's billed invoice (signed variance per day and model, honest about every gap, never presenting the estimate as the bill) — all off by default. The only thing left for v0.4.0 is the release itself. Threshold alerts ride on this.
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

> **v0.3.0 is published** — all commands below work today. Built and released by [cargo-dist](https://github.com/axodotdev/cargo-dist) (binary `dist`). Release binaries carry build-provenance attestations + checksums but are not yet OS-code-signed, so on first run macOS may show an "unidentified developer" prompt and Windows a SmartScreen prompt — see [Security & privacy](#security--privacy).

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
costroid setup-statusline        # wire Claude Code's statusLine to capture live 5h/7d quota
costroid setup-statusline --undo # restore the original statusLine + remove the cache
costroid export --format json    # FOCUS export (--format json | csv)
costroid --plain                 # one-shot ASCII, no color (screen-reader & pipe friendly)
```

On an interactive terminal, `costroid` and `costroid trends` open a navigable view (press `?` for keys, `q` to quit); when the output is piped or `--plain` is set, they render once and exit. `statusline` and `export` are always one-shot.

`costroid setup-statusline` is idempotent and safe: it backs up `settings.json` first (restore with `--undo`), injects a capture snippet into an existing `statusLine` or sets Costroid as the status line if you have none, and writes only two percentages + two reset stamps to a local cache — never a token, prompt, or credential, and never over the network. (The captured quota is read, sanitized, cross-checked, and rendered on the now-screen and statusline.) If you have a `statusLine` you can't edit through `setup-statusline`, wrap it manually: `costroid statusline --wrap '<your-status-command>'` captures the quota and then runs your command on the same input (it degrades to a blank line on error, never breaking your prompt). (`costroid statusline --capture-only` is the internal capture step the generated snippet calls, not a command you run directly.)

## Security & privacy

- **No telemetry, by default.** Any update check is opt-in and clearly disclosed.
- **Your data stays on your machine.** The default, local-only build reads local logs only and makes no network calls — enforced by an offline-acceptance test. Network access happens only through the opt-in, feature-gated connections path, and only to a provider endpoint you explicitly authorized; nothing is uploaded to Costroid.
- **Secrets live in your OS keychain.** The keychain credential store and the `costroid connect` CLI live in the feature-gated `costroid-connect` path, off by default — the default binary doesn't even link it, and only an explicit `connect` / `connections --check` reaches the network. When you do connect, your key is read from stdin only (never an argument or environment variable), goes only to the OS keychain — never to disk, a config file, or a log — and is used only between your device and the provider, never routed through any Costroid server.
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

Costroid uses only local and provider-sanctioned data sources — it never reuses a credential or session against a non-sanctioned, undocumented, or internal endpoint, and the default build makes no network calls. If you optionally connect your own API key or a sanctioned login, you remain responsible for your own use of those credentials and for complying with each provider's terms of service.