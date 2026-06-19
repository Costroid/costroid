//! Costroid's local SQLite usage store — a metadata-only persistent ledger.
//!
//! # R4, the Cardinal Rule: the store cannot hold content
//!
//! The single [`usage_rows`](USAGE_ROWS_TABLE) table is an **explicit metadata
//! allowlist** ([`USAGE_ROWS_COLUMNS`]). It persists only the bounded, non-free-text
//! metadata needed to reconstruct a [`FocusRecord`] on replay (a later milestone) via
//! [`FocusRecord::unpriced_usage`] plus cost overrides: token counts, model, lane, the
//! costs (as decimal *strings*, never floats), the charge timestamp, provider identity,
//! and SKU identifiers.
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

use costroid_focus::{FocusRecord, FOCUS_VERSION};
use rusqlite::{Connection, OptionalExtension};
use thiserror::Error;

/// The schema version of the [`usage_rows`](USAGE_ROWS_TABLE) layout. Bumped whenever
/// the persisted column set changes so a future replay/migration step can tell shapes
/// apart. Stamped into [`STORE_META_TABLE`] on open.
pub const SCHEMA_VERSION: i64 = 1;

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
    "x_consumed_tokens",
    "billing_currency",
    // Provider identity — bounded identifier strings.
    "service_name",
    "service_provider_name",
    "host_provider_name",
    "invoice_issuer_name",
    // SKU identifiers — bounded; NOT `sku_price_details` (dropped, free-text-capable).
    "sku_id",
    "sku_meter",
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
            x_consumed_tokens TEXT NOT NULL,\n  \
            billing_currency TEXT NOT NULL,\n  \
            service_name TEXT NOT NULL,\n  \
            service_provider_name TEXT NOT NULL,\n  \
            host_provider_name TEXT NOT NULL,\n  \
            invoice_issuer_name TEXT NOT NULL,\n  \
            sku_id TEXT,\n  \
            sku_meter TEXT\n\
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
                    billed_cost, effective_cost, x_consumed_tokens, billing_currency, \
                    service_name, service_provider_name, host_provider_name, \
                    invoice_issuer_name, sku_id, sku_meter\
                ) VALUES (\
                    ?1, ?2, ?3, ?4, ?5, ?6, ?7, ?8, ?9, ?10, ?11, ?12, ?13, ?14, ?15, \
                    ?16, ?17, ?18, ?19\
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
                    row.x_consumed_tokens.to_string(),
                    row.billing_currency,
                    row.service_name,
                    row.service_provider_name,
                    row.host_provider_name,
                    row.invoice_issuer_name,
                    row.sku_id,
                    row.sku_meter,
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

    /// The `x_lane` value of every persisted row, in insertion order — a minimal
    /// read-back proving the whitelisted metadata round-trips. (Full `FocusRecord`
    /// reconstruction + aggregate + export is a later milestone.)
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

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{LocalResult, TimeZone, Utc};
    use costroid_focus::{
        FocusAccessPath, LedgerLane, TokenType, UnpricedUsage, DEFAULT_BILLING_CURRENCY,
    };
    use rust_decimal::Decimal;

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
}
