//! Costroid's local SQLite usage store — a metadata-only persistent ledger.
//!
//! # R4, the Cardinal Rule: the store cannot hold content
//!
//! The single [`usage_rows`](USAGE_ROWS_TABLE) table is an **explicit metadata
//! allowlist** ([`USAGE_ROWS_COLUMNS`]). It persists only the bounded, non-free-text
//! metadata needed to reconstruct a [`FocusRecord`] on replay (a later milestone) via
//! [`FocusRecord::unpriced_usage`] plus cost/pricing overrides: token counts, model,
//! lane, the full priced cost set + the catalog-priced quantity/unit-price columns (all
//! as decimal *strings*, never floats), the charge timestamp, provider identity, and the
//! bounded SKU + pricing-category/unit identifiers — so a *priced* row round-trips
//! byte-identically rather than reverting to the unpriced 0.
//!
//! It deliberately **drops every free-text-capable or non-derivable FOCUS column** —
//! `ChargeDescription` (the derived `"{model} {token_type} tokens"`, re-derived on
//! replay), `ResourceId` / `ResourceName` / `ResourceType`, `Tags` / `AllocatedTags`,
//! `SkuPriceDetails`, and the region / sub-account / availability-zone columns. The
//! store is therefore *structurally incapable* of holding prompt or response content.
//! The fail-closed [`r4_schema_forbids_free_text_columns`](#) test asserts the DDL
//! carries none of the forbidden substrings and that every column is in the allowlist —
//! so adding a column to the DDL without reviewing it fails the build (mirroring the
//! `CONNECT_ALLOWED` / `SERVER_ALLOWED` fail-closed allowlists).
//!
//! # Money is a decimal string, never a float
//!
//! Every cost column is stored as a `TEXT` decimal string (mirroring the FOCUS export),
//! never `f64` — exact decimal arithmetic, no binary-float drift.

use std::path::Path;
use std::str::FromStr;

use chrono::{DateTime, Utc};
use costroid_focus::{
    FocusAccessPath, FocusRecord, LedgerLane, TokenType, UnpricedUsage, FOCUS_VERSION,
};
use rusqlite::{Connection, OptionalExtension};
use rust_decimal::Decimal;
use thiserror::Error;

/// The schema version of the [`usage_rows`](USAGE_ROWS_TABLE) layout. Bumped whenever
/// the persisted column set changes so a future replay/migration step can tell shapes
/// apart. Stamped into [`STORE_META_TABLE`] on open.
pub const SCHEMA_VERSION: i64 = 6;

/// The one persisted table.
pub const USAGE_ROWS_TABLE: &str = "usage_rows";

/// The tiny stamp table holding [`SCHEMA_VERSION`] + the [`FOCUS_VERSION`] the rows were
/// written under (so a later replay/migration can detect a FOCUS-shape change).
pub const STORE_META_TABLE: &str = "store_meta";

/// The **explicit metadata allowlist** — every column the [`usage_rows`](USAGE_ROWS_TABLE)
/// table is permitted to carry, and nothing else (R4, the Cardinal Rule).
///
/// This is the single source of truth the fail-closed schema test checks the live DDL
/// against: every real column must be a member, so a column added to the DDL without
/// also being reviewed into this list fails the test. Each entry is bounded metadata
/// (an enum string, an identifier, a decimal string, or a timestamp) — never a
/// free-text-capable or non-derivable FOCUS column.
pub const USAGE_ROWS_COLUMNS: &[&str] = &[
    // Time — RFC 3339 (UTC) string; the FOCUS charge-period start of the meter row.
    "charge_period_start",
    // Costroid x_ taxonomy — all bounded enum/identifier strings.
    "x_lane",
    "x_tool",
    "x_model",
    "x_token_type",
    "x_access_path",
    "x_pricing_status",
    "x_estimated", // INTEGER 0/1
    "x_project",   // nullable; a project path/label, not free-text content
    // Costs — decimal STRINGS (never floats); raw token count as a decimal string.
    "billed_cost",
    "effective_cost",
    // The remaining FOCUS cost columns a priced row sets — non-null `Decimal`
    // (`cloud_usage_to_focus` + `apply_pricing` stamp these); decimal STRINGS, never
    // floats. Dropping them previously reverted a real priced row to the unpriced 0.
    "list_cost",
    "contracted_cost",
    "pricing_currency_effective_cost",
    "x_consumed_tokens",
    "billing_currency",
    // Quantity / unit-price columns a catalog-priced SKU populates (`apply_pricing`) —
    // nullable `Option<Decimal>` (null on an unpriced row, Some on a priced one);
    // decimal STRINGS, never floats. Bounded numeric metadata (R4-safe).
    "consumed_quantity",
    "pricing_quantity",
    "list_unit_price",
    "contracted_unit_price",
    "pricing_currency_list_unit_price",
    "pricing_currency_contracted_unit_price",
    // Provider identity — bounded identifier strings.
    "service_name",
    "service_provider_name",
    "host_provider_name",
    "invoice_issuer_name",
    // SKU / pricing identifiers — bounded enum/identifier strings; NOT
    // `sku_price_details` (dropped, free-text-capable). `sku_price_id`, `pricing_category`,
    // and `pricing_unit` are populated by `apply_pricing` on a catalog-priced row.
    "sku_id",
    "sku_price_id",
    "sku_meter",
    "pricing_category",
    "pricing_unit",
    // Import provenance — the FOCUS spec version a row was imported from (nullable; a
    // bounded version string like "1.2", null on non-imported rows). Never content.
    "x_focus_input_version",
    // Collector attribution taxonomy — all bounded enum/flag/version strings, never content.
    "x_sidechain",              // INTEGER 0/1
    "x_attribution_confidence", // "confident" / "uncertain"
    "x_collector_version",      // the Costroid version that minted the row
    // Pricing provenance — the bundled/override snapshot that priced an estimated row
    // ("{source}@{as_of}#{hash8}"); nullable (null on source-priced/unpriced). Never content.
    "x_pricing_snapshot_id",
    // Bedrock application-inference-profile id (bounded system id; nullable, null off-Bedrock).
    // Never the profile name/tags. Never content.
    "x_inference_profile_id",
];

/// `CREATE TABLE` DDL for the [`usage_rows`](USAGE_ROWS_TABLE) metadata-allowlist table.
///
/// Exposed as a function so the fail-closed R4 schema test can read the exact DDL the
/// store applies and assert it carries no free-text column. Column order mirrors
/// [`USAGE_ROWS_COLUMNS`].
pub fn usage_rows_ddl() -> String {
    format!(
        "CREATE TABLE IF NOT EXISTS {USAGE_ROWS_TABLE} (\n  \
            id INTEGER PRIMARY KEY,\n  \
            charge_period_start TEXT NOT NULL,\n  \
            x_lane TEXT NOT NULL,\n  \
            x_tool TEXT NOT NULL,\n  \
            x_model TEXT NOT NULL,\n  \
            x_token_type TEXT NOT NULL,\n  \
            x_access_path TEXT NOT NULL,\n  \
            x_pricing_status TEXT NOT NULL,\n  \
            x_estimated INTEGER NOT NULL,\n  \
            x_project TEXT,\n  \
            billed_cost TEXT NOT NULL,\n  \
            effective_cost TEXT NOT NULL,\n  \
            list_cost TEXT NOT NULL,\n  \
            contracted_cost TEXT NOT NULL,\n  \
            pricing_currency_effective_cost TEXT NOT NULL,\n  \
            x_consumed_tokens TEXT NOT NULL,\n  \
            billing_currency TEXT NOT NULL,\n  \
            consumed_quantity TEXT,\n  \
            pricing_quantity TEXT,\n  \
            list_unit_price TEXT,\n  \
            contracted_unit_price TEXT,\n  \
            pricing_currency_list_unit_price TEXT,\n  \
            pricing_currency_contracted_unit_price TEXT,\n  \
            service_name TEXT NOT NULL,\n  \
            service_provider_name TEXT NOT NULL,\n  \
            host_provider_name TEXT NOT NULL,\n  \
            invoice_issuer_name TEXT NOT NULL,\n  \
            sku_id TEXT,\n  \
            sku_price_id TEXT,\n  \
            sku_meter TEXT,\n  \
            pricing_category TEXT,\n  \
            pricing_unit TEXT,\n  \
            x_focus_input_version TEXT,\n  \
            x_sidechain INTEGER NOT NULL,\n  \
            x_attribution_confidence TEXT NOT NULL,\n  \
            x_collector_version TEXT NOT NULL,\n  \
            x_pricing_snapshot_id TEXT,\n  \
            x_inference_profile_id TEXT\n\
        )"
    )
}

/// `CREATE TABLE` DDL for the [`store_meta`](STORE_META_TABLE) stamp table.
fn store_meta_ddl() -> String {
    format!(
        "CREATE TABLE IF NOT EXISTS {STORE_META_TABLE} (\n  \
            key TEXT PRIMARY KEY,\n  \
            value TEXT NOT NULL\n\
        )"
    )
}

/// Errors the store can return. Typed (no `unwrap`/`expect`/`panic!`); wraps the
/// underlying SQLite error and the schema-stamp mismatch case.
#[derive(Debug, Error)]
pub enum StoreError {
    /// A `rusqlite` (SQLite) error — open, prepare, execute, or query failed.
    #[error("sqlite error: {0}")]
    Sqlite(#[from] rusqlite::Error),

    /// The opened database carries a `schema_version` the running code does not know how
    /// to read (a future migration step's job; for now it is a fail-closed mismatch).
    #[error("unsupported store schema version: found {found}, expected {expected}")]
    SchemaVersion { found: i64, expected: i64 },

    /// A persisted row could not be reconstructed into a [`FocusRecord`] on replay — a
    /// malformed stored enum (an `x_lane`/`x_token_type`/`x_access_path` value with no
    /// `from_focus_str` inverse), an unparseable timestamp, or an unparseable decimal
    /// string. The store fails closed with this typed error rather than panicking or
    /// silently substituting a default (which would corrupt the replayed ledger).
    #[error("failed to reconstruct a FocusRecord from a stored row: {0}")]
    Reconstruct(String),

    /// Building the FOCUS record from the reconstructed [`UnpricedUsage`] failed — the
    /// underlying [`costroid_focus::FocusError`] (e.g. an out-of-range timestamp for the
    /// FOCUS billing-period calculation), surfaced typed.
    #[error("failed to build a FocusRecord on replay: {0}")]
    Focus(#[from] costroid_focus::FocusError),
}

/// The local SQLite usage store. Wraps a single [`rusqlite::Connection`]; the schema is
/// applied (and the version stamp checked) on open.
pub struct Store {
    conn: Connection,
}

impl Store {
    /// Open an in-memory store (used by tests and ephemeral runs). The schema is created
    /// and stamped immediately.
    pub fn open_in_memory() -> Result<Self, StoreError> {
        let conn = Connection::open_in_memory()?;
        Self::from_connection(conn)
    }

    /// Open (creating if absent) a file-backed store at `path`. The schema is created if
    /// the file is new, and the version stamp is checked against [`SCHEMA_VERSION`].
    pub fn open(path: &Path) -> Result<Self, StoreError> {
        let conn = Connection::open(path)?;
        Self::from_connection(conn)
    }

    /// Apply the schema (idempotent), then read/write the version stamp. A pre-existing
    /// database carrying a different `schema_version` is a fail-closed
    /// [`StoreError::SchemaVersion`].
    fn from_connection(conn: Connection) -> Result<Self, StoreError> {
        conn.execute_batch(&format!("{};\n{};", store_meta_ddl(), usage_rows_ddl()))?;

        let existing: Option<String> = conn
            .query_row(
                &format!("SELECT value FROM {STORE_META_TABLE} WHERE key = 'schema_version'"),
                [],
                |row| row.get(0),
            )
            .optional()?;

        match existing {
            Some(value) => {
                let found: i64 = value.parse().unwrap_or(-1);
                if found != SCHEMA_VERSION {
                    return Err(StoreError::SchemaVersion {
                        found,
                        expected: SCHEMA_VERSION,
                    });
                }
            }
            None => {
                conn.execute(
                    &format!(
                        "INSERT INTO {STORE_META_TABLE} (key, value) VALUES \
                         ('schema_version', ?1), ('focus_version', ?2)"
                    ),
                    rusqlite::params![SCHEMA_VERSION.to_string(), FOCUS_VERSION],
                )?;
            }
        }

        Ok(Self { conn })
    }

    /// Insert the whitelisted metadata columns from each [`FocusRecord`]. Runs in a
    /// single transaction; returns the number of rows inserted.
    ///
    /// Only the [`USAGE_ROWS_COLUMNS`] allowlist is read off each record — the derived
    /// `ChargeDescription` and every free-text-capable / non-derivable FOCUS column are
    /// never touched (R4).
    pub fn ingest(&self, rows: &[FocusRecord]) -> Result<usize, StoreError> {
        let tx = self.conn.unchecked_transaction()?;
        {
            let mut stmt = tx.prepare(&format!(
                "INSERT INTO {USAGE_ROWS_TABLE} (\
                    charge_period_start, x_lane, x_tool, x_model, x_token_type, \
                    x_access_path, x_pricing_status, x_estimated, x_project, \
                    billed_cost, effective_cost, list_cost, contracted_cost, \
                    pricing_currency_effective_cost, x_consumed_tokens, billing_currency, \
                    consumed_quantity, pricing_quantity, list_unit_price, \
                    contracted_unit_price, pricing_currency_list_unit_price, \
                    pricing_currency_contracted_unit_price, service_name, \
                    service_provider_name, host_provider_name, invoice_issuer_name, \
                    sku_id, sku_price_id, sku_meter, pricing_category, pricing_unit, \
                    x_focus_input_version, x_sidechain, x_attribution_confidence, \
                    x_collector_version, x_pricing_snapshot_id, x_inference_profile_id\
                ) VALUES (\
                    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, \
                    ?16, ?17, ?18, ?19, ?20, ?21, ?22, ?23, ?24, ?25, ?26, ?27, ?28, \
                    ?29, ?30, ?31, ?32, ?33, ?34, ?35, ?36, ?37\
                )"
            ))?;

            for row in rows {
                stmt.execute(rusqlite::params![
                    row.charge_period_start.to_rfc3339(),
                    row.x_lane,
                    row.x_tool,
                    row.x_model,
                    row.x_token_type,
                    row.x_access_path,
                    row.x_pricing_status,
                    i64::from(row.x_estimated),
                    row.x_project,
                    row.billed_cost.to_string(),
                    row.effective_cost.to_string(),
                    row.list_cost.to_string(),
                    row.contracted_cost.to_string(),
                    row.pricing_currency_effective_cost.to_string(),
                    row.x_consumed_tokens.to_string(),
                    row.billing_currency,
                    decimal_opt_to_string(&row.consumed_quantity),
                    decimal_opt_to_string(&row.pricing_quantity),
                    decimal_opt_to_string(&row.list_unit_price),
                    decimal_opt_to_string(&row.contracted_unit_price),
                    decimal_opt_to_string(&row.pricing_currency_list_unit_price),
                    decimal_opt_to_string(&row.pricing_currency_contracted_unit_price),
                    row.service_name,
                    row.service_provider_name,
                    row.host_provider_name,
                    row.invoice_issuer_name,
                    row.sku_id,
                    row.sku_price_id,
                    row.sku_meter,
                    row.pricing_category,
                    row.pricing_unit,
                    row.x_focus_input_version,
                    i64::from(row.x_sidechain),
                    row.x_attribution_confidence,
                    row.x_collector_version,
                    row.x_pricing_snapshot_id,
                    row.x_inference_profile_id,
                ])?;
            }
        }
        tx.commit()?;
        Ok(rows.len())
    }

    /// The number of rows currently persisted in [`usage_rows`](USAGE_ROWS_TABLE).
    pub fn row_count(&self) -> Result<usize, StoreError> {
        let count: i64 = self.conn.query_row(
            &format!("SELECT COUNT(*) FROM {USAGE_ROWS_TABLE}"),
            [],
            |row| row.get(0),
        )?;
        Ok(count.max(0) as usize)
    }

    /// Faithfully reconstruct every persisted row back into a [`FocusRecord`], in
    /// insertion order (T11 replay).
    ///
    /// For each row this rebuilds an [`UnpricedUsage`] from the whitelisted columns —
    /// the three enums via [`LedgerLane::from_focus_str`] / [`TokenType::from_focus_str`]
    /// / [`FocusAccessPath::from_focus_str`] (the single source of truth for the
    /// string↔enum mapping; a malformed stored enum yields a typed
    /// [`StoreError::Reconstruct`], never a panic or silent default) — calls
    /// [`FocusRecord::unpriced_usage`] (which re-derives `charge_description` and the
    /// always-null non-persisted FOCUS columns identically), then applies the stored
    /// cost/pricing overrides — the full cost set (`billed_cost` / `effective_cost` /
    /// `list_cost` / `contracted_cost` / `pricing_currency_effective_cost`), the
    /// catalog-priced quantity/unit-price columns (`consumed_quantity` /
    /// `pricing_quantity` / `list_unit_price` / `contracted_unit_price` /
    /// `pricing_currency_list_unit_price` / `pricing_currency_contracted_unit_price`),
    /// `x_estimated` / `x_pricing_status` / `x_consumed_tokens`, and the stored SKU +
    /// pricing identifiers (`sku_id` / `sku_price_id` / `sku_meter` / `pricing_category`
    /// / `pricing_unit`) — so a **source-priced cloud row** OR a **catalog-priced
    /// dev-tool row** (`x_estimated = false`, `x_pricing_status = "priced"`, a real cost,
    /// populated unit-price/quantity columns) reconstructs faithfully rather than
    /// collapsing to the unpriced default.
    ///
    /// The store names no `costroid-core` type: the replay-aggregate + export round-trip
    /// is the caller's (apps/cli) job — this method only rebuilds the records.
    pub fn all_focus_rows(&self) -> Result<Vec<FocusRecord>, StoreError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT charge_period_start, x_lane, x_tool, x_model, x_token_type, \
                x_access_path, x_pricing_status, x_estimated, x_project, billed_cost, \
                effective_cost, list_cost, contracted_cost, \
                pricing_currency_effective_cost, x_consumed_tokens, billing_currency, \
                consumed_quantity, pricing_quantity, list_unit_price, \
                contracted_unit_price, pricing_currency_list_unit_price, \
                pricing_currency_contracted_unit_price, service_name, \
                service_provider_name, host_provider_name, invoice_issuer_name, sku_id, \
                sku_price_id, sku_meter, pricing_category, pricing_unit, \
                x_focus_input_version, x_sidechain, x_attribution_confidence, \
                x_collector_version, x_pricing_snapshot_id, x_inference_profile_id \
             FROM {USAGE_ROWS_TABLE} ORDER BY id"
        ))?;

        let mut records = Vec::new();
        let mut query = stmt.query([])?;
        while let Some(row) = query.next()? {
            records.push(Self::reconstruct_row(row)?);
        }
        Ok(records)
    }

    /// Reconstruct a single SQLite row into a [`FocusRecord`] (see [`all_focus_rows`]).
    /// Every fallible step (enum parse, timestamp parse, decimal parse) maps to a typed
    /// [`StoreError`] — no `unwrap`/`expect`/`panic!`, no silent default.
    fn reconstruct_row(row: &rusqlite::Row<'_>) -> Result<FocusRecord, StoreError> {
        // Columns in the SELECT order above.
        let charge_period_start: String = row.get(0)?;
        let x_lane: String = row.get(1)?;
        let x_tool: String = row.get(2)?;
        let x_model: String = row.get(3)?;
        let x_token_type: String = row.get(4)?;
        let x_access_path: String = row.get(5)?;
        let x_pricing_status: String = row.get(6)?;
        let x_estimated: i64 = row.get(7)?;
        let x_project: Option<String> = row.get(8)?;
        let billed_cost: String = row.get(9)?;
        let effective_cost: String = row.get(10)?;
        let list_cost: String = row.get(11)?;
        let contracted_cost: String = row.get(12)?;
        let pricing_currency_effective_cost: String = row.get(13)?;
        let x_consumed_tokens: String = row.get(14)?;
        let billing_currency: String = row.get(15)?;
        let consumed_quantity: Option<String> = row.get(16)?;
        let pricing_quantity: Option<String> = row.get(17)?;
        let list_unit_price: Option<String> = row.get(18)?;
        let contracted_unit_price: Option<String> = row.get(19)?;
        let pricing_currency_list_unit_price: Option<String> = row.get(20)?;
        let pricing_currency_contracted_unit_price: Option<String> = row.get(21)?;
        let service_name: String = row.get(22)?;
        let service_provider_name: String = row.get(23)?;
        let host_provider_name: String = row.get(24)?;
        let invoice_issuer_name: String = row.get(25)?;
        let sku_id: Option<String> = row.get(26)?;
        let sku_price_id: Option<String> = row.get(27)?;
        let sku_meter: Option<String> = row.get(28)?;
        let pricing_category: Option<String> = row.get(29)?;
        let pricing_unit: Option<String> = row.get(30)?;
        let x_focus_input_version: Option<String> = row.get(31)?;
        let x_sidechain: i64 = row.get(32)?;
        let x_attribution_confidence: String = row.get(33)?;
        let x_collector_version: String = row.get(34)?;
        let x_pricing_snapshot_id: Option<String> = row.get(35)?;
        let x_inference_profile_id: Option<String> = row.get(36)?;

        // Enums via the single-source-of-truth inverse: a malformed stored enum is a
        // typed Reconstruct error, never a panic or a silent default.
        let lane = LedgerLane::from_focus_str(&x_lane)
            .ok_or_else(|| StoreError::Reconstruct(format!("unknown x_lane `{x_lane}`")))?;
        let token_type = TokenType::from_focus_str(&x_token_type).ok_or_else(|| {
            StoreError::Reconstruct(format!("unknown x_token_type `{x_token_type}`"))
        })?;
        let access_path = FocusAccessPath::from_focus_str(&x_access_path).ok_or_else(|| {
            StoreError::Reconstruct(format!("unknown x_access_path `{x_access_path}`"))
        })?;

        // Timestamp: RFC 3339 (UTC) back to a DateTime<Utc>.
        let timestamp = DateTime::parse_from_rfc3339(&charge_period_start)
            .map_err(|err| {
                StoreError::Reconstruct(format!(
                    "unparseable charge_period_start `{charge_period_start}`: {err}"
                ))
            })?
            .with_timezone(&Utc);

        // Decimals: the exact decimal strings back to `Decimal` (never via f64).
        let billed = parse_decimal("billed_cost", &billed_cost)?;
        let effective = parse_decimal("effective_cost", &effective_cost)?;
        let list = parse_decimal("list_cost", &list_cost)?;
        let contracted = parse_decimal("contracted_cost", &contracted_cost)?;
        let pricing_currency_effective = parse_decimal(
            "pricing_currency_effective_cost",
            &pricing_currency_effective_cost,
        )?;
        // Nullable decimals: NULL → None; a present (non-null) value parses exactly or
        // fails closed (a stored Some that won't parse is malformed data, never legit).
        let consumed_qty = parse_decimal_opt("consumed_quantity", &consumed_quantity)?;
        let pricing_qty = parse_decimal_opt("pricing_quantity", &pricing_quantity)?;
        let list_unit = parse_decimal_opt("list_unit_price", &list_unit_price)?;
        let contracted_unit = parse_decimal_opt("contracted_unit_price", &contracted_unit_price)?;
        let pricing_currency_list_unit = parse_decimal_opt(
            "pricing_currency_list_unit_price",
            &pricing_currency_list_unit_price,
        )?;
        let pricing_currency_contracted_unit = parse_decimal_opt(
            "pricing_currency_contracted_unit_price",
            &pricing_currency_contracted_unit_price,
        )?;
        let consumed = parse_decimal("x_consumed_tokens", &x_consumed_tokens)?;
        // `UnpricedUsage::token_count` is a u64; the persisted token count is a whole
        // decimal. Convert exactly (no fractional tokens) and fail closed otherwise.
        let token_count = decimal_to_u64("x_consumed_tokens", &consumed)?;

        let input = UnpricedUsage {
            lane,
            timestamp,
            tool: x_tool,
            model: x_model,
            token_type,
            token_count,
            project: x_project,
            access_path,
            service_name,
            service_provider_name,
            host_provider_name,
            invoice_issuer_name,
            billing_currency,
        };

        let mut record = FocusRecord::unpriced_usage(input)?;
        // Apply the stored overrides so a source-priced row reconstructs faithfully —
        // `unpriced_usage` defaults cost=0 / x_estimated=true / x_pricing_status="missing_price".
        record.billed_cost = billed;
        record.effective_cost = effective;
        // The remaining priced cost columns: `cloud_usage_to_focus`/`apply_pricing` stamp
        // these on a priced row; restore them verbatim so a real priced row no longer
        // reverts to the unpriced 0 (the fidelity bug this fix closes).
        record.list_cost = list;
        record.contracted_cost = contracted;
        record.pricing_currency_effective_cost = pricing_currency_effective;
        // The catalog-priced quantity/unit-price columns (nullable): None on an unpriced
        // row, Some on a priced one. Restore verbatim.
        record.consumed_quantity = consumed_qty;
        record.pricing_quantity = pricing_qty;
        record.list_unit_price = list_unit;
        record.contracted_unit_price = contracted_unit;
        record.pricing_currency_list_unit_price = pricing_currency_list_unit;
        record.pricing_currency_contracted_unit_price = pricing_currency_contracted_unit;
        record.x_estimated = x_estimated != 0;
        record.x_pricing_status = x_pricing_status;
        // x_consumed_tokens is re-derived as Decimal::from(token_count); apply the stored
        // decimal verbatim so scale/representation match exactly.
        record.x_consumed_tokens = consumed;
        // SKU / pricing identifiers are persisted (nullable); apply them verbatim rather
        // than keeping the unpriced-default derivation, so a priced row's SKU + pricing
        // category/unit round-trip.
        record.sku_id = sku_id;
        record.sku_price_id = sku_price_id;
        record.sku_meter = sku_meter;
        record.pricing_category = pricing_category;
        record.pricing_unit = pricing_unit;
        // Import provenance (nullable): None on a non-imported row, Some("1.2") on a
        // FOCUS-v1.2-imported one. Restore verbatim so an imported row round-trips.
        record.x_focus_input_version = x_focus_input_version;
        // Collector attribution taxonomy: restore verbatim so a sidechain/uncertain row
        // (and the collector version that minted it) round-trips instead of reverting to
        // the unpriced_usage defaults (false / "confident" / current version).
        record.x_sidechain = x_sidechain != 0;
        record.x_attribution_confidence = x_attribution_confidence;
        record.x_collector_version = x_collector_version;
        // Pricing provenance (nullable): None on a source-priced/unpriced row, Some on an
        // estimated one. Restore verbatim so the snapshot stamp round-trips (R8).
        record.x_pricing_snapshot_id = x_pricing_snapshot_id;
        // Bedrock inference-profile id (nullable): None off-Bedrock. Restore verbatim.
        record.x_inference_profile_id = x_inference_profile_id;

        Ok(record)
    }

    /// The `x_lane` value of every persisted row, in insertion order — a minimal
    /// read-back proving the whitelisted metadata round-trips. (Full `FocusRecord`
    /// reconstruction is [`all_focus_rows`].)
    pub fn stored_lanes(&self) -> Result<Vec<String>, StoreError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT x_lane FROM {USAGE_ROWS_TABLE} ORDER BY id"
        ))?;
        let rows = stmt.query_map([], |row| row.get::<_, String>(0))?;
        let mut lanes = Vec::new();
        for lane in rows {
            lanes.push(lane?);
        }
        Ok(lanes)
    }

    /// The `billed_cost` decimal string of every persisted row, in insertion order — a
    /// minimal read-back proving costs round-trip as exact decimal strings (never
    /// floats).
    pub fn stored_billed_costs(&self) -> Result<Vec<String>, StoreError> {
        let mut stmt = self.conn.prepare(&format!(
            "SELECT billed_cost FROM {USAGE_ROWS_TABLE} ORDER BY id"
        ))?;
        let rows = stmt.query_map([], |row| row.get::<_, String>(0))?;
        let mut costs = Vec::new();
        for cost in rows {
            costs.push(cost?);
        }
        Ok(costs)
    }
}

/// Parse a persisted decimal-string column back into a [`Decimal`] (exact, never via
/// `f64`), mapping a malformed value to a typed [`StoreError::Reconstruct`].
fn parse_decimal(column: &str, value: &str) -> Result<Decimal, StoreError> {
    Decimal::from_str(value)
        .map_err(|err| StoreError::Reconstruct(format!("unparseable {column} `{value}`: {err}")))
}

/// Parse a persisted *nullable* decimal-string column: SQL `NULL` → `None`, a present
/// value through [`parse_decimal`] (exact, never via `f64`; malformed → typed
/// [`StoreError::Reconstruct`]). Mirrors the FOCUS `Option<Decimal>` columns.
fn parse_decimal_opt(column: &str, value: &Option<String>) -> Result<Option<Decimal>, StoreError> {
    match value {
        Some(text) => Ok(Some(parse_decimal(column, text)?)),
        None => Ok(None),
    }
}

/// Render a nullable [`Decimal`] column for persistence: `Some(d)` → its exact decimal
/// string (never via `f64`), `None` → SQL `NULL`. The exact inverse of
/// [`parse_decimal_opt`], so an `Option<Decimal>` column round-trips byte-identically.
fn decimal_opt_to_string(value: &Option<Decimal>) -> Option<String> {
    value.as_ref().map(Decimal::to_string)
}

/// Convert a persisted token-count decimal to the `u64` [`UnpricedUsage::token_count`]
/// expects. Token counts are whole non-negative integers; a fractional or out-of-range
/// value fails closed (it would be malformed stored data, never legitimate).
fn decimal_to_u64(column: &str, value: &Decimal) -> Result<u64, StoreError> {
    use rust_decimal::prelude::ToPrimitive;
    if value.fract() != Decimal::ZERO {
        return Err(StoreError::Reconstruct(format!(
            "{column} `{value}` is not a whole token count"
        )));
    }
    value.to_u64().ok_or_else(|| {
        StoreError::Reconstruct(format!(
            "{column} `{value}` is out of range for a token count"
        ))
    })
}

#[cfg(test)]
mod tests {
    // `super::*` already re-exports `DateTime`/`Utc`, the FOCUS enums + `UnpricedUsage` +
    // `FocusRecord`, and `Decimal` (all now used by the reconstruction path), so the test
    // module only adds the chrono builders + the billing-currency constant.
    use super::*;
    use chrono::{LocalResult, TimeZone};
    use costroid_focus::DEFAULT_BILLING_CURRENCY;

    /// Substrings no `usage_rows` column name (or the DDL at large) may contain — every
    /// free-text-capable or content-bearing shape (R4). Checked case-insensitively.
    const FORBIDDEN_SUBSTRINGS: &[&str] = &[
        "description",
        "resource",
        "tags",
        "content",
        "prompt",
        "completion",
        "message",
        "text", // catches `sku_price_details`-style free text *and* any *_text column
        "sku_price_details",
    ];

    fn record(lane: LedgerLane, billed_cents: i64) -> FocusRecord {
        let timestamp = match Utc.with_ymd_and_hms(2026, 1, 15, 12, 34, 56) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        };
        let input = UnpricedUsage {
            lane,
            timestamp,
            tool: "codex".to_string(),
            model: "example-model".to_string(),
            token_type: TokenType::Input,
            token_count: 1_500,
            project: Some("/work/project".to_string()),
            access_path: FocusAccessPath::Subscription,
            service_name: "Codex".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };
        let Ok(mut rec) = FocusRecord::unpriced_usage(input) else {
            panic!("record should build");
        };
        // Override the (default-zero) cost to a non-trivial decimal so the round-trip is
        // non-vacuous: cents -> dollars (e.g. 1234 -> "12.34").
        let billed = Decimal::new(billed_cents, 2);
        rec.billed_cost = billed;
        rec.effective_cost = billed;
        rec
    }

    /// R4 — the Cardinal Rule, fail-closed. The live DDL (with the SQLite type keywords
    /// removed, so the unavoidable `TEXT`/`INTEGER` column types don't false-positive on
    /// the `text` substring) must contain NONE of the content-bearing substrings, and
    /// every real column must be in the documented allowlist (so a column added to the
    /// DDL without review fails this test).
    #[test]
    fn r4_schema_forbids_free_text_columns_and_is_allowlist_bounded() {
        // Strip the SQL type tokens: R4 forbids content-bearing *column names*, not the
        // SQLite `TEXT` type the metadata columns are necessarily stored as.
        let ddl = usage_rows_ddl()
            .to_lowercase()
            .replace("text", "")
            .replace("integer", "");
        for forbidden in FORBIDDEN_SUBSTRINGS {
            // `text` is checked against column names below, not the type-stripped DDL.
            if *forbidden == "text" {
                continue;
            }
            assert!(
                !ddl.contains(forbidden),
                "R4 violation: usage_rows DDL contains forbidden substring `{forbidden}`:\n{ddl}"
            );
        }

        // Every real column (read from the live SQLite schema) must be in the allowlist.
        let Ok(store) = Store::open_in_memory() else {
            panic!("in-memory store should open");
        };
        let Ok(mut stmt) = store
            .conn
            .prepare(&format!("PRAGMA table_info({USAGE_ROWS_TABLE})"))
        else {
            panic!("table_info pragma should prepare");
        };
        let Ok(rows) = stmt.query_map([], |row| row.get::<_, String>(1)) else {
            panic!("table_info should query");
        };
        let mut columns = Vec::new();
        for col in rows {
            let Ok(col) = col else {
                panic!("column name should read");
            };
            columns.push(col);
        }

        let allow: std::collections::BTreeSet<&str> = USAGE_ROWS_COLUMNS.iter().copied().collect();
        for col in &columns {
            // `id` is the synthetic primary key, not a metadata column.
            if col == "id" {
                continue;
            }
            // No column name may contain a content-bearing substring (this is where the
            // `text` rule actually bites — e.g. it forbids any `*_text` column).
            let lower = col.to_lowercase();
            for forbidden in FORBIDDEN_SUBSTRINGS {
                assert!(
                    !lower.contains(forbidden),
                    "R4 violation: column `{col}` contains forbidden substring `{forbidden}`"
                );
            }
            assert!(
                allow.contains(col.as_str()),
                "fail-closed: column `{col}` is in the DDL but NOT in USAGE_ROWS_COLUMNS — \
                 review it (does it carry content?) before allowlisting it"
            );
        }
        // And the allowlist is non-vacuous / actually realized (no typo'd entry that the
        // DDL never creates).
        let real: std::collections::BTreeSet<&str> = columns.iter().map(String::as_str).collect();
        for allowed in USAGE_ROWS_COLUMNS {
            assert!(
                real.contains(allowed),
                "allowlist column `{allowed}` is not present in the live usage_rows schema"
            );
        }
    }

    /// R4 + faithfulness forcing function (T16): the store's persist-or-drop decision over
    /// `FocusRecord` is COMPILE-TIME exhaustive. Destructures every field with NO `..`, so
    /// adding a field to `FocusRecord` fails to COMPILE here (`E0027`) until a human comes
    /// to this site and consciously decides: PERSIST it (add to `USAGE_ROWS_COLUMNS` + the
    /// DDL + `ingest` + `reconstruct_row`) or DROP it (re-derived/null on replay). This is
    /// the exact bug class T11 hit — the store silently dropping a `FocusRecord` field —
    /// converted from a runtime surprise into a forced compile-time review.
    #[test]
    fn store_persist_or_drop_decision_is_field_exhaustive_over_focus_record() {
        let FocusRecord {
            // PERSISTED (restored verbatim on replay) — keep in lockstep with
            // USAGE_ROWS_COLUMNS / ingest / reconstruct_row.
            billed_cost: _,
            effective_cost: _,
            list_cost: _,
            contracted_cost: _,
            pricing_currency_effective_cost: _,
            billing_currency: _,
            charge_period_start: _,
            consumed_quantity: _,
            pricing_quantity: _,
            list_unit_price: _,
            contracted_unit_price: _,
            pricing_currency_list_unit_price: _,
            pricing_currency_contracted_unit_price: _,
            service_name: _,
            service_provider_name: _,
            host_provider_name: _,
            invoice_issuer_name: _,
            sku_id: _,
            sku_price_id: _,
            sku_meter: _,
            pricing_category: _,
            pricing_unit: _,
            x_lane: _,
            x_model: _,
            x_token_type: _,
            x_access_path: _,
            x_estimated: _,
            x_tool: _,
            x_project: _,
            x_pricing_status: _,
            x_consumed_tokens: _,
            x_focus_input_version: _,
            x_sidechain: _,
            x_attribution_confidence: _,
            x_collector_version: _,
            x_pricing_snapshot_id: _,
            x_inference_profile_id: _,
            // INTENTIONALLY DROPPED — re-derived identically by `unpriced_usage` on replay
            // (so the round-trip stays byte-identical) OR a free-text-capable / non-derivable
            // FOCUS column that R4 forbids the store from retaining at all.
            billing_account_id: _,           // re-derived (placeholder const)
            billing_account_name: _,         // re-derived (placeholder const)
            billing_account_type: _,         // re-derived (placeholder const)
            billing_period_start: _,         // re-derived from charge_period_start
            billing_period_end: _,           // re-derived from charge_period_start
            charge_period_end: _,            // re-derived (start + 1s)
            charge_category: _,              // re-derived (const "Usage")
            charge_class: _,                 // re-derived (None)
            charge_description: _,           // re-derived "{model} {token_type} tokens"
            charge_frequency: _,             // re-derived (const)
            service_category: _,             // re-derived (const)
            service_subcategory: _,          // re-derived (const)
            provider_name: _,                // re-derived (mirrors service_provider_name)
            publisher_name: _,               // re-derived (mirrors invoice_issuer_name)
            invoice_id: _,                   // re-derived (None)
            sku_price_details: _,            // DROPPED, R4: free-text-capable
            pricing_currency: _,             // re-derived (== billing_currency)
            consumed_unit: _,                // re-derived (const "tokens")
            commitment_discount_category: _, // re-derived (None)
            commitment_discount_id: _,       // re-derived (None)
            commitment_discount_name: _,     // re-derived (None)
            commitment_discount_quantity: _, // re-derived (None)
            commitment_discount_status: _,   // re-derived (None)
            commitment_discount_type: _,     // re-derived (None)
            commitment_discount_unit: _,     // re-derived (None)
            capacity_reservation_id: _,      // re-derived (None)
            capacity_reservation_status: _,  // re-derived (None)
            region_id: _,                    // re-derived (None)
            region_name: _,                  // re-derived (None)
            availability_zone: _,            // re-derived (None)
            resource_id: _,                  // DROPPED, R4: free-text-capable
            resource_name: _,                // DROPPED, R4: free-text-capable
            resource_type: _,                // re-derived (None)
            sub_account_id: _,               // re-derived (None)
            sub_account_name: _,             // re-derived (None)
            sub_account_type: _,             // re-derived (None)
            tags: _,                         // DROPPED, R4: free-text-capable
            contract_applied: _,             // re-derived (None)
            allocated_method_id: _,          // re-derived (None)
            allocated_method_details: _,     // DROPPED, R4: free-text-capable
            allocated_resource_id: _,        // re-derived (None)
            allocated_resource_name: _,      // DROPPED, R4: free-text-capable
            allocated_tags: _,               // DROPPED, R4: free-text-capable
        } = record(LedgerLane::DeveloperTool, 0);
    }

    /// Ingest round-trip (metadata only): build several records, ingest, and prove the
    /// whitelisted lanes + costs persisted exactly.
    #[test]
    fn ingest_round_trips_whitelisted_metadata() {
        let Ok(store) = Store::open_in_memory() else {
            panic!("in-memory store should open");
        };

        let rows = [
            record(LedgerLane::DeveloperTool, 1_234), // "12.34"
            record(LedgerLane::CloudApi, 50),         // "0.50"
            record(LedgerLane::LocalInference, 0),    // "0"
        ];

        let Ok(count) = store.ingest(&rows) else {
            panic!("ingest should succeed");
        };
        assert_eq!(count, 3);

        let Ok(total) = store.row_count() else {
            panic!("row_count should succeed");
        };
        assert_eq!(total, 3);

        let Ok(lanes) = store.stored_lanes() else {
            panic!("stored_lanes should succeed");
        };
        assert_eq!(
            lanes,
            vec![
                "developer_tool".to_string(),
                "cloud_api".to_string(),
                "local_inference".to_string(),
            ]
        );

        let Ok(costs) = store.stored_billed_costs() else {
            panic!("stored_billed_costs should succeed");
        };
        // Costs round-trip as exact decimal strings (never floats). The decimals keep
        // their scale (cents -> two fractional digits), so 0 cents is "0.00".
        assert_eq!(
            costs,
            vec!["12.34".to_string(), "0.50".to_string(), "0.00".to_string()]
        );
    }

    /// A second ingest appends (transaction-per-call), and the count reflects both.
    #[test]
    fn ingest_appends_across_calls() {
        let Ok(store) = Store::open_in_memory() else {
            panic!("in-memory store should open");
        };
        let Ok(_) = store.ingest(&[record(LedgerLane::DeveloperTool, 100)]) else {
            panic!("first ingest should succeed");
        };
        let Ok(_) = store.ingest(&[
            record(LedgerLane::CloudApi, 200),
            record(LedgerLane::CloudApi, 300),
        ]) else {
            panic!("second ingest should succeed");
        };
        let Ok(total) = store.row_count() else {
            panic!("row_count should succeed");
        };
        assert_eq!(total, 3);
    }

    /// A **catalog-priced** row mirroring the dominant `apply_pricing` shape: a real,
    /// priced charge (not an estimate) with EVERY now-persisted priced column set to a
    /// non-default value — the full cost set (`list_cost`/`contracted_cost`/
    /// `pricing_currency_effective_cost`), the catalog quantity/unit-price columns
    /// (`consumed_quantity`/`pricing_quantity`/`list_unit_price`/`contracted_unit_price`/
    /// the two `pricing_currency_*_unit_price`), and the `sku_price_id`/`pricing_category`/
    /// `pricing_unit` identifiers. Built by hand (the store crate cannot depend on
    /// `costroid-core`'s `apply_pricing`); the byte-identical-via-`export_focus_csv` proof
    /// over a *real* `apply_pricing` row lives in `apps/cli/tests/store_replay.rs`. This is
    /// the row whose faithful reconstruction the deciding test below turns on: it must NOT
    /// collapse to the unpriced default `unpriced_usage` produces — which, before this fix,
    /// silently reverted these columns to 0/None.
    fn priced_cloud_record() -> FocusRecord {
        let timestamp = match Utc.with_ymd_and_hms(2026, 2, 3, 9, 0, 0) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        };
        let input = UnpricedUsage {
            lane: LedgerLane::CloudApi,
            timestamp,
            tool: "anthropic-api".to_string(),
            model: "claude-opus".to_string(),
            token_type: TokenType::Output,
            token_count: 4_096,
            project: None,
            access_path: FocusAccessPath::Api,
            service_name: "Anthropic API".to_string(),
            service_provider_name: "Anthropic".to_string(),
            host_provider_name: "Anthropic".to_string(),
            invoice_issuer_name: "Anthropic".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };
        let Ok(mut rec) = FocusRecord::unpriced_usage(input) else {
            panic!("priced cloud record should build");
        };
        // Priced: a real cost, NOT an estimate. Set EVERY now-persisted priced column to a
        // value distinct from the unpriced default (0 / None) so the round-trip is
        // non-vacuous — a regression to the lossy behavior (these reverting to 0/None)
        // fails the equality below.
        let cost = Decimal::new(12_345, 4); // 1.2345
        rec.billed_cost = cost;
        rec.effective_cost = cost;
        rec.list_cost = cost;
        rec.contracted_cost = cost;
        rec.pricing_currency_effective_cost = cost;
        // Catalog quantity/unit-price columns — non-default (the unpriced default is None).
        let quantity = Decimal::from(4_096);
        let per_token = Decimal::new(15, 8); // 0.00000015 — a per-token unit price
        rec.consumed_quantity = Some(quantity);
        rec.pricing_quantity = Some(quantity);
        rec.list_unit_price = Some(per_token);
        rec.contracted_unit_price = Some(per_token);
        rec.pricing_currency_list_unit_price = Some(per_token);
        rec.pricing_currency_contracted_unit_price = Some(per_token);
        // SKU + pricing identifiers — non-default (sku_price_id/pricing_category/pricing_unit
        // are None on an unpriced row).
        rec.sku_price_id = Some("claude-opus:output:standard".to_string());
        rec.pricing_category = Some("Standard".to_string());
        rec.pricing_unit = Some("tokens".to_string());
        rec.x_estimated = false;
        rec.x_pricing_status = "priced".to_string();
        // Import provenance — a cloud row imported from a FOCUS v1.2 export. Non-default
        // (None on a directly-collected row) so the round-trip below is non-vacuous.
        rec.x_focus_input_version = Some("1.2".to_string());
        // A DISTINCT (non-current) collector version — proves the round-trip restores the
        // STORED value, not the current COLLECTOR_VERSION const (a row minted by an older
        // Costroid must replay as that older version).
        rec.x_collector_version = "0.5.0-test".to_string();
        rec
    }

    /// R4-faithful round-trip (the deciding T11 test): ingest a Vec mixing a developer_tool
    /// row AND a fully priced row (every now-persisted priced column set non-default),
    /// replay via `all_focus_rows`, and assert the reconstructed rows EQUAL the originals
    /// (full `==`). The M1 row types leave every *non-persisted* FOCUS column
    /// `None`/derived-identical on both sides (both are built through `unpriced_usage`,
    /// which re-derives `charge_description` identically), so the equality is exact, not a
    /// persisted-subset compromise — and the persisted priced columns now round-trip
    /// instead of reverting to 0/None.
    #[test]
    fn all_focus_rows_round_trips_records_faithfully_including_priced_cloud() {
        let Ok(store) = Store::open_in_memory() else {
            panic!("in-memory store should open");
        };

        // A sidechain (sub-agent) dev-tool row: non-default x_sidechain + uncertain
        // attribution, so the collector taxonomy round-trip is non-vacuous.
        let mut sidechain_dev = record(LedgerLane::DeveloperTool, 1_234); // estimated, "12.34"
        sidechain_dev.x_sidechain = true;
        sidechain_dev.x_attribution_confidence = "uncertain".to_string();
        // An ESTIMATED row carries the pricing-snapshot stamp (R8); set it non-default so
        // its round-trip is non-vacuous (the source-priced cloud row below keeps None).
        sidechain_dev.x_pricing_snapshot_id = Some("litellm@2026-06-18#36c8994e".to_string());
        let originals = vec![
            sidechain_dev,         // estimated dev-tool sidechain row
            priced_cloud_record(), // source-priced cloud_api row
        ];

        let Ok(count) = store.ingest(&originals) else {
            panic!("ingest should succeed");
        };
        assert_eq!(count, 2);

        let Ok(reconstructed) = store.all_focus_rows() else {
            panic!("all_focus_rows should succeed");
        };

        // Full structural equality — replay reconstructs the records exactly.
        assert_eq!(reconstructed, originals);

        // Non-vacuous: the priced row kept its real cost, "priced" status, not-estimated
        // flag, and cloud_api lane after the round-trip (it did NOT collapse to the
        // unpriced default).
        // The estimated dev-tool row's pricing-snapshot stamp survived replay (R8); the
        // source-priced cloud row carries no stamp (None) and stays None.
        assert_eq!(
            reconstructed[0].x_pricing_snapshot_id,
            Some("litellm@2026-06-18#36c8994e".to_string())
        );
        assert!(reconstructed[1].x_pricing_snapshot_id.is_none());

        let cloud = &reconstructed[1];
        assert_eq!(cloud.x_lane, "cloud_api");
        assert_eq!(cloud.x_pricing_status, "priced");
        assert!(!cloud.x_estimated);
        assert_eq!(cloud.billed_cost, Decimal::new(12_345, 4));
        assert_eq!(cloud.effective_cost, Decimal::new(12_345, 4));
        assert_eq!(cloud.x_access_path, "api");
        assert_eq!(cloud.x_token_type, "output");
        assert_eq!(cloud.x_consumed_tokens, Decimal::from(4_096));

        // The deciding assertions: every now-persisted priced column survived the
        // round-trip at its non-default value. Before this fix these reverted to the
        // unpriced default (0 / None) — so each of these would have failed.
        assert_eq!(cloud.list_cost, Decimal::new(12_345, 4));
        assert_eq!(cloud.contracted_cost, Decimal::new(12_345, 4));
        assert_eq!(
            cloud.pricing_currency_effective_cost,
            Decimal::new(12_345, 4)
        );
        let quantity = Decimal::from(4_096);
        let per_token = Decimal::new(15, 8);
        assert_eq!(cloud.consumed_quantity, Some(quantity));
        assert_eq!(cloud.pricing_quantity, Some(quantity));
        assert_eq!(cloud.list_unit_price, Some(per_token));
        assert_eq!(cloud.contracted_unit_price, Some(per_token));
        assert_eq!(cloud.pricing_currency_list_unit_price, Some(per_token));
        assert_eq!(
            cloud.pricing_currency_contracted_unit_price,
            Some(per_token)
        );
        assert_eq!(
            cloud.sku_price_id,
            Some("claude-opus:output:standard".to_string())
        );
        assert_eq!(cloud.pricing_category, Some("Standard".to_string()));
        assert_eq!(cloud.pricing_unit, Some("tokens".to_string()));
        assert_eq!(
            cloud.x_focus_input_version,
            Some("1.2".to_string()),
            "import provenance round-trips (not lost on replay)"
        );
        assert_eq!(
            cloud.x_collector_version, "0.5.0-test",
            "the STORED collector version is restored, not the current const"
        );
        // Guard the unpriced (dev-tool) row's null columns survived as null too — the
        // restore must not fabricate Some on a row where apply_pricing never ran.
        let dev = &reconstructed[0];
        assert_eq!(dev.pricing_quantity, None);
        assert_eq!(dev.sku_price_id, None);
        assert_eq!(dev.pricing_unit, None);
        assert_eq!(dev.list_cost, Decimal::ZERO);
        assert_eq!(
            dev.x_focus_input_version, None,
            "a directly-collected (non-imported) row stays None"
        );
        assert!(dev.x_sidechain, "the sidechain flag round-trips");
        assert_eq!(
            dev.x_attribution_confidence, "uncertain",
            "uncertain attribution round-trips (not reverted to the confident default)"
        );
    }

    /// A malformed stored enum (an `x_lane` with no `from_focus_str` inverse) yields a
    /// typed `StoreError::Reconstruct` on replay — never a panic or a silent default.
    #[test]
    fn all_focus_rows_fails_closed_on_a_malformed_stored_enum() {
        let Ok(store) = Store::open_in_memory() else {
            panic!("in-memory store should open");
        };
        let Ok(_) = store.ingest(&[record(LedgerLane::DeveloperTool, 100)]) else {
            panic!("ingest should succeed");
        };
        // Corrupt the persisted enum directly (bypassing the typed ingest path) to simulate
        // a malformed/garbage stored value a future migration or external write might leave.
        let Ok(_) = store.conn.execute(
            &format!("UPDATE {USAGE_ROWS_TABLE} SET x_lane = 'not_a_lane'"),
            [],
        ) else {
            panic!("update should succeed");
        };

        match store.all_focus_rows() {
            Err(StoreError::Reconstruct(msg)) => {
                assert!(
                    msg.contains("x_lane"),
                    "error should name the bad column: {msg}"
                );
            }
            other => panic!("expected a Reconstruct error, got {other:?}"),
        }
    }
}
