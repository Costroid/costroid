# Costroid-Next — PROGRESS

> **Living state for the post-v0.6.0 build** defined in [`docs/COSTROID-NEXT.md`](docs/COSTROID-NEXT.md):
> a local-first, self-hostable, FOCUS-native AI cost tool that adds a **measured/estimated
> local-inference economics engine**, a **cloud/API cost lane**, a **break-even calculator**, and
> a **local web UI** on top of the shipped v0.6.0 tool. This file is the resume-point (§2.5): a new
> session reads `CLAUDE.md` → this file → the last handoff note, then runs the gate to confirm state.
>
> **M1 (core model + storage + FOCUS export + collectors) — ✅ MERGED to `main`** (PR #2,
> rebased, 2026-06-20; CI green incl. macOS/Windows builds + the FOCUS validator). It was
> executed end-to-end (T0–T19 + C1) on the per-task dev-loop, independently reviewed at the
> milestone boundary (5× APPROVE, 0 HIGH/MEDIUM), and the LOW fold-ins folded in.
>
> **Current milestone: M2 (cloud/API cost lane) — NOT STARTED.** Scope + deciding test +
> task seeds are framed in the M2 section below; the detailed T-plan is synthesized at the
> M2 kickoff (the M0→M1 pattern). Work happens on branch `costroid-next` (recreated off the
> merged main); the human reviews at the M2 milestone boundary before the next merge.

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
> **Detailed task plan: [`docs/M1-PLAN.md`](docs/M1-PLAN.md)** — 20 ordered tasks (T0–T19), repo-fit-
> verified, with the C1 dependency map + the cross-cutting risks. **Awaiting human sign-off** on the
> export-schema additions (T2/T3: `x_Lane` + the local/cloud `x_` columns) and the `import` CLI
> subcommand (T19) before execution, per CLAUDE.md "ask before changing the export/output schema."
- Three-lane canonical event model (developer-tool / cloud-API / local-inference) with a mandatory
  `x_Lane` discriminator + `x_PascalCase` extension schema (§6.4); a **typed lane-separation guard**
  so lanes are never summed across (v0.6.0 dev-tool totals stay byte-for-byte).
- **SQLite store** (`rusqlite`, bundled — A5) behind a `store` feature on a CLI-reachable crate, with
  its own `STORE_ALLOWED` offline allowlist; **metadata-only whitelist schema** (R4 — no free-text
  column, fail-closed subset assertion on schema + ingest mapper). **Parquet DEFERRED** — the T1 spike
  found it gate-clean (parquet 59.0.0, all-permissive, 1.88) but a heavy 90-pkg/C-codec surface, so
  **CSV + JSON are the M1 exports**; the deciding test never depends on Parquet.
- The **v1.2-in / v1.3-out** FOCUS importer with an isolated version-mapping seam (`FocusV12Mapping`),
  built against synthetic v1.2 fixtures now; the real-AWS leg is **C1-gated** (T18) and M1 closes
  without it (synthetic round-trip green, real leg present-but-SKIPPED with a loud C1 notice).
- Extend the Claude/Codex/Cursor parsers into golden-tested collectors; sidechain attribution
  (`x_Sidechain`/`x_AttributionConfidence`, keep counting + annotate) — Cardinal Rule R4: metadata
  only, never prompt/response content (enforced by a no-`..` field-exhaustive structural test, T16).
- **Deliverable:** schema-valid FOCUS export of real developer-tool data. **Deciding test:** the FOCUS
  1.3 validator (existing `focus_conformance.sh`, extended with a JSON leg + a synthetic-v1.2 round-trip
  leg) passes + a Claude/Codex collector golden test asserting the normalized `FocusRecord` row.

### M2 — Cloud/API cost lane  *(NEXT — teed up 2026-06-20)*
- LiteLLM pricing **bundled dated snapshot + user override** (never a runtime fetch, R8); API-log
  pricing (historical/tiered); **AWS Data Exports FOCUS import** + **Bedrock Application Inference
  Profile** path — **ingest user-provided exported files only (pure-local parse in providers/core)**;
  any live AWS/Bedrock API call or credential read lives **only** in `costroid-connect` behind
  `connect`, keychain-only, with human sign-off (⚑ D).
- **Deliverable:** unified developer-tool + cloud/API ledger. **Deciding test:** a merged-ledger
  fixture test (synthetic AWS FOCUS sample + dev-tool logs → one FOCUS ledger).

> **M2 starting position (what M1 already laid down):** the `cloud_api` ledger lane + the
> `CloudUsageEvent` model + `cloud_usage_to_focus` (source-priced) + `focus_records_from_v12_import`
> + the `costroid import` CLI + the FOCUS-import seam (`FocusV12Mapping`) all exist and are merged.
> So M2 builds ON this, not from scratch.
>
> **M2 task seeds (synthesize the detailed T-plan at the M2 /goal):**
> 1. **LiteLLM pricing snapshot** — a bundled dated `litellm-prices.<date>.json` + user override;
>    source at build time (R8, never a runtime fetch); wire into the cloud-lane repricing path
>    (the M1 bridge already reprices usage-only rows from the *bundled* catalog — generalize it).
> 2. **Carry the foreign authoritative pricing** through the import (the M1 deferral): read the
>    v1.2 `SkuPriceId`/`PricingQuantity`/unit-price columns into the cloud row so a source-priced
>    import is fully-priced (closes the per-token-rate gap in `docs/limitations.md`), + multi-currency.
> 3. **AWS Data Exports FOCUS** + **Bedrock Application Inference Profile** — true the synthetic
>    AWS fixtures to real column shapes; expand `fixtures/focus/v1.2/` toward full mandatory coverage
>    and (now feasible — see below) **wire the v1.2-INPUT validation leg** the M1 fold-in deferred.
> 4. **Merged-ledger view** — dev-tool + cloud_api in one FOCUS ledger; the merged-ledger deciding
>    test (synthetic AWS FOCUS sample + dev-tool logs → one ledger), lane totals never cross-summed.
>
> **M2 inputs:** **C4** (a real AWS account w/ Data Exports FOCUS + a Bedrock AIP) unblocks the
> *real* AWS/Bedrock leg — but **M2 can begin entirely on synthetic AWS FOCUS samples**, exactly as
> M1 did (real leg stays present-but-SKIP until C4). No human input is required to START M2.
>
> **Dev note:** a real `focus-validator 2.1.0` is installable locally via `uv` (a venv) — used at the
> M1 boundary to verify the conformance legs against the real validator. M2 should do the same when
> expanding the AWS fixtures + the input-validation leg.

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
- [x] **M1** — three-lane event model + SQLite store + v1.2-in/v1.3-out FOCUS export (validated) +
  golden collectors. **✅ MERGED to `main` (PR #2, 2026-06-20; CI green).** Lean export schema
  (T2/T3) + `import` CLI (T19) signed off; Parquet deferred (T1 spike clean but heavy); C1 resolved
  synthetically (real-AWS leg present-but-SKIP, T18). Milestone-boundary review: 5× APPROVE, fold-ins in.
- [ ] **M2** — LiteLLM snapshot pricing; AWS-FOCUS import; Bedrock AIP path; merged ledger.
- [ ] **M3a** — PowerSampler engine + runner + harness + synthetic cost-math (cross-platform green).
- [ ] **M3b** — native-Linux sysfs `power1_average` confirmation + captured joules/token *(human)*.
- [ ] **M4** — break-even + scenario engine (incl. "never" case); DeepSWE-Bench dated snapshot wired.
- [ ] **M5** — CLI/TUI + tiny_http local API + 3-view embedded web UI (loopback-only).
- [ ] **M6** — tests/CI/datasets/docs/demo/packaging; benchmark dataset + writeup; ARCHITECTURE reconciled.

---

## Handoff note (latest)

- **2026-06-20 (g) — M1 ✅ MERGED to `main` (PR #2); M2 teed up; ready for the M2 /goal.**
  After the human's milestone-boundary review (APPROVED for merge), the 4 LOW fold-ins landed
  on the per-task dev-loop, independently re-reviewed by a 5-agent adversarial workflow
  (**5× APPROVE, 0 HIGH/MEDIUM**; 3 LOW: the `window_token_volume` ungated-token-summer
  asymmetry was FIXED with a gate + cross-lane test, `docs/limitations.md` tightened, the
  `all_lane_daily_token_series` name kept as cosmetic). Fold-in commits `cedae3d` (core
  lane-gate + deferral doc) + `27d6f28` (conformance hardening + accurate v1.2 docs).
  **Poisoned warm cache resolved** (`cargo clean` → clean rebuild, no phantom errors).
  **Verified the conformance gate against a REAL focus-validator 2.1.0** (installed locally via
  `uv`): CSV leg Fail:9 (exact pin), JSON equivalence (15 rows), four v1.2 round-trip legs
  Fail:4 (clean subset), real-AWS SKIP. Pushed `costroid-next` → **CI green on every job
  (incl. macOS + Windows cross-platform builds, FOCUS validator, MSRV 1.88, cargo-deny,
  offline-acceptance)** → merged via `gh pr merge --rebase --delete-branch`. main builds clean.
  **Next: the M2 /goal** — see the "M2 — Cloud/API cost lane" section above for the scope,
  deciding test, the 4 task seeds, and the M1 starting position M2 builds on. M2 can begin on
  synthetic AWS FOCUS samples with NO human input (C4 unblocks only the real AWS/Bedrock leg).
  Work on the recreated `costroid-next` branch; stop at the M2 milestone boundary for review.
- **2026-06-19 (f) — M1 COMPLETE (T0–T19 + C1); ⛔ STOP at the milestone boundary for the
  human's full fresh-eyes review before merge.** Branch `costroid-next`, the FOCUS-import
  half landed on the per-task dev-loop (build → independent adversarial review → fix →
  commit). Six commits since the store half:
  - `db36bfd` **C1 (resolved, no external dep)** — synthetic `fixtures/focus/v1.2/` (marked +
    unmarked CSV, JSON, AWS-shaped sample with x_ServiceCode/x_UsageType extras; R4 README) +
    vendored `scripts/focus-ruleset-1.2/model-1.2.0.1.json` (official v1.2 release asset,
    CC-BY-4.0, sha256 639b302a…).
  - `24ba887` **T13+T14** — `costroid-providers::focus_import` (FocusInputVersion/detect_version/
    RawFocusRow metadata-only/FocusV12Mapping/import_focus_{csv,json}; unknown columns dropped by
    serde, non-USD refused) + core `focus_records_from_v12_import` (reuses `cloud_usage_to_focus`,
    source-priced kept verbatim / usage-only repriced) + `x_FocusInputVersion` column persisted in
    the store (schema v3). **Independently reviewed: APPROVE-WITH-FIXES → fix folded in.**
  - `3ff574f` **T15** — sidechain attribution: `UsageEvent.is_sidechain` (Claude reads
    `isSidechain`, Codex always false) + `x_Sidechain`/`x_AttributionConfidence`/`x_CollectorVersion`
    columns (kept-counting + annotated-uncertain), persisted in the store (schema v4); golden test
    `claude_sidechain_golden.rs`; `docs/limitations.md`. **Independently reviewed: APPROVE.**
  - `153e6f2` **T16** — R4 no-`..` field-exhaustive destructure of `FocusRecord` (focus) + a
    persist-or-drop forcing function over `FocusRecord` (store): a new field is a COMPILE error
    until classified — proves content *cannot* enter the export/store, not just that it doesn't.
  - `17cc3b4` **T19** — public `costroid import` CLI (human-approved); integration test asserts the
    binary is byte-identical to the library path; no new offline-gate dep edge; README updated.
  - `6d0dff9` **T17+T18** — `focus_conformance.sh` deciding test: CSV leg (exact, pin unchanged) +
    JSON-equivalence leg (locally verifiable) + synthetic v1.2-in→v1.3-out round-trip legs
    (`--subset` contract) + real-AWS leg present-but-SKIP (`COSTROID_REAL_AWS_FOCUS`). CI job runs
    the script → all legs ride it.
  - **Local gate GREEN:** fmt; clippy (default + `--features store`); `test --workspace` (24 suites);
    store-feature CLI tests; offline.rs (7 pass); cargo-deny (default + `--all-features`); MSRV 1.88
    `-p costroid --features store`. **CI-only arbiters (run them in review):** `focus_conformance.sh`
    validator legs (no validator locally — the v1.2 `--subset` legs assert no NEW failing rule; if a
    new rule surfaces, it's a one-line fixture/allowlist adjust), cross-OS compile, strace
    `offline_acceptance.sh`.
  - **Human review focus (you flagged these):** (1) **T16 non-vacuity** — the no-`..` destructure is a
    genuine compile-time forcing function (adding a field breaks the build) + asserts the free-text-
    capable FOCUS columns are None / `charge_description` derived. (2) **T15 honesty** — sidechain rows
    are counted *and* tagged `x_AttributionConfidence=uncertain` (golden proves 130 of 430 tokens kept);
    `docs/limitations.md` records why. (3) **T17 real-schema** — validates the 1.3 OUTPUT against the
    vendored `scripts/focus-ruleset/` ruleset in CI (the v1.2 input also validates via the bundled +
    vendored 1.2.0.1 model). (4) **STORE_ALLOWED** carried the two new x_ column families with no
    network/TLS crate (offline gate still 7-pass). **Do not merge to main until you sign off.**
- **2026-06-19 (e) — M1 EVENT-MODEL + STORE HALF DONE (T2–T12 committed).** Branch `costroid-next`,
  16 commits. Each task: fresh-context build → independent review → green → commit. **Three real
  bugs caught by the per-task independent review + fixed before commit** (the dev-loop working):
  T6 an ungated `history_api_spend` cross-lane $-summer; T5 a source-priced row left `missing_price`;
  T11 the store dropped `list_cost`/priced-SKU columns (export not byte-identical) — all fixed +
  regression-tested. Done: T2 x_Lane · T4 canonical events · T5 dispatch · T6 lane-guard
  (`287389e`) · T7 R14 serde · T8 aggregate_rows · **T9–T12 costroid-store** (`bdc13cd`, `edfea88`):
  SQLite/rusqlite bundled behind off-by-default `store` feature; R4 metadata-whitelist schema
  (v2, fail-closed); faithful replay (priced row round-trips byte-identical); `STORE_ALLOWED`
  fail-closed offline guard (exact 12-crate delta, no forbidden/over-broad entry — the security-
  sensitive guard the human flagged for the M1 boundary) + runtime no-AF_INET proof; MSRV 1.88 +
  deny clean; default CLI/bar/server graphs store-free. **Next: the FOCUS-import half** — T13
  (FOCUS v1.2 version-detection seam + reader, providers, against SYNTHETIC fixtures) → T14 (core
  source-priced import bridge, reusing T5's `cloud_usage_to_focus`) → T15 (collector sidechain
  attribution + golden tests) → T16 (R4 no-`..` structural test) → T17 (deciding test: conformance
  CSV+JSON+synthetic-v1.2 legs) → T19 (public `import` CLI, human-approved). **T18** (real-AWS v1.2
  sample) blocked on **C1** (human researching a license-clean schema+sample to vendor). Verify:
  run the gate block at the top of this file; the human runs the full fresh-eyes review at the M1
  milestone boundary before merge.
- **2026-06-19 (d) — M1 execution: T2–T10 committed (each: fresh-context build → independent
  review → green → commit).** Branch `costroid-next`. Done: **T2** x_Lane (`c1f955d`) · **T4**
  canonical event model (`08a65d4`) · **T5** focus_records_from_canonical dispatch (`af7d9ef`) ·
  **T6** lane-separation $-guard (`287389e`, dual-reviewed — the review CAUGHT + I fixed an ungated
  `history_api_spend`; human-approved with the `aggregate_activity` hygiene gate folded in) · **T7**
  R14 serde round-trips (`7d637e3`) · **T8** public `aggregate_rows` (`fa78cb7`) · **T9+T10**
  costroid-store foundation (`bdc13cd` — SQLite/rusqlite bundled behind off-by-default `store`
  feature; R4 metadata-whitelist schema, fail-closed; MSRV 1.88 + deny clean; default CLI store-free).
  T3 = no-op (lean). **Next: T11** (store ingest→replay via `aggregate_rows`→export round-trip) →
  **T12** (`STORE_ALLOWED` offline guard — SECURITY-SENSITIVE: a fail-closed subset like
  `SERVER_ALLOWED`/`CONNECT_ALLOWED`, the human will scrutinize this at the M1 boundary) → T13–T17
  (FOCUS v1.2 import seam + collectors + R4 structural test + deciding test) → T19 (import CLI).
  **T18** (real-AWS v1.2 sample) blocked on **C1** (human is researching a license-clean
  schema+sample to vendor). Review cadence: per-task independent review (mine); the human runs the
  full fresh-eyes review at the **M1 milestone boundary** before merge.
- **2026-06-19 (c) — M1 EXECUTION started; T2 done.** Sign-offs locked (`b2c7f34`): lean column
  set, import CLI approved, synthetic/C1-later, Parquet deferred. **T2 committed (`c1f955d`)** — the
  `x_Lane` three-lane discriminator + `LedgerLane` enum, threaded through all 13 `UnpricedUsage` sites
  (all `DeveloperTool`; v0.6.0 export preserved). Built fresh-context → independently adversarial-
  reviewed (verdict: correct/complete/non-regressing) → committed. Gate green. **M1 task status:**
  T0 ✅ (doc) · T1 ✅ (Parquet spike → deferred) · **T2 ✅ (x_Lane)** · T3 = no-op (lean) · next **T4**
  (CanonicalEvent/CloudUsageEvent/LocalRunEvent in providers) → T5 (core normalizers) → **T6 (lane-
  separation $-summer guard — highest blast radius)** → T7 (R14 serde tests) → T8 (public aggregate_rows)
  → T9–T12 (store) → T13–T16 (FOCUS import + collectors) → T17 (deciding test) → T19 (import CLI). All
  buildable now on synthetic fixtures; only T18 (real-AWS sample) is C1-gated. Each task: fresh-context
  build → independent review → commit (dev-loop).
- **2026-06-19 (b) — M0 approved + committed; M1 PLANNED, awaiting export-schema sign-off.** M0
  committed on branch `costroid-next` (`707abcf`) with the independent-review fixes folded in (loopback
  regex quote-anchored; cost.rs guards negative inputs; unused deps dropped). Ran the M0→M1 design
  workflow → **[`docs/M1-PLAN.md`](docs/M1-PLAN.md)** (T0–T19, repo-fit-verified). Ran the T1 Parquet
  spike (gate-clean but heavy → Parquet deferred; CSV+JSON are the M1 exports). Reconciled the DuckDB→
  SQLite doc-drift (`COSTROID-NEXT.md` §3.3 M1 line). **Next:** human signs off on the M1 export-schema
  additions (T2/T3 `x_Lane` + `x_` columns) + the `import` subcommand (T19) per CLAUDE.md, and confirms
  M1-closes-without-C1; provide **C1** (FOCUS v1.2 schema + AWS sample) to unblock T18. Then execute M1
  in order: T0 (doc) → T2–T8 (event model + core, the foundation) → T9–T12 (store) → T13–T17 (FOCUS
  import + collectors + deciding test). All of T0–T17/T19 are buildable now on synthetic fixtures.
- **2026-06-19 (a) — M0 complete, presented for checkpoint.** Scaffolded `costroid-power` +
  `costroid-server`; locked A1–A5; ran spikes B (DuckDB rejected → SQLite, R11); wired the server's
  `SERVER_ALLOWED` allowlist + loopback-only proof; added the cross-OS CI build matrix. Full gate green.
