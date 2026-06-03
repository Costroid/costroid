# HANDOFF.md — Costroid (start here)

This is the navigational spine for the project: the vision, the principles, the scope, the product model, the phase plan, and the map of where everything lives. It links the detailed specs rather than repeating them — read this first, then follow the pointers at the bottom.

## Vision & goal

Costroid is **secure, open-source, FOCUS-native FinOps tooling for AI and cloud cost**. The mission is to give people honest, portable visibility into what their AI tools cost — and eventually to do that for whole teams and companies.

The product arc has two stages, deliberately in this order:

1. **A local developer tool (this repo).** A CLI and interactive TUI (and later a tray app) that shows individual developers their AI-tool spend and limits, entirely from local data. This comes first because bottoms-up open-source tools win on distribution, community, and credibility — and because the FOCUS-native local data model we build here is the bridge to stage two.
2. **A team/company web platform (a separate, later repo).** A Finout-style service for organization-wide cost management. It will be built on the same FOCUS foundation, but it is **not** part of this repo and is licensed separately.

We do not build stage two here. We build the foundation that makes stage two easy.

## Principles

- **Local-first and secure.** No telemetry by default. Data never leaves the device. Secrets live only in the OS keychain. Releases carry build-provenance attestations + SHA-256 checksums (not OS-code-signed yet).
- **FOCUS-native.** Costs are normalized into the open [FOCUS](https://focus.finops.org) standard from day one. The data model is both the product's integrity and the bridge to the platform.
- **Vendor-neutral recommendations.** Model suggestions draw on multiple benchmarks; CursorBench is included only as one clearly-labeled vendor input, never the sole basis. Every recommendation is transparent and advisory.
- **A colleague, not a chatbot.** Costroid surfaces proactive, plain-language, well-timed insight. It embeds no conversational LLM interface.
- **Open-core.** The tool is permissively licensed so it can be adopted and contributed to freely; the future commercial platform is a separate, separately-licensed project built on top of it.

## Scope

**In scope now:** the local developer tool — CLI + TUI (tray later) — for **Claude Code, Codex, and Cursor**, reading local data, exporting FOCUS records.

**Out of scope here:** the web platform (separate repo); any provider beyond the three; any chat/LLM-chat interface; any networked telemetry. See [AGENTS.md](AGENTS.md) for the full guardrails.

## Product model

Costroid has **two screens**:

- **now** — live **5-hour** and **weekly** subscription limits with reset countdowns, plus your **current API spend** by model.
- **trends** — spend over **day / week / month / year**, grouped or filtered by **model** or **app / total**.

These rest on one crucial distinction:

- **Subscription limits** are a flat monthly fee. They have a quota percentage and a reset window, but **no per-use dollar amount** — they are not summable into a bill.
- **API costs** are pay-as-you-go. They are **real, summable, per-token dollars per model**, and they are what becomes FOCUS records.

So the two are modeled separately. Recommendations apply **only** to API-cost rows (where switching models changes the bill), never to subscription rows. A model used both ways appears in both screens, marked by access path.

## Phase plan

Phases are built in order. The full, checkable acceptance criteria and Definition of Done for each phase live in [AGENTS.md](AGENTS.md); below is the one-line summary of each.

**Phase 1 — buildable core (CLI + TUI, local logs only).**
Goal: a useful, shippable local tool with zero network access.
Deliverables: both screens from local data; FOCUS export (JSON/CSV); `statusline`; `--live`; `--plain` ASCII fallback; bundled pricing; shipped via cargo-dist (installers) and crates.io (`cargo install costroid` / `cargo binstall costroid`).
Acceptance (summary): on a machine with real provider logs and **networking disabled**, the now/trends screens, FOCUS export, and `--plain` all produce correct output.

**Phase 2 — live quota, optional login, alerts.**
Goal: live limits without compromising the security model.
Deliverables: reuse existing local sessions for live quotas; optional OAuth login (tokens in the OS keychain, device↔provider only); a connections view with revoke; threshold notifications. Browser-cookie reading only as a disclosed last resort.
Acceptance (summary): a user can log in via browser, see live quotas, revoke access, and verify no secret was written outside the keychain.

**Phase 3 — tray app (`apps/bar`, Tauri 2).**
Goal: glanceable status on the desktop.
Deliverables: a cross-platform tray/menu-bar app (Windows, macOS, GNOME, KDE) sharing the core; a dynamic braille icon; per-provider and merge-icon modes; provider incident badges; signed auto-updates.
Acceptance (summary): installs and runs on each target OS, the icon updates live, and no data leaves the device. (Built and tested on the host OS, not WSL.)

**Phase 4 — MCP server + recommendations.**
Goal: make costs queryable from inside an AI agent, and add quality-per-dollar advice.
Deliverables: an MCP server exposing FOCUS data and recommendations; a vendor-neutral, multi-benchmark recommendation engine.
Acceptance (summary): an MCP client receives a sourced, transparent quality-per-dollar suggestion for an API-cost line — and never for a subscription line.

**Out of scope:** the web platform — a separate repo, later.

## Repo & ecosystem map

- **`Costroid/costroid`** — this repo. The Rust Cargo workspace: the engine, FOCUS layer, provider adapters, MCP server, the `costroid` CLI/TUI, and (Phase 3) the tray app. Crate-by-crate detail is in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).
- **`Costroid/costroid-web`** — the marketing site and docs for costroid.com (Astro, or Zola if we want it all-Rust).
- **`Costroid/homebrew-tap`** — the Homebrew tap repo; the formula is **auto-generated and pushed by cargo-dist** at release time, not hand-maintained. (cargo-dist does **not** support Scoop, so there is no `scoop-bucket` for v0.1.0 — Windows users use the PowerShell installer or `cargo binstall`. A hand-rolled Scoop bucket could be added later if there's demand.)
- **`Costroid/costroid-cloud`** *(later)* — the team/company web platform. Separate repo, separate license. Do not build it from this repo.
- **`Costroid/focus-rs`** *(optional, later)* — if the FOCUS layer proves stable and generally useful, extract it as a standalone Rust implementation of the FOCUS schema. A community/standards-goodwill move that also pre-builds the platform's foundation.

A tooling note for distribution: cargo-dist's binary is named `dist` and it is **actively maintained** (latest v0.32.0, May 2026) by a small team at axodotdev. We proceed with it; if it ever stalls, the fallbacks are hand-written installers plus `cargo-binstall`, or `release-plz` for release automation.

## Licensing strategy

This repo is **Apache-2.0** — chosen for its explicit patent grant (protecting the company and contributors once there's a commercial entity), its fit with the FinOps/cloud-cost ecosystem (OpenCost and most CNCF projects are Apache-2.0), and because it is fully permissive, so it does not slow adoption and it lets the future platform build on this core cleanly.

The **platform is a separate repo, licensed later** — proprietary or a source-available license such as BSL 1.1, which permits running the SaaS while preventing competitors from reselling it. To keep that option open, **this core must stay permissive: no copyleft (GPL/AGPL/LGPL/SSPL) dependencies**. The platform's license is not decided here and does not need to be until it is built.

## Read next

- [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) — how the system is built: the workspace, data flow, provider abstraction, auth tiers, rendering, distribution.
- [docs/DATA-MODEL.md](docs/DATA-MODEL.md) — the FOCUS-native data model and per-provider log parsing.
- [docs/DESIGN-SYSTEM.md](docs/DESIGN-SYSTEM.md) — the braille rendering system and the TUI/CLI UX.
- [AGENTS.md](AGENTS.md) — the operating manual, guardrails, and per-phase acceptance criteria for anyone (human or agent) building Costroid.
- [README.md](README.md) — the public front door.

## Status

Phase 1 is **complete and shipped as v0.1.0.** Packaged installers (shell, PowerShell, Homebrew, npm) and crates.io (`cargo install costroid` / `cargo binstall costroid`) are all live, and build-from-source still works (see the README). Next milestone is **Phase 2**, which has not started.