//! End-to-end golden-dollar regression test for Claude usage de-duplication.
//!
//! Drives the REAL Claude parser (`ClaudeCodeProvider::parse_usage`, which now
//! de-dupes by `(message.id, requestId)`) over two transcript files that together
//! hold six assistant lines for two real turns:
//!   - entry X streamed three times (output 1 -> 20000 -> 40000, input/cache fixed)
//!   - entry Y once, then both finalized messages copied verbatim into a second
//!     file (resume-style cross-file duplication)
//!
//! It then prices the de-duped events through `focus_records_from_usage` and sums
//! `billed_cost`.
//!
//! The golden total is computed against OUR bundled `pricing.v1.json` rates
//! (claude-sonnet-4-6: input $3, output $15, cache_read $0.30, cache_write $3.75
//! per 1M) on the DE-DUPED token set — never hard-coded to ccusage's figure:
//!   X: (100000*3 + 40000*15 + 1000000*0.30 + 40000*3.75)/1e6 = $1.35
//!   Y: (200000*3 + 60000*15 + 5000000*0.30 +     0*3.75)/1e6 = $3.00
//!   total = $4.35
//! Before de-dup the six occurrences are all counted and the total balloons well
//! past $4.35, so this fails loudly on pre-fix code.

use std::path::{Path, PathBuf};

use costroid_core::focus_records_from_usage;
use costroid_providers::{ClaudeCodeProvider, DataLocation, Provider, ProviderId};
use rust_decimal::Decimal;

fn claude_fixtures_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/claude-code")
}

#[test]
fn claude_deduped_cost_is_435() {
    let dir = claude_fixtures_dir();
    let loc = DataLocation {
        provider: ProviderId::ClaudeCode,
        root: dir.clone(),
        files: vec![
            dir.join("dedup-golden-a.jsonl"),
            dir.join("dedup-golden-b.jsonl"),
        ],
    };

    let events = match ClaudeCodeProvider.parse_usage(&loc) {
        Ok(value) => value,
        Err(err) => panic!("claude dedup fixtures should parse: {err}"),
    };
    // Six assistant lines collapse to two unique turns before pricing.
    assert_eq!(events.len(), 2);

    let rows = match focus_records_from_usage(&events) {
        Ok(value) => value,
        Err(err) => panic!("records should price: {err}"),
    };
    let total: Decimal = rows.iter().map(|row| row.billed_cost).sum();

    // De-duped tokens x OUR rates = $4.35, exact in Decimal; assert within $0.01.
    assert!(
        (total - Decimal::new(435, 2)).abs() <= Decimal::new(1, 2),
        "expected ~4.35, got {total}"
    );
}
