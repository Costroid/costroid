//! The brand's 9-step dot-density warning scale (`0 idle → 8 critical`) and the
//! selection of the most-constrained live quota window.
//!
//! Severity is encoded by **how many dots are filled** in the tray's 3×3 grid, so the
//! glance survives grayscale and color-blindness with no `!`/`!!` badge — the dot count
//! *is* the "never rely on color alone" guarantee (DESIGN-SYSTEM "Brand basics" /
//! STEP6-TASKBAR-DESIGN §0). Color is only a secondary tint over that count.
//!
//! Pure logic, no egui/tray types — unit-tested against `NowSummary` fixtures.

use costroid_core::{LimitAvailability, LimitMeasure, LimitSummary, NowSummary};

/// The maximum dot-density step (`0..=8`, nine levels).
pub const MAX_STEP: u8 = 8;

/// Map a consumed fraction onto the `0..=8` dot-density step.
///
/// Monotonic and honest:
/// * `f <= 0` (or non-finite) → `0` — the idle / empty grid;
/// * `f >= 1` → `8` — at or over the limit (the full grid);
/// * otherwise `round(f * 8)` clamped to `1..=7` — `1` is the min-visibility floor (any
///   nonzero usage shows at least one dot, mirroring the CLI meter's min-visibility
///   half-cell), and `7` is the ceiling below 100% so `8` means strictly "at/over the
///   limit", never merely "near it".
///
/// Build-time decision (T18): the design pin fixes the per-step *colors* but defers the
/// fraction→step *curve* and glyph geometry to build time (STEP6-TASKBAR-DESIGN §13).
/// This linear round-with-floor curve is the obvious default; T19's in-window meters
/// reuse it so the dot language is identical edge-to-edge.
pub fn severity_step(fraction: f64) -> u8 {
    if !fraction.is_finite() || fraction <= 0.0 {
        return 0;
    }
    if fraction >= 1.0 {
        return MAX_STEP;
    }
    let step = (fraction * f64::from(MAX_STEP)).round();
    (step as i64).clamp(1, 7) as u8
}

/// A live, fillable quota reading: the window plus its consumed fraction (`>= 0.0`,
/// `>= 1.0` means at/over the limit).
#[derive(Debug, Clone)]
pub struct Constraint {
    pub limit: LimitSummary,
    pub fraction: f64,
}

impl Constraint {
    /// The dot-density step this constraint maps to.
    pub fn step(&self) -> u8 {
        severity_step(self.fraction)
    }
}

/// Pick the most-constrained *fresh `Available`* quota window — the one whose consumed
/// fraction is highest — or `None` when no window contributes a confident fill.
///
/// Only the `LimitAvailability::Available` arm yields a fill: the upstream
/// sanitize / cross-check / age-out already demotes stale, unverified, or implausible
/// readings to the other arms (STATUSLINE-CAPTURE-BRIEF §5 / ARCHITECTURE §9.2). A
/// degraded arm (`Unverified`/`Estimated`/`Partial`/`Unavailable`) never contributes a
/// fill — the tray shows the idle / `?` muted grid instead, honesty over a guessed level
/// (STEP6-TASKBAR-DESIGN §3).
///
/// A `Spend` dollar pool carries no token fraction, exactly as the CLI meter treats it
/// (`render.rs::measure_fraction`), so it does not drive the tray fill in v0.6.0; when a
/// `Spend`-emitting provider ships, surfacing a bounded pool's `used/included` as a tray
/// fraction is a follow-up.
pub fn most_constrained_available(summary: &NowSummary) -> Option<Constraint> {
    summary
        .limits
        .iter()
        .filter_map(|limit| {
            available_fraction(limit).map(|fraction| Constraint {
                limit: limit.clone(),
                fraction,
            })
        })
        .max_by(|a, b| a.fraction.total_cmp(&b.fraction))
}

/// The consumed token fraction of an `Available` window, or `None` for any other arm or
/// a non-token (`Spend`) measure.
fn available_fraction(limit: &LimitSummary) -> Option<f64> {
    match &limit.availability {
        LimitAvailability::Available {
            measure: LimitMeasure::TokenFraction(fraction),
            ..
        } if fraction.is_finite() => Some(fraction.max(0.0)),
        _ => None,
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{DateTime, Utc};
    use costroid_core::{GroupBy, LimitKind, NowSummary, PeriodRange, ProviderId};

    // `panic!` is permitted in tests; `unwrap`/`expect` are denied workspace-wide.
    fn ts(secs: i64) -> DateTime<Utc> {
        match DateTime::from_timestamp(secs, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp {secs}"),
        }
    }

    fn token_window(tool: ProviderId, kind: LimitKind, fraction: f64) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: ts(1_900_000_000),
            availability: LimitAvailability::Available {
                measure: LimitMeasure::TokenFraction(fraction),
                resets_at: ts(1_900_003_600),
                reset_in_seconds: 3_600,
            },
        }
    }

    fn unavailable_window(tool: ProviderId, kind: LimitKind) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: ts(0),
            availability: LimitAvailability::Unavailable {
                reason: "no sanctioned source".to_owned(),
            },
        }
    }

    fn unverified_window(tool: ProviderId, kind: LimitKind, fraction: f64) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: ts(1_900_000_000),
            availability: LimitAvailability::Unverified {
                measure: LimitMeasure::TokenFraction(fraction),
                resets_at: None,
                reset_in_seconds: None,
            },
        }
    }

    fn summary_with(limits: Vec<LimitSummary>) -> NowSummary {
        NowSummary {
            generated_at: ts(1_900_000_500),
            cost_period: PeriodRange {
                start: ts(1_899_000_000),
                end: ts(1_901_000_000),
            },
            group_by: GroupBy::Model,
            limits,
            current_costs: Vec::new(),
            providers: Vec::new(),
        }
    }

    #[test]
    fn severity_step_endpoints_and_floor() {
        assert_eq!(severity_step(0.0), 0, "zero usage is idle");
        assert_eq!(severity_step(-0.5), 0, "negative is clamped to idle");
        assert_eq!(
            severity_step(f64::NAN),
            0,
            "non-finite is idle, never a guess"
        );
        assert_eq!(severity_step(0.0001), 1, "any nonzero usage shows >= 1 dot");
        assert_eq!(severity_step(1.0), 8, "at the limit is the full grid");
        assert_eq!(severity_step(1.5), 8, "over the limit stays the full grid");
    }

    #[test]
    fn severity_step_is_monotonic_nondecreasing() {
        let mut last = 0u8;
        let mut f = 0.0;
        while f <= 1.2 {
            let step = severity_step(f);
            assert!(
                step >= last,
                "step must never decrease as fraction grows: f={f} step={step} last={last}"
            );
            assert!(step <= MAX_STEP);
            last = step;
            f += 0.01;
        }
    }

    #[test]
    fn severity_step_reserves_eight_for_at_or_over_limit() {
        // Below 100% never reaches the full grid (8), even at the CLI critical threshold.
        assert!(severity_step(0.95) <= 7);
        assert!(severity_step(0.999) <= 7);
        assert_eq!(severity_step(1.0), 8);
    }

    #[test]
    fn most_constrained_picks_highest_available_fraction() {
        let summary = summary_with(vec![
            token_window(ProviderId::Codex, LimitKind::FiveHour, 0.40),
            token_window(ProviderId::ClaudeCode, LimitKind::FiveHour, 0.92),
            token_window(ProviderId::ClaudeCode, LimitKind::Weekly, 0.51),
        ]);
        let chosen = most_constrained_available(&summary);
        let Some(chosen) = chosen else {
            panic!("expected a most-constrained window");
        };
        assert_eq!(chosen.limit.tool, ProviderId::ClaudeCode);
        assert_eq!(chosen.limit.kind, LimitKind::FiveHour);
        assert!((chosen.fraction - 0.92).abs() < 1e-9);
        assert_eq!(chosen.step(), severity_step(0.92));
    }

    #[test]
    fn all_degraded_windows_yield_idle_none() {
        let summary = summary_with(vec![
            unavailable_window(ProviderId::Cursor, LimitKind::Monthly),
            unverified_window(ProviderId::ClaudeCode, LimitKind::Weekly, 0.99),
        ]);
        assert!(
            most_constrained_available(&summary).is_none(),
            "an Unverified/Unavailable reading must never drive a confident tray fill"
        );
    }

    #[test]
    fn empty_summary_is_idle() {
        assert!(most_constrained_available(&summary_with(Vec::new())).is_none());
    }

    #[test]
    fn available_window_among_degraded_ones_is_still_selected() {
        let summary = summary_with(vec![
            unavailable_window(ProviderId::Cursor, LimitKind::Monthly),
            token_window(ProviderId::Codex, LimitKind::FiveHour, 0.23),
            unverified_window(ProviderId::ClaudeCode, LimitKind::Weekly, 0.99),
        ]);
        let Some(chosen) = most_constrained_available(&summary) else {
            panic!("the one Available window must be selected");
        };
        assert_eq!(chosen.limit.tool, ProviderId::Codex);
    }
}
