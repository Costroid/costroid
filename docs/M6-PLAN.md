# M6 — Quality, docs, data, demo, packaging (the FINAL milestone)

> **Status:** PLAN SYNTHESIZED 2026-06-21 — ⛔ awaiting the coordinator's **pre-coding plan
> review** + the **D1–D4 sign-off** below. **No M6 code is written until then** (CLAUDE.md "ask
> first"). Scope canon: [`docs/COSTROID-NEXT.md`](COSTROID-NEXT.md) §6.6–6.12. **Deciding test for
> the milestone = the §6.12 Definition-of-Done checklist** (closed against, never self-judged by
> prose). Branch `costroid-next` off `main` @ `631b5a4` (M0–M5 merged, PRs #2–#6); tree clean.

M6 is the **release-ready** milestone: it adds no new product capability — it hardens what M1–M5
built (cross-OS test execution, professional docs, bundled demo data, a deterministic demo, and the
release flip for the three new crates) and **closes the whole project against §6.12**. The actual
release **tag/publish stays a human step** (tag-triggered, see [`RELEASING.md`](../RELEASING.md));
M6 makes everything ready for that human to pull the trigger.

---

## T0 — Current-state audit (reconciled to the live tree 2026-06-21)

Read the repo; **the code wins over any doc**. Findings that shape this plan:

**Workspace (10 members).** `apps/{cli,bar,server}` + `crates/costroid-{focus,providers,core,config,connect,power,store}`. Version `0.6.0` lockstep; MSRV 1.88 (libs+CLI), 1.92 (bar). Dep direction holds: `apps → core → {providers,focus}`; `connect → core` (off-by-default `connect`); **no `core→power` edge** (verified — `costroid-power` is a true leaf: `serde`/`serde_json`/`thiserror` only; `costroid-store` → `costroid-focus`; `costroid-server` → `costroid-core` + `costroid-store`).

**The three new members are NOT yet released** (the explicit M6 packaging task):
- `crates/costroid-power/Cargo.toml` → `publish = false`
- `crates/costroid-store/Cargo.toml` → `publish = false`
- `apps/server/Cargo.toml` → `publish = false` **and** `[package.metadata.dist] dist = false`

**CI** ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) — 7 jobs: `pre-pr` (Linux fmt/clippy/test + connect + power feature legs), **`cross-platform` (macOS+Windows BUILD-ONLY — the §6.6 hardening target)**, `msrv` (1.88), `focus-conformance` (validator + pricing/power integrity sha256), `license` (cargo-deny offline), `advisories` (cargo-deny online), `offline-acceptance` (Linux strace static + dynamic). The `cross-platform` job header already names M6 as where test *execution* lands.

**Release** ([`RELEASING.md`](../RELEASING.md) + [`dist-workspace.toml`](../dist-workspace.toml)): cargo-dist 0.32, `precise-builds = true` (only `-p costroid` shipped — keeps libdbus off release runners), SHA-256 + keyless provenance, shell/PS/Homebrew/npm + crates.io for the CLI, archives + crates.io for the bar (no npm/Homebrew/musl). Publish ladder today: `focus → providers → core → config → connect → costroid → costroid-bar`. **Connect tests use a keyring MOCK** (`install_mock_keychain`); the real `keyring::Entry` path is `connect`-feature-gated → the default `cargo test --workspace` is keychain-safe cross-OS.

**Fixtures** (`fixtures/`, the existing discipline — synthetic, never real user data): `claude-code/`, `codex/`, `cursor/`, `discovery/`, `focus/v1.2/` (incl. `synthetic-aws-v12*.csv`), `local/` (llama/ollama/LHM goldens), `wsl-windows/`. M3 data artifacts: `crates/costroid-power/{profiles/hardware.v1.json,models/gemma4.v1.json}` each with a `.sha256` sidecar + a fail-closed `check_power_profiles.sh` integrity gate.

**Docs:** `README.md`, `docs/{ARCHITECTURE,COSTROID-NEXT,DESIGN-SYSTEM,ROADMAP,limitations,M1..M5-PLAN}.md`, `CHANGELOG.md`. **Missing for §6.8:** a methodology page, a Mermaid architecture diagram + "what ccusage doesn't" table + hero-GIF placeholder in README, and an ARCHITECTURE reconcile for M6. **`docs/limitations.md`** exists (M3-era) and must be consolidated with M4/M5 caveats. **No `make demo` / Makefile exists** (net-new, §6.9). **No benchmark writeup / versioned benchmark manifest exists** (net-new, §6.10).

**PROGRESS.md** reconciled this task: M5 → MERGED (PR #6); current milestone = M6; handoff note (p) added.

### The synthetic-now / real-post-M3b split (explicit — M3b never blocks M6)

| Ships **now** (synthetic / estimated, every figure R8/R10-stamped) | Deferred to a **post-M3b human follow-up** |
|---|---|
| The benchmark **manifest format** + raw **synthetic** outputs + the writeup **with placeholders** | The real captured **joules/token** numbers that fill the placeholders |
| The deterministic **`make demo`** path (estimated mode, no hardware, no network) | The 60–90s **hero-GIF screen recording** of a real run |
| All sample **datasets** (synthetic local usage + AWS FOCUS + benchmark pack) | Truing the synthetic AWS fixtures against a **real** export (present-but-SKIP leg already exists) |
| Every doc, table, methodology figure carrying **"estimated — pending M3b measurement"** | The doc edit that flips those stamps to "measured" once M3b lands |

A short **`docs/POST-M3B-REFRESH.md`** checklist (T8) enumerates exactly which files/figures the human refreshes after the wall-meter run — so the deferral is a scoped, written follow-up, not a loose end.

---

## Decisions D1–D4 — ✅ SIGNED OFF 2026-06-21 (all at the recommended default)

The human signed off all four at the recommended default; the plan below builds against these.

- **D1 — Benchmark writeup data → ✅ SHIP NOW, STAMPED.** The manifest format + raw synthetic outputs + the blog-ready writeup land now; **every figure stamped "estimated — pending M3b measurement"** (R8/R10); real numbers fill the placeholders in a documented post-M3b refresh (`docs/POST-M3B-REFRESH.md`). Keeps the §6.10 DoD box closable at the milestone. *(Drives T8.)*
- **D2 — Demo / hero GIF → ✅ `make demo` + GIF PLACEHOLDER.** The agent ships a deterministic `make demo` (synthetic fixtures, offline, no hardware) + a README hero-GIF **placeholder** stamped "capture pending M3b"; the actual screen recording is a human step. *(Drives T2, T4.)*
- **D3 — Packaging → ✅ MIRROR `apps/bar`.** `costroid-power` + `costroid-store` = crates.io libraries (`publish = true`, no dist archives). `costroid-server` = binary → archives + crates.io (no npm/Homebrew/musl), `publish = true` + `dist = true` (archive installers only). **Publish ladder:** `focus → providers → core → config → connect → store → power → costroid → server → bar`. *(Drives T9.)*
- **D4 — Cross-OS test-execution scope → ✅ CORE + POWER + SERVER + OFFLINE-STATIC.** macOS + Windows run `cargo test --workspace` + `cargo test -p costroid --features power` + `cargo test -p costroid-server` + the static `--test offline` dependency proof. **Linux-only stays:** the strace dynamic offline-acceptance harness, the FOCUS-validator conformance, `cargo deny`, the MSRV check (toolchain/OS-pinned for determinism). *(Drives T3.)*

---

## Ordered task breakdown (T0..T11) — each with its deciding test + §6.12 DoD mapping

> §6.12 boxes referenced as **DoD-N** (1..12 in document order). M6's net-new boxes are **DoD-2**
> (cross-OS test exec + dist flip), **DoD-7** (datasets/docs/demo/packaging/benchmark), **DoD-11**
> (the M6 deciding test), **DoD-12** (ARCHITECTURE reconcile). DoD-1/3/4/5/6/8/9/10 were satisfied
> by M1–M5; M6 **regression-guards** them at the project close.

### T0 — Audit + plan + PROGRESS reconcile *(this deliverable; no code)*
- **Do:** the audit above; reconcile PROGRESS (M5→merged PR #6, current=M6, handoff (p)); write this plan; surface D1–D4.
- **Deciding test:** PROGRESS reflects merged M5 + current M6; `docs/M6-PLAN.md` present with T0..Tn + a DoD map; D1–D4 surfaced with defaults. **→ DoD-11 (kickoff).**

### T1 — Synthetic sample datasets (§6.7)
- **Do:** a curated, discoverable `samples/` tree (distinct from CI `fixtures/`, but same synthetic discipline) the demo + docs read: **(a)** a synthetic local-usage ledger (a `costroid-store` SQLite DB or the JSONL the importer reads), **(b)** a demo-grade synthetic AWS FOCUS v1.2 export, **(c)** a benchmark pack (synthetic `bench` outputs keyed to the Gemma 4 manifest). Each carries a `README.md` stamping it synthetic + estimated. No real data; no weights.
- **Deciding test:** a Rust test (in `costroid-core` or an `apps/server`/`apps/cli` integration test) loads each sample and asserts it parses + round-trips to **FOCUS 1.3 (schema-valid)**; the conformance gate gains a `samples/` leg (or reuses the existing merged-ledger leg). Offline, no hardware. **→ DoD-7, DoD-3 (regression).**

### T2 — `make demo` / one-command deterministic path (§6.9)
- **Do:** a top-level `Makefile` (+ a thin cross-platform fallback script) chaining **import → bench (estimated) → (synthetic power capture) → breakeven/compare → export FOCUS** over the T1 samples, `--features power`, **fully offline, no hardware**, deterministic output. A `make demo` README quickstart line.
- **Deciding test:** a CI leg (Linux, inside `offline-acceptance` or a new step) runs the demo end-to-end and asserts **exit 0 + the expected FOCUS-1.3 artifact**; the CLI legs emit **no socket** (the existing static `offline` + strace harness still green). **→ DoD-7, DoD-10 (regression).**

### T3 — CI: cross-OS test *execution* (§6.6)
- **Do:** promote the `cross-platform` job from `cargo build` to **`cargo test`** per D4: `cargo test --workspace`, `cargo test -p costroid --features power`, `cargo test -p costroid-server`, `cargo test -p costroid --test offline` on macOS + Windows. **Confirm** the default workspace test never touches a real OS keychain on the runners (connect tests are mock-backed + feature-gated — verify, add a guard if needed). **Keep Linux-only:** strace offline-acceptance, FOCUS conformance, cargo-deny, MSRV (unchanged).
- **Deciding test:** the CI matrix is **green with test execution on all three OSes**; the Linux-only gates are byte-unchanged; no keychain prompt/hang on mac/win. **→ DoD-2, DoD-1 (regression).**

### T4 — README rewrite (§6.8)
- **Do:** one-paragraph problem statement; a **hero-GIF placeholder** (stamped "capture pending M3b", D2); a **one-command quickstart** (`cargo install costroid` / `make demo`); a **"what this does that ccusage doesn't"** table (FOCUS-native, 3-lane ledger, local-inference economics, break-even, loopback web UI, reconciliation, zero-network default — sourced from §5.7, star counts omitted/verified); a **Mermaid** architecture diagram of the crate graph + the three lanes + the loopback server.
- **Deciding test:** a doc-presence test (or a CI markdown check) asserts the README contains each required section + a fenced `mermaid` block; **no un-stamped real/hero number**; the Mermaid parses. **→ DoD-7.**

### T5 — Methodology page (§6.8)
- **Do:** `docs/methodology.md` — exactly how energy/token is derived: **measured vs estimated** (`x_MeasurementMode` ladder), **package-power vs wall** (the ~20–40% caveat), the **energy-only `e` over total (in+out) tokens** basis (the M5 lock), and the break-even math (calendar-fixed amortization, the sensitivity band, the "never"/infeasible case). Cross-link limitations + ARCHITECTURE.
- **Deciding test:** the page exists + is linked from README/ARCHITECTURE; its `e` formula and the measured ladder **match the code** (`local_energy_only_rate`, `MeasurementMode`) — a reviewer-checkable consistency claim, plus a test that the cited default electricity rate equals the profile's. **→ DoD-7, DoD-5 (regression).**

### T6 — `docs/limitations.md` consolidation (§6.8)
- **Do:** extend (don't replace) the M3-era file with **M4** (break-even ranges; the "never" outcome; one-lifetime rule), **M5** (interface caveats: text/table break-even web view; loopback-only), and an explicit **uncertain-row annotation** cross-ref (`x_AttributionConfidence`, sub-agent undercount, package-vs-wall) showing where the UI surfaces it.
- **Deciding test:** limitations.md covers sub-agent undercount + package-vs-wall + the uncertain-row annotation, and each claim maps to a real column/behavior in code. **→ DoD-7, DoD-9 (regression: the annotation is the non-color cue).**

### T7 — `ARCHITECTURE.md` reconcile (§6.8, DoD-12)
- **Do:** extend the existing doc with the M6 close: the final 10-member crate graph, the three-lane ledger, the loopback server data path (§10 — verify against `apps/server`), the offline model (per-binary allowlists; CLI byte-for-byte; server loopback-only), the datasets/demo/packaging additions. **Reconcile, never rewrite.**
- **Deciding test:** ARCHITECTURE describes the final scope with **no contradiction against the code** (crate graph, lanes, server path, offline guarantees); a reviewer diff confirms additive edits. **→ DoD-12.**

### T8 — Benchmark dataset + writeup (§6.10) *(D1)*
- **Do:** a **versioned manifest** (`benchmarks/<id>/manifest.v1.json` mirroring the `gemma4.v1.json` schema style — `schema`/`as_of`/`source`/per-run records) + **raw synthetic outputs** + a **blog-ready methodology writeup** (`docs/benchmark-gemma4-vs-cloud.md`): "what Gemma 4 31B Dense (Apache-2.0) actually costs locally on a 128 GB APU vs Bedrock/Anthropic", full reproduction details. **Every figure stamped "estimated — pending M3b measurement"** (R8/R10). Add the manifest to the `check_power_profiles.sh`-style sha256 integrity gate. Write `docs/POST-M3B-REFRESH.md` (the scoped human follow-up).
- **Deciding test:** the manifest validates + has a committed `.sha256` the integrity gate checks; a test asserts **no un-stamped hero number** in the writeup (every cost/tok-s figure carries the estimated/pending-M3b stamp); the reproduction steps run via `make demo`. **→ DoD-7.**

### T9 — Packaging: flip dist/publish on the new crates (§6.9) *(D3)*
- **Do:** per D3 — `publish = true` on `costroid-power` + `costroid-store` (crates.io libraries; bundled assets — power's `profiles/`+`models/` JSON — included via `cargo package`); `publish = true` + `[package.metadata.dist] dist = true` (archive installers, no npm/Homebrew/musl) on `costroid-server`. Workspace deps gain `version` (publishable). Update [`RELEASING.md`](../RELEASING.md): the new ladder `focus → providers → core → config → connect → store → power → costroid → server → bar`, the server archive row, and the "what ships" list. Keep `precise-builds = true` (CLI stays connect-OFF on release runners; the server has no libdbus/GTK deps → builds clean on all targets).
- **Deciding test:** `cargo package -p <crate> --list` / `cargo publish --dry-run` succeeds for power/store/server with their bundled assets present; `dist plan` includes the `costroid-server` archives for the 5 targets (no musl); RELEASING ladder + dist config consistent; the offline/forbidden-crates gates still green (server stays loopback-only). **→ DoD-2, DoD-7.**

### T10 — Packaging polish: issue templates, changelog, license (§6.9)
- **Do:** `.github/ISSUE_TEMPLATE/` (bug report, feature request, provider-request) + `config.yml`; a CHANGELOG **Unreleased** section consolidating M1–M6 (the costroid-next feature set); confirm the LICENSE + per-crate license metadata + `cargo deny` cover the three new crates. **The version bump + tag is a human release step** (RELEASING.md) — M6 stages the CHANGELOG, does not tag.
- **Deciding test:** issue templates are valid (GitHub form schema / YAML lint); CHANGELOG has the Unreleased section naming the new crates + features; `cargo deny check licenses bans` green (incl. the new crates). **→ DoD-7.**

### T11 — Milestone close: full §6.12 DoD verification (the deciding test)
- **Do:** run the **whole gate** (`fmt` · `clippy --workspace --all-targets -D warnings` · `test --workspace` · power feature clippy+test · store · `cargo deny` default+all-features · MSRV 1.88 · pricing+power+benchmark integrity · FOCUS conformance · `offline_acceptance.sh </dev/null` · the new cross-OS test legs via CI) and **tick every §6.12 box with its evidence** in a close-out section appended to this plan. Independent adversarial review at the boundary; fold-ins; ⛔ STOP for the human's final boundary review before the PR.
- **Deciding test:** **the §6.12 Definition-of-Done checklist, all green, each box backed by a named test/artifact** — never self-judged by prose. **→ DoD-11 (close) + a final pass over DoD-1..12.**

---

## §6.12 DoD → task traceability

| DoD box | Satisfied by | M6 task |
|---|---|---|
| 1 — new crates integrated; existing behavior intact | M0–M5 | regression-guarded T3, T11 |
| 2 — cross-platform build + distribution green; power/cfg gates | M0–M5 build + **M6** | **T3 (test exec), T9 (dist flip)** |
| 3 — unified 3-lane FOCUS ledger; v1.2-in/v1.3-out; CI-validated | M1–M3 | regression-guarded T1 |
| 4 — four-source PowerSampler; measured cross-OS via wall meter; mode stamped | M3a (+M3b real numbers) | doc'd T5/T8; **M3b deferred** |
| 5 — break-even + scenario modeling; honest ranges | M4 | doc'd T5; regression T11 |
| 6 — CLI/TUI + API + 3-view web UI (local-only) | M5 | regression T3, T11 |
| 7 — full tests, CI gates, datasets, docs, demo, packaging, benchmark | **M6** | **T1,T2,T4,T5,T6,T8,T9,T10** |
| 8 — Cardinal Rule (R4): no prompt/response content anywhere | M1–M5 | regression T1, T3, T11 |
| 9 — accessibility: `--plain` + never color-alone on every new visual | M1–M5 | T4/T6 (docs note the cues); regression T11 |
| 10 — offline intact: CLI byte-for-byte; server loopback-only; offline+strace green | M0–M5 | regression **T2, T3, T9, T11** |
| 11 — each milestone closed against a written deciding test | M1–M5 | **T0, T11 (M6's)** |
| 12 — ARCHITECTURE reconciled with the new scope | — | **T7** |

---

## Hazards to watch (carry into the per-task dev loop)

1. **Cross-OS keychain (T3):** confirm `cargo test --workspace` never opens a real OS keychain on mac/win runners (connect is mock-backed + feature-gated — verify, don't assume).
2. **Publish-ability (T9):** workspace path-deps need a `version` to publish; bundled assets must live under the crate dir (`cargo package --list` is the proof). `precise-builds` must keep the release runners libdbus-free.
3. **Determinism (T2/T8):** the demo + benchmark outputs must be byte-stable (no timestamps/RNG in committed artifacts) so the deciding tests aren't flaky — stamp dates from the dated profile/manifest, never "now".
4. **R8/R10 honesty (T4/T5/T8):** every estimated/synthetic figure carries the stamp; CI asserts **no un-stamped hero number** — the same discipline M3a's CI already enforces for power.
5. **Additive docs (T6/T7):** extend `limitations.md` / `ARCHITECTURE.md`, never replace (the code wins; don't introduce a doc that drifts from it).

---

## Pre-PR gate (every task + the milestone close)

```bash
cargo fmt --all -- --check \
 && cargo clippy --workspace --all-targets -- -D warnings \
 && cargo test --workspace
cargo clippy -p costroid --features power --all-targets -- -D warnings && cargo test -p costroid --features power
cargo test -p costroid-power --features power && cargo test -p costroid-store --features store
cargo deny check licenses bans && cargo deny --all-features check licenses bans
cargo +1.88.0 check --workspace --all-targets --exclude costroid-bar
bash scripts/check_power_profiles.sh && bash scripts/check_pricing_snapshots.sh
bash scripts/offline_acceptance.sh </dev/null
```
Cross-OS test execution is the **CI** arbiter (T3) — "green in WSL is not green" (§2.3).
