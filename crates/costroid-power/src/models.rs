//! The bundled, dated Gemma 4 model manifest (M3, R10).
//!
//! Standardizes the local-inference benchmark spectrum on the **Gemma 4 family (Apache-2.0)**
//! (§3.1.E / §5.5). A **vendored data artifact** (`models/gemma4.v1.json`, compiled in via
//! `include_str!`, never fetched). Carries each model's specs + quant set + the speculative
//! draft flag, the **published** quality score (cited, never re-derived — R10; `None` here, a
//! structural placeholder filled into the Frontier at M4/M6), and a tok/s **estimate** flagged
//! `tok_s_estimated` (the harness produces the real throughput at M3b). Costroid ships **no
//! weights**.

use serde::{Deserialize, Serialize};

use crate::error::PowerError;

/// The bundled manifest, compiled in (never fetched — R10/R8).
const BUNDLED_MODELS_JSON: &str = include_str!("../models/gemma4.v1.json");

/// The `as_of` date the bundled manifest records — pinned in Rust so a swapped/edited artifact
/// is caught by the loader test.
pub const GEMMA4_MANIFEST_AS_OF: &str = "2026-06-20";

/// The full bundled local-model manifest.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ModelManifest {
    pub family: String,
    pub license: String,
    pub as_of: String,
    pub source: String,
    pub models: Vec<ModelSpec>,
}

/// One Gemma 4 model's specs + estimated throughput + published-quality pointer.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ModelSpec {
    pub id: String,
    pub display_name: String,
    pub class: String,
    pub params_b: f64,
    pub active_params_b: f64,
    pub context_tokens: u64,
    pub default_quant: String,
    pub quants: Vec<String>,
    /// Whether the model ships a speculative-decoding draft model (a throughput lever to
    /// **measure** at M3b, not assume).
    pub has_draft_model: bool,
    /// The estimated decode throughput (tokens/sec). An ESTIMATE only — see `tok_s_estimated`.
    pub estimated_tok_s: f64,
    /// Always `true` for the bundled manifest: tok/s is never measured here (R10).
    pub tok_s_estimated: bool,
    pub quality: ModelQuality,
}

/// A pointer to a model's **published** quality score (R10: cited, never re-derived). `score`
/// is `None` in the bundled manifest (a structural placeholder, `metric = "as published"`),
/// filled into the Frontier from the cited `source` at M4/M6.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct ModelQuality {
    pub score: Option<f64>,
    pub metric: String,
    pub source: String,
    pub as_of: String,
}

impl ModelSpec {
    /// Whether `quant` is a quant this model is offered in.
    pub fn supports_quant(&self, quant: &str) -> bool {
        self.quants.iter().any(|q| q == quant)
    }
}

/// Parse the bundled Gemma 4 manifest (never fetched — R10). A malformed bundled artifact is a
/// typed [`PowerError`], never a panic.
pub fn bundled_models() -> Result<ModelManifest, PowerError> {
    serde_json::from_str(BUNDLED_MODELS_JSON).map_err(|e| {
        PowerError::InvalidProfile(format!("bundled gemma4.v1.json failed to parse: {e}"))
    })
}

impl ModelManifest {
    /// The model spec with the given id, if present.
    pub fn model(&self, id: &str) -> Option<&ModelSpec> {
        self.models.iter().find(|m| m.id == id)
    }

    /// Resolve a model + quant for a run: look up the id, and validate the quant (default the
    /// model's `default_quant` when `quant` is `None`). A typed [`PowerError`] for an unknown
    /// model or an unsupported quant — never a panic.
    pub fn resolve<'a>(
        &'a self,
        model_id: &str,
        quant: Option<&str>,
    ) -> Result<(&'a ModelSpec, String), PowerError> {
        let spec = self.model(model_id).ok_or_else(|| {
            PowerError::InvalidProfile(format!(
                "unknown model id `{model_id}` (not in the Gemma 4 manifest)"
            ))
        })?;
        let quant = quant.unwrap_or(&spec.default_quant);
        if !spec.supports_quant(quant) {
            return Err(PowerError::InvalidProfile(format!(
                "model `{model_id}` is not offered in quant `{quant}` (have {:?})",
                spec.quants
            )));
        }
        Ok((spec, quant.to_string()))
    }
}

#[cfg(test)]
mod tests {
    // Repo rule: clippy denies `unwrap`/`expect` even in tests; use `let-else { panic! }`.
    use super::*;

    #[test]
    fn bundled_models_parses_with_pinned_as_of_and_apache_license() {
        let Ok(manifest) = bundled_models() else {
            panic!("the bundled gemma4.v1.json must parse");
        };
        assert_eq!(manifest.as_of, GEMMA4_MANIFEST_AS_OF);
        // §5.5: pinned to Gemma 4, which is Apache-2.0 (permissive); Gemma 1-3 are non-OSI.
        assert_eq!(manifest.license, "Apache-2.0");
        // The core spectrum (§3.1.E): the fast MoE + the dense flagship + 12B + the two edge.
        assert_eq!(manifest.models.len(), 5);
        for id in [
            "gemma-4-26b-a4b",
            "gemma-4-31b-dense",
            "gemma-4-12b-unified",
            "gemma-4-e2b",
            "gemma-4-e4b",
        ] {
            assert!(manifest.model(id).is_some(), "{id} must be present");
        }
    }

    #[test]
    fn every_model_flags_tok_s_estimated_and_cites_a_quality_source() {
        let Ok(manifest) = bundled_models() else {
            panic!("manifest must parse");
        };
        for m in &manifest.models {
            // R10: tok/s is an estimate here, never measured.
            assert!(
                m.tok_s_estimated,
                "{} tok/s must be flagged estimated",
                m.id
            );
            assert!(m.estimated_tok_s > 0.0);
            // R10: quality is "as published" and carries a citation; the score itself is a
            // structural placeholder (None) filled into the Frontier from the source at M4/M6 —
            // never a re-derived/guessed number.
            assert!(
                !m.quality.source.is_empty(),
                "{} cites a quality source",
                m.id
            );
            assert_eq!(m.quality.metric, "as published");
            assert!(!m.default_quant.is_empty());
            assert!(m.supports_quant(&m.default_quant));
        }
    }

    #[test]
    fn resolve_defaults_the_quant_and_rejects_unknowns() {
        let Ok(manifest) = bundled_models() else {
            panic!("manifest must parse");
        };
        // Default quant when none is requested.
        let Ok((spec, quant)) = manifest.resolve("gemma-4-26b-a4b", None) else {
            panic!("the MoE model should resolve");
        };
        assert_eq!(spec.id, "gemma-4-26b-a4b");
        assert_eq!(quant, "Q4_K_M");
        // An explicit supported quant.
        assert!(manifest.resolve("gemma-4-31b-dense", Some("Q8_0")).is_ok());
        // Unknown model + unsupported quant are typed errors.
        assert!(matches!(
            manifest.resolve("gemma-9-ultra", None),
            Err(PowerError::InvalidProfile(_))
        ));
        assert!(matches!(
            manifest.resolve("gemma-4-e2b", Some("Q2_K")),
            Err(PowerError::InvalidProfile(_))
        ));
    }
}
