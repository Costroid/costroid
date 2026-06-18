//! The Overview: this-period API spend above the painted quota meters.
//!
//! The Overview is the window body — a pure consumer of `costroid_core::now_summary`
//! (STEP6-TASKBAR-DESIGN §4/§5). It adds no compute: the period spend is the engine's
//! `now_api_spend_display` (money stays `Decimal` in core; the bar only displays the
//! hedged string), and each quota meter is a `MeterModel` over one `now_summary` window,
//! honest across all five availability arms (`meter.rs`). The active-alerts banner and the
//! four live panels are T20; this card is the meters + the spend header only.

use costroid_core::{CostLane, CostLaneSummary, NowSummary, ProviderStatusKind};

use crate::app::{color_of, ASH, BONE, SIGNAL};
use crate::format::{provider_label, provider_status_word};
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
    for meter in &model.meters {
        ui.add_space(4.0);
        meter::paint(ui, meter);
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
/// provider notes (STEP6-TASKBAR-DESIGN §4/§5; the persistent header above is the spend, meters,
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
}

impl NowBreakdown {
    pub fn from_summary(summary: &NowSummary) -> NowBreakdown {
        // API-lane rows only (a subscription/unknown lane is never a summable $ — §170); highest
        // spend first, ties broken by model name, exactly as the CLI's `sorted_lane_rows`. The
        // `Decimal` `billed_cost` is compared + formatted through core, never named in the bar.
        let mut rows: Vec<&CostLaneSummary> = summary
            .current_costs
            .iter()
            .filter(|row| row.lane == CostLane::Api)
            .collect();
        rows.sort_by(|left, right| {
            right
                .totals
                .billed_cost
                .cmp(&left.totals.billed_cost)
                .then_with(|| left.group.value.cmp(&right.group.value))
        });
        let costs = rows
            .into_iter()
            .map(|row| NowCostRow {
                model: row.group.value.clone(),
                spend_display: costroid_core::format_money_usd(&row.totals.billed_cost, true),
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
            egui::RichText::new("api spend by model (this week)")
                .monospace()
                .color(color_of(ASH)),
        );
    });
    if breakdown.costs.is_empty() {
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new("no api-billed usage this week")
                    .monospace()
                    .color(color_of(ASH)),
            );
        });
    } else {
        for row in &breakdown.costs {
            ui.horizontal(|ui| {
                ui.add_space(8.0);
                ui.label(
                    egui::RichText::new(format!("{:<22}", row.model))
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
    }
    for note in &breakdown.notes {
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
