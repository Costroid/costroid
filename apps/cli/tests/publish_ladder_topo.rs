//! Publish-ladder topo-sort guard (M6 T9 / PKG-1).
//!
//! The crates.io publish ladder documented in `RELEASING.md` must be a valid **topological order**
//! of the real `Cargo.toml` dependency graph: every crate must be published AFTER every
//! `costroid-*` crate it depends on (a dependent can't publish before its dependency exists on the
//! registry). This `#[test]` derives the graph from the live manifests and the ladder from the doc,
//! then asserts the ladder respects every edge — so the documented order can't silently drift from
//! the code.
//!
//! Fully offline + dependency-free: it hand-parses the TOML it needs (std only — no `toml` dep),
//! reading exactly the `[package].name` + the real dependency sections of each workspace member.
//!
//! Scope of edges:
//! - Only **real** dependency sections count toward publish order: `[dependencies]`,
//!   `[build-dependencies]`, and `[target.*.dependencies]`. `[dev-dependencies]` are EXCLUDED —
//!   they are not part of the published package and never constrain publish order (e.g. `apps/cli`
//!   dev-depends on `costroid-focus`; `apps/server` dev-depends on `costroid-providers` — neither
//!   may be treated as a ladder edge).
//! - Only `costroid-*` edges matter (third-party crates are already on the registry).

use std::collections::{BTreeMap, BTreeSet};
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

/// Every workspace member's `Cargo.toml`, by repo-relative dir (kept in sync with the root
/// `[workspace].members` list — the manifest below mirrors `Cargo.toml`).
const MEMBER_DIRS: &[&str] = &[
    "apps/bar",
    "apps/cli",
    "apps/server",
    "crates/costroid-config",
    "crates/costroid-connect",
    "crates/costroid-core",
    "crates/costroid-focus",
    "crates/costroid-power",
    "crates/costroid-providers",
    "crates/costroid-store",
];

/// The `[package] name = "..."` from a manifest body.
fn package_name(manifest: &str) -> String {
    let mut in_package = false;
    for raw in manifest.lines() {
        let line = strip_comment(raw).trim();
        if let Some(section) = section_header(line) {
            in_package = section == "package";
            continue;
        }
        if in_package {
            if let Some(value) = key_value(line, "name") {
                return unquote(value);
            }
        }
    }
    panic!("manifest has no [package] name:\n{manifest}");
}

/// The set of `costroid-*` crates this manifest depends on via a REAL dependency section
/// (`[dependencies]`, `[build-dependencies]`, `[target.*.dependencies]`) — `[dev-dependencies]`
/// excluded.
fn costroid_deps(manifest: &str) -> BTreeSet<String> {
    let mut deps = BTreeSet::new();
    let mut in_real_dep_section = false;
    for raw in manifest.lines() {
        let line = strip_comment(raw).trim();
        if let Some(section) = section_header(line) {
            in_real_dep_section = is_real_dependency_section(section);
            continue;
        }
        if !in_real_dep_section || line.is_empty() {
            continue;
        }
        // A dependency entry is `key = ...`. The key is either a bare crate name (`costroid-core =
        // { ... }`) or a dotted key whose FIRST segment is the crate name
        // (`costroid-config.workspace = true`). Take the first dotted segment as the dep name.
        let Some((key, _)) = line.split_once('=') else {
            continue;
        };
        let first_segment = unquote(key.trim());
        let name = first_segment
            .split('.')
            .next()
            .unwrap_or(&first_segment)
            .to_string();
        if name.starts_with("costroid-") {
            deps.insert(name);
        }
    }
    deps
}

/// True for a real (publish-affecting) dependency section header.
///   `dependencies`                         → yes
///   `build-dependencies`                   → yes
///   `target.'cfg(...)'.dependencies`       → yes
///   `target.'cfg(...)'.dev-dependencies`   → NO
///   `dev-dependencies`                     → NO
fn is_real_dependency_section(section: &str) -> bool {
    let tail = section.rsplit('.').next().unwrap_or(section);
    matches!(tail, "dependencies" | "build-dependencies")
}

/// If `line` is a section header `[...]`, return its inner text; else `None`.
fn section_header(line: &str) -> Option<&str> {
    // `[[bin]]` and other array-of-tables: treat the inner text the same; it never matches a
    // dependency section, so it correctly resets `in_*` flags to false.
    let inner = line.strip_prefix('[')?.strip_suffix(']')?;
    Some(
        inner
            .trim()
            .trim_start_matches('[')
            .trim_end_matches(']')
            .trim(),
    )
}

/// If `line` is `key = value` for `key`, return the trimmed value; else `None`.
fn key_value<'a>(line: &'a str, key: &str) -> Option<&'a str> {
    let (lhs, rhs) = line.split_once('=')?;
    (lhs.trim() == key).then(|| rhs.trim())
}

/// Strip a `#` line comment that is not inside a quoted string. (Our manifests never put `#` inside
/// the values this test reads, so a simple first-`#` split is sufficient and conservative.)
fn strip_comment(line: &str) -> &str {
    match line.find('#') {
        Some(idx) => &line[..idx],
        None => line,
    }
}

/// Remove a single pair of surrounding `"`/`'` quotes if present.
fn unquote(value: &str) -> String {
    let v = value.trim();
    let bytes = v.as_bytes();
    if bytes.len() >= 2 {
        let first = bytes[0];
        let last = bytes[bytes.len() - 1];
        if (first == b'"' && last == b'"') || (first == b'\'' && last == b'\'') {
            return v[1..v.len() - 1].to_string();
        }
    }
    v.to_string()
}

/// Parse the ladder out of `RELEASING.md`: the single fenced line of `a → b → c …` crate names
/// inside the "## crates.io publish" section.
fn parse_ladder(releasing_md: &str) -> Vec<String> {
    for raw in releasing_md.lines() {
        let line = raw.trim();
        if line.contains('→') && line.contains("costroid-focus") {
            let ladder: Vec<String> = line
                .split('→')
                .map(|s| s.trim().to_string())
                .filter(|s| s.starts_with("costroid"))
                .collect();
            if ladder.len() >= 2 {
                return ladder;
            }
        }
    }
    panic!("RELEASING.md has no `costroid-focus → … ` publish ladder line");
}

/// Build (package-name → its costroid-* deps) over all members.
fn dependency_graph() -> BTreeMap<String, BTreeSet<String>> {
    let root = repo_root();
    let mut graph = BTreeMap::new();
    for dir in MEMBER_DIRS {
        let manifest = read(&root.join(dir).join("Cargo.toml"));
        let name = package_name(&manifest);
        let deps = costroid_deps(&manifest);
        graph.insert(name, deps);
    }
    graph
}

/// Core check: every edge `crate → dep` must place `dep` strictly before `crate` in `ladder`.
/// Returns `Err(msg)` on the first violation so the test can drive it both ways (real graph passes;
/// a deliberately-broken ladder fails) — keeping the guard non-vacuous.
fn ladder_respects_graph(
    ladder: &[String],
    graph: &BTreeMap<String, BTreeSet<String>>,
) -> Result<(), String> {
    let position: BTreeMap<&str, usize> = ladder
        .iter()
        .enumerate()
        .map(|(i, name)| (name.as_str(), i))
        .collect();

    for (crate_name, deps) in graph {
        let Some(&crate_pos) = position.get(crate_name.as_str()) else {
            return Err(format!("ladder omits workspace member `{crate_name}`"));
        };
        for dep in deps {
            let Some(&dep_pos) = position.get(dep.as_str()) else {
                return Err(format!(
                    "ladder omits `{dep}` (a dependency of `{crate_name}`)"
                ));
            };
            if dep_pos >= crate_pos {
                return Err(format!(
                    "`{crate_name}` (pos {crate_pos}) depends on `{dep}` (pos {dep_pos}) — \
                     the dependency must come EARLIER in the ladder"
                ));
            }
        }
    }
    Ok(())
}

#[test]
fn releasing_ladder_is_a_valid_topological_order() {
    let graph = dependency_graph();
    let ladder = parse_ladder(&read(&repo_root().join("RELEASING.md")));

    // Sanity: the ladder lists exactly the workspace members (no missing/extra crate).
    let ladder_set: BTreeSet<&str> = ladder.iter().map(String::as_str).collect();
    let member_set: BTreeSet<&str> = graph.keys().map(String::as_str).collect();
    assert_eq!(
        ladder_set,
        member_set,
        "the RELEASING.md ladder must list exactly the {} workspace members",
        member_set.len()
    );

    if let Err(msg) = ladder_respects_graph(&ladder, &graph) {
        panic!("RELEASING.md publish ladder is not a valid topo order: {msg}");
    }
}

/// Non-vacuity proof: the SAME check must FAIL on a ladder that violates a real edge. We reverse the
/// real ladder — a reversed valid topo order places every dependency AFTER its dependent, so the
/// guard must reject it. (If `ladder_respects_graph` were vacuous, this would wrongly pass.)
#[test]
fn topo_check_rejects_a_wrongly_reordered_ladder() {
    let graph = dependency_graph();
    let mut reversed = parse_ladder(&read(&repo_root().join("RELEASING.md")));
    reversed.reverse();

    let result = ladder_respects_graph(&reversed, &graph);
    assert!(
        result.is_err(),
        "the reversed ladder violates dependency order, so the guard MUST reject it — \
         a passing result means the topo check is vacuous"
    );
}

/// The published-ness invariant T9 establishes: every workspace member that participates in the
/// ladder is `publish`-able (not `publish = false`). Guards against a future crate sneaking back to
/// `publish = false` while still being listed in the ladder.
#[test]
fn every_laddered_crate_is_publishable() {
    let root = repo_root();
    for dir in MEMBER_DIRS {
        let manifest = read(&root.join(dir).join("Cargo.toml"));
        let name = package_name(&manifest);
        let is_unpublished = manifest
            .lines()
            .map(strip_comment)
            .any(|l| l.trim().replace(' ', "") == "publish=false");
        assert!(
            !is_unpublished,
            "`{name}` ({dir}) is `publish = false` but is part of the crates.io publish ladder"
        );
    }
}
