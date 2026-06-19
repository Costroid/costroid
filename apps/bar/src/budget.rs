//! The Budget panel — `budget_view`, rendered in the brand's painted dot/braille language.
//!
//! Mirrors the CLI's `render_budget_*` SEMANTICS: per-scope spent-vs-target (API-lane $, this
//! month, always `~`-hedged estimates), the pace reference, the strict over-by overshoot, and the
//! honest "no budget set" / withheld-tool empty states (§170 — a flat-fee subscription or an
//! unclassified install is never given a fabricated `$0 / target` row). The per-budget meter
//! reuses the same painted dot primitive as the quota meters (`meter::paint`); its FILL LENGTH is
//! the utilization fraction (the never-color-alone density cue) and its TINT is the budget state.

use costroid_core::{
    format_money_usd, format_over_by_usd, BudgetExcludedTool, BudgetExclusion, BudgetPace,
    BudgetRow, BudgetScope, BudgetView, ALERT_CRITICAL_FRACTION, ALERT_WARN_FRACTION,
};

use crate::app::{chip, color_of, ASH, BONE, CAUTION, CRITICAL, HEALTHY};
use crate::format::percent;
use crate::meter::{self, MeterFill, MeterModel};

const BUDGET_CONFIG_HINT: &str =
    "no budget set — add [budget] targets in ~/.config/costroid/config.toml";
const BUDGET_NO_USABLE_TARGETS: &str =
    "no usable budget targets — check ~/.config/costroid/config.toml";

/// Draw the Budget panel. Pure of app/thread state — a headless egui pass exercises it. The
/// persistent header status line carries the "estimates" honesty caveat, so the panel does not
/// repeat it (lean taskbar — the CLI keeps the long-form notes).
pub fn draw(ui: &mut egui::Ui, view: &BudgetView) {
    draw_header(ui, view);

    if view.no_budget_set {
        draw_empty_state(ui);
        return;
    }

    let mut any = false;
    for row in &view.rows {
        ui.add_space(4.0);
        meter::paint(ui, &row_meter(row));
        draw_pace(ui, row, view);
        any = true;
    }
    for excluded in &view.excluded_tools {
        ui.add_space(2.0);
        text_line(ui, &excluded_line(excluded), ASH, false);
        any = true;
    }
    if !any {
        text_line(ui, BUDGET_NO_USABLE_TARGETS, ASH, false);
    }
}

/// The pace line under a budget row: a colored state chip (`on track` / `ahead of pace` / `over
/// budget`) followed by the used-vs-elapsed numbers + any over-by overshoot — the chip WORD + the
/// over-by `$` carry the state without relying on color (never color alone).
fn draw_pace(ui: &mut egui::Ui, row: &BudgetRow, view: &BudgetView) {
    ui.horizontal(|ui| {
        ui.spacing_mut().item_spacing.x = 6.0;
        ui.add_space(8.0);
        chip(ui, pace_phrase(row.pace), pace_color(row.pace));
        let mut detail = format!(
            "{} used · {} elapsed",
            percent(row.fraction),
            percent(view.month_elapsed_fraction),
        );
        if let Some(over) = &row.over_by_usd {
            detail.push_str(&format!(" · over by {}", format_over_by_usd(over)));
        }
        ui.label(
            egui::RichText::new(detail)
                .monospace()
                .size(11.0)
                .color(color_of(ASH)),
        );
    });
}

fn draw_header(ui: &mut egui::Ui, view: &BudgetView) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("budget")
                .monospace()
                .color(color_of(ASH)),
        );
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new(format_money_usd(&view.spent_total_usd, true))
                .monospace()
                .strong()
                .color(color_of(BONE)),
        );
        ui.add_space(4.0);
        ui.label(
            egui::RichText::new("this month")
                .monospace()
                .size(11.0)
                .color(color_of(ASH)),
        );
    });
}

/// One budget row as a reusable [`MeterModel`]: the scope label, a painted fill to the utilization
/// fraction (clamped — an over-budget bar reads full), tinted by the budget state, then the
/// `~$spent / ~$target  NN%` detail. The over-state is carried by the dot density + the "over by"
/// / pace lines below (the brand's never-color-alone rule), never by a bare `!`/color.
fn row_meter(row: &BudgetRow) -> MeterModel {
    MeterModel {
        label: format!("{:<18}", scope_label(&row.scope)),
        fill: MeterFill::Confident {
            fraction: row.fraction,
            step: budget_step(row),
        },
        detail: format!(
            "{} / {}  {}",
            format_money_usd(&row.spent_usd, true),
            format_money_usd(&row.target_usd, true),
            percent(row.fraction),
        ),
        stamp: String::new(),
        caveat: None,
    }
}

/// The 0–8 dot-grid step for a budget row's fill TINT, keyed on the budget STATE (not the raw
/// fraction) so a comfortably-under-budget row reads green even at mid-month: Over → 8 (critical),
/// Critical (>= 0.95) → 6, Warn (>= 0.80) → 4, else 2. "Over" is the core's STRICT `over_by_usd`
/// (so an exactly-at-budget row is Critical, never "over"). Density (fill length) is the primary
/// cue; this tint is secondary.
fn budget_step(row: &BudgetRow) -> u8 {
    if row.over_by_usd.is_some() {
        8
    } else if row.fraction >= ALERT_CRITICAL_FRACTION {
        6
    } else if row.fraction >= ALERT_WARN_FRACTION {
        4
    } else {
        2
    }
}

fn pace_phrase(pace: BudgetPace) -> &'static str {
    match pace {
        BudgetPace::OnTrack => "on track",
        BudgetPace::AheadOfPace => "ahead of pace",
        BudgetPace::OverBudget => "over budget",
    }
}

/// The pace chip's color — green on track, amber ahead of pace (spending faster than the month
/// elapses), red over budget. Always paired with [`pace_phrase`] (never color alone).
fn pace_color(pace: BudgetPace) -> [u8; 4] {
    match pace {
        BudgetPace::OnTrack => HEALTHY,
        BudgetPace::AheadOfPace => CAUTION,
        BudgetPace::OverBudget => CRITICAL,
    }
}

fn scope_label(scope: &BudgetScope) -> String {
    match scope {
        BudgetScope::Total => "total (all tools)".to_string(),
        BudgetScope::Tool(tool) => tool.clone(),
    }
}

/// The honest note for a budgeted tool with no API lane (§170): no API bill to budget, so no $
/// comparison is shown — distinguishing a flat-fee subscription (assertable) from a merely
/// unclassified install. Mirrors the CLI's `budget_excluded_line`.
fn excluded_line(excluded: &BudgetExcludedTool) -> String {
    match excluded.reason {
        BudgetExclusion::FlatFeeSubscription => format!(
            "{}: flat-fee subscription - no $ budget applies (not API-billed)",
            excluded.tool
        ),
        BudgetExclusion::NotApiBilled => format!(
            "{}: no API-billed usage - a $ budget tracks API spend only",
            excluded.tool
        ),
    }
}

/// The "no budget set" empty state — lean: one hint line + one compact example (the CLI carries the
/// full copy-paste `[budget]` schema; the taskbar points at it without the wall of text).
fn draw_empty_state(ui: &mut egui::Ui) {
    text_line(ui, BUDGET_CONFIG_HINT, ASH, false);
    ui.add_space(2.0);
    text_line(
        ui,
        "e.g.  total_monthly_usd = 100.00  ·  [budget.per_tool] claude-code = 60.00",
        ASH,
        false,
    );
}

/// A single indented monospace text line in the given ink (optionally strong).
fn text_line(ui: &mut egui::Ui, text: &str, ink: [u8; 4], strong: bool) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        let mut rich = egui::RichText::new(text).monospace().color(color_of(ink));
        if strong {
            rich = rich.strong();
        }
        ui.label(rich);
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{DateTime, Utc};

    fn ts() -> DateTime<Utc> {
        match DateTime::from_timestamp(1_900_000_000, 0) {
            Some(dt) => dt,
            None => panic!("valid ts"),
        }
    }

    fn row(scope: BudgetScope, fraction: f64, over: bool, pace: BudgetPace) -> BudgetRow {
        BudgetRow {
            scope,
            target_usd: Default::default(),
            spent_usd: Default::default(),
            fraction,
            // `Some(Default::default())` is a zero `Decimal` over-by (the bar names no money type);
            // only its presence drives the over-state + the dot step here.
            over_by_usd: over.then(Default::default),
            pace,
        }
    }

    fn view(
        rows: Vec<BudgetRow>,
        excluded: Vec<BudgetExcludedTool>,
        no_budget: bool,
    ) -> BudgetView {
        BudgetView {
            generated_at: ts(),
            rows,
            excluded_tools: excluded,
            no_budget_set: no_budget,
            spent_total_usd: Default::default(),
            month_elapsed_fraction: 0.5,
        }
    }

    #[test]
    fn budget_step_keys_on_state_not_raw_fraction() {
        // A comfortably-under row stays green even at mid-month (50% used) — never over-warned.
        assert_eq!(
            budget_step(&row(BudgetScope::Total, 0.5, false, BudgetPace::OnTrack)),
            2
        );
        assert_eq!(
            budget_step(&row(
                BudgetScope::Total,
                0.85,
                false,
                BudgetPace::AheadOfPace
            )),
            4
        );
        assert_eq!(
            budget_step(&row(
                BudgetScope::Total,
                0.97,
                false,
                BudgetPace::AheadOfPace
            )),
            6
        );
        // STRICT over (over_by present) -> 8, regardless of the fraction value.
        assert_eq!(
            budget_step(&row(BudgetScope::Total, 1.2, true, BudgetPace::OverBudget)),
            8
        );
    }

    #[test]
    fn over_row_paints_a_full_clamped_bar_at_the_critical_step() {
        let over = row(
            BudgetScope::Tool("codex".into()),
            1.5,
            true,
            BudgetPace::OverBudget,
        );
        let model = row_meter(&over);
        assert_eq!(
            model.fill,
            MeterFill::Confident {
                fraction: 1.5,
                step: 8
            }
        );
        assert!(model.detail.contains("150%"), "detail: {}", model.detail);
    }

    #[test]
    fn excluded_lines_name_the_honest_reason() {
        assert!(excluded_line(&BudgetExcludedTool {
            tool: "claude-code".into(),
            reason: BudgetExclusion::FlatFeeSubscription,
        })
        .contains("flat-fee subscription"));
        assert!(excluded_line(&BudgetExcludedTool {
            tool: "codex".into(),
            reason: BudgetExclusion::NotApiBilled,
        })
        .contains("no API-billed usage"));
    }

    #[test]
    fn headless_draw_covers_every_state() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let states = [
            view(Vec::new(), Vec::new(), true), // no budget set
            view(
                vec![
                    row(BudgetScope::Total, 0.4, false, BudgetPace::OnTrack),
                    row(
                        BudgetScope::Tool("codex".into()),
                        1.3,
                        true,
                        BudgetPace::OverBudget,
                    ),
                ],
                vec![BudgetExcludedTool {
                    tool: "claude-code".into(),
                    reason: BudgetExclusion::FlatFeeSubscription,
                }],
                false,
            ),
            view(Vec::new(), Vec::new(), false), // targets set but none usable
        ];
        for v in states {
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                draw(ui, &v);
            });
        }
    }
}
