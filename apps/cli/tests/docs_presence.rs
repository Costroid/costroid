//! M6 T5 — the shared `docs_presence` deciding test (covers T4 README, T5 methodology, T6
//! limitations).
//!
//! Asserts each required doc exists and contains its required sections / anchors — grepping for
//! specific heading text and structural markers so the assertions are non-vacuous (a doc that lost a
//! section, the mermaid fence, or a required table fails). Also closes the canonical-stamp loop: the
//! `scripts/check_doc_stamps.sh` gate must reference the Rust const `PENDING_M3B_STAMP` by name (so
//! the script and the Rust source can't drift), and the const's value must be the literal the docs
//! actually carry.
//!
//! Fully offline; a pure text scan of committed files.

use std::path::PathBuf;

fn repo_root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn read(rel: &str) -> String {
    let path = repo_root().join(rel);
    match std::fs::read_to_string(&path) {
        Ok(value) => value,
        Err(err) => panic!(
            "required doc {} is missing/unreadable: {err}",
            path.display()
        ),
    }
}

/// Assert `haystack` contains every `needle`, naming the doc + the missing marker on failure.
fn assert_contains_all(doc: &str, haystack: &str, needles: &[&str]) {
    for needle in needles {
        assert!(
            haystack.contains(needle),
            "{doc} must contain the section/anchor: {needle:?}"
        );
    }
}

#[test]
fn readme_has_the_required_sections_and_a_mermaid_fence() {
    let readme = read("README.md");
    assert_contains_all(
        "README.md",
        &readme,
        &[
            // Problem statement (T4).
            "## The problem",
            // Hero-GIF placeholder, stamped (T4 / D2).
            "capture pending M3b",
            // The one-command quickstart (T2, integrated not duplicated).
            "make demo",
            // The "what ccusage doesn't" feature-contrast table (T4).
            "## What this does that ccusage doesn't",
            "FOCUS-native",
            "Three-lane ledger",
            "Local-inference economics",
            "Break-even",
            "Loopback web UI",
            "Zero-network default",
            // The Mermaid architecture diagram (T4): a fenced mermaid block + the crate graph +
            // the three lanes + the loopback server.
            "```mermaid",
            "costroid-server",
            "local_inference",
        ],
    );
    // Non-vacuous mermaid check: the fence must open AND close, and contain a diagram directive.
    let fence_opens = readme.matches("```mermaid").count();
    assert_eq!(fence_opens, 1, "README has exactly one mermaid fence");
    assert!(
        readme.contains("flowchart"),
        "the README mermaid block declares a diagram type (flowchart)"
    );
    // OMIT competitor star counts (R-batch low): the contrast table is feature-only.
    assert!(
        !readme.contains("stars") && !readme.contains("⭐"),
        "the ccusage contrast table must NOT cite competitor star counts (drift-prone)"
    );
}

#[test]
fn methodology_has_the_required_headings() {
    let methodology = read("docs/methodology.md");
    assert_contains_all(
        "docs/methodology.md",
        &methodology,
        &[
            // Measured-vs-estimated ladder.
            "Measured vs estimated",
            "x_MeasurementMode",
            "measured_wallmeter",
            // Package power vs wall (the ~20–40% caveat).
            "Package power vs wall",
            "~20–40%",
            // The energy-only e over total (in+out) tokens (the M5 lock).
            "energy-only",
            "total (in+out) tokens",
            "local_energy_only_rate",
            // A WORKED numeric example.
            "Worked example",
            "0.000000516666665",
            // The break-even math (calendar-fixed amortization, band, never/infeasible).
            "Break-even math",
            "calendar-fixed",
            "Never",
            "Infeasible",
            // Cross-links.
            "limitations.md",
            "ARCHITECTURE.md",
            // The canonical honesty stamp.
            "estimated — pending M3b measurement",
        ],
    );
}

#[test]
fn limitations_covers_the_uncertain_row_annotation_and_m4_m5() {
    let limitations = read("docs/limitations.md");
    assert_contains_all(
        "docs/limitations.md",
        &limitations,
        &[
            // M4 — break-even ranges, never/infeasible, one-lifetime.
            "Break-even is a range",
            "Never",
            "Infeasible",
            "One break-even lifetime",
            // M5 — interface caveats: text/table-only break-even web view, loopback-only.
            "loopback web UI (M5)",
            "text + table only",
            "loopback-only",
            // The uncertain-row annotation, mapped to real columns/behavior.
            "Uncertain-row annotation",
            "x_AttributionConfidence",
            "x_Sidechain",
            "x_MeasurementMode",
            // Sub-agent undercount + package-vs-wall (the named claims).
            "Sub-agent",
            "package",
            // Where the cue surfaces (non-color).
            "never a colored alarm",
        ],
    );
}

#[test]
fn check_doc_stamps_script_uses_the_canonical_stamp() {
    // The script must read the const by NAME out of the Rust source (single source of truth) —
    // not duplicate the stamp literal. This closes the drift loop from the script side; the Rust
    // const itself is the authority for the value.
    let script = read("scripts/check_doc_stamps.sh");
    assert!(
        script.contains("PENDING_M3B_STAMP"),
        "check_doc_stamps.sh must reference the canonical const PENDING_M3B_STAMP by name"
    );
    // And the const's value must be exactly the literal the docs carry.
    assert_eq!(
        costroid_core::PENDING_M3B_STAMP,
        "estimated — pending M3b measurement",
        "the canonical stamp value must match what the docs/scanner expect"
    );
    let methodology = read("docs/methodology.md");
    assert!(
        methodology.contains(costroid_core::PENDING_M3B_STAMP),
        "docs/methodology.md must carry the canonical PENDING_M3B_STAMP literal"
    );
}
