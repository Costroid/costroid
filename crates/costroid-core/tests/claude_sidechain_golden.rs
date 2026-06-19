//! Golden test for sidechain (sub-agent) attribution at the normalized `FocusRecord`
//! level (T15).
//!
//! Drives the REAL Claude parser over a fixture holding one mainline turn + one
//! sidechain turn, then normalizes through `focus_records_from_usage` and asserts the
//! resulting FOCUS rows. The Cardinal-Rule-safe contract: sidechain usage is **kept**
//! (counted), only **annotated** (`x_Sidechain=true`, `x_AttributionConfidence=uncertain`)
//! — never dropped. Codex (which has no sidechain concept) is asserted all-mainline.

use std::path::{Path, PathBuf};

use costroid_core::{focus_records_from_usage, FocusRecord};
use costroid_providers::{ClaudeCodeProvider, CodexProvider, DataLocation, Provider, ProviderId};
use rust_decimal::Decimal;

fn fixtures(sub: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../fixtures")
        .join(sub)
}

fn consumed<'a>(rows: impl IntoIterator<Item = &'a FocusRecord>) -> Decimal {
    rows.into_iter().map(|row| row.x_consumed_tokens).sum()
}

#[test]
fn claude_sidechain_rows_are_kept_and_annotated_not_dropped() {
    let dir = fixtures("claude-code");
    let loc = DataLocation {
        provider: ProviderId::ClaudeCode,
        root: dir.clone(),
        files: vec![dir.join("sidechain-golden.jsonl")],
    };

    let events = match ClaudeCodeProvider.parse_usage(&loc) {
        Ok(value) => value,
        Err(err) => panic!("sidechain fixture should parse: {err}"),
    };
    // Both turns survive parsing — the sidechain turn is NOT dropped.
    assert_eq!(events.len(), 2, "mainline + sidechain both kept");
    assert_eq!(
        events.iter().filter(|e| e.is_sidechain).count(),
        1,
        "exactly one turn is flagged sidechain"
    );

    let rows = match focus_records_from_usage(&events) {
        Ok(value) => value,
        Err(err) => panic!("records should normalize: {err}"),
    };
    // Mainline turn: input(100) + output(200) = 2 meter rows. Sidechain turn:
    // input(50) + output(80) = 2 meter rows. Four rows total.
    assert_eq!(rows.len(), 4, "four meter rows across the two turns");

    let sidechain: Vec<&FocusRecord> = rows.iter().filter(|r| r.x_sidechain).collect();
    let mainline: Vec<&FocusRecord> = rows.iter().filter(|r| !r.x_sidechain).collect();
    assert_eq!(sidechain.len(), 2, "the sidechain turn's two meter rows");
    assert_eq!(mainline.len(), 2, "the mainline turn's two meter rows");

    // Attribution annotated honestly per row.
    assert!(
        sidechain
            .iter()
            .all(|r| r.x_attribution_confidence == "uncertain"),
        "sidechain rows are uncertain"
    );
    assert!(
        mainline
            .iter()
            .all(|r| r.x_attribution_confidence == "confident"),
        "mainline rows are confident"
    );

    // KEEP COUNTING: the sidechain tokens (50 + 80 = 130) are present in the total, not
    // dropped. Total across all rows = 100 + 200 + 50 + 80 = 430.
    assert_eq!(
        consumed(sidechain.iter().copied()),
        Decimal::from(130),
        "sidechain tokens counted"
    );
    assert_eq!(consumed(&rows), Decimal::from(430), "no usage dropped");

    // Every row carries the collector version stamp (non-empty).
    assert!(
        rows.iter().all(|r| !r.x_collector_version.is_empty()),
        "every row is stamped with the collector version"
    );
}

#[test]
fn codex_rows_are_all_mainline_no_sidechain_concept() {
    let dir = fixtures("codex");
    let loc = DataLocation {
        provider: ProviderId::Codex,
        root: dir.clone(),
        files: vec![dir.join("golden-gpt55-small-3.jsonl")],
    };
    let events = match CodexProvider.parse_usage(&loc) {
        Ok(value) => value,
        Err(err) => panic!("codex fixture should parse: {err}"),
    };
    assert!(!events.is_empty(), "the codex fixture yields events");
    assert!(
        events.iter().all(|e| !e.is_sidechain),
        "Codex has no sidechain concept — every turn is mainline"
    );

    let rows = match focus_records_from_usage(&events) {
        Ok(value) => value,
        Err(err) => panic!("records should normalize: {err}"),
    };
    assert!(
        rows.iter()
            .all(|r| !r.x_sidechain && r.x_attribution_confidence == "confident"),
        "every Codex row is mainline + confident"
    );
}
