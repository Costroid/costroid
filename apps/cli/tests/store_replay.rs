//! Store replay → aggregate + export round-trip (T11), gated on the off-by-default
//! `store` feature.
//!
//! The store (`costroid-store`) is a `costroid-focus(+chrono+rust_decimal)` leaf — it does
//! NOT depend on `costroid-core`, so the replay-aggregate + export round-trip is the
//! caller's job. `apps/cli` has both the store and the core, so this is where the wiring is
//! proven: ingest a Vec of FocusRecords, replay them via `Store::all_focus_rows`, and assert
//!
//!   * `costroid_core::aggregate_rows(&reconstructed, ..) == aggregate_rows(&original, ..)`
//!     (replay preserves the ledger the aggregation engine sees), AND
//!   * `export_focus_csv(&reconstructed)` is BYTE-IDENTICAL to `export_focus_csv(&original)`
//!     (replay preserves the exact FOCUS export).
//!
//! Crucially, the priced fixtures are built through the REAL pricing path
//! (`costroid_core::focus_records_from_usage` for the dominant catalog-priced dev-tool
//! shape — `apply_pricing` populates the SKU / unit-price / quantity columns — and
//! `focus_records_from_canonical(Cloud{..})` for the source-priced cloud shape), so the
//! round-trip actually exercises the cost/pricing columns the store must persist. A
//! regression to the earlier lossy store (which dropped `list_cost` / `pricing_quantity` /
//! `list_unit_price` / the rest, reverting them to the unpriced 0/None) breaks the
//! byte-identical assertion below.
//!
//! It is `#[cfg(feature = "store")]` so it only compiles/runs under `--features store`; the
//! default CLI build never links the store, so this never touches the default graph.
#![cfg(feature = "store")]

use chrono::{LocalResult, TimeZone, Utc};
use costroid_core::{
    aggregate_rows, export_focus_csv, focus_records_from_canonical, focus_records_from_usage,
    GroupBy,
};
use costroid_focus::FocusRecord;
use costroid_providers::{AccessPath, CanonicalEvent, CloudUsageEvent, ProviderId, UsageEvent};
use costroid_store::Store;
use rust_decimal::Decimal;

fn timestamp(day: u32, hour: u32) -> chrono::DateTime<Utc> {
    match Utc.with_ymd_and_hms(2026, 3, day, hour, 0, 0) {
        LocalResult::Single(value) => value,
        LocalResult::Ambiguous(_, _) | LocalResult::None => {
            panic!("test timestamp should be valid")
        }
    }
}

/// A **catalog-priced** developer_tool row built through the REAL pricing path
/// (`focus_records_from_usage` → `apply_pricing`): `model` MUST be in the bundled pricing
/// catalog so `apply_pricing` runs and populates the SKU / unit-price / quantity columns
/// (`list_cost` / `contracted_cost` / `pricing_currency_effective_cost` / `consumed_quantity`
/// / `pricing_quantity` / `list_unit_price` / `contracted_unit_price` / the two
/// `pricing_currency_*_unit_price` / `sku_price_id` / `pricing_category` / `pricing_unit`).
/// This is the dominant path; persisting only billed/effective_cost would silently revert
/// every one of those to its unpriced default on replay.
fn priced_dev_tool_rows(model: &str, input_tokens: u64, day: u32) -> Vec<FocusRecord> {
    let event = UsageEvent {
        tool: ProviderId::ClaudeCode,
        model: model.to_string(),
        timestamp: timestamp(day, 12),
        input_tokens,
        output_tokens: 0,
        cache_read_tokens: 0,
        cache_write_tokens: 0,
        project: Some("/work/alpha".to_string()),
        access_path: AccessPath::Api,
    };
    match focus_records_from_usage(&[event]) {
        Ok(rows) => rows,
        Err(err) => panic!("priced dev-tool rows should build: {err}"),
    }
}

/// A **source-priced cloud_api** row built through the REAL cloud path
/// (`focus_records_from_canonical(Cloud{..})` → `cloud_usage_to_focus`): an authoritative
/// `billed_cost` stamps `billed_cost` / `effective_cost` / `list_cost` / `contracted_cost` /
/// `pricing_currency_effective_cost` and flips `x_estimated = false`, `x_pricing_status =
/// "priced"`. This covers the second priced shape (the cloud columns the lossy store also
/// dropped).
fn priced_cloud_rows() -> Vec<FocusRecord> {
    let cloud = CloudUsageEvent {
        timestamp: timestamp(5, 9),
        service_name: "Anthropic API".to_string(),
        service_provider_name: "Anthropic".to_string(),
        model: Some("claude-opus".to_string()),
        token_count: Some(4_096),
        billed_cost: Some("1.2345".to_string()),
    };
    match focus_records_from_canonical(&[CanonicalEvent::Cloud(cloud)]) {
        Ok(rows) => rows,
        Err(err) => panic!("priced cloud rows should build: {err}"),
    }
}

#[test]
fn store_replay_preserves_aggregate_and_export_byte_for_byte() {
    let store = match Store::open_in_memory() {
        Ok(value) => value,
        Err(err) => panic!("in-memory store should open: {err}"),
    };

    // Build the original ledger entirely through the REAL pricing path so the priced
    // cost/pricing columns are genuinely populated (not hand-set).
    let mut original: Vec<FocusRecord> = Vec::new();
    original.extend(priced_dev_tool_rows("claude-sonnet-4-6", 2_000, 1));
    original.extend(priced_dev_tool_rows("claude-sonnet-4-6", 1_500, 2)); // same model -> same group
    original.extend(priced_dev_tool_rows("claude-haiku-4-5", 500, 3));
    original.extend(priced_cloud_rows());

    // Sanity: the catalog-priced rows really did populate the columns the lossy store
    // dropped — otherwise the round-trip below would be vacuous. Find a catalog-priced
    // dev-tool row and assert its priced columns are non-default BEFORE the round-trip.
    let priced = match original
        .iter()
        .find(|row| row.x_lane == "developer_tool" && row.x_pricing_status == "priced")
    {
        Some(row) => row,
        None => panic!(
            "a catalog-priced dev-tool row must exist (the fixture is the apply_pricing path)"
        ),
    };
    assert!(
        priced.list_cost > Decimal::ZERO,
        "fixture pre-check: a priced dev-tool row's list_cost must be non-zero"
    );
    assert!(
        matches!(priced.pricing_quantity, Some(q) if q > Decimal::ZERO),
        "fixture pre-check: a priced dev-tool row's pricing_quantity must be Some(non-zero)"
    );
    assert!(
        matches!(priced.list_unit_price, Some(p) if p > Decimal::ZERO),
        "fixture pre-check: a priced dev-tool row's list_unit_price must be Some(non-zero)"
    );
    assert!(
        priced.sku_price_id.is_some(),
        "fixture pre-check: a priced dev-tool row's sku_price_id must be Some"
    );
    assert!(
        priced.pricing_category.is_some() && priced.pricing_unit.is_some(),
        "fixture pre-check: a priced dev-tool row's pricing_category/pricing_unit must be Some"
    );

    let count = match store.ingest(&original) {
        Ok(value) => value,
        Err(err) => panic!("ingest should succeed: {err}"),
    };
    assert_eq!(count, original.len());

    let reconstructed = match store.all_focus_rows() {
        Ok(value) => value,
        Err(err) => panic!("all_focus_rows should succeed: {err}"),
    };

    // The deciding assertion: full FocusRecord equality, so EVERY now-persisted priced
    // column survived (the lossy store reverted list_cost/pricing_quantity/list_unit_price/
    // sku_price_id/… to 0/None, which would fail this).
    assert_eq!(
        reconstructed, original,
        "replay must reconstruct every FocusRecord exactly, including the priced columns"
    );

    // Replay preserves the ledger the aggregation engine sees, across every grouping.
    for group_by in [GroupBy::Model, GroupBy::App, GroupBy::Total] {
        assert_eq!(
            aggregate_rows(&reconstructed, group_by),
            aggregate_rows(&original, group_by),
            "aggregate differs after replay for {group_by:?}"
        );
    }
    // Non-vacuous: the dev-tool aggregate actually has rows (the §170 gate didn't drop
    // everything), so the equality above is meaningful.
    assert!(
        !aggregate_rows(&original, GroupBy::Model).is_empty(),
        "the dev-tool aggregate must be non-empty for the replay equality to be meaningful"
    );

    // Replay preserves the exact FOCUS CSV export, byte-for-byte — the load-bearing proof
    // that the persisted priced columns round-trip (the CSV serializes every cost/pricing
    // column, so a dropped column would print 0/empty here and break this).
    let original_csv = match export_focus_csv(&original) {
        Ok(value) => value,
        Err(err) => panic!("original csv should serialize: {err}"),
    };
    let replayed_csv = match export_focus_csv(&reconstructed) {
        Ok(value) => value,
        Err(err) => panic!("replayed csv should serialize: {err}"),
    };
    assert_eq!(
        replayed_csv, original_csv,
        "CSV export must be byte-identical after store replay"
    );
}
