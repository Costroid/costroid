//! Value-preserving golden snapshot for the FOCUS v1.2 → v1.3 round trip (M1 fold-in).
//!
//! The `focus_conformance.sh` gate proves the re-emitted output is 1.3-CONFORMANT (a
//! structural upgrade — never byte-identical to the v1.2 input). This test is the
//! complementary VALUE net: it pins the exact v1.3 CSV the library path produces for the
//! committed synthetic v1.2 fixture, so any drift in the mapping or the source-priced
//! bridge (a changed cost, lane, model, token count, `x_FocusInputVersion`, …) fails
//! loudly against the committed golden. The one legitimately-varying field —
//! `x_CollectorVersion` (the Costroid version stamp) — is normalized to a placeholder on
//! both sides so a version bump doesn't churn the golden.

use std::path::{Path, PathBuf};

use costroid_core::{export_focus_csv, focus_records_from_v12_import};
use costroid_focus::COLLECTOR_VERSION;
use costroid_providers::focus_import::import_focus_csv;

const VERSION_PLACEHOLDER: &str = "<COLLECTOR_VERSION>";

fn repo_path(rel: &str) -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../..")
        .join(rel)
}

/// Replace the live collector-version stamp with a stable placeholder so the golden is
/// version-agnostic. The stamp is the `x_CollectorVersion` column, now followed by the
/// (empty, on these source-priced rows) `x_PricingSnapshotId` column — i.e. `,<version>,`
/// near the end of every data row.
fn normalize_version(csv: &str) -> String {
    let needle = format!(",{COLLECTOR_VERSION},\n");
    csv.replace(&needle, &format!(",{VERSION_PLACEHOLDER},\n"))
}

#[test]
fn v12_marked_round_trip_matches_the_committed_golden() {
    let input =
        match std::fs::read_to_string(repo_path("fixtures/focus/v1.2/synthetic-v12-marked.csv")) {
            Ok(value) => value,
            Err(err) => panic!("fixture should read: {err}"),
        };
    let import = match import_focus_csv(&input) {
        Ok(value) => value,
        Err(err) => panic!("import should succeed: {err}"),
    };
    let rows = match focus_records_from_v12_import(&import.events, &import.detection.version) {
        Ok(value) => value,
        Err(err) => panic!("bridge should succeed: {err}"),
    };
    let produced = match export_focus_csv(&rows) {
        Ok(value) => value,
        Err(err) => panic!("export should succeed: {err}"),
    };

    let golden = match std::fs::read_to_string(repo_path(
        "fixtures/focus/v1.2/golden/synthetic-v12-marked.v13.csv",
    )) {
        Ok(value) => value,
        Err(err) => panic!("golden should read: {err}"),
    };

    assert_eq!(
        normalize_version(&produced),
        golden,
        "v1.2→v1.3 round-trip drifted from the committed golden \
         (fixtures/focus/v1.2/golden/synthetic-v12-marked.v13.csv). If this change is \
         intentional, regenerate the golden in the same commit."
    );

    // Non-vacuous guards: the golden actually carries the value-preserving signal, so a
    // future accidental over-normalization (e.g. blanking the file) can't pass.
    assert!(
        golden.contains("cloud_api"),
        "golden carries the cloud lane"
    );
    assert!(
        golden.contains(",1.2,"),
        "golden carries x_FocusInputVersion=1.2"
    );
    assert!(
        golden.contains("0.0123"),
        "golden preserves the source cost"
    );
    assert!(
        golden.contains(VERSION_PLACEHOLDER),
        "golden is version-normalized"
    );
    // T4: the foreign per-token pricing detail is carried through (was null before T4) — so
    // a regression that dropped it back to null can't silently pass this golden.
    assert!(
        golden.contains("claude-sonnet-4-6-output"),
        "golden carries the foreign SkuPriceId"
    );
    assert!(
        golden.contains("0.0000015"),
        "golden carries the foreign per-token ListUnitPrice"
    );
}
