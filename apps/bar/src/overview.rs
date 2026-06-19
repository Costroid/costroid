//! The Overview: this-period API spend above the painted quota meters.
//!
//! The Overview is the window body — a pure consumer of `costroid_core::now_summary`
//! (DESIGN-SYSTEM). It adds no compute: the period spend is the engine's
//! `now_api_spend_display` (money stays `Decimal` in core; the bar only displays the
//! hedged string), and each quota meter is a `MeterModel` over one `now_summary` window,
//! honest across all five availability arms (`meter.rs`). The active-alerts banner and the
//! four live panels (Budget/Forecast/Anomalies/Providers) live in their own modules (added in
//! T20); this Overview card is the meters + the spend header.

use costroid_core::{NowSummary, ProviderStatusKind};

use crate::app::{color_of, series_color, ASH, BONE, SIGNAL};
use crate::format::{provider_label, provider_status_word};
use crate::glyph;
use crate::meter::{self, MeterModel};

/// The Overview's GPU-free model: the period-spend display string + one meter per window.
/// Pure, so the whole Overview is unit-testable without a window.
#[derive(Debug, Clone)]
pub struct OverviewModel {
    /// This period's API-lane spend, `~`-hedged + estimate-labeled (e.g. `"~$42.18"`).
    pub spend_display: String,
    /// One painted meter per `now_summary` limit window.
    pub meters: Vec<MeterModel>,
}

impl OverviewModel {
    pub fn from_summary(summary: &NowSummary) -> OverviewModel {
        OverviewModel {
            // Money stays `Decimal` in the engine; the bar receives the finished string.
            spend_display: costroid_core::now_api_spend_display(summary),
            meters: summary
                .limits
                .iter()
                .map(|limit| MeterModel::from_limit(limit, summary.generated_at))
                .collect(),
        }
    }
}

/// Draw the Overview body: the period-spend header, then the stacked quota meters.
pub fn draw(ui: &mut egui::Ui, model: &OverviewModel) {
    draw_spend_header(ui, &model.spend_display);
    header_rule(ui);

    if model.meters.is_empty() {
        ui.add_space(2.0);
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new("no local limit data found")
                    .monospace()
                    .color(color_of(ASH)),
            );
        });
        return;
    }
    let mut caveat: Option<&'static str> = None;
    for meter in &model.meters {
        ui.add_space(4.0);
        meter::paint(ui, meter);
        if caveat.is_none() {
            caveat = meter.caveat;
        }
    }
    // The Claude chat caveat is shown ONCE under the stack (deduped), not repeated per Claude window.
    if let Some(text) = caveat {
        ui.add_space(3.0);
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new(text)
                    .monospace()
                    .size(10.0)
                    .color(color_of(ASH)),
            );
        });
    }
}

/// The header: the period label, the `~`-hedged spend (Bone, the headline figure), and the
/// explicit "estimate" label — every dollar is hedged AND estimate-labeled (§5/§6). The
/// period label mirrors the CLI now-header's "this week" (the bar collects with
/// `NowOptions::default()`, i.e. `Period::Week`).
fn draw_spend_header(ui: &mut egui::Ui, spend_display: &str) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("this week")
                .monospace()
                .color(color_of(ASH)),
        );
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new(spend_display)
                .monospace()
                .strong()
                .size(18.0)
                .color(color_of(BONE)),
        );
        ui.add_space(6.0);
        ui.label(
            egui::RichText::new("estimate")
                .monospace()
                .size(11.0)
                .color(color_of(ASH)),
        );
    });
}

/// The Overview tab's lower region: the now per-model API-cost breakdown + the non-`Available`
/// provider notes (DESIGN-SYSTEM; the persistent header above is the spend, meters,
/// and banner). Pure data — money stays `Decimal` in core (the bar receives the finished
/// `~`-hedged string), so the bar names no money type.
#[derive(Debug, Clone)]
pub struct NowBreakdown {
    /// One API-lane row per model, highest spend first: the model id + the `~`-hedged $ estimate.
    pub costs: Vec<NowCostRow>,
    /// One note per non-`Available` provider (Cursor's detect-and-defer, a partial/missing/error
    /// provider) — inline + non-fatal, mirroring the CLI now-screen's `push_provider_notes`.
    pub notes: Vec<String>,
}

/// One model's API-lane spend for the Overview breakdown.
#[derive(Debug, Clone)]
pub struct NowCostRow {
    pub model: String,
    /// The `~`-hedged + estimate-labeled spend (e.g. `"~$24.10"`).
    pub spend_display: String,
    /// This model's spend as a `0.0..=1.0` share of the top model's spend — the colored share-bar
    /// length (the top model is `1.0`). Computed in core (the bar names no money type).
    pub fraction: f64,
}

impl NowBreakdown {
    pub fn from_summary(summary: &NowSummary) -> NowBreakdown {
        // API-lane rows, highest spend first + normalized share — computed in core so the bar names
        // no money type (`costroid_core::now_model_spend_breakdown`, mirroring the CLI ordering).
        let costs = costroid_core::now_model_spend_breakdown(summary)
            .into_iter()
            .map(|row| NowCostRow {
                model: row.model,
                spend_display: row.spend_display,
                fraction: row.fraction,
            })
            .collect();

        // Non-Available providers surface as inline notes (mirrors `render.rs::push_provider_notes`).
        let notes = summary
            .providers
            .iter()
            .filter(|provider| provider.status != ProviderStatusKind::Available)
            .map(|provider| {
                let message = provider
                    .message
                    .as_deref()
                    .unwrap_or("local data incomplete");
                format!(
                    "provider {} {}: {message}",
                    provider_label(provider.provider),
                    provider_status_word(provider.status),
                )
            })
            .collect();

        NowBreakdown { costs, notes }
    }
}

/// Draw the Overview tab's lower region: the per-model API-cost rows (or an honest empty line)
/// followed by any provider notes. Cost rows never carry a severity cue (amber is for limits,
/// not spend — DESIGN-SYSTEM "API cost bar"); the dollar is always `~`-hedged + estimate-labeled.
pub fn draw_breakdown(ui: &mut egui::Ui, breakdown: &NowBreakdown) {
    ui.add_space(2.0);
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("by model")
                .monospace()
                .color(color_of(ASH)),
        );
    });
    if breakdown.costs.is_empty() {
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new("no api-billed usage yet")
                    .monospace()
                    .color(color_of(ASH)),
            );
        });
    } else {
        for (index, row) in breakdown.costs.iter().enumerate() {
            draw_cost_row(ui, index, row);
        }
    }
    for note in &breakdown.notes {
        ui.add_space(1.0);
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new(note)
                    .monospace()
                    .size(11.0)
                    .color(color_of(ASH)),
            );
        });
    }
}

/// One per-model spend row: a leading Series-hued legend dot + a single-row share dot-bar in that
/// hue (the rank-by-spend cue, distinct from the quota meters' dense 2×4 dot blocks) + the model
/// name + the `~`-hedged `$`. The dot + name carry identity even without color (the share value is
/// also in the `$` label), so it never relies on color alone. The `index` keys the Series hue by
/// spend rank, exactly matching the CLI's per-model coloring.
///
/// The leading legend dot is **deliberately decorative** — it carries no AccessKit name (unlike the
/// share dot-bar, which is named with its share %), because it is fully redundant with the row's
/// real text labels (the model name + the `$` spend beside it) and the named share bar; naming it
/// too would only add a noisy, redundant screen-reader announcement per row.
fn draw_cost_row(ui: &mut egui::Ui, index: usize, row: &NowCostRow) {
    let hue = series_color(index);
    ui.add_space(3.0);
    ui.horizontal(|ui| {
        ui.spacing_mut().item_spacing.x = 6.0;
        ui.add_space(8.0);
        // The legend dot.
        let (dot_rect, _r) = ui.allocate_exact_size(egui::Vec2::splat(8.0), egui::Sense::hover());
        ui.painter_at(dot_rect)
            .circle_filled(dot_rect.center(), 3.0, hue);
        // The share dot-bar.
        paint_share_dots(ui, row.fraction, hue);
        ui.label(
            egui::RichText::new(format!("{:<16}", row.model))
                .monospace()
                .color(color_of(BONE)),
        );
        ui.label(
            egui::RichText::new(&row.spend_display)
                .monospace()
                .strong()
                .color(color_of(BONE)),
        );
    });
}

/// Paint a compact single-row share bar: `DOTS` dots lit to `fraction` in `hue`, the rest the dim
/// track. One row of dots (not the meters' dense 2×4 blocks) so the spend-share read is visually
/// distinct from a quota meter. Any nonzero share lights at least one dot (min-visibility).
fn paint_share_dots(ui: &mut egui::Ui, fraction: f64, hue: egui::Color32) {
    const DOTS: usize = 10;
    let width = 58.0_f32;
    let height = 10.0_f32;
    let (rect, response) = ui.allocate_exact_size(egui::vec2(width, height), egui::Sense::hover());
    response.widget_info(|| {
        egui::WidgetInfo::labeled(
            egui::WidgetType::ProgressIndicator,
            true,
            format!(
                "spend share {}%",
                (fraction.clamp(0.0, 1.0) * 100.0).round()
            ),
        )
    });
    let painter = ui.painter_at(rect);
    let lit = if fraction.is_finite() && fraction > 0.0 {
        ((fraction.clamp(0.0, 1.0) * DOTS as f64).round() as usize).clamp(1, DOTS)
    } else {
        0
    };
    let cell_w = rect.width() / DOTS as f32;
    let radius = (cell_w * 0.5).min(rect.height() * 0.5) * 0.6;
    let track = color_of(glyph::EMPTY_DOT);
    for i in 0..DOTS {
        let cx = rect.left() + (i as f32 + 0.5) * cell_w;
        let on = i < lit;
        painter.circle_filled(
            egui::pos2(cx, rect.center().y),
            radius,
            if on { hue } else { track },
        );
    }
}

/// A thin Signal-lime accent rule under the header — the Overview's single, sparing use of
/// the "live" accent (§0/§6: lime is the active/"live" highlight; the active-tab/selected
/// uses arrive with T20's tab strip). Marks the live glance header, never relied on for
/// meaning (it carries no severity — that is the meters' dot density).
fn header_rule(ui: &mut egui::Ui) {
    ui.add_space(6.0);
    let width = ui.available_width().min(320.0);
    let (rect, _response) = ui.allocate_exact_size(egui::vec2(width, 2.0), egui::Sense::hover());
    ui.painter().rect_filled(rect, 0.0, color_of(SIGNAL));
    ui.add_space(6.0);
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{DateTime, Utc};
    use costroid_core::{
        GroupBy, LimitAvailability, LimitKind, LimitMeasure, LimitSummary, PeriodRange, ProviderId,
    };

    fn ts(secs: i64) -> DateTime<Utc> {
        match DateTime::from_timestamp(secs, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp {secs}"),
        }
    }

    fn window(tool: ProviderId, kind: LimitKind, availability: LimitAvailability) -> LimitSummary {
        LimitSummary {
            tool,
            plan: None,
            kind,
            label: None,
            captured_at: ts(1_900_000_000),
            availability,
        }
    }

    /// A summary exercising every availability arm — render time 15 min after capture so the
    /// aged readings stamp their age. No API cost rows (so the dollar sum needs no `Decimal`
    /// in the bar — the nonzero sum is covered by `costroid-core`'s own test).
    fn all_arms_summary() -> NowSummary {
        NowSummary {
            generated_at: ts(1_900_000_000 + 15 * 60),
            cost_period: PeriodRange {
                start: ts(1_899_000_000),
                end: ts(1_901_000_000),
            },
            group_by: GroupBy::Model,
            limits: vec![
                window(
                    ProviderId::ClaudeCode,
                    LimitKind::FiveHour,
                    LimitAvailability::Available {
                        measure: LimitMeasure::TokenFraction(0.92),
                        resets_at: ts(1_900_003_600),
                        reset_in_seconds: 41 * 60,
                    },
                ),
                window(
                    ProviderId::ClaudeCode,
                    LimitKind::Weekly,
                    LimitAvailability::Unverified {
                        measure: LimitMeasure::TokenFraction(0.96),
                        resets_at: None,
                        reset_in_seconds: Some(3 * 86_400),
                    },
                ),
                window(
                    ProviderId::Codex,
                    LimitKind::FiveHour,
                    LimitAvailability::Partial {
                        measure: None,
                        resets_at: None,
                        reset_in_seconds: None,
                        reason: "thin data".to_owned(),
                    },
                ),
                window(
                    ProviderId::ClaudeCode,
                    LimitKind::Weekly,
                    LimitAvailability::Estimated {
                        volume_tokens: 1_234_567,
                        estimated_usd: None,
                    },
                ),
                window(
                    ProviderId::Cursor,
                    LimitKind::Monthly,
                    LimitAvailability::Unavailable {
                        reason: "no sanctioned source".to_owned(),
                    },
                ),
            ],
            current_costs: Vec::new(),
            providers: Vec::new(),
        }
    }

    fn empty_summary() -> NowSummary {
        NowSummary {
            generated_at: ts(1_900_000_000),
            cost_period: PeriodRange {
                start: ts(1_899_000_000),
                end: ts(1_901_000_000),
            },
            group_by: GroupBy::Model,
            limits: Vec::new(),
            current_costs: Vec::new(),
            providers: Vec::new(),
        }
    }

    #[test]
    fn model_maps_each_window_and_hedges_the_spend() {
        let model = OverviewModel::from_summary(&all_arms_summary());
        assert_eq!(model.meters.len(), 5, "one meter per window");
        // No API usage → the honest, hedged zero (the nonzero arithmetic is core-tested).
        assert_eq!(model.spend_display, "~$0.00");
    }

    #[test]
    fn only_the_available_window_paints_a_confident_fill() {
        use crate::meter::MeterFill;
        let model = OverviewModel::from_summary(&all_arms_summary());
        let confident: Vec<_> = model
            .meters
            .iter()
            .filter(|meter| matches!(meter.fill, MeterFill::Confident { .. }))
            .collect();
        assert_eq!(
            confident.len(),
            1,
            "exactly the one Available window paints a confident fill"
        );
        // ...and that one is the 92% Claude 5h window, at its 0–8 warning-ramp step.
        assert_eq!(
            confident[0].fill,
            MeterFill::Confident {
                fraction: 0.92,
                step: crate::severity::severity_step(0.92),
            }
        );
    }

    #[test]
    fn degraded_windows_never_fabricate_a_fill() {
        use crate::meter::MeterFill;
        let model = OverviewModel::from_summary(&all_arms_summary());
        for meter in &model.meters {
            if meter.detail.contains("unavailable")
                || meter.detail.contains("partial")
                || meter.detail.contains("? unverified")
                || meter.detail.contains("quota % unavailable")
            {
                assert!(
                    !matches!(meter.fill, MeterFill::Confident { .. }),
                    "a degraded window painted a confident fill: {}",
                    meter.detail
                );
            }
        }
    }

    #[test]
    fn headless_draw_does_not_panic() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);

        for summary in [all_arms_summary(), empty_summary()] {
            let model = OverviewModel::from_summary(&summary);
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                draw(ui, &model);
            });
        }
    }
}
