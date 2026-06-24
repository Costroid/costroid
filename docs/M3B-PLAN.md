# M3B-PLAN — provenance-anchored per-model partial measurement (Phase 1: code/docs only)

> **Status:** PLAN — awaiting the coordinator's pre-coding plan-review. **No code is written until
> this is approved.** Phase 1 (this plan) is **code + docs only**; the agent does **not** run
> hardware. Phase 2 (the human runs the wall meter, commits the measured 31b data, flips the
> allowlist) is **documented here, not done here**.

## Scope & guardrails

M3b is a **MEASUREMENT-ONLY** refresh: it changes **no** FOCUS schema, **no** break-even math, **no**
CLI surface. It replaces *estimated values* with *measured* ones for **one** model
(`gemma-4-31b-dense`), measured in WSL2 on the Strix Halo (gfx1151) via GPU-accelerated **llama.cpp**
+ a display-only wall meter.

The job of Phase 1 is to make the post-M3b refresh support a **per-model PARTIAL** flip:

- the **full gate stays green NOW** — all data estimated, **zero data change**; and
- it **stays green after** the human's later 31b-only flip — **provenance-anchored**, not merely
  internally consistent.

Golden rules held throughout: no network; no schema/CLI/math change; R8/R10 honesty stamps; **no
fabrication**; fix the now-stale "strictly estimated" doc-comments.

## Decisions (defaults — recommended)

- **D1 — Authoritative allowlist.** Gate "is this benchmark row legitimately measured?" on a single
  committed declaration `MEASURED_MODELS` in `crates/costroid-power`, **empty today**. Bare
  cross-consistency (run-mode == raw-mode == coupled `x_Estimated`) is **too weak**: a *fabricated*
  measured row is internally consistent and would pass green. The allowlist is the human attestation
  the guard keys off. **Chosen.**
- **D2 — Directory-agnostic guard.** Iterate **all** `benchmarks/**/manifest.v1.json`; do not
  hardcode `gemma4-vs-cloud-2026-06`. Whether Phase 2 flips **in place** or into a **new dated dir**
  is deferred to Phase 2 (the guard works either way). **Chosen.**
- **D3 — Bundled profile + estimate stay estimated.** Do **not** re-true
  `crates/costroid-power/models/gemma4.v1.json` or `profiles/hardware.v1.json`. The bundled estimate
  remains the default → the demo/samples stay byte-stable and the shared profile stays estimated.
  Re-truing the shared profile is **out of scope**. **Chosen.**
- **D4 — Demo samples stay estimated.** `samples/benchmark/**` ships hardware-free and stays
  `estimated` by design; `samples_datasets.rs`'s "strictly estimated" guard remains **correct** and
  is **not touched**. **Chosen.**

---

## T0 — audit (current state)

### What exists

| Surface | State today |
|---|---|
| [crates/costroid-power/src/mode.rs](../crates/costroid-power/src/mode.rs) | `MeasurementMode` { `MeasuredWallmeter`, `MeasuredSysfs`, `MeasuredLhm`, `Estimated` }; `as_focus_str()` → the wire strings; `is_measured()` = `!Estimated`. |
| [crates/costroid-power/src/lib.rs](../crates/costroid-power/src/lib.rs) | `pub use mode::MeasurementMode;` — no allowlist yet. |
| [benchmarks/gemma4-vs-cloud-2026-06/manifest.v1.json](../benchmarks/gemma4-vs-cloud-2026-06/manifest.v1.json) | 3 runs (31b/26b/12b), each `measurement_mode: "estimated"`, with `raw_output`, `model`. `runtime_kind: "ollama"`, `quant: "Q4_K_M"`. |
| `benchmarks/…/raw/<model>.bench.json` | One FOCUS-1.3 row each: `x_Model`, `x_MeasurementMode: "estimated"`, `x_Estimated: true`, `x_RuntimeKind: "ollama"`, `x_BenchmarkId: "gemma4-local-v1/<model>/Q4_K_M/ollama"`. |
| [apps/cli/tests/post_m3b_refresh.rs](../apps/cli/tests/post_m3b_refresh.rs) | 3 tests: (1) closed-checklist doc↔`benchmarks/**`; (2) every manifest run `== "estimated"`; (3) every raw row `x_MeasurementMode == "estimated"`. Tests (2)+(3) are the **bare** guards to refactor. |
| [apps/cli/tests/samples_datasets.rs](../apps/cli/tests/samples_datasets.rs) | DO NOT TOUCH. Samples stay estimated (D4); already pins the `"estimated"` literal to `MeasurementMode::Estimated.as_focus_str()` under `#[cfg(feature = "power")]`. |
| [docs/POST-M3B-REFRESH.md](POST-M3B-REFRESH.md) | The closed runbook. Step 1 flips the **bundled** data (full re-true); Step 2 flips **all** runs; the ⚠️ note tells the human to "update those guards." Needs the partial-flip rewrite. |
| [docs/benchmark-gemma4-vs-cloud.md](benchmark-gemma4-vs-cloud.md) | The writeup. Hero tables list all 3 models; one global "estimated — pending M3b" stamp. Needs **per-row** mode labels post-flip (31b measured; 12b/26b still pending). |
| [scripts/check_doc_stamps.sh](../scripts/check_doc_stamps.sh) | **Presence-only**: if a doc shows any hero metric it must contain the canonical stamp *once*. It **cannot** police a *mixed* doc (it can't tell which row is measured vs pending). |
| [scripts/check_benchmarks.sh](../scripts/check_benchmarks.sh) | `sha256sum -c` over every `benchmarks/**/*.json`. Editing any committed manifest/raw now would break its sidecar → must NOT pre-edit benchmark JSON in Phase 1. |
| [.github/workflows/ci.yml](../.github/workflows/ci.yml) | Runs both `cargo test --workspace` (default) **and** `cargo test -p costroid --features power` → the `cfg(power)` tie IS exercised. |

### Constraints / landmines discovered

1. **The allowlist const is not linkable from the default test build.** `apps/cli` links
   `costroid-power` only behind `power` ([apps/cli/Cargo.toml:46](../apps/cli/Cargo.toml#L46)). The
   guard must run in the **default** `cargo test --workspace` to keep the gate green now, so it
   operates on **string** modes read from JSON and consults a **string mirror** of the allowlist. A
   `#[cfg(feature = "power")]` tie pins the mirror to the authoritative const (CI runs the power job,
   so the tie is enforced). This is the **same necessary pattern** `samples_datasets.rs` already uses
   — not a second source of truth, a test-local shadow with an anti-drift pin.
2. **The closed-checklist drift-guard constrains the runbook rewrite.** The runbook must still
   reference **all four** artifact paths verbatim and introduce **no** phantom `benchmarks/…json`
   token. The partial-flip rewrite keeps every path listed (31b flips; 12b/26b stay estimated but
   remain enumerated).
3. **No pre-editing of committed benchmark JSON.** Today everything is honestly estimated; editing
   `manifest.v1.json` / raw rows / `benchmarks/README.md` now would either break a `.sha256` or claim
   a measured number pre-run. All of that is **Phase-2 runbook content**, not a Phase-1 edit.
4. **`samples_datasets.rs` "strictly estimated" is NOT stale** (D4) → untouched. Only the
   benchmarks-guard comments in `post_m3b_refresh.rs` go stale and get fixed.

---

## Design 1 — the authoritative allowlist (D1)

**Location:** new module `crates/costroid-power/src/measured.rs`, re-exported from `lib.rs`. (Lives
in `costroid-power` because it owns `MeasurementMode` and the measurement semantics — per the task.)

```rust
// crates/costroid-power/src/measured.rs
use crate::mode::MeasurementMode;

/// The AUTHORITATIVE, human-gated allowlist of model ids whose committed `benchmarks/**` dataset
/// carries a REAL measured run — paired with the exact mode that run used. EMPTY until the M3b
/// wall-meter run lands.
///
/// Adding an entry is a human ATTESTATION (R10) that this model's shipped benchmark *manifest run*
/// AND *raw FOCUS row* are real captured numbers, not estimates. The post-M3b drift-guard
/// (`apps/cli/tests/post_m3b_refresh.rs`) keys off THIS list:
///   * a benchmark row claiming a measured mode whose model is absent here FAILS the build
///     (a fabricated or half-flipped measured claim cannot ship); and
///   * a model present here MUST carry exactly this mode in BOTH its manifest run and its raw
///     row, with `x_Estimated` cleared.
pub const MEASURED_MODELS: &[(&str, MeasurementMode)] = &[
    // Phase 2 (human, after the wall-meter run):
    //   ("gemma-4-31b-dense", MeasurementMode::MeasuredWallmeter),
];

/// The declared measured mode for `model_id`, if it is on the authoritative allowlist.
pub fn measured_mode_for(model_id: &str) -> Option<MeasurementMode> {
    MEASURED_MODELS
        .iter()
        .find(|(id, _)| *id == model_id)
        .map(|(_, mode)| *mode)
}
```

`lib.rs`: `pub use measured::{measured_mode_for, MEASURED_MODELS};`

### Why provenance-anchored, not cross-consistent — the threat model

`expected(model)` = the allowlisted mode string if the model is on the allowlist, else `"estimated"`.

| Tampering attempt | Outcome |
|---|---|
| Fabricate a measured row, **don't** touch `MEASURED_MODELS` (and don't touch the mirror) | **Default build FAILS** — mirror empty → `expected = "estimated"` → run/raw mode mismatch. |
| Fabricate a measured row, add the model only to the **test mirror** (not the const) | **Power CI job FAILS** — the `cfg(power)` tie asserts `mirror == MEASURED_MODELS`. |
| Fabricate a measured row, add the model only to the **const** (not the mirror) | **Default build FAILS** — mirror still empty → estimated expected. |
| Half-flip: manifest run measured, raw row estimated (or vice versa) | **FAILS** — JOIN asserts raw mode == run mode. |
| `x_Estimated` left `true` on a measured row | **FAILS** — coupling `x_Estimated == (x_MeasurementMode == "estimated")`. |
| Measured manifest run with a **missing/empty/unreadable raw** (claim with no backing data) | **FAILS** — fail-closed JOIN + per-run measured-backing non-vacuity. |
| Add the model to **both** const + mirror **and** ship real measured data | **PASSES** — this is exactly the legitimate Phase-2 attestation. |

The only way to ship a measured benchmark row is for a human to edit the authoritative
`MEASURED_MODELS` (mirrored, tied under `power`, CI-enforced) **and** have the data match it. That is
the provenance anchor.

> **CI dependency (must hold):** the mirror↔const tie is enforced **only** by the
> `cargo test -p costroid --features power` job. That job must remain a **required** status check on
> `main` — if it is dropped, a mirror that drifts from `MEASURED_MODELS` would no longer fail. Flag
> this in the PR (branch-protection lives outside the repo, so it is a review/ops item, not a code
> change).

### The mirror + the tie (in `post_m3b_refresh.rs`)

```rust
/// Default-build mirror of `costroid_power::MEASURED_MODELS` — the crate const can't be linked
/// without the `power` feature, and this guard must run in `cargo test --workspace`. EMPTY today.
/// The `#[cfg(feature = "power")]` tie below pins it to the authoritative const; CI runs
/// `cargo test -p costroid --features power`, so a drift between the two FAILS there.
const MEASURED_MODELS_MIRROR: &[(&str, &str)] = &[
    // ("gemma-4-31b-dense", "measured_wallmeter"),  // Phase 2 — mirror of the crate const
];

#[cfg(feature = "power")]
#[test]
fn mirror_equals_the_authoritative_measured_models() {
    use costroid_power::{measured_mode_for, MEASURED_MODELS};
    assert_eq!(MEASURED_MODELS_MIRROR.len(), MEASURED_MODELS.len(), "mirror length drifted");
    for (id, mode_str) in MEASURED_MODELS_MIRROR {
        let Some(mode) = measured_mode_for(id) else {
            panic!("mirror lists `{id}` but the authoritative MEASURED_MODELS does not");
        };
        assert_eq!(mode.as_focus_str(), *mode_str, "mirror mode for `{id}` drifted from the const");
    }
}
```

---

## Design 2 — the refactored guard (replaces tests 2 + 3)

A single root-agnostic helper, reused by the real test **and** the fixture test:

```rust
struct Stats { manifests: usize, runs: usize, rows: usize }

/// Validate one benchmark tree against `allowlist` (model_id → focus-mode string). Returns
/// Err(message) on the first violation (so the fixture test can assert failure without a panic).
/// Iterates ALL `**/manifest.v1.json` under `bench_root`.
fn check_benchmark_provenance(bench_root: &Path, allowlist: &[(&str, &str)]) -> Result<Stats, String>
```

`expected(model)` = `allowlist`'s mode for `model`, else `"estimated"`.

The rules it enforces. **Per manifest** (Err if `runs[]` is missing/empty — per-file non-vacuity),
for **each run**:

1. **Run rule (provenance).** `run.measurement_mode == expected(run.model)`.
   *(model not allowlisted ⇒ must be estimated; allowlisted ⇒ must be exactly its declared mode.)*
2. **JOIN rule — fail-closed (R1 fold-in).** `run.raw_output` must be present + non-empty; resolve
   `raw = manifest_dir.join(run.raw_output)`; it **must exist, read, and parse** (a missing/unreadable
   /malformed raw is an Err, never a skip) with a **non-empty** `rows[]` (per-file non-vacuity). For
   **each** row: `row.x_Model == run.model` **and** `row.x_MeasurementMode == run.measurement_mode`.
3. **Measured-backing — per-run non-vacuity (R1 fold-in).** If `expected(run.model) != "estimated"`
   (a measured run), the joined raw must have **≥1 row** with `x_MeasurementMode == expected(run.model)`
   **and** `x_Model == run.model`. *(A measured manifest run MUST have a real backing measured raw row
   — a measured run with an empty/missing raw FAILS here.)*

**Raw sweep** (every `**/*.bench.json` under the root — catches orphan raws too; Err if a raw's
`rows[]` is empty — per-file non-vacuity), for **each row**:

4. **Raw provenance.** `row.x_MeasurementMode == expected(row.x_Model)`.
5. **Coupling — keyed off the row's OWN mode (R3 fold-in).**
   `row.x_Estimated == (row.x_MeasurementMode == "estimated")`. *(An internal-consistency check on the
   row, independent of the allowlist; the allowlist match is rule 4, kept separate.)*
6. **Measured-needs-allowlist (explicit).** If `row.x_MeasurementMode != "estimated"` then
   `(row.x_Model, row.x_MeasurementMode)` must be in `allowlist`.

**Non-vacuous (two layers, R2 fold-in):** per-file (every manifest `runs[]` and every raw `rows[]`
non-empty, enforced above) **and** aggregate — the real test asserts
`manifests > 0 && runs > 0 && rows > 0` (no silent pass).

The real test:

```rust
#[test]
fn benchmark_dataset_provenance_matches_the_measured_models_allowlist() {
    let bench_root = repo_root().join("benchmarks");
    let stats = match check_benchmark_provenance(&bench_root, MEASURED_MODELS_MIRROR) {
        Ok(s) => s,
        Err(msg) => panic!("committed benchmarks/** violate the provenance guard: {msg}"),
    };
    assert!(stats.manifests > 0 && stats.runs > 0 && stats.rows > 0, "guard ran vacuously");
}
```

With the empty mirror this is **behaviorally identical to today** (every run/row must be estimated)
⇒ green now, **zero data change**. The original [post_m3b_refresh.rs:122-203](../apps/cli/tests/post_m3b_refresh.rs#L122-L203)
tests 2+3 are **replaced** by this (their "strictly estimated" guarantee is the empty-allowlist case
of the new rule). The closed-checklist test (test 1) is **kept unchanged**.

---

## Design 3 — the non-vacuous fixture test (throwaway tree)

A separate `#[test]` that builds synthetic trees under `std::env::temp_dir()` (unique per
`std::process::id()` + a per-case tag) — **NEVER** under the repo `benchmarks/` (so it can't pollute
the real guard or `check_benchmarks.sh`). Build the tree, run `check_benchmark_provenance` into a
`let result`, `remove_dir_all` (best-effort), **then** assert (clean up before asserting). No
`unwrap`/`expect` — `let-else { panic! }` / `match`. No new dependency (std only).

The five deciding cases:

| Case | Setup | Expected |
|---|---|---|
| **PASS — allowlisted + consistent** | allowlist `[("fx-31b","measured_wallmeter")]`; run `measured_wallmeter`, raw `x_MeasurementMode=measured_wallmeter`, `x_Estimated=false`; plus an estimated 2nd model. | `Ok` |
| **FAIL — undeclared measured** | allowlist `[]`; a run/raw claims `measured_wallmeter`. | `Err` (run rule / measured-needs-allowlist) |
| **FAIL — half-flip** | allowlist `[("fx-31b","measured_wallmeter")]`; run `measured_wallmeter` but raw row `x_MeasurementMode="estimated"`. | `Err` (JOIN rule) |
| **FAIL — `x_Estimated` vs mode mismatch** | allowlist `[("fx-31b","measured_wallmeter")]`; run + raw `measured_wallmeter` but raw `x_Estimated=true`. | `Err` (coupling rule) |
| **FAIL — measured run, empty/missing raw** (R1) | allowlist `[("fx-31b","measured_wallmeter")]`; run `measured_wallmeter` but its `raw_output` file is missing (and a variant with `rows:[]`). | `Err` (fail-closed JOIN / measured-backing non-vacuity) |

**Mode-set tie (`cfg(power)`).** The fixture's measured literal is tied to the enum:

```rust
#[cfg(feature = "power")]
assert_eq!("measured_wallmeter", costroid_power::MeasurementMode::MeasuredWallmeter.as_focus_str());
```

This satisfies "tie the mode set to `MeasurementMode::as_focus_str()` under `cfg(power)`": the strings
the guard treats as "measured" are exactly the enum's wire forms.

---

## Ordered tasks (each with its deciding test)

> Per the dev loop: each task is a card → fresh-context build → independent adversarial review → fix
> → ⛔ present → commit on approval. Builder context never judges its own work.

- **T1 — Allowlist const + helper.** Add `measured.rs` (`MEASURED_MODELS` empty + `measured_mode_for`);
  re-export from `lib.rs`. *Deciding:* `cargo test -p costroid-power --features power` green; a unit
  test in `measured.rs` asserts `MEASURED_MODELS.is_empty()` today and `measured_mode_for` returns
  the paired mode for a synthetic in-test list (no fabrication of bundled data).
- **T2 — Refactor the benchmarks guard.** Replace tests 2+3 in `post_m3b_refresh.rs` with
  `check_benchmark_provenance` + the real test + the mirror + the `cfg(power)` tie; fix the stale
  "strictly estimated" module/test doc-comments to describe the estimated-**unless-allowlisted**
  model. *Deciding:* `cargo test --workspace` green (empty mirror ⇒ identical to today, zero data
  change); `cargo test -p costroid --features power` green (the tie runs).
- **T3 — Fixture test (throwaway tree).** Add the four-case non-vacuous fixture test + the mode-set
  tie. *Deciding:* the PASS case is `Ok`, all three FAIL cases are `Err`; the test creates nothing
  under repo `benchmarks/` (assert the temp path is outside `repo_root()/benchmarks`).
- **T4 — Runbook rewrite (POST-M3B-REFRESH.md).** Partial-flip (31b-only): demote Step-1 bundled
  re-true to an *optional full-re-true* note; add the omitted surfaces (writeup per-row labels,
  manifest `note`/`source`, `benchmarks/README.md`, the `check_doc_stamps.sh` presence-only caveat);
  replace the ⚠️ "update those guards" note with the allowlist flip step; add the **Step-0
  protocol**. *Deciding:* the closed-checklist test
  (`post_m3b_refresh_enumerates_exactly_the_committed_benchmark_artifacts`) stays green — all four
  artifact paths verbatim, no phantom `benchmarks/…json` token; `bash scripts/check_doc_stamps.sh`
  stays green.
- **T5 — Stale-comment sweep.** Grep for "strictly estimated" / "every run … estimated" / "pending
  M3b" in code comments that pertain to **benchmarks** (not samples, not bundled) and reconcile to
  the allowlist model. *Deciding:* `cargo clippy --workspace --all-targets -- -D warnings` clean;
  manual grep shows no benchmarks-guard comment still claims unconditional "estimated".
- **T6 — Full gate + boundary review → PR #8.** *Deciding:* the verification gate below, then the
  milestone boundary review.

---

## POST-M3B-REFRESH.md revision spec (T4 detail)

**Partial-flip framing (31b only).**

- **Step 0 — protocol (NEW, replaces the thin capture step).** Add an explicit measurement protocol:
  - **Warm-then-time, DECODE-ONLY.** Warm the model (load weights + prefill) first; time **only** the
    decode/generation window — the engine's `wall_seconds = tokens_out / tok_s` is decode-dominated,
    so the measured tok/s and watts must come from the decode phase, not load/prefill.
  - **Avg watts from the meter kWh-delta over that SAME window.** Read the wall meter's cumulative
    kWh before/after the decode window; `avg_watts = Δkwh / window_hours`. **≥3 runs**; report the
    average and the spread.
  - **Keep `SOURCE_DATE_EPOCH=1781913600` on the capture command (R4 fold-in).** The measured raw
    row's timestamp must stay byte-deterministic so its regenerated `.sha256` is reproducible (D5);
    the capture command keeps the epoch pin exactly as the estimated regen loop does.
  - **The run is llama.cpp, not the committed `ollama`.** Update the manifest run's `runtime_kind`
    (→ `llama.cpp`) and `quant` if it differs; the regenerated raw row carries `x_RuntimeKind` /
    `x_BenchmarkId` (`…/llama.cpp`) from the runner.
  - **Size `.wslconfig` so 31b-Q4 weights + KV fit the ROCm GTT pool.** 31B-dense `Q4_K_M` weights +
    the KV cache for the 20k-token window must fit the WSL2 GPU GTT allocation; set `.wslconfig`
    memory high enough (record the value) and the gfx1151 ROCm env.
  - **HARD-abort the run unless `rocm-smi` shows the iGPU busy + weights resident and tok/s ≈ 10–13.**
    A CPU fallback (≈2–4 tok/s, no GPU residency) must **abort**, never be recorded as a GPU number.
  - **Wall = total-system draw, idle-inclusive.** The wall meter reads the whole machine; **record
    the idle draw** separately so `load_watts` is understood as total-system-under-load.
- **Step 1 — bundled data.** For a **partial** flip: **SKIP** (D3 — the bundled profile + manifest
  stay estimated; re-truing the shared profile is out of scope). Keep the old full-re-true steps as an
  **optional** "if you later re-base the shared profile" note.
- **Step 2 — the versioned dataset.** Flip **only** `gemma-4-31b-dense`'s manifest run + raw row to
  `measured_wallmeter` (and add 31b to `MEASURED_MODELS` + the test mirror — the **allowlist flip**,
  replacing the old ⚠️ "update those guards" note). Keep 12b/26b **estimated** and still listed (so
  the closed checklist stays closed). Regen only the changed `.sha256`s. Update the manifest header
  `note`/`source` to describe the now-**mixed** dataset (no longer "every run estimated").
- **Step 2b — `benchmarks/README.md`.** Rewrite the "every run carries `measurement_mode ==
  estimated`" line to the mixed reality (31b measured, others pending). *(Not `.sha256`-pinned, so a
  free edit — but it's still a Phase-2 surface, documented here.)*
- **Step 3 — the writeup.** The hero tables are **mixed**: add **per-row** mode labels (31b →
  measured; 12b/26b keep the `estimated — pending M3b measurement` stamp). Add a caveat that
  `check_doc_stamps.sh` is **presence-only** — it cannot police a mixed doc, so the per-row labels are
  the human's responsibility; the canonical stamp must still appear at least once (12b/26b keep it).
- **Step 4 — demo samples.** Unchanged (D4 — stay estimated).
- **Step 5 — integrity re-pass.** Unchanged gate list; note the guards are now the **allowlist** ones.

---

## Out of scope / DO NOT TOUCH

`crates/costroid-power/models/gemma4.v1.json`, `…/profiles/hardware.v1.json`,
`crates/costroid-power/src/models.rs`, `…/src/profile.rs`,
[apps/cli/tests/samples_datasets.rs](../apps/cli/tests/samples_datasets.rs),
`apps/cli/tests/methodology_crosscheck.rs`, `scripts/offline_acceptance.sh`, and **all committed
`benchmarks/**` JSON** (manifest + raw + sidecars). The measured datapoint lives **only** in
`benchmarks/**` (Phase 2) + the writeup; the bundled estimate stays the default.

## Phase 2 — human handoff (documented, NOT done here)

1. Run the wall-meter measurement per the **Step-0 protocol** (≥3 decode-only runs, GPU-verified).
2. Regenerate `gemma-4-31b-dense`'s raw bench row (measured) + flip its manifest run; regen `.sha256`.
3. Add `("gemma-4-31b-dense", MeasurementMode::MeasuredWallmeter)` to `MEASURED_MODELS` **and** the
   `MEASURED_MODELS_MIRROR` (the `cfg(power)` tie + the default guard then both expect 31b measured).
4. Update the writeup per-row labels, the manifest `note`/`source`, `benchmarks/README.md`.
5. Run the verification gate — green = the partial refresh is complete and the stamps tell the truth.

## Verification gate (Phase 1 done-when)

```bash
cargo fmt --all -- --check
cargo clippy --workspace --all-targets -- -D warnings
cargo test --workspace
cargo clippy -p costroid --features power --all-targets -- -D warnings
cargo test -p costroid --features power          # exercises the cfg(power) mirror↔const tie
cargo test -p costroid-power --features power
bash scripts/check_benchmarks.sh
bash scripts/check_doc_stamps.sh
bash scripts/check_power_profiles.sh
```

All green, **with zero change to any committed `benchmarks/**` JSON or bundled data**, = Phase 1 done.
