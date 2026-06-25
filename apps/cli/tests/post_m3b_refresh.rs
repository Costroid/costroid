//! Drift-guard for the POST-M3B-REFRESH runbook ↔ the committed `benchmarks/**` dataset (M6 T8;
//! provenance-anchored at M3b).
//!
//! Two guarantees, both fully offline:
//!
//! 1. **Closed checklist.** `docs/POST-M3B-REFRESH.md` must enumerate **exactly** the set of
//!    committed benchmark artifacts under `benchmarks/**` (every `manifest.v1.json` + every raw
//!    `*.bench.json`, excluding the `.sha256` sidecars). A new artifact added later without an
//!    entry in the runbook FAILS here — so the deferred post-M3b refresh can never silently grow a
//!    loose end. (The byte-level integrity is `scripts/check_benchmarks.sh`; this is the other side
//!    of the loop: the human-facing checklist can't drift from what is actually shipped.)
//!
//! 2. **Provenance guard (M3b).** Every committed benchmark row's measurement claim must match the
//!    authoritative, human-gated allowlist `costroid_power::MEASURED_MODELS` (as of M3b Phase 2:
//!    `gemma-4-31b-dense` measured, every other model estimated). A model NOT on the allowlist MUST
//!    be `estimated` in BOTH its manifest run and its joined raw row; a model ON the allowlist MUST
//!    carry exactly its declared measured mode in BOTH, with `x_Estimated` cleared. A measured row
//!    whose model is absent from the allowlist FAILS — bare cross-consistency would let a
//!    *fabricated* measured row pass, so the allowlist is the anchor. For a model off the allowlist
//!    this is exactly the "every committed row is estimated" invariant; for `gemma-4-31b-dense` it
//!    requires the measured claim end-to-end (manifest run + raw row + `x_Estimated = false`).
//!
//!    Because `apps/cli` links `costroid-power` only behind the `power` feature, this test (which
//!    runs in the default `cargo test --workspace`) consults a string **mirror** of the allowlist
//!    (`MEASURED_MODELS_MIRROR`, the `gemma-4-31b-dense` entry today); the `#[cfg(feature = "power")]` tie below pins the
//!    mirror to the authoritative const, and CI runs `cargo test -p costroid --features power`, so
//!    a drift fails there. (Same necessary pattern `samples_datasets.rs` uses for the `"estimated"`
//!    literal — a test-local shadow with an anti-drift pin, not a second source of truth.)

use std::path::{Path, PathBuf};

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn read(path: &Path) -> String {
    match std::fs::read_to_string(path) {
        Ok(value) => value,
        Err(err) => panic!("reading {} should succeed: {err}", path.display()),
    }
}

/// Enumerate every committed benchmark artifact under `benchmarks/` as a repo-relative path string
/// (manifests + raw outputs; the `.sha256` sidecars are NOT artifacts). Sorted, deterministic.
fn committed_benchmark_artifacts() -> Vec<String> {
    let root = repo_root();
    let bench_root = root.join("benchmarks");
    assert!(
        bench_root.is_dir(),
        "benchmarks/ must exist (the versioned benchmark dataset, T8)"
    );
    let mut found = Vec::new();
    collect_json(&bench_root, &root, &mut found);
    found.sort();
    assert!(
        !found.is_empty(),
        "benchmarks/ must contain at least one JSON artifact (no vacuous pass)"
    );
    found
}

/// Recursively collect `*.json` files (excluding `*.sha256`), as repo-relative `/`-joined paths.
fn collect_json(dir: &Path, repo_root: &Path, out: &mut Vec<String>) {
    let entries = match std::fs::read_dir(dir) {
        Ok(value) => value,
        Err(err) => panic!("reading dir {} should succeed: {err}", dir.display()),
    };
    for entry in entries {
        let entry = match entry {
            Ok(value) => value,
            Err(err) => panic!("a benchmarks/ dir entry should read: {err}"),
        };
        let path = entry.path();
        if path.is_dir() {
            collect_json(&path, repo_root, out);
            continue;
        }
        let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if !name.ends_with(".json") || name.ends_with(".sha256") {
            continue;
        }
        let Ok(rel) = path.strip_prefix(repo_root) else {
            panic!("{} should be under the repo root", path.display());
        };
        // Normalize to forward slashes so the literal matches the doc on every OS.
        out.push(rel.to_string_lossy().replace('\\', "/"));
    }
}

#[test]
fn post_m3b_refresh_enumerates_exactly_the_committed_benchmark_artifacts() {
    let root = repo_root();
    let runbook = root.join("docs/POST-M3B-REFRESH.md");
    let text = read(&runbook);
    let artifacts = committed_benchmark_artifacts();

    // (a) Every committed artifact path appears verbatim in the runbook (none missed).
    for artifact in &artifacts {
        assert!(
            text.contains(artifact.as_str()),
            "POST-M3B-REFRESH.md is missing a checklist entry for the committed artifact `{artifact}` \
             — add it (the refresh checklist must be CLOSED over benchmarks/**)"
        );
    }

    // (b) Every `benchmarks/...json` path the runbook references actually exists (no stale entry).
    // Scan the doc for `benchmarks/.../*.json` tokens (excluding sidecars) and require each on disk.
    for token in benchmarks_json_tokens(&text) {
        assert!(
            artifacts.contains(&token),
            "POST-M3B-REFRESH.md references `{token}`, which is NOT a committed benchmarks/** \
             artifact — remove the stale entry or commit the artifact"
        );
    }
}

/// Pull every `benchmarks/<...>.json` token out of the doc (excluding `.sha256` sidecars), so the
/// reverse direction (no stale checklist entry) can be enforced. A token is a maximal run of
/// path-ish characters starting with `benchmarks/` and ending in `.json`.
fn benchmarks_json_tokens(text: &str) -> Vec<String> {
    let mut tokens = Vec::new();
    for raw in text.split(|c: char| {
        // Split on anything that can't be part of a repo-relative path token in prose/backticks.
        !(c.is_alphanumeric() || matches!(c, '/' | '.' | '-' | '_'))
    }) {
        if raw.starts_with("benchmarks/") && raw.ends_with(".json") && !raw.ends_with(".sha256") {
            let owned = raw.to_string();
            if !tokens.contains(&owned) {
                tokens.push(owned);
            }
        }
    }
    tokens
}

// ────────────────────────────────────────────────────────────────────────────────────────────────
// Provenance guard (M3b) — the allowlist-anchored replacement for the old "strictly estimated"
// manifest + raw guards. Operates on string modes (so it runs in the default workspace test) and
// is reused, root-agnostic, by the fixture test below.
// ────────────────────────────────────────────────────────────────────────────────────────────────

/// The FOCUS wire string for an estimate (the only "not measured" mode). Tied to
/// `costroid_power::MeasurementMode::Estimated.as_focus_str()` under `#[cfg(feature = "power")]`.
const ESTIMATED_MODE: &str = "estimated";

/// Default-build mirror of `costroid_power::MEASURED_MODELS` — the crate const can't be linked
/// without the `power` feature, and this guard must run in `cargo test --workspace`. Holds the M3b
/// Phase-2 `gemma-4-31b-dense` attestation today. The `#[cfg(feature = "power")]` tie below pins it
/// to the authoritative const; CI runs `cargo test -p costroid --features power`, so any drift FAILS there.
const MEASURED_MODELS_MIRROR: &[(&str, &str)] = &[
    // M3b Phase 2 (2026-06-25) — mirror of crates/costroid-power MEASURED_MODELS (tied to the const
    // under #[cfg(feature = "power")] by mirror_equals_the_authoritative_measured_models):
    ("gemma-4-31b-dense", "measured_wallmeter"),
];

/// Tally of what the guard actually inspected — so the real test can reject a vacuous pass.
struct Stats {
    manifests: usize,
    runs: usize,
    rows: usize,
}

/// The expected measurement-mode string for `model`: its allowlisted mode, else `"estimated"`.
fn expected_mode<'a>(allowlist: &[(&'a str, &'a str)], model: &str) -> &'a str {
    allowlist
        .iter()
        .find(|entry| entry.0 == model)
        .map(|entry| entry.1)
        .unwrap_or(ESTIMATED_MODE)
}

fn str_field<'a>(value: &'a serde_json::Value, key: &str) -> Option<&'a str> {
    value.get(key).and_then(|v| v.as_str())
}

fn bool_field(value: &serde_json::Value, key: &str) -> Option<bool> {
    value.get(key).and_then(|v| v.as_bool())
}

/// Read + parse a JSON file. **Fail-closed:** a missing, unreadable, or malformed file is an `Err`,
/// never a skip — a measured manifest run that points at a vanished raw can't slip through.
fn parse_json_file(path: &Path) -> Result<serde_json::Value, String> {
    let text =
        std::fs::read_to_string(path).map_err(|e| format!("reading {}: {e}", path.display()))?;
    serde_json::from_str(&text).map_err(|e| format!("parsing {}: {e}", path.display()))
}

fn file_name_is(path: &Path, name: &str) -> bool {
    path.file_name().and_then(|n| n.to_str()) == Some(name)
}

fn file_name_ends_with(path: &Path, suffix: &str) -> bool {
    path.file_name()
        .and_then(|n| n.to_str())
        .map(|n| n.ends_with(suffix))
        .unwrap_or(false)
}

/// Recursively collect `*.json` files (excluding `*.sha256`) under `dir` as absolute paths. Unlike
/// `collect_json`, this is root-agnostic (used for arbitrary trees, incl. the throwaway fixtures).
fn collect_json_files(dir: &Path, out: &mut Vec<PathBuf>) -> Result<(), String> {
    let entries = std::fs::read_dir(dir).map_err(|e| format!("read_dir {}: {e}", dir.display()))?;
    for entry in entries {
        let entry = entry.map_err(|e| format!("dir entry under {}: {e}", dir.display()))?;
        let path = entry.path();
        if path.is_dir() {
            collect_json_files(&path, out)?;
            continue;
        }
        let name = path.file_name().and_then(|n| n.to_str()).unwrap_or("");
        if name.ends_with(".json") && !name.ends_with(".sha256") {
            out.push(path);
        }
    }
    Ok(())
}

/// Validate one benchmark tree against `allowlist` (model_id → focus-mode string). Returns
/// `Err(message)` on the first violation (so the fixture test can assert failure without a panic);
/// iterates ALL `**/manifest.v1.json` and ALL `**/*.bench.json` under `bench_root`.
///
/// Manifest rules (per run + the raw it joins to):
///
/// - Run provenance: `run.measurement_mode == expected(run.model)`.
/// - Fail-closed JOIN: `run.raw_output` present + non-empty; `manifest_dir/raw_output` must exist,
///   parse, and have a non-empty `rows[]`; every row's `x_Model`/`x_MeasurementMode` equals the
///   run's `model`/`measurement_mode`.
/// - Measured-backing (per-run non-vacuity): a measured run must have >=1 joined row with that
///   exact mode+model.
///
/// Raw-sweep rules (every `*.bench.json`, so orphan raws are caught too; per row):
///
/// - Raw provenance: `x_MeasurementMode == expected(x_Model)`.
/// - Coupling, keyed off the row's OWN mode: `x_Estimated == (x_MeasurementMode == "estimated")`.
/// - Measured-needs-allowlist: a measured `x_MeasurementMode` requires `(x_Model, mode)` on the
///   allowlist.
fn check_benchmark_provenance(
    bench_root: &Path,
    allowlist: &[(&str, &str)],
) -> Result<Stats, String> {
    let mut json_files: Vec<PathBuf> = Vec::new();
    collect_json_files(bench_root, &mut json_files)?;

    let mut stats = Stats {
        manifests: 0,
        runs: 0,
        rows: 0,
    };

    // ── Manifests: run provenance + fail-closed JOIN + measured-backing ──────────────────────────
    for manifest_path in json_files
        .iter()
        .filter(|p| file_name_is(p, "manifest.v1.json"))
    {
        let manifest = parse_json_file(manifest_path)?;
        let runs = manifest
            .get("runs")
            .and_then(|r| r.as_array())
            .ok_or_else(|| format!("{}: missing `runs` array", manifest_path.display()))?;
        if runs.is_empty() {
            return Err(format!(
                "{}: `runs` must be non-empty (per-file non-vacuity)",
                manifest_path.display()
            ));
        }
        let manifest_dir = manifest_path
            .parent()
            .ok_or_else(|| format!("{}: has no parent dir", manifest_path.display()))?;

        for run in runs {
            stats.runs += 1;
            let model = str_field(run, "model")
                .ok_or_else(|| format!("{}: a run is missing `model`", manifest_path.display()))?;
            let run_mode = str_field(run, "measurement_mode").ok_or_else(|| {
                format!(
                    "{}: run `{model}` missing `measurement_mode`",
                    manifest_path.display()
                )
            })?;
            let expected = expected_mode(allowlist, model);

            // Rule 1 — run provenance.
            if run_mode != expected {
                return Err(format!(
                    "{}: run `{model}` measurement_mode `{run_mode}` != expected `{expected}` \
                     (allowlist mismatch)",
                    manifest_path.display()
                ));
            }

            // Rule 2 — fail-closed JOIN.
            let raw_output = str_field(run, "raw_output")
                .filter(|s| !s.is_empty())
                .ok_or_else(|| {
                    format!(
                        "{}: run `{model}` has a missing/empty `raw_output`",
                        manifest_path.display()
                    )
                })?;
            let raw_path = manifest_dir.join(raw_output);
            let raw = parse_json_file(&raw_path)?; // missing/unreadable/malformed => Err (fail-closed)
            let rows = raw
                .get("rows")
                .and_then(|r| r.as_array())
                .ok_or_else(|| format!("{}: missing `rows` array", raw_path.display()))?;
            if rows.is_empty() {
                return Err(format!(
                    "{}: `rows` must be non-empty (per-file non-vacuity)",
                    raw_path.display()
                ));
            }
            for row in rows {
                let row_model = str_field(row, "x_Model")
                    .ok_or_else(|| format!("{}: a row missing `x_Model`", raw_path.display()))?;
                let row_mode = str_field(row, "x_MeasurementMode").ok_or_else(|| {
                    format!("{}: a row missing `x_MeasurementMode`", raw_path.display())
                })?;
                if row_model != model {
                    return Err(format!(
                        "{}: row x_Model `{row_model}` != run model `{model}` (JOIN)",
                        raw_path.display()
                    ));
                }
                if row_mode != run_mode {
                    return Err(format!(
                        "{}: row x_MeasurementMode `{row_mode}` != run measurement_mode \
                         `{run_mode}` (JOIN / half-flip)",
                        raw_path.display()
                    ));
                }
            }

            // Rule 3 — a measured run MUST have a real backing measured raw row (per-run non-vacuity).
            if expected != ESTIMATED_MODE {
                let backed = rows.iter().any(|row| {
                    str_field(row, "x_MeasurementMode") == Some(expected)
                        && str_field(row, "x_Model") == Some(model)
                });
                if !backed {
                    return Err(format!(
                        "{}: measured run `{model}` ({expected}) has no backing raw row with that \
                         mode+model",
                        raw_path.display()
                    ));
                }
            }
        }
        stats.manifests += 1;
    }

    // ── Raw sweep: provenance + coupling + measured-needs-allowlist (every *.bench.json) ──────────
    for raw_path in json_files
        .iter()
        .filter(|p| file_name_ends_with(p, ".bench.json"))
    {
        let raw = parse_json_file(raw_path)?;
        let rows = raw
            .get("rows")
            .and_then(|r| r.as_array())
            .ok_or_else(|| format!("{}: missing `rows` array", raw_path.display()))?;
        if rows.is_empty() {
            return Err(format!(
                "{}: `rows` must be non-empty (per-file non-vacuity)",
                raw_path.display()
            ));
        }
        for row in rows {
            stats.rows += 1;
            let row_model = str_field(row, "x_Model")
                .ok_or_else(|| format!("{}: a row missing `x_Model`", raw_path.display()))?;
            let row_mode = str_field(row, "x_MeasurementMode").ok_or_else(|| {
                format!("{}: a row missing `x_MeasurementMode`", raw_path.display())
            })?;
            let row_estimated = bool_field(row, "x_Estimated")
                .ok_or_else(|| format!("{}: a row missing `x_Estimated`", raw_path.display()))?;
            let expected = expected_mode(allowlist, row_model);

            // Rule 4 — raw provenance.
            if row_mode != expected {
                return Err(format!(
                    "{}: row `{row_model}` x_MeasurementMode `{row_mode}` != expected `{expected}` \
                     (allowlist mismatch)",
                    raw_path.display()
                ));
            }
            // Rule 5 — coupling keyed off the row's OWN mode (internal consistency).
            let should_be_estimated = row_mode == ESTIMATED_MODE;
            if row_estimated != should_be_estimated {
                return Err(format!(
                    "{}: row `{row_model}` x_Estimated `{row_estimated}` is inconsistent with mode \
                     `{row_mode}`",
                    raw_path.display()
                ));
            }
            // Rule 6 — a measured row's (model, mode) must be on the allowlist.
            if row_mode != ESTIMATED_MODE {
                let on_list = allowlist
                    .iter()
                    .any(|e| e.0 == row_model && e.1 == row_mode);
                if !on_list {
                    return Err(format!(
                        "{}: row `{row_model}` claims measured mode `{row_mode}` but is NOT on the \
                         allowlist (fabricated/undeclared measured claim)",
                        raw_path.display()
                    ));
                }
            }
        }
    }

    Ok(stats)
}

#[test]
fn benchmark_dataset_provenance_matches_the_measured_models_allowlist() {
    // The committed benchmarks/** must satisfy the allowlist-anchored provenance guard. With the
    // M3b Phase-2 mirror (gemma-4-31b-dense), the guard requires 31b measured end-to-end (manifest
    // run + raw row + x_Estimated = false) and every other model estimated; doing either the data
    // flip or the allowlist flip alone would fail it.
    let bench_root = repo_root().join("benchmarks");
    assert!(bench_root.is_dir(), "benchmarks/ must exist");
    let stats = match check_benchmark_provenance(&bench_root, MEASURED_MODELS_MIRROR) {
        Ok(s) => s,
        Err(msg) => panic!("committed benchmarks/** violate the provenance guard: {msg}"),
    };
    // Non-vacuous (aggregate): something was actually inspected at every level.
    assert!(stats.manifests > 0, "no manifests were checked (vacuous)");
    assert!(stats.runs > 0, "no manifest runs were checked (vacuous)");
    assert!(stats.rows > 0, "no raw bench rows were checked (vacuous)");
}

/// Pin the string mirror to the authoritative `costroid_power::MEASURED_MODELS` (and the estimated
/// literal to the enum's wire form). Only compiled under `--features power`; CI runs that job, so a
/// mirror that drifts from the const fails here even though the default workspace test can't link it.
#[cfg(feature = "power")]
#[test]
fn mirror_equals_the_authoritative_measured_models() {
    use costroid_power::{measured_mode_for, MeasurementMode, MEASURED_MODELS};

    assert_eq!(
        MEASURED_MODELS_MIRROR.len(),
        MEASURED_MODELS.len(),
        "the test mirror drifted from costroid_power::MEASURED_MODELS (length)"
    );
    for (id, mode_str) in MEASURED_MODELS_MIRROR {
        let Some(mode) = measured_mode_for(id) else {
            panic!("mirror lists `{id}` but costroid_power::MEASURED_MODELS does not");
        };
        assert_eq!(
            mode.as_focus_str(),
            *mode_str,
            "mirror mode for `{id}` drifted from the authoritative const"
        );
    }
    // The "not measured" wire string the guard keys off must equal the enum serialization.
    assert_eq!(ESTIMATED_MODE, MeasurementMode::Estimated.as_focus_str());
}

// ────────────────────────────────────────────────────────────────────────────────────────────────
// Non-vacuous fixture test — proves the guard PASSES on a consistent allowlisted tree and FAILS on
// each tampering shape. Built in throwaway trees under the OS temp dir; NEVER under the repo
// `benchmarks/` (so it can't pollute the real guard or `check_benchmarks.sh`).
// ────────────────────────────────────────────────────────────────────────────────────────────────

fn tmp_root(tag: &str) -> PathBuf {
    // Unique per process + per case; the fixture writes/cleans under here, never under benchmarks/.
    std::env::temp_dir().join(format!("costroid-m3b-fixture-{}-{tag}", std::process::id()))
}

fn write_file(path: &Path, contents: &str) {
    if let Some(parent) = path.parent() {
        if let Err(e) = std::fs::create_dir_all(parent) {
            panic!("create_dir_all {}: {e}", parent.display());
        }
    }
    if let Err(e) = std::fs::write(path, contents) {
        panic!("write {}: {e}", path.display());
    }
}

fn manifest_json(runs: serde_json::Value) -> String {
    serde_json::json!({ "schema": "costroid.benchmarks/v1", "id": "fixture", "runs": runs })
        .to_string()
}

fn run_obj(model: &str, mode: &str, raw_output: &str) -> serde_json::Value {
    serde_json::json!({ "model": model, "measurement_mode": mode, "raw_output": raw_output })
}

fn raw_json(rows: serde_json::Value) -> String {
    serde_json::json!({ "focusVersion": "1.3", "rows": rows }).to_string()
}

fn raw_row(model: &str, mode: &str, estimated: bool) -> serde_json::Value {
    serde_json::json!({ "x_Model": model, "x_MeasurementMode": mode, "x_Estimated": estimated })
}

/// Run the guard on a freshly-built tree, then clean it up BEFORE returning (so an assert failure
/// doesn't leak a temp dir). Returns the guard's `Ok`/`Err` outcome.
fn run_guard_on_tree(root: &Path, allowlist: &[(&str, &str)]) -> Result<(), String> {
    let outcome = check_benchmark_provenance(root, allowlist).map(|_| ());
    let _ = std::fs::remove_dir_all(root); // best-effort cleanup before the assert
    outcome
}

#[test]
fn provenance_guard_fixture_passes_consistent_and_fails_tampering() {
    let measured = "measured_wallmeter";
    let allow: &[(&str, &str)] = &[("fx-31b", measured)];

    // Mode-set tie: the fixture's measured + estimated literals are exactly the enum wire forms.
    #[cfg(feature = "power")]
    {
        assert_eq!(
            measured,
            costroid_power::MeasurementMode::MeasuredWallmeter.as_focus_str()
        );
        assert_eq!(
            ESTIMATED_MODE,
            costroid_power::MeasurementMode::Estimated.as_focus_str()
        );
    }

    // CASE 1 — PASS: allowlisted+measured 31b + an estimated 2nd model, all consistent end-to-end.
    {
        let root = tmp_root("pass");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([
                run_obj("fx-31b", measured, "raw/fx-31b.bench.json"),
                run_obj("fx-12b", ESTIMATED_MODE, "raw/fx-12b.bench.json"),
            ])),
        );
        write_file(
            &root.join("bench/raw/fx-31b.bench.json"),
            &raw_json(serde_json::json!([raw_row("fx-31b", measured, false)])),
        );
        write_file(
            &root.join("bench/raw/fx-12b.bench.json"),
            &raw_json(serde_json::json!([raw_row("fx-12b", ESTIMATED_MODE, true)])),
        );
        let outcome = run_guard_on_tree(&root, allow);
        assert!(
            outcome.is_ok(),
            "allowlisted+consistent tree must PASS, got {outcome:?}"
        );
    }

    // CASE 2 — FAIL undeclared-measured: empty allowlist but the data claims measured.
    {
        let root = tmp_root("undeclared");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([run_obj(
                "fx-31b",
                measured,
                "raw/fx-31b.bench.json"
            )])),
        );
        write_file(
            &root.join("bench/raw/fx-31b.bench.json"),
            &raw_json(serde_json::json!([raw_row("fx-31b", measured, false)])),
        );
        let outcome = run_guard_on_tree(&root, &[]);
        assert!(outcome.is_err(), "undeclared-measured tree must FAIL");
    }

    // CASE 3 — FAIL half-flip: manifest run measured, raw row estimated.
    {
        let root = tmp_root("halfflip");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([run_obj(
                "fx-31b",
                measured,
                "raw/fx-31b.bench.json"
            )])),
        );
        write_file(
            &root.join("bench/raw/fx-31b.bench.json"),
            &raw_json(serde_json::json!([raw_row("fx-31b", ESTIMATED_MODE, true)])),
        );
        let outcome = run_guard_on_tree(&root, allow);
        assert!(outcome.is_err(), "half-flip tree must FAIL");
    }

    // CASE 4 — FAIL x_Estimated-vs-mode mismatch: measured everywhere but x_Estimated left true.
    {
        let root = tmp_root("coupling");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([run_obj(
                "fx-31b",
                measured,
                "raw/fx-31b.bench.json"
            )])),
        );
        write_file(
            &root.join("bench/raw/fx-31b.bench.json"),
            &raw_json(serde_json::json!([raw_row("fx-31b", measured, true)])),
        );
        let outcome = run_guard_on_tree(&root, allow);
        assert!(outcome.is_err(), "x_Estimated/mode-mismatch tree must FAIL");
    }

    // CASE 5 — FAIL measured run with empty/missing raw (a measured claim with no backing data).
    {
        // (a) the raw file is missing entirely → fail-closed JOIN.
        let root = tmp_root("noraw-missing");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([run_obj(
                "fx-31b",
                measured,
                "raw/fx-31b.bench.json"
            )])),
        );
        // (intentionally do NOT write raw/fx-31b.bench.json)
        let outcome = run_guard_on_tree(&root, allow);
        assert!(
            outcome.is_err(),
            "measured run with a MISSING raw must FAIL (fail-closed)"
        );

        // (b) the raw exists but rows[] is empty → per-file non-vacuity.
        let root = tmp_root("noraw-empty");
        write_file(
            &root.join("bench/manifest.v1.json"),
            &manifest_json(serde_json::json!([run_obj(
                "fx-31b",
                measured,
                "raw/fx-31b.bench.json"
            )])),
        );
        write_file(
            &root.join("bench/raw/fx-31b.bench.json"),
            &raw_json(serde_json::json!([])),
        );
        let outcome = run_guard_on_tree(&root, allow);
        assert!(
            outcome.is_err(),
            "measured run with EMPTY raw rows must FAIL (per-file non-vacuity)"
        );
    }
}
