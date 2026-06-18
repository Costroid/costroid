//! One quota meter, in the brand's PAINTED dot/braille language.
//!
//! The TUI draws its limit meters as Unicode braille cells (`⣿`/`⡇`/`⣀`), but the bundled
//! JetBrains Mono has no braille glyph coverage (T18, `fonts.rs`), so the taskbar **paints**
//! the dots instead of typesetting them — the same dot primitive the tray `C⠉` mark uses
//! (`glyph.rs`), realized as a horizontal meter. The fill LENGTH still follows the TUI's
//! `meter_segments` (floor + a boundary half-cell + a min-visibility floor), and the fill
//! TINT follows the brand's 0–8 warning ramp (`severity::severity_step` +
//! `glyph::step_fill_color`) — so the dot language is identical edge-to-edge with the tray
//! (STEP6-TASKBAR-DESIGN §6 / DESIGN-SYSTEM "Limit meter").
//!
//! Honesty is mirrored from the CLI's `render_limit_line`: the five `LimitAvailability` arms
//! render distinctly, a degraded reading is NEVER dressed as a confident fill, and the
//! never-color-alone guarantee is the dot DENSITY + the warning ramp (the 0–8 system), never
//! color alone. Pure model (`MeterModel::from_limit`) + a paint pass, so the model is
//! unit-testable without a GPU.

use chrono::{DateTime, Utc};
use costroid_core::{LimitAvailability, LimitMeasure, LimitSummary, ProviderId};

use crate::app::{color_of, ASH, BONE};
use crate::format::{
    freshness_stamp, kind_label, percent, provider_label, reset_countdown, with_thousands,
};
use crate::glyph;
use crate::severity::severity_step;

/// The Claude chat-under-report caveat (brief §8): claude.ai chat shares the 5h/7d limit
/// but is invisible to the cache, so a Claude window that shows usage carries this note.
/// Mirrors `render.rs::CLAUDE_CHAT_CAVEAT`.
const CLAUDE_CHAT_CAVEAT: &str =
    "reflects Claude Code's view; claude.ai chat usage may make true usage higher.";

/// The color-free cue for an unverified (cross-check-failed) reading — replaces a confident
/// warning state so a maxed-looking but unverified number never reads as an alarm (brief §8).
const UNVERIFIED_CUE: &str = " ? unverified";

/// Cells in the painted meter — the TUI's `W = 12` (DESIGN-SYSTEM "Limit meter"). Each cell
/// is a 2×4 dot block (a braille glyph's shape), painted dot by dot.
const METER_CELLS: usize = 12;

/// The painted meter's allocated size (px). Compact + terminal-native; the dots scale to it.
const METER_W: f32 = 96.0;
const METER_H: f32 = 18.0;

/// What a meter paints for its fill — the never-color-alone cue is the dot DENSITY here, the
/// ramp tint is secondary.
#[derive(Debug, Clone, Copy, PartialEq)]
pub enum MeterFill {
    /// `Available` token fraction — a real painted fill at its 0–8 warning-ramp `step`.
    Confident { fraction: f64, step: u8 },
    /// `Unverified` token fraction — a painted fill, but NEVER a confident warning step: the
    /// dots show the density honestly in neutral ink, paired with the ` ? unverified` cue.
    Unverified { fraction: f64 },
    /// No painted fill — `Spend` / `Partial` / `Estimated` / `Unavailable` render text only
    /// (a degraded reading is never dressed as a confident fill or a high dot grid).
    None,
}

/// One quota meter's render-ready, GPU-free model: the aligned label, the fill (or its
/// absence), the right-hand detail text, the freshness stamp, and the optional Claude caveat.
#[derive(Debug, Clone)]
pub struct MeterModel {
    /// `"claude code 5h "` — fixed-width so a column of monospace meters aligns.
    pub label: String,
    /// The painted fill, or `None` for the text-only (degraded / dollar-pool) arms.
    pub fill: MeterFill,
    /// The right-hand text — percent + reset, the spend pool, or the partial/estimated/
    /// unavailable status — already composed and honest per arm.
    pub detail: String,
    /// `"as of HH:MM"` / `"capture time unknown"` / `""` (still fresh) — the age signal.
    pub stamp: String,
    /// The Claude chat-under-report caveat sub-line, when this window shows Claude usage.
    pub caveat: Option<&'static str>,
}

impl MeterModel {
    /// Build the meter model for one quota window, honest across all five availability arms.
    /// Mirrors `apps/cli/src/render.rs::render_limit_line`, minus the terminal `!`/`!!`
    /// badges (the brand replaces those with the dot-density fill itself, §0/§6).
    pub fn from_limit(limit: &LimitSummary, generated_at: DateTime<Utc>) -> MeterModel {
        let label = format!(
            "{:<12} {:<3}",
            provider_label(limit.tool),
            kind_label(limit.kind)
        );
        // Only stamp an arm that carries a reading to age — `Estimated` (a local volume
        // estimate, no quota observation) and `Unavailable` (no reading) get no stamp, and a
        // measure-less `Partial` has nothing to age either. Mirrors the CLI's
        // `render_limit_line`, which stamps Available / Unverified / measure-Partial only.
        let stamp = if arm_carries_reading(&limit.availability) {
            freshness_stamp(limit.captured_at, generated_at)
        } else {
            String::new()
        };
        let caveat = claude_caveat(limit);
        let (fill, detail) = render_arm(&limit.availability);
        MeterModel {
            label,
            fill,
            detail,
            stamp,
            caveat,
        }
    }
}

/// Whether an availability arm carries a quota reading worth aging with a freshness stamp.
fn arm_carries_reading(availability: &LimitAvailability) -> bool {
    match availability {
        LimitAvailability::Available { .. } | LimitAvailability::Unverified { .. } => true,
        LimitAvailability::Partial { measure, .. } => measure.is_some(),
        LimitAvailability::Estimated { .. } | LimitAvailability::Unavailable { .. } => false,
    }
}

/// The `(fill, detail)` for one availability arm. Kept separate so the honesty logic is one
/// self-contained, unit-tested function.
fn render_arm(availability: &LimitAvailability) -> (MeterFill, String) {
    // The reset clause for the arms that carry an optional reset (`Partial`/`Unverified`).
    let optional_reset = |seconds: &Option<i64>| {
        seconds
            .map(reset_countdown)
            .map(|value| format!("  resets {value}"))
            .unwrap_or_default()
    };

    // `"$used / $included used"` (or `"$used used"` with no published allowance) — never a
    // fabricated denominator. A local closure keeps the `Decimal` types inferred so the bar
    // names no money type: it routes the values through `costroid-core`, which owns money
    // formatting (the engine computes, the bar only displays). Mirrors `render.rs::spend_text`.
    let spend_text = |used, included: &Option<_>| -> String {
        match included {
            Some(included) => format!(
                "{} / {} used",
                costroid_core::format_money_usd(used, false),
                costroid_core::format_money_usd(included, false)
            ),
            None => format!("{} used", costroid_core::format_money_usd(used, false)),
        }
    };

    match availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure {
            LimitMeasure::TokenFraction(fraction) => {
                let fraction = *fraction;
                (
                    MeterFill::Confident {
                        fraction,
                        step: severity_step(fraction),
                    },
                    format!(
                        "{}  resets {}",
                        percent(fraction),
                        reset_countdown(*reset_in_seconds)
                    ),
                )
            }
            LimitMeasure::Spend {
                used_usd,
                included_usd,
            } => (
                MeterFill::None,
                format!(
                    "{}  resets {}",
                    spend_text(used_usd, included_usd),
                    reset_countdown(*reset_in_seconds)
                ),
            ),
        },
        LimitAvailability::Unverified {
            measure,
            reset_in_seconds,
            ..
        } => {
            let reset = optional_reset(reset_in_seconds);
            match measure {
                LimitMeasure::TokenFraction(fraction) => {
                    let fraction = *fraction;
                    (
                        MeterFill::Unverified { fraction },
                        format!("{}{}{}", percent(fraction), UNVERIFIED_CUE, reset),
                    )
                }
                LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                } => (
                    MeterFill::None,
                    format!(
                        "{}{}{}",
                        spend_text(used_usd, included_usd),
                        UNVERIFIED_CUE,
                        reset
                    ),
                ),
            }
        }
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let reset = optional_reset(reset_in_seconds);
            // A partial reading is non-Available — it never paints a confident fill; its
            // percent (when present) is shown as plain text, qualified by "partial:".
            let usage = match measure {
                Some(LimitMeasure::TokenFraction(fraction)) => format!("{}  ", percent(*fraction)),
                Some(LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                }) => format!("{}  ", spend_text(used_usd, included_usd)),
                None => String::new(),
            };
            (MeterFill::None, format!("{usage}partial: {reason}{reset}"))
        }
        LimitAvailability::Estimated {
            volume_tokens,
            estimated_usd,
        } => {
            // No fill — Costroid's own volume-based estimate stands in for an absent quota
            // source; the quota % is honestly "unavailable", never a fabricated meter. The
            // value suffix mirrors `render.rs::estimated_value_suffix`: the estimate-labeled
            // `~$` when priced, the bare "(estimated)" qualifier when the model is unpriced —
            // never a guessed price (`estimated_usd` is `None`).
            let value_suffix = match estimated_usd {
                Some(value) => {
                    format!(
                        " ({}, estimated)",
                        costroid_core::format_money_usd(value, true)
                    )
                }
                None => " (estimated)".to_string(),
            };
            (
                MeterFill::None,
                format!(
                    "usage: {}{} — quota % unavailable",
                    estimated_volume_text(*volume_tokens),
                    value_suffix
                ),
            )
        }
        LimitAvailability::Unavailable { reason } => {
            (MeterFill::None, format!("unavailable: {reason}"))
        }
    }
}

/// The `Estimated` arm's local token volume, thousands-grouped. Mirrors
/// `render.rs::estimated_volume_text`.
fn estimated_volume_text(volume_tokens: u64) -> String {
    format!("{} tokens", with_thousands(&volume_tokens.to_string()))
}

/// The Claude chat-under-report caveat for a Claude window that shows usage, else `None`
/// (mirrors `render.rs::claude_caveat`: `Available` / `Unverified` / `Estimated`).
fn claude_caveat(limit: &LimitSummary) -> Option<&'static str> {
    let shows_usage = matches!(
        limit.availability,
        LimitAvailability::Available { .. }
            | LimitAvailability::Unverified { .. }
            | LimitAvailability::Estimated { .. }
    );
    (limit.tool == ProviderId::ClaudeCode && shows_usage).then_some(CLAUDE_CHAT_CAVEAT)
}

/// How the painted meter fills: the TUI's `meter_segments` (floor + a boundary half-cell +
/// a min-visibility floor), so the fill LENGTH matches the terminal meter exactly.
struct Segments {
    full: usize,
    half: bool,
}

fn meter_segments(fraction: f64, width: usize) -> Segments {
    if width == 0 {
        return Segments {
            full: 0,
            half: false,
        };
    }
    let clamped = if fraction.is_finite() {
        fraction.clamp(0.0, 1.0)
    } else {
        0.0
    };
    if clamped >= 1.0 {
        return Segments {
            full: width,
            half: false,
        };
    }
    if clamped <= 0.0 {
        return Segments {
            full: 0,
            half: false,
        };
    }
    let exact = clamped * width as f64;
    let mut full = exact.floor() as usize;
    let mut half = exact - full as f64 >= 0.5;
    // Min-visibility: any nonzero usage lights at least the boundary half-cell.
    if full == 0 && !half {
        half = true;
    }
    if half && full >= width {
        half = false;
    }
    if full + usize::from(half) > width {
        full = width;
        half = false;
    }
    Segments { full, half }
}

/// Paint one meter row: the aligned label, the painted fill (when any), the detail + stamp,
/// and an optional Claude caveat sub-line.
pub fn paint(ui: &mut egui::Ui, model: &MeterModel) {
    let detail = if model.stamp.is_empty() {
        model.detail.clone()
    } else {
        format!("{}  {}", model.detail, model.stamp)
    };
    // The painted dot fill is otherwise invisible to a screen reader, so the meter's full line
    // (label + detail) is attached to it as an AccessKit name below (T21 accessibility pass); the
    // adjacent text labels carry the same content for sighted users.
    let accessible = format!("{} {}", model.label.trim_end(), detail);
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new(&model.label)
                .monospace()
                .color(color_of(BONE)),
        );
        match &model.fill {
            MeterFill::Confident { fraction, step } => {
                paint_bar(
                    ui,
                    *fraction,
                    color_of(glyph::step_fill_color(*step)),
                    &accessible,
                );
            }
            MeterFill::Unverified { fraction } => {
                // Neutral ink — an unverified fill shows density, never a confident tint.
                paint_bar(ui, *fraction, color_of(glyph::MARK_INK), &accessible);
            }
            MeterFill::None => {}
        }
        ui.label(egui::RichText::new(detail).monospace().color(color_of(ASH)));
    });
    if let Some(caveat) = model.caveat {
        ui.horizontal(|ui| {
            ui.add_space(20.0);
            ui.label(
                egui::RichText::new(caveat)
                    .monospace()
                    .size(10.0)
                    .color(color_of(ASH)),
            );
        });
    }
}

/// Paint the dot-grid fill bar to the consumed `fraction`, lit dots in `lit`, track dots dim.
/// `name` is the meter's accessible label (T21): the painted dots carry no text, so the line is
/// attached to the allocated rect as an AccessKit progress-indicator node.
fn paint_bar(ui: &mut egui::Ui, fraction: f64, lit: egui::Color32, name: &str) {
    ui.add_space(6.0);
    let (rect, response) =
        ui.allocate_exact_size(egui::vec2(METER_W, METER_H), egui::Sense::hover());
    response
        .widget_info(|| egui::WidgetInfo::labeled(egui::WidgetType::ProgressIndicator, true, name));
    let painter = ui.painter_at(rect);
    let empty = color_of(glyph::EMPTY_DOT);
    let seg = meter_segments(fraction, METER_CELLS);

    let cell_w = rect.width() / METER_CELLS as f32;
    // Two dot-columns and four dot-rows per cell — a braille glyph's 2×4 layout, painted.
    let col_frac = [0.3_f32, 0.7];
    let row_frac = [0.16_f32, 0.38, 0.62, 0.84];
    let radius = (cell_w * 0.5).min(rect.height() / 4.0) * 0.62;

    for cell in 0..METER_CELLS {
        let left = rect.left() + cell as f32 * cell_w;
        // A full cell lights both columns; the boundary half-cell lights the left column
        // only (mirrors the TUI's `⡇`); a track cell stays dim.
        let (left_lit, right_lit) = if cell < seg.full {
            (true, true)
        } else if cell == seg.full && seg.half {
            (true, false)
        } else {
            (false, false)
        };
        for (col_index, &cf) in col_frac.iter().enumerate() {
            let lit_col = if col_index == 0 { left_lit } else { right_lit };
            let color = if lit_col { lit } else { empty };
            for &rf in &row_frac {
                let center = egui::pos2(left + cf * cell_w, rect.top() + rf * rect.height());
                painter.circle_filled(center, radius, color);
            }
        }
    }
    ui.add_space(6.0);
}

#[cfg(test)]
mod tests {
    use super::*;
    use costroid_core::LimitKind;

    fn ts(secs: i64) -> DateTime<Utc> {
        match DateTime::from_timestamp(secs, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp {secs}"),
        }
    }

    /// A render time 15 minutes after the captures below, so the freshness stamp fires.
    fn generated() -> DateTime<Utc> {
        ts(1_900_000_000 + 15 * 60)
    }

    fn limit(tool: ProviderId, kind: LimitKind, availability: LimitAvailability) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: ts(1_900_000_000),
            availability,
        }
    }

    #[test]
    fn available_token_fraction_paints_a_confident_ramp_step() {
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::ClaudeCode,
                LimitKind::FiveHour,
                LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.92),
                    resets_at: ts(1_900_003_600),
                    reset_in_seconds: 41 * 60,
                },
            ),
            generated(),
        );
        // Confident fill carries both the exact fraction and its 0–8 warning-ramp step.
        assert_eq!(
            model.fill,
            MeterFill::Confident {
                fraction: 0.92,
                step: severity_step(0.92),
            }
        );
        assert!(model.detail.contains("92%"), "detail: {}", model.detail);
        assert!(
            model.detail.contains("resets 41m"),
            "detail: {}",
            model.detail
        );
        // captured_at = ts(1_900_000_000) = 17:46 UTC; render 15 min later → past the
        // 10-minute threshold → the age is stamped.
        assert_eq!(
            model.stamp, "as of 17:46",
            "aged reading must stamp its age"
        );
        assert!(
            model.caveat.is_some(),
            "a Claude usage window carries the chat caveat"
        );
    }

    #[test]
    fn fresh_available_reading_has_no_stamp() {
        let captured = ts(1_900_000_000);
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.20),
                    resets_at: captured,
                    reset_in_seconds: 3600,
                },
            ),
            // Render only 2 minutes later → still fresh → no stamp.
            ts(1_900_000_000 + 2 * 60),
        );
        assert_eq!(model.stamp, "");
        assert!(model.caveat.is_none(), "non-Claude windows carry no caveat");
    }

    #[test]
    fn unverified_shows_the_cue_and_a_non_confident_fill() {
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::ClaudeCode,
                LimitKind::Weekly,
                LimitAvailability::Unverified {
                    measure: LimitMeasure::TokenFraction(0.96),
                    resets_at: None,
                    reset_in_seconds: Some(3 * 86_400),
                },
            ),
            generated(),
        );
        assert!(
            !matches!(model.fill, MeterFill::Confident { .. }),
            "an unverified reading must never render a confident fill"
        );
        assert_eq!(model.fill, MeterFill::Unverified { fraction: 0.96 });
        assert!(
            model.detail.contains("? unverified"),
            "detail: {}",
            model.detail
        );
        assert!(model.detail.contains("96%"));
    }

    #[test]
    fn partial_is_text_only_with_its_reason() {
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                LimitAvailability::Partial {
                    measure: Some(LimitMeasure::TokenFraction(0.55)),
                    resets_at: None,
                    reset_in_seconds: Some(7200),
                    reason: "cross-check pending".to_owned(),
                },
            ),
            generated(),
        );
        assert_eq!(model.fill, MeterFill::None, "partial paints no fill");
        assert!(model.detail.contains("partial: cross-check pending"));
        // The percent is shown as plain text, qualified by "partial:" — not as a fill.
        assert!(model.detail.contains("55%"));
    }

    #[test]
    fn estimated_shows_volume_and_quota_unavailable_with_no_fill() {
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::ClaudeCode,
                LimitKind::Weekly,
                LimitAvailability::Estimated {
                    volume_tokens: 1_234_567,
                    estimated_usd: None,
                },
            ),
            generated(),
        );
        assert_eq!(model.fill, MeterFill::None);
        // Unpriced model → the bare "(estimated)" qualifier, never a guessed price.
        assert!(
            model.detail.contains("usage: 1,234,567 tokens (estimated)"),
            "detail: {}",
            model.detail
        );
        assert!(model.detail.contains("quota % unavailable"));
        assert!(
            model.caveat.is_some(),
            "an estimated Claude window keeps the chat caveat"
        );
    }

    #[test]
    fn estimated_priced_path_shows_the_estimate_labeled_value() {
        // A priced Estimated window appends the estimate-labeled `~$` suffix (mirroring the
        // CLI's `estimated_value_suffix`). `Default::default()` is a zero `Decimal`, so the
        // bar names no money type here; the dollar formatting itself is covered by
        // costroid-core's `format_money_usd` tests.
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::Codex,
                LimitKind::FiveHour,
                LimitAvailability::Estimated {
                    volume_tokens: 500,
                    estimated_usd: Some(Default::default()),
                },
            ),
            generated(),
        );
        assert_eq!(model.fill, MeterFill::None, "estimated paints no fill");
        assert!(
            model.detail.contains("(~$0.00, estimated)"),
            "priced estimate must carry the estimate-labeled value: {}",
            model.detail
        );
        assert!(model.detail.contains("quota % unavailable"));
    }

    #[test]
    fn unavailable_is_an_honest_reason_with_no_fill() {
        let model = MeterModel::from_limit(
            &limit(
                ProviderId::Cursor,
                LimitKind::Monthly,
                LimitAvailability::Unavailable {
                    reason: "no sanctioned source".to_owned(),
                },
            ),
            generated(),
        );
        assert_eq!(model.fill, MeterFill::None);
        assert_eq!(model.detail, "unavailable: no sanctioned source");
        assert!(
            model.stamp.is_empty(),
            "unavailable carries no reading to stamp"
        );
    }

    #[test]
    fn every_degraded_arm_yields_no_confident_fill() {
        let degraded = [
            LimitAvailability::Unverified {
                measure: LimitMeasure::TokenFraction(0.99),
                resets_at: None,
                reset_in_seconds: None,
            },
            LimitAvailability::Partial {
                measure: None,
                resets_at: None,
                reset_in_seconds: None,
                reason: "thin data".to_owned(),
            },
            LimitAvailability::Estimated {
                volume_tokens: 42,
                estimated_usd: None,
            },
            LimitAvailability::Unavailable {
                reason: "no source".to_owned(),
            },
        ];
        for availability in degraded {
            let model = MeterModel::from_limit(
                &limit(ProviderId::ClaudeCode, LimitKind::FiveHour, availability),
                generated(),
            );
            assert!(
                !matches!(model.fill, MeterFill::Confident { .. }),
                "degraded arm fabricated a confident fill: {}",
                model.detail
            );
        }
    }

    #[test]
    fn meter_segments_floor_half_and_min_visibility() {
        // 0.42 of 6 cells → floor(2.52)=2 full, remainder 0.52 ≥ 0.5 → a half-cell.
        let seg = meter_segments(0.42, 6);
        assert_eq!((seg.full, seg.half), (2, true));
        // A tiny nonzero fraction still lights the boundary half-cell (min-visibility).
        let seg = meter_segments(0.001, 12);
        assert_eq!((seg.full, seg.half), (0, true));
        // At/over the limit fills every cell, no half.
        let seg = meter_segments(1.0, 12);
        assert_eq!((seg.full, seg.half), (12, false));
        // Zero lights nothing.
        let seg = meter_segments(0.0, 12);
        assert_eq!((seg.full, seg.half), (0, false));
        // Non-finite degrades to empty, never a panic.
        let seg = meter_segments(f64::NAN, 12);
        assert_eq!((seg.full, seg.half), (0, false));
    }
}
