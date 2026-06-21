//! Drift-guard for the POST-M3B-REFRESH runbook ↔ the committed `benchmarks/**` dataset (M6 T8).
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
//! 2. **Inverse honesty guard (shared with T1).** Every run record in every committed manifest is
//!    `measurement_mode == "estimated"` — no shipped artifact may claim a measured number before
//!    the M3b wall-meter run lands.

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

#[test]
fn every_committed_manifest_run_is_strictly_estimated() {
    // Inverse honesty guard (shared with samples_datasets.rs): no shipped benchmark manifest run
    // may claim a measured number before M3b. Find every `manifest.v1.json`, parse it, and assert
    // each run's `measurement_mode == "estimated"`.
    let artifacts = committed_benchmark_artifacts();
    let root = repo_root();
    let manifests: Vec<&String> = artifacts
        .iter()
        .filter(|p| p.ends_with("manifest.v1.json"))
        .collect();
    assert!(
        !manifests.is_empty(),
        "there must be at least one committed benchmark manifest"
    );

    let mut runs_checked = 0usize;
    for manifest_path in manifests {
        let json = read(&root.join(manifest_path));
        let value: serde_json::Value = match serde_json::from_str(&json) {
            Ok(v) => v,
            Err(err) => panic!("{manifest_path} should be valid JSON: {err}"),
        };
        let Some(runs) = value.get("runs").and_then(|r| r.as_array()) else {
            panic!("{manifest_path} must have a `runs` array");
        };
        assert!(!runs.is_empty(), "{manifest_path} `runs` must be non-empty");
        for run in runs {
            let mode = run.get("measurement_mode").and_then(|m| m.as_str());
            assert_eq!(
                mode,
                Some("estimated"),
                "{manifest_path}: every run must be measurement_mode == \"estimated\" pre-M3b, \
                 got {mode:?}"
            );
            runs_checked += 1;
        }
    }
    assert!(runs_checked > 0, "no manifest runs were checked (vacuous)");
}
