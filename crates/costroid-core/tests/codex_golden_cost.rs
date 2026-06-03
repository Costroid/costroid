//! End-to-end golden-dollar regression tests for the Codex cache-read
//! double-count bug.
//!
//! These drive the REAL Codex parser (`CodexProvider::parse_usage`) with raw
//! `last_token_usage` whose `input_tokens` INCLUDES `cached_input_tokens` — the
//! exact shape that triggered the bug — then price the parsed events through the
//! same path the app uses (`focus_records_from_usage`) and sum `billed_cost`.
//!
//! Before the fix the cached tokens are billed twice (input rate + cache-read
//! rate) and the totals overshoot by `cached × input_rate`, so fixtures 1–3 fail
//! loudly (e.g. fixture 1: $138.19 instead of $25.41). After the fix each bucket
//! is priced once and the totals land within a cent of the ccusage-verified
//! golden numbers.
//!
//! Fixture 4 (claude-sonnet-4-6) is built directly as a `UsageEvent` and routes
//! through the (already-correct) Anthropic path — it CANNOT reproduce the Codex
//! bug. It is here as a coverage guard for the multi-meter Anthropic case,
//! notably the `cache_write` rate ($3.75/1M) that gpt-5.5 has no rate for.

use std::path::{Path, PathBuf};

use costroid_core::focus_records_from_usage;
use costroid_providers::{
    AccessPath, CodexProvider, DataLocation, Provider, ProviderId, UsageEvent,
};
use rust_decimal::Decimal;

fn codex_fixtures_dir() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../../fixtures/codex")
}

/// Parse a single Codex rollout fixture and sum the priced cost across every
/// emitted meter row.
fn codex_fixture_total_cost(file_name: &str) -> Decimal {
    let dir = codex_fixtures_dir();
    let loc = DataLocation {
        provider: ProviderId::Codex,
        root: dir.clone(),
        files: vec![dir.join(file_name)],
    };
    let events = match CodexProvider.parse_usage(&loc) {
        Ok(value) => value,
        Err(err) => panic!("codex golden fixture should parse: {err}"),
    };
    let rows = match focus_records_from_usage(&events) {
        Ok(value) => value,
        Err(err) => panic!("records should price: {err}"),
    };
    rows.iter().map(|row| row.billed_cost).sum()
}

/// Assert a computed total is within $0.01 of the ccusage-verified golden value.
fn assert_within_a_cent(actual: Decimal, expected: Decimal) {
    assert!(
        (actual - expected).abs() <= Decimal::new(1, 2),
        "expected ~{expected}, got {actual}"
    );
}

#[test]
fn codex_gpt55_cache_heavy_1_costs_2541() {
    // gpt-5.5: uncached input 1,715,819 · output 185,271 · cache_read 22,555,520.
    // Raw fixture input = 1,715,819 + 22,555,520. Buggy code ≈ $138.19.
    assert_within_a_cent(
        codex_fixture_total_cost("golden-gpt55-cache-heavy-1.jsonl"),
        Decimal::new(2541, 2),
    );
}

#[test]
fn codex_gpt55_cache_heavy_2_costs_2640() {
    // gpt-5.5: uncached input 1,408,452 · output 145,142 · cache_read 30,006,656.
    // Buggy code ≈ $176.43.
    assert_within_a_cent(
        codex_fixture_total_cost("golden-gpt55-cache-heavy-2.jsonl"),
        Decimal::new(2640, 2),
    );
}

#[test]
fn codex_gpt55_small_3_costs_058() {
    // gpt-5.5: uncached input 54,770 · output 7,990 · cache_read 135,296.
    // Buggy code ≈ $1.26.
    assert_within_a_cent(
        codex_fixture_total_cost("golden-gpt55-small-3.jsonl"),
        Decimal::new(58, 2),
    );
}

#[test]
fn sonnet_multi_meter_costs_004() {
    // COVERAGE GUARD, not a Codex-bug regression test: claude-sonnet-4-6 routes
    // through the already-correct Anthropic path (input is cache-exclusive), so
    // this passes on buggy code too. It exercises all four meters — in 3 · out 60
    // · cache_read 12,037 · cache_create 10,174 — and confirms cache_write is
    // billed at $3.75/1M, which gpt-5.5 fixtures cannot cover.
    let timestamp = match "2026-05-01T10:00:00Z".parse::<chrono::DateTime<chrono::Utc>>() {
        Ok(value) => value,
        Err(err) => panic!("valid timestamp: {err}"),
    };
    let event = UsageEvent {
        tool: ProviderId::ClaudeCode,
        // Catalog key, not the display name "sonnet-4-6" — must match to price.
        model: "claude-sonnet-4-6".to_string(),
        timestamp,
        input_tokens: 3,
        output_tokens: 60,
        cache_read_tokens: 12_037,
        cache_write_tokens: 10_174,
        project: Some("/home/example/project".to_string()),
        access_path: AccessPath::Api,
    };
    let rows = match focus_records_from_usage(&[event]) {
        Ok(value) => value,
        Err(err) => panic!("records should price: {err}"),
    };
    let total: Decimal = rows.iter().map(|row| row.billed_cost).sum();
    assert_within_a_cent(total, Decimal::new(4, 2));
}
