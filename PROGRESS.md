# Costroid-Next — PROGRESS

> **Living state for the post-v0.6.0 build** defined in [`docs/COSTROID-NEXT.md`](docs/COSTROID-NEXT.md):
> a local-first, self-hostable, FOCUS-native AI cost tool that adds a **measured/estimated
> local-inference economics engine**, a **cloud/API cost lane**, a **break-even calculator**, and
> a **local web UI** on top of the shipped v0.6.0 tool. This file is the resume-point (§2.5): a new
> session reads `CLAUDE.md` → this file → the last handoff note, then runs the gate to confirm state.
>
> **Current milestone: M0 (audit + plan + scaffolding) — COMPLETE, awaiting human checkpoint.**
> Do not start M1 until the human approves at the M0 checkpoint (§2.4).

---

## How to verify current state (run before continuing)

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace
cargo deny check licenses bans                 # default (offline) gate
cargo deny --all-features check licenses bans  # connect-on superset (mirrors CI)
cargo +1.88.0 check --workspace --all-targets --exclude costroid-bar   # MSRV
bash scripts/offline_acceptance.sh             # CLI/bar no-socket + server loopback-only
```
All of the above are **green as of the M0 checkpoint** (Linux/WSL2). Cross-OS compile is the CI
arbiter (the new `cross-platform` job + the existing cargo-dist release matrix) — "green in WSL is
not green" (§2.3).

---

## Repo audit (current state, reconciled to the tree 2026-06-19)

Costroid is a Rust Cargo workspace, **feature-complete at v0.6.0**, Apache-2.0, edition 2021. The
audit confirmed `docs/COSTROID-NEXT.md` §5.1's current-state facts and the invariant machinery:

- **Crates (libs):** `costroid-focus` (FOCUS 1.3 types + serde, leaf), `costroid-providers` (the
  `Provider` trait + Claude/Codex/Cursor adapters + WSL-aware discovery = the existing "collectors"),
  `costroid-core` (engine: orchestration, cost calc, **bundled dated pricing**, **bench/Frontier**,
  `vendor_report`, `reconcile`, display helpers), `costroid-config` (read-only `[budget]`/`[alerts]`
  TOML), `costroid-connect` (**all** network + credential code, feature-gated `connect` OFF).
- **Apps (binaries):** `apps/cli` → `costroid` (CLI + Ratatui TUI + statusline + `--live` + alerts +
  connect/reconcile), `apps/bar` → `costroid-bar` (egui/eframe taskbar, MSRV 1.92, AccessKit on).
- **Dependency direction:** `apps → core → {providers, focus}`; `apps → config → core`;
  `connect → core`. No cycles. MSRV 1.88 (libs + CLI), 1.92 (bar).
- **Invariant machinery (the load-bearing part for this work):**
  - `apps/cli/tests/offline.rs` — a **per-binary** resolved-graph proof. BFS rooted at *one* binary
    (`costroid`, then `costroid-bar`) over all 6 shipped targets; `ALWAYS_FORBIDDEN_CRATES` bans every
    network/TLS/telemetry/async-runtime crate (incl. `tiny_http`, `axum`, `hyper`, `tokio`); the
    `connect` trio + the bar's AccessKit subtree are **subset-allowlists** (`CONNECT_ALLOWED`,
    `BAR_ACCESSKIT_ALLOWED`) so any *new* dep trips the gate for human review.
  - `deny.toml` — fail-closed license allowlist (no copyleft) + "rustls never OpenSSL"; licenses+bans
    offline, advisories online. The sole MPL exception is `option-ext` (scoped).
  - `scripts/offline_acceptance.sh` — the dynamic counterpart: every command under strace/netns proves
    no `AF_INET` egress.
  - CI (`ci.yml`): fmt/clippy/test (Linux), MSRV 1.88, FOCUS 1.3 validator, cargo-deny (licenses+bans
    + online advisories), offline acceptance. Release via cargo-dist (`dist-workspace.toml`).

**How the new work maps on (no parallel `collectors/bench/pricing` set — extend the `costroid-*`
family, §6.2):** cloud/API + AWS-FOCUS + Bedrock importers → **new modules in `costroid-providers`**;
the new cost model + LiteLLM pricing snapshots → **extend `costroid-core`**; storage → a new store
behind a feature/crate (MSRV-isolated, see Decisions); local-inference economics → **new
`costroid-power`** (off-by-default `power` feature); local HTTP API + web UI → **new `costroid-server`
binary** (separate, loopback-only). FOCUS 1.2-in / 1.3-out extends `costroid-focus`/`costroid-core`.

---

## M0 — Architecture decisions LOCKED (⚑ Readiness gate A; recommended defaults adopted)

| # | Decision | Resolution |
|---|---|---|
| **A1** | HTTP API/web UI placement | **Separate `costroid-server` binary** (`apps/server`), never linked into `costroid`/`costroid-bar`. Uses the **blocking `tiny_http`** server (no `tokio`, sidesteps the async-runtime ban). Own reviewed per-binary allowlist `SERVER_ALLOWED` + a runtime **loopback-only** proof. CLI stays byte-for-byte no-network. **DONE** (scaffolded + gated). |
| **A2** | Local inference | **Subprocess** to user-installed `llama.cpp`/Ollama (CLI/stdout, **not** the localhost HTTP API — so `costroid-power` needs no HTTP client). `unsafe_code = "forbid"` rules out FFI bindings; subprocess adds zero heavy deps. Output contract (model id, quant, flags, tok/s, token counts) defined for M3 golden tests. **LOCKED** (no dep needed). |
| **A3** | Web-UI sub-stack | **Maud + htmx + uPlot** (server-rendered, no WASM toolchain, MSRV-safe). Maud/rust-embed are Rust deps (spiked clean); htmx/uPlot are **vendored JS assets** (license-only). Leptos/Dioxus→WASM only if a richer SPA is later required. **LOCKED** (deps spiked; integration = M5). |
| **A4** | Per-crate MSRV | `costroid-{focus,core,providers,config}` + CLI stay **1.88**; `apps/bar` stays **1.92**. `costroid-power`/`costroid-server` **inherit 1.88** today (their M0 deps build on 1.88); each may declare its own higher `rust-version` later if a heavy dep demands it. **DONE.** |
| **A5** | Storage | **DuckDB REJECTED by the M0 spike** (see B). Adopt the **pre-approved SQLite fallback (R11)**: `rusqlite` (bundled `libsqlite3-sys`). To protect core's 1.88 MSRV + publish ladder, the store lands **behind a feature / in a dedicated crate** at M1, not unconditionally in `costroid-core`. **DECIDED** (integration = M1). |

---

## M0 — Dependency spikes (⚑ Readiness gate B) — resolved license sets recorded

Each spike: pin the exact version, `cargo deny check licenses bans` (repo policy), Linux build,
per-target graph resolution (offline, all 6 shipped targets), MSRV 1.88 check, forbidden-crate scan.

| Dep family | Decision | Pinned | Packages | Resolved license set | MSRV 1.88 | Forbidden-crate scan |
|---|---|---|---|---|---|---|
| **Server (`tiny_http`)** | ✅ ADOPT | `=0.12.0` (+ `ascii`, `chunked_transfer`, `httpdate`) | +4 over CLI | MIT, Apache-2.0, MIT/Apache-2.0 | ✅ builds | clean (only `tiny_http` itself — the reviewed inbound listener) |
| **Web UI (`maud`+`rust-embed`)** | ✅ ADOPT | `maud=0.27.0`, `rust-embed=8.7.2` | 31 (combined w/ server) | MIT, Apache-2.0 OR MIT, Unlicense OR MIT, `(MIT OR Apache-2.0) AND Unicode-3.0` | ✅ builds | clean (no async/HTTP-client/TLS) |
| **Chart lib (uPlot)** | ✅ ADOPT | vendored JS (M5) | 0 Rust deps | **MIT** (uPlot), htmx 0BSD/MIT | n/a | n/a (static asset, embedded via `rust-embed`) |
| **Inference path** | ✅ ADOPT (subprocess) | — (std::process) | 0 | — | n/a | n/a (no crate; no FFI per A2) |
| **Storage — DuckDB** | ❌ **REJECT** | `1.10504.0` bundled | **268** | pulls **`webpki-roots` (CDLA-Permissive-2.0)** → not on allowlist | (compiles) | **FAILS:** graph contains `reqwest, hyper, hyper-util, hyper-rustls, tokio`; `libduckdb-sys` has a **build-dep on `reqwest`** (build fetches network) |
| **Storage — SQLite (fallback)** | ✅ **ADOPT (R11)** | `rusqlite=0.37.0` (`libsqlite3-sys=0.35.0`, bundled) | **15** | MIT, MIT/Apache-2.0, Zlib | ✅ builds | clean |

**Why DuckDB is out (three independent disqualifiers, any one fatal):** (1) license gate fails
(`webpki-roots` CDLA-Permissive-2.0); (2) forbidden-crates gate fails — `reqwest/hyper/tokio` in the
graph, and the offline BFS walks **build-deps**, so DuckDB in `costroid-core` would trip it; (3) its
build fetches over the network (`libduckdb-sys` → `reqwest` build-dep), breaking reproducible offline
builds. SQLite (rusqlite, bundled) is clean on all three — 15 packages, all permissive, builds on 1.88,
zero forbidden crates, no build-time network. (Per-target resolution confirmed identical, zero
forbidden crates, on all 6 shipped targets.)

> The full 3-OS *compile* of the bundled C path (rusqlite at M1) is a CI matter — `libsqlite3-sys`
> bundled is a single, famously-portable C file (low risk vs DuckDB's C++ amalgamation, which was the
> R11 concern and is now removed). The pure-Rust web stack (tiny_http/maud/rust-embed) has no cross-OS
> compile risk.

---

## M0 — Scaffolding delivered (tree green on the local gate; CI confirms cross-OS)

- **`crates/costroid-power`** (new lib, leaf): `PowerSampler` trait + 3 impls (`Sysfs`/`WallMeter`/
  `Estimated`) + runtime selector (§6.3); `MeasurementMode` stamp (§6.4 `x_MeasurementMode`); the §3.2
  cost model with deterministic worked-example tests. Off-by-default **`power`** feature (R1, **not**
  `telemetry`); sysfs read path gated `#[cfg(all(target_os="linux", feature="power"))]` with a clean
  "unavailable" stub elsewhere. No `unwrap`/`expect`/`panic!`; typed `PowerError`. `publish = false`.
- **`apps/server`** → **`costroid-server`** (new binary): blocking `tiny_http` loopback server; binds
  `127.0.0.1` **by construction** (address built only from `Ipv4Addr::LOCALHOST`); `--self-check`
  one-shot proves loopback-bind + no egress and exits; `/healthz` + placeholder index. `publish=false`,
  `dist=false` (out of the release pipeline until M5/M6).
- **`apps/cli/tests/offline.rs`**: new `SERVER_ALLOWED` allowlist (`ascii`, `chunked_transfer`,
  `httpdate`, `tiny_http`) + `server_build_admits_only_the_reviewed_local_listen_subtree` test (rooted
  at `costroid-server`; forbids every outbound network/TLS/telemetry crate except the reviewed inbound
  listener; subset-allowlist fail-closes new deps) + `#[ignore] print_server_delta` regenerator.
- **`scripts/offline_acceptance.sh`**: new `assert_loopback_only` (allow loopback bind, forbid any
  non-loopback bind/connect) + a `costroid-server --self-check` proof block (strace-gated assertion;
  degrades honestly under netns/none).
- **`.github/workflows/ci.yml`**: new `cross-platform` job (macOS + Windows `cargo build --workspace
  --all-targets`). MSRV job already covers the two new 1.88 crates.
- **Root `Cargo.toml`**: members + `tiny_http`/`costroid-power` workspace deps.

---

## The phased plan (M0–M6) — deliverable + the deciding test per milestone

> Triage: **M1 (FOCUS export) and M3 (measured engine) are the irreducible core** — protect them
> over breadth (§3.3). Keep build+tests green on all OSes after each milestone (§2.3).

### M0 — Audit + plan + scaffolding ✅ (awaiting checkpoint)
**Deliverable:** a working scaffold that builds everywhere; decisions A locked; spikes B recorded;
plan + human inputs written. **Deciding test:** the verification gate above is green (it is).

### M1 — Core model + storage + FOCUS export + collectors  *(irreducible core)*
- Canonical event model; `x_PascalCase` extension schema (§6.4); **SQLite + Parquet** store behind a
  feature/dedicated crate (A5); the **v1.2-in / v1.3-out** FOCUS exporter (isolate version mappings).
- Extend the Claude/Codex/Cursor parsers into golden-tested collectors; annotate sub-agent-undercount
  uncertainty (Cardinal Rule R4: metadata only, never prompt/response content).
- **Deliverable:** schema-valid FOCUS export of real developer-tool data. **Deciding test:** the FOCUS
  1.3 validator (existing `focus_conformance.sh`) passes on the new export + a collector golden test.

### M2 — Cloud/API cost lane
- LiteLLM pricing **bundled dated snapshot + user override** (never a runtime fetch, R8); API-log
  pricing (historical/tiered); **AWS Data Exports FOCUS import** + **Bedrock Application Inference
  Profile** path — **ingest user-provided exported files only (pure-local parse in providers/core)**;
  any live AWS/Bedrock API call or credential read lives **only** in `costroid-connect` behind
  `connect`, keychain-only, with human sign-off (⚑ D).
- **Deliverable:** unified developer-tool + cloud/API ledger. **Deciding test:** a merged-ledger
  fixture test (synthetic AWS FOCUS sample + dev-tool logs → one FOCUS ledger).

### M3 — Dual-mode local-inference engine  *(irreducible core)*
- **M3a (agent-ownable, CI-tested):** the `PowerSampler` trait + 3 impls + runtime probing +
  `EstimatedPowerSampler` + deterministic cost-math on **synthetic** power fixtures, green
  cross-platform (the M0 scaffold is the seed). The subprocess inference **runner** (llama.cpp/Ollama,
  Vulkan default, A2/R7) + the **benchmark harness**; FOCUS-conformant local records. CI **never**
  asserts a real power number.
- **M3b (human-gated handoff — does NOT block M4):** native-Linux **sysfs `power1_average`**
  confirmation on the gfx1151 APU + a captured **joules/token** figure. If sysfs reads → measured-sysfs
  confirmed; else the human confirms the wall-meter path. **Never invent a power number** (R10).
- **Deliverable:** measured local cost-per-token (sysfs or wall-meter) with estimated fallback;
  measurement mode stamped on every record (R6). **Deciding test (M3a):** cost-math on synthetic power
  fixtures (worked examples) — present in the scaffold; extended with the runner contract.

#### M3 benchmark spectrum (§3.1.E) — record exactly this, produce numbers by the harness (R10)
The harness measures **cost / energy / throughput** by running each model on the Strix Halo; the
**quality** axis is taken from each model's **published** score (never re-derived here):

**Local models standardize on one family: Gemma 4 (Apache-2.0).** One permissive family from edge to
flagship, all on the 128 GB APU. **License is clean** — Apache-2.0 (verified vs the Gemma 4 model card
+ Google's 2026 launch); it is on the permissive allowlist and is the same license Costroid ships under.
**Pin to Gemma 4 specifically** — Gemma 1–3 used the restrictive non-OSI custom *"Gemma Terms of Use."*

| Class | Model (Gemma 4 family — Apache-2.0) | Notes / what it answers |
|---|---|---|
| **Fast MoE** | **Gemma 4 26B A4B** (25.2B total / **3.8B active**, 256K ctx) | the MoE speed point — near-4B latency, higher reasoning (community Qwen3-30B-A3B ~96–100 tok/s is the analog; reproduce). |
| **Dense flagship / coding counterexample** | **Gemma 4 31B Dense** (30.7B, 256K ctx) | the honest slow/expensive dense point — *"what does running a top open-weights coding model locally cost vs cloud?"* Bandwidth-bound; tok/s are **estimates until M3b**. |
| **Compute-efficient & edge** (optional breadth) | **Gemma 4 12B Unified** (encoder-free multimodal) · **E2B/E4B** (≤4B eff, 128K) | cheap floor points for the cost curve. |

> **Quality axis (R10):** take each variant's **published** Gemma 4 score (model card / public arena
> rankings — cite source + date, the board moves); where a coding-specific score isn't published, plot
> by cost/energy/throughput and mark quality *as published / n/a* — never re-derive or guess one here.
> A heavier ~120B-class MoE is **out of the standardized family** — include only as a clearly-labeled
> out-of-family reference, if at all.

> **Local models (we *run*) ≠ DeepSWE-Bench (we *read*).** The local set is the **Gemma 4 family**
> above. The Datacurve **DeepSWE-Bench** leaderboard below is a *dated cloud cost+quality reference*
> (name collision only with the *DeepSWE-Preview* model, which is no longer in our local set — unrelated;
> do not conflate).

### M4 — Break-even + scenario engine
- Workload-profile crossover vs named cloud prices (OpenAI/Anthropic/Bedrock), both directions, incl.
  amortized hardware + utilization; scenario inputs (mix, utilization, electricity rate, depreciation,
  pricing-snapshot date). Present **ranges + methodology, never a single hero number** (R6).
- **Cloud-side empirical anchor:** wire the **Datacurve DeepSWE-Bench** leaderboard
  (`deepswe.datacurve.ai`, §5.5 — per-model **Pass@1 · avg $/task · output tokens · steps** on the
  `mini-swe-agent` scaffold, **v1.1 2026-06-14**, 113 tasks / 5 langs / 91 repos, isolated-container
  grading) as a **dated snapshot** for the break-even comparison (§3.1.C) + a Frontier overlay.
  **Pull as a snapshot (R8); never hardcode a value** — the board moves (~$2.8–$21.6/task @ ~30–70%
  Pass@1). *(The shipped v0.6.0 Frontier snapshot was already refreshed to DeepSWE-Bench v1.1 + the
  `$/task` columns — done 2026-06-19; M4 consumes that snapshot for the break-even comparison.)*
- **Deliverable:** "for this workload, local breaks even at N tokens/day — or never, with the reason."
  **Deciding test:** a break-even unit test including a **"never" case**.

### M5 — Interfaces
- CLI/TUI surface over the new ledger; the `costroid-server` **Axum-equivalent (tiny_http) local API**;
  the **three-view web UI** (timeline / comparison / break-even) as **embedded static assets** (A3,
  §6.11) over `127.0.0.1` — bundled, never hosted. Every view ships a `--plain` path; never color-alone.
- **Deliverable:** a coherent local app over the ledger. **Deciding test:** the loopback-only proof
  still passes with the real routes (server binds 127.0.0.1, no egress).

### M6 — Quality, docs, data, demo, packaging
- Full test suite + CI gates; bundled sample datasets (synthetic local usage + AWS FOCUS + benchmark
  pack — entire demo with no Strix Halo / no cloud); README + Mermaid architecture + methodology page +
  `docs/limitations.md`; the **versioned benchmark dataset + blog-ready writeup** (R10 reproducibility);
  release packaging (flip `dist`/`publish` on the new crates; mirror apps/bar's archive choices);
  `docs/ARCHITECTURE.md` reconciled. **Cross-OS test *execution*** (vs the M0 build-only matrix) hardens
  here. **Deliverable:** a release-ready repository. **Deciding test:** the §6.12 DoD checklist.

---

## Human pre-flight inputs needed (⚑ Readiness gate C) — requested at this M0 checkpoint

The agent develops against **synthetic fixtures** meanwhile; these unblock the *real-data / real-run*
steps. None is needed to start M1 except **C1** (and even M1 can begin on synthetic FOCUS samples).

1. **C1 — FOCUS schema + samples (needed early, ~M1).** The official **FOCUS v1.2 schema + sample
   data** and a small **AWS Data Exports FOCUS sample**, to commit as CI fixtures — **with their
   redistribution license confirmed vendoring-compatible** (CI is offline). The repo today vendors only
   the 1.3 output ruleset.
2. **C2 — Native-Linux dual-boot (needed at M3b).** Ubuntu 24.04+ (kernel ~6.16.x, BIOS UMA/GTT split)
   — the only place `power1_average` can be confirmed on gfx1151.
3. **C3 — A configured wall meter (M3, fallback for M3b).** Manual value / CSV / smart-plug local API.
4. **C4 — AWS account (M2 + benchmarks).** Data Exports FOCUS enabled + a Bedrock Application Inference
   Profile.
5. **C5 — A dated electricity-rate default (+ hardware price/lifetime).** Under the R8 "stamp the
   assumption + date" discipline (Turkey EPDK tariff is the default template). Used by M3/M4.

**Plus the explicit go/no-go:** approve A1–A5 + the SQLite-over-DuckDB swap (A5/R11), then approve
starting **M1**.

---

## Milestone checklist (tick as completed)

- [x] **M0** — audit; decisions A locked; spikes B recorded (DuckDB→SQLite); scaffold green; plan +
  human inputs written; offline allowlist + loopback proof in place. *(Awaiting human checkpoint.)*
- [ ] **M1** — event model; SQLite+Parquet store; v1.2-in/v1.3-out FOCUS export (validated); collectors.
- [ ] **M2** — LiteLLM snapshot pricing; AWS-FOCUS import; Bedrock AIP path; merged ledger.
- [ ] **M3a** — PowerSampler engine + runner + harness + synthetic cost-math (cross-platform green).
- [ ] **M3b** — native-Linux sysfs `power1_average` confirmation + captured joules/token *(human)*.
- [ ] **M4** — break-even + scenario engine (incl. "never" case); DeepSWE-Bench dated snapshot wired.
- [ ] **M5** — CLI/TUI + tiny_http local API + 3-view embedded web UI (loopback-only).
- [ ] **M6** — tests/CI/datasets/docs/demo/packaging; benchmark dataset + writeup; ARCHITECTURE reconciled.

---

## Handoff note (latest)

- **2026-06-19 — M0 complete, awaiting checkpoint.** Scaffolded `costroid-power` + `costroid-server`;
  locked A1–A5; ran spikes B (DuckDB rejected on license + forbidden-crates + build-network → SQLite
  adopted, R11); wired the server's `SERVER_ALLOWED` allowlist + loopback-only runtime proof; added the
  cross-OS CI build matrix. Full local gate green (fmt/clippy/test/deny/MSRV/offline-acceptance).
  **Next:** human approves A-decisions + the M1 start; provide C1 (FOCUS samples) when convenient. Then
  begin **M1** (event model + SQLite store + v1.2-in/v1.3-out FOCUS export + golden-tested collectors).
