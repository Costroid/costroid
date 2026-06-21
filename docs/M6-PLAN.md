# M6 — Quality, docs, data, demo, packaging (the FINAL milestone)

> **Status:** **Rev 2 CONFIRMED — coding cleared 2026-06-21.** The coordinator's pre-coding plan
> review (0 blockers / 1 high / 4 med / ~12 low) is folded in (see the Rev-2 changelog below);
> **D1–D5 ALL SIGNED OFF** (D1–D4 at the recommended defaults; **D5 → honor `SOURCE_DATE_EPOCH`**);
> the Rev 2 deltas are **confirmed → T1 cleared**. Now executing M6 on the per-task dev loop
> (fresh-context build → independent adversarial review → fold-in), task by task, stopping at the
> milestone boundary for the final review. Scope canon: [`docs/COSTROID-NEXT.md`](COSTROID-NEXT.md)
> §6.6–6.12. **Deciding test for the milestone = the §6.12 Definition-of-Done checklist** (closed
> against, never self-judged by prose). Branch `costroid-next` off `main` @ `631b5a4` (M0–M5 merged,
> PRs #2–#6); tree clean.

M6 is the **release-ready** milestone: it adds no new product capability — it hardens what M1–M5
built (cross-OS test execution, professional docs, bundled demo data, a deterministic demo, and the
release flip for the three new crates) and **closes the whole project against §6.12**. The actual
release **tag/publish stays a human step** (tag-triggered, see [`RELEASING.md`](../RELEASING.md));
M6 makes everything ready for that human to pull the trigger.

---

## Rev 2 changelog — coordinator's pre-coding review, folded in (2026-06-21)

**HIGH**
- **PKG-1 — publish deciding-test is now the WORKSPACE form.** T9 + RELEASING.md no longer claim a
  per-package `cargo publish --dry-run -p <crate>` as the *runnable pre-publish gate* — that cannot
  run before the siblings are on crates.io and the published `focus 0.6.0` has drifted from local (M1
  added fields with no bump). The runnable gate is `cargo package --workspace` /
  `cargo publish --dry-run --workspace`; `cargo package -p <crate> --list` is kept **only** as the
  bundled-assets-presence check. Real per-package publish stays the **human ladder at the bumped
  version** — and **the version bump is what makes the ladder clean** (re-trues `focus`).

**MED**
- **(M1) bench determinism is a CLI-surface change → SURFACED as D5.** `apps/cli/src/bench.rs:128`
  stamps `chrono::Utc::now()`; a deterministic demo/benchmark golden needs a fixed clock. Scoped a
  sub-task to honor `SOURCE_DATE_EPOCH` (default → the profile `as_of`), pin the T2/T8 goldens, and
  assert a **byte-identical re-run**. Because it changes observable CLI behavior, it is **D5 (sign-off
  required)**, not an internal detail.
- **(M2) `make demo` cross-OS** — scoped Linux/macOS via `make` (+ a macOS CI smoke leg, since D4
  already tests macOS), with the **raw `cargo` equivalents documented for Windows** (no `make`).
- **(M3) `POST-M3B-REFRESH.md` is a closed file-by-file checklist** — each file + `.sha256` regen +
  `as_of` bump + integrity re-pass, guarded by a **drift-guard test** (T8).
- **(M4) doc deciding-tests are named** — a docs-presence test + a `scripts/check_doc_stamps.sh`
  scanning against **one canonical stamp constant**; T5's e-formula is a **real cross-check** (not a
  prose claim); plus the **INVERSE guard**: every committed FOCUS sample/benchmark/demo row asserts
  `x_MeasurementMode == "estimated"`. Hazard 4's "same as M3a" wording corrected.

**LOW (batch, all folded into the tasks below):** PKG-2 publish only at the bumped version · PKG-3
reserve the three crate names (RELEASING note) · `apps/server` `[package.metadata.dist]` =
`installers = []` + the explicit 5 targets (mirror bar) + assert no npm/homebrew/musl artifact +
`dist generate --check` · the keychain guard is **non-optional** + a written mock invariant + T0
audit wording fixed · `check_benchmarks.sh` rooted at `benchmarks/` · the demo runs CLI-only under
the `assert_no_inet` harness · **omit** competitor star counts · a one-line SBOM/signing defer ·
a dedicated `samples/` conformance leg with a row-count guard · `measurement_mode` recorded in the
manifest · a publish-ladder **topo-sort assertion**.

**REFUTED (coordinator withdrew / confirmed non-issues — no action):** the D2 static-hero
suggestion and the D3 "server needs its own `limitations.md` caveat" suggestion (both withdrawn);
bare `cargo test --workspace` **is** keychain-safe (the guard is added as defense-in-depth, not a
fix); **DoD-9 does not govern doc Mermaid/GIF** (the DoD-9 row no longer cites T4).

---

## T0 — Current-state audit (reconciled to the live tree 2026-06-21)

Read the repo; **the code wins over any doc**. Findings that shape this plan:

**Workspace (10 members).** `apps/{cli,bar,server}` + `crates/costroid-{focus,providers,core,config,connect,power,store}`. Version `0.6.0` lockstep; MSRV 1.88 (libs+CLI), 1.92 (bar). Dep direction holds: `apps → core → {providers,focus}`; `connect → core` (off-by-default `connect`); **no `core→power` edge** (verified — `costroid-power` is a true leaf: `serde`/`serde_json`/`thiserror` only; `costroid-store` → `costroid-focus`; `costroid-server` → `costroid-core` + `costroid-store`).

**The three new members are NOT yet released** (the explicit M6 packaging task):
- `crates/costroid-power/Cargo.toml` → `publish = false`
- `crates/costroid-store/Cargo.toml` → `publish = false`
- `apps/server/Cargo.toml` → `publish = false` **and** `[package.metadata.dist] dist = false`

**CI** ([`.github/workflows/ci.yml`](../.github/workflows/ci.yml)) — 7 jobs: `pre-pr` (Linux fmt/clippy/test + connect + power feature legs), **`cross-platform` (macOS+Windows BUILD-ONLY — the §6.6 hardening target)**, `msrv` (1.88), `focus-conformance` (validator + pricing/power integrity sha256), `license` (cargo-deny offline), `advisories` (cargo-deny online), `offline-acceptance` (Linux strace static + dynamic). The `cross-platform` job header already names M6 as where test *execution* lands.

**Release** ([`RELEASING.md`](../RELEASING.md) + [`dist-workspace.toml`](../dist-workspace.toml)): cargo-dist 0.32, `precise-builds = true` (only `-p costroid` shipped — keeps libdbus off release runners), SHA-256 + keyless provenance, shell/PS/Homebrew/npm + crates.io for the CLI, archives + crates.io for the bar (no npm/Homebrew/musl). Publish ladder today: `focus → providers → core → config → connect → costroid → costroid-bar`. The three new members are unpublished, and **the published `focus 0.6.0` has drifted from local** (M1 added fields with no version bump) — so the version bump at release is what re-trues the ladder (PKG-1/PKG-2).

**Keychain invariant (the D4 cross-OS guard).** Bare `cargo test --workspace` is keychain-safe: every `costroid-connect` test that reaches `keyring` first installs the process-global **mock** (`install_mock_keychain`), and the real `keyring::Entry` (the `CredentialStore`) is only constructed under the `connect`/`connect-test-support` features + the runtime `connect` action — none of which the default workspace test exercises. T3 makes this a **written, enforced invariant** (a non-optional guard), not an incidental fact, so a future test that constructs a real `Entry` without the mock fails loudly rather than touching a CI runner's OS keychain.

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

### D5 — ✅ SIGNED OFF 2026-06-21: honor `SOURCE_DATE_EPOCH` (the bench timestamp source)

`costroid bench` currently stamps each local-run row with `chrono::Utc::now()`
([`apps/cli/src/bench.rs:128`](../apps/cli/src/bench.rs#L128)). A **deterministic** `make demo` (T2) +
benchmark goldens (T8) — and the "byte-identical re-run" deciding tests — require a **fixed clock**.
Because it changes observable CLI behavior it was surfaced for sign-off (CLAUDE.md: "changing the
public CLI surface"). **Decision (human, recommended option):**

- **✅ Honor the de-facto reproducible-builds env var `SOURCE_DATE_EPOCH`** when set (the row
  timestamp = that epoch); otherwise keep `Utc::now()` unchanged. The demo/benchmark scripts export
  `SOURCE_DATE_EPOCH` derived from the profile/manifest `as_of` (never "now"), so committed artifacts
  are byte-stable while the default interactive `bench` is untouched. **No new flag**; one env-var
  read; documented in `--help` + methodology. *(Drives the T2/T8 determinism sub-tasks.)*
- *(Not chosen: a `--as-of` flag — adds permanent CLI surface; or normalize-in-comparison-only —
  weaker determinism, goldens can't assert the stamped row verbatim.)*

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
- **Deciding test:** a Rust test (in `costroid-core` or an `apps/server`/`apps/cli` integration test) loads each sample and asserts it parses + round-trips to **FOCUS 1.3 (schema-valid)**; a **dedicated `samples/` conformance leg** is added to `scripts/focus_conformance.sh` (not folded into the merged-ledger leg) with a **row-count guard** (the leg fails if the export is empty/short, so it can't pass vacuously). The **INVERSE honesty guard** (shared with T8): every local-lane row in every committed sample asserts `x_MeasurementMode == "estimated"` (`MeasurementMode::Estimated`) — no committed artifact may claim a measured number pre-M3b. Offline, no hardware. **→ DoD-7, DoD-3 (regression), DoD-8 (R4 metadata-only).**

### T2 — `make demo` / one-command deterministic path (§6.9)
- **Do:** a top-level `Makefile` (Linux + macOS — `make` is POSIX) chaining **import → bench (estimated) → breakeven/compare → export FOCUS** over the T1 samples, `--features power`, **fully offline, no hardware**, deterministic output. **Windows:** no `make` — the README documents the **raw `cargo run` equivalents** (the same ordered commands) so the demo path exists on every OS. Determinism comes from **D5** (the scripts export `SOURCE_DATE_EPOCH` from the sample/profile `as_of`, never "now"), so the exported FOCUS artifact is **byte-identical across re-runs**.
- **Deciding test:** an `offline-acceptance` leg (Linux) runs `make demo` end-to-end **CLI-only under the `assert_no_inet` harness** and asserts **exit 0 + a byte-identical FOCUS-1.3 export on a second run**; a **macOS CI smoke leg** runs the same `make demo` (D4 already provisions macOS). The CLI legs emit **no socket** (the static `--test offline` + strace harness stay green). **→ DoD-7, DoD-10 (regression).**

### T3 — CI: cross-OS test *execution* (§6.6)
- **Do:** promote the `cross-platform` job from `cargo build` to **`cargo test`** per D4: `cargo test --workspace`, `cargo test -p costroid --features power`, `cargo test -p costroid-server`, `cargo test -p costroid --test offline` on macOS + Windows. Add a **non-optional keychain guard** (not "if needed"): a test/assertion + a **written mock invariant** (a doc-comment in `costroid-connect::test_support` that any keyring-touching test MUST `install_mock_keychain` first) so a future real-`Entry` test fails loudly instead of hitting a runner's OS keychain. **Keep Linux-only:** strace offline-acceptance, FOCUS conformance, cargo-deny, MSRV (unchanged).
- **Deciding test:** the CI matrix is **green with test execution on all three OSes**; the Linux-only gates are byte-unchanged; no keychain prompt/hang on mac/win; the keychain guard is present + the invariant documented. **→ DoD-2, DoD-1 (regression).**

### T4 — README rewrite (§6.8)
- **Do:** one-paragraph problem statement; a **hero-GIF placeholder** (stamped "capture pending M3b", D2); a **one-command quickstart** (`cargo install costroid` / `make demo`); a **"what this does that ccusage doesn't"** table (FOCUS-native, 3-lane ledger, local-inference economics, break-even, loopback web UI, reconciliation, zero-network default — feature contrasts only, **competitor star counts OMITTED** as drift-prone/unverifiable offline); a **Mermaid** architecture diagram of the crate graph + the three lanes + the loopback server.
- **Deciding test:** the **docs-presence test** (T-shared, see T5) asserts the README contains each required section + a fenced `mermaid` block; `scripts/check_doc_stamps.sh` asserts **no un-stamped real/hero number** (every figure carries the canonical stamp constant); the Mermaid block parses. **→ DoD-7.** *(DoD-9 does NOT govern the Mermaid/GIF — accessibility applies to runtime visuals, not doc images.)*

### T5 — Methodology page (§6.8)
- **Do:** `docs/methodology.md` — exactly how energy/token is derived: **measured vs estimated** (`x_MeasurementMode` ladder), **package-power vs wall** (the ~20–40% caveat), the **energy-only `e` over total (in+out) tokens** basis (the M5 lock), and the break-even math (calendar-fixed amortization, the sensitivity band, the "never"/infeasible case). Cross-link limitations + ARCHITECTURE.
- **Named deciding tests** (the doc-test machinery, shared by T4/T6/T8):
  - **`docs_presence` test** (a `#[test]` in `apps/cli` or a small `xtask`-style test) — asserts each required doc + each required section/anchor exists (README sections, the `mermaid` fence, `methodology.md`, `limitations.md` headings).
  - **`scripts/check_doc_stamps.sh`** — scans the docs for figure patterns and fails on any un-stamped number, checked against **one canonical stamp constant** (a single `PENDING_M3B_STAMP` string defined once and reused, so the stamp text can't drift between docs and tests).
  - **e-formula real cross-check** — a `#[test]` computes `local_energy_only_rate` on a fixture and asserts the **numeric result** matches the worked example printed in `methodology.md` (a real value comparison, not a prose "matches the code" claim); a second assert ties the cited default electricity rate to `hardware.v1.json`. **→ DoD-7, DoD-5 (regression).**

### T6 — `docs/limitations.md` consolidation (§6.8)
- **Do:** extend (don't replace) the M3-era file with **M4** (break-even ranges; the "never" outcome; one-lifetime rule), **M5** (interface caveats: text/table break-even web view; loopback-only), and an explicit **uncertain-row annotation** cross-ref (`x_AttributionConfidence`, sub-agent undercount, package-vs-wall) showing where the UI surfaces it.
- **Deciding test:** the `docs_presence` test asserts limitations.md covers sub-agent undercount + package-vs-wall + the uncertain-row annotation, and each claim maps to a real column/behavior in code (`x_AttributionConfidence`, `x_MeasurementMode`). **→ DoD-7.** *(DoD-9 — never color-alone — is satisfied by the M1–M5 runtime visuals and regression-guarded at T11; documenting the cue here is a DoD-7 doc artifact, not a DoD-9 claim.)*

### T7 — `ARCHITECTURE.md` reconcile (§6.8, DoD-12)
- **Do:** extend the existing doc with the M6 close: the final 10-member crate graph, the three-lane ledger, the loopback server data path (§10 — verify against `apps/server`), the offline model (per-binary allowlists; CLI byte-for-byte; server loopback-only), the datasets/demo/packaging additions. **Reconcile, never rewrite.**
- **Deciding test:** ARCHITECTURE describes the final scope with **no contradiction against the code** (crate graph, lanes, server path, offline guarantees); a reviewer diff confirms additive edits. **→ DoD-12.**

### T8 — Benchmark dataset + writeup (§6.10) *(D1)*
- **Do:** a **versioned manifest** (`benchmarks/<id>/manifest.v1.json` mirroring the `gemma4.v1.json` schema style — `schema`/`as_of`/`source`/per-run records) that **records `measurement_mode` per run** (= `"estimated"` now; the field the post-M3b refresh flips to `"measured_wallmeter"`) + **raw synthetic outputs** + a **blog-ready methodology writeup** (`docs/benchmark-gemma4-vs-cloud.md`): "what Gemma 4 31B Dense (Apache-2.0) actually costs locally on a 128 GB APU vs Bedrock/Anthropic", full reproduction details. **Every figure stamped with the canonical `PENDING_M3B_STAMP`** (R8/R10). A new **`scripts/check_benchmarks.sh` rooted at `benchmarks/`** (a sibling of `check_power_profiles.sh`) sha256-verifies every manifest + raw output and is wired into the conformance CI job.
- **`docs/POST-M3B-REFRESH.md` = a CLOSED file-by-file checklist** — for each artifact the human refreshes after the wall-meter run: the file path, the figures/fields to replace, the **`.sha256` regen**, the **`as_of` bump**, the **`measurement_mode` flip**, and the **integrity re-pass** (`check_benchmarks.sh`). A **drift-guard test** asserts the checklist enumerates exactly the set of committed benchmark artifacts (so a new artifact added later can't silently escape the refresh list).
- **Deciding test:** `check_benchmarks.sh` passes (every manifest/output matches its committed `.sha256`); `check_doc_stamps.sh` finds **no un-stamped hero number** in the writeup; the **inverse guard** (shared with T1) asserts every manifest run + every committed sample row carries `measurement_mode`/`x_MeasurementMode == "estimated"`; the reproduction steps run via `make demo`; the POST-M3B drift-guard test passes. **→ DoD-7.**

### T9 — Packaging: flip dist/publish on the new crates (§6.9) *(D3)*
- **Do:** per D3 — `publish = true` on `costroid-power` + `costroid-store` (crates.io libraries; bundled assets — power's `profiles/`+`models/` JSON — included via `cargo package`); on `costroid-server` set `publish = true` **and** `[package.metadata.dist]` to **mirror `apps/bar`**: `dist = true`, **`installers = []`** (archives only — no npm/Homebrew/musl), and the **explicit 5 targets** (Linux gnu x86_64/aarch64, macOS x86_64/aarch64, Windows x86_64 — *no* `x86_64-unknown-linux-musl`, since the server need not static-link). Workspace path-deps gain `version` (publishable). Keep `precise-builds = true` (CLI stays connect-OFF on release runners; the server has no libdbus/GTK deps → builds clean on all targets).
- **RELEASING.md updates:** the new ladder `focus → providers → core → config → connect → store → power → costroid → server → bar`; the server archive row in "what ships"; **PKG-2** — publish happens **only at the bumped workspace version** (the bump re-trues the drifted `focus`); **PKG-3** — a note to **reserve the three crate names** (`costroid-power`/`-store`/`-server`) on crates.io early (a placeholder publish or name hold) so the ladder isn't blocked at release; a **one-line defer** for SBOM + OS code-signing (out of scope this milestone, tracked in SECURITY.md).
- **Deciding test (PKG-1 — runnable pre-publish gate):** `cargo package --workspace` + `cargo publish --dry-run --workspace` succeed (the **workspace** forms — a per-package `--dry-run -p <crate>` is *not* runnable pre-publish: unpublished siblings + the drifted published `focus 0.6.0`). `cargo package -p <crate> --list` is used **only** to assert each new crate's bundled assets are present. `dist plan` / `dist generate --check` show the `costroid-server` **archives for the 5 targets** and **assert no npm/Homebrew/musl artifact** is generated for it. A **ladder topo-sort assertion** (a small test) verifies the RELEASING ladder is a valid topological order of the actual `Cargo.toml` dep graph (so the documented order can't drift from the code). The offline/forbidden-crates gates stay green (server loopback-only). Real per-package `cargo publish` stays the **human ladder at the bumped version**. **→ DoD-2, DoD-7.**

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
| 9 — accessibility: `--plain` + never color-alone on every new visual | M1–M5 (runtime visuals) | regression T11 — *does NOT govern the doc Mermaid/GIF* |
| 10 — offline intact: CLI byte-for-byte; server loopback-only; offline+strace green | M0–M5 | regression **T2, T3, T9, T11** |
| 11 — each milestone closed against a written deciding test | M1–M5 | **T0, T11 (M6's)** |
| 12 — ARCHITECTURE reconciled with the new scope | — | **T7** |

---

## Hazards to watch (carry into the per-task dev loop)

1. **Cross-OS keychain (T3):** `cargo test --workspace` is keychain-safe today (mock-backed + feature-gated), but T3 adds a **non-optional guard + a written mock invariant** so a future real-`Entry` test can't silently reach a runner's OS keychain — defense-in-depth, not a fix for a present bug.
2. **Publish-ability (T9):** workspace path-deps need a `version` to publish; bundled assets must live under the crate dir (`cargo package -p <crate> --list` is the assets proof; `cargo package --workspace` is the runnable pre-publish gate). `precise-builds` must keep the release runners libdbus-free. The published `focus 0.6.0` has drifted from local — only the version bump re-trues the ladder.
3. **Determinism (T2/T8, D5):** committed demo/benchmark artifacts must be byte-stable. `bench` stamps `Utc::now()` today (D5) — the demo/benchmark scripts pin the clock via `SOURCE_DATE_EPOCH` (from the dated `as_of`, never "now"); the deciding tests assert a byte-identical re-run, so a stray timestamp/RNG fails loudly rather than flaking.
4. **R8/R10 honesty (T1/T4/T5/T8):** every estimated/synthetic figure carries the canonical `PENDING_M3B_STAMP`. The enforcement is **new M6 machinery** — `check_doc_stamps.sh` (no un-stamped hero number in docs) + the inverse `x_MeasurementMode == "estimated"` guard on every committed row — applying the **same honesty principle** M3a established for power (M3a's CI asserts *no real power number* via synthetic fixtures; it does not scan docs — that is this milestone's addition).
5. **Additive docs (T6/T7):** extend `limitations.md` / `ARCHITECTURE.md`, never replace (the code wins; don't introduce a doc that drifts from it).

---

## Pre-PR gate (every task + the milestone close)

```bash
cargo fmt --all -- --check \
 && cargo clippy --workspace --all-targets -- -D warnings \
 && cargo test --workspace
cargo clippy -p costroid --features power --all-targets -- -D warnings && cargo test -p costroid --features power
cargo test -p costroid-power --features power && cargo test -p costroid-store --features store && cargo test -p costroid-server
cargo deny check licenses bans && cargo deny --all-features check licenses bans
cargo +1.88.0 check --workspace --all-targets --exclude costroid-bar
bash scripts/check_power_profiles.sh && bash scripts/check_pricing_snapshots.sh && bash scripts/check_benchmarks.sh
bash scripts/offline_acceptance.sh </dev/null   # includes the CLI-only `make demo` leg (byte-identical re-run)
```
Cross-OS test execution is the **CI** arbiter (T3) — "green in WSL is not green" (§2.3).

---

## §6.12 Definition of Done — CLOSE-OUT (T11, 2026-06-22)

M6 built T0–T11 on the per-task dev loop (fresh-context build → independent adversarial review →
fold-in → commit), `costroid-next` off `main` @ `631b5a4`. **Full gate re-run on the integrated tree
— all green** (the M6 deciding test): `fmt` · `clippy --workspace --all-targets -D warnings` ·
`test --workspace` (0 failures) · power feature clippy+test · `costroid-store`/`costroid-server`/
`connect-test-support` · `cargo deny` default **and** `--all-features` · MSRV 1.88 (workspace +
power) · `check_power_profiles.sh` · `check_pricing_snapshots.sh` · `check_benchmarks.sh` ·
`check_doc_stamps.sh` · FOCUS conformance (10 "conformance holds", exit 0, incl. the samples 3-lane
merged leg) · `make demo` + `make demo-verify` (byte-identical) · `dist generate --check` (clean;
`costroid-server` = 5 archives, no musl/npm/Homebrew) · `offline_acceptance.sh </dev/null` (CLI/bar
no socket, server loopback-only no egress). **Each box closed against a named test/artifact, never
prose:**

| # | §6.12 box | Status | Evidence |
|---|---|---|---|
| 1 | New crates integrated; existing behavior intact | ✅ | 10-member workspace builds/tests green; the pre-M6 CLI/TUI/bar suites still pass (e.g. 211 + 178 + … rows, 0 failures); the byte-for-byte no-network CLI is unchanged. |
| 2 | Cross-platform build + distribution green; power/cfg gates | ✅ | **T3** promoted the macOS+Windows CI job to test execution; **T9** flipped `dist`/`publish` (server = 5 archives no musl; power/store = crates.io libs); `dist generate --check` clean; the `power` feature gates compile/lint/test clean in both states. |
| 3 | Unified 3-lane FOCUS ledger; v1.2-in/v1.3-out; CI-validated | ✅ | FOCUS conformance green incl. the dedicated **samples 3-lane merged leg** (`developer_tool`+`cloud_api`+`local_inference`, 20 rows) with row-count guards (T1). |
| 4 | Four-source PowerSampler; measured cross-OS via wall meter; mode stamped | ✅ (M3a) | Documented in **methodology.md** (the ladder + package-vs-wall) + **T8**; estimated mode is the default; `x_MeasurementMode` stamped per row; the **inverse guard** asserts every committed row is `"estimated"`. Real captured numbers are the **post-M3b** human refresh (`POST-M3B-REFRESH.md`) — does not block M6. |
| 5 | Break-even + scenario modeling; honest ranges | ✅ (M4) | **methodology.md** §4 (e-formula, sensitivity band, Never/Infeasible) pinned by `methodology_crosscheck.rs`; the **benchmark writeup** presents ranges + a break-even volume, never a hero number. |
| 6 | CLI/TUI + API + 3-view web UI (local-only) | ✅ (M5) | `costroid-server` loopback-only proven by `offline_acceptance.sh` (no egress) + the per-binary `SERVER_ALLOWED` allowlist; the power-gated TUI overlay (`b`). |
| 7 | Full tests, CI gates, datasets, docs, demo, packaging, benchmark | ✅ | **T1** samples + conformance leg; **T2** `make demo`; **T4–T7** README/methodology/limitations/ARCHITECTURE + doc-test machinery; **T8** versioned benchmark dataset + writeup + `check_benchmarks.sh`; **T9** packaging; **T10** issue templates + CHANGELOG. |
| 8 | Cardinal Rule (R4): no prompt/response content anywhere | ✅ | Samples are metadata-only (the R4 field-name scan + inverse measurement-mode guard); the bug-report form has a required "no prompt content / secrets / real logs" check; the store remains structurally metadata-only. |
| 9 | Accessibility: `--plain` + never color-alone | ✅ (regression) | M6 adds no new **runtime** visual; the M1–M5 `--plain`/never-color-alone visuals + their snapshot tests still pass (DoD-9 does not govern the doc Mermaid/GIF). limitations.md documents the non-color uncertain-row cue. |
| 10 | Offline intact: CLI byte-for-byte; server loopback-only; offline+strace green | ✅ | `cargo test -p costroid --test offline` (9 passed) + `offline_acceptance.sh` PASSED; the `make demo` leg runs CLI-only under the no-inet assertion. |
| 11 | Each milestone closed against a written deciding test (M6 = this checklist) | ✅ | **This close-out** (the §6.12 checklist, each box evidenced). |
| 12 | ARCHITECTURE reconciled with the new scope | ✅ (T7) | **ARCHITECTURE §10.1** additively reconciles the final 10-member graph, the three lanes, the loopback server path, the offline model, and the M6 additions. |

**Per-task review ledger (all APPROVE / APPROVE-WITH-FIXES, every fold-in landed + re-verified):**
T1 (L1 sidecar gate) · T2 (HIGH Windows-demo hermetic + LOW UTF-8) · T3 (APPROVE, no fixes) ·
T4–T7 (MEDIUM: two optional crate edges) · T8 (MEDIUM wall_seconds basis + 2 LOW wording) ·
T9 (LOW stale Cargo.toml comments) · T10 (LOW server `--version`). M6 commit range: `f71e31e..HEAD`.

**⛔ Milestone boundary:** M6 is BUILT + self-verified green; **awaiting the human's final fresh-eyes
boundary review before the PR / merge to `main`.** Do not merge from the agent.
