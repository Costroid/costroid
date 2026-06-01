//! FOCUS export primitives for Costroid.
//!
//! The complete FOCUS 1.3 row type is intentionally deferred until the schema
//! is verified against the current upstream specification. This crate already
//! owns the canonical JSON export envelope so downstream code cannot emit a
//! bare array by accident.

use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use thiserror::Error;

pub const FOCUS_VERSION: &str = "1.3";

pub type FocusTimestamp = DateTime<Utc>;

#[derive(Debug, Error)]
pub enum FocusError {
    #[error("FOCUS 1.3 record schema has not been finalized in this skeleton")]
    SchemaPending,
}

#[derive(Debug, Clone, PartialEq, Eq, Serialize, Deserialize)]
pub struct FocusExportEnvelope<T> {
    #[serde(rename = "focusVersion")]
    pub focus_version: String,
    pub rows: Vec<T>,
}

impl<T> FocusExportEnvelope<T> {
    pub fn new(rows: Vec<T>) -> Self {
        Self {
            focus_version: FOCUS_VERSION.to_string(),
            rows,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn export_envelope_uses_canonical_focus_version() {
        let envelope = FocusExportEnvelope::<()>::new(Vec::new());

        assert_eq!(envelope.focus_version, FOCUS_VERSION);
        assert!(envelope.rows.is_empty());
    }
}
