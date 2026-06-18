//! The opt-in alert banner — `active_alerts`, painted in the brand's dot-grid language.
//!
//! When the user has enabled `[alerts]` and at least one crossing is active, the banner sits atop
//! the Overview (STEP6-TASKBAR-DESIGN §7). It mirrors the CLI's `push_alert_banner`/`alert_line`
//! SEMANTICS — one human sentence per active alert, sentence-case, hedged, quota framed as
//! quota-extension and budgets in dollars — with one brand difference: severity is the **0–8
//! dot-grid step** (§0), NOT the terminal `!`/`!!` badges. The dot DENSITY is the never-color-alone
//! cue; the ramp tint is secondary.
//!
//! Gating lives upstream (`app.rs` builds `active_alerts` only when `config.alerts_enabled()`, and
//! passes the advisory views only when their sub-flags are on), so an empty slice here simply draws
//! nothing — the banner is absent when alerts are off OR there is no crossing.

use costroid_core::{
    anomaly_multiple_phrase, format_money_usd, format_over_by_usd, Alert, BudgetScope, LimitKind,
};

use crate::app::color_of;
use crate::format::{percent, provider_label, reset_countdown};
use crate::glyph;

/// The dot-grid severity step for an alert line (STEP6-TASKBAR-DESIGN §7: "critical/over-budget =
/// high step, warn/advisory = mid"). The two-tier split is exactly the core's `Alert::is_critical`
/// (a CRITICAL quota reading or any over-budget crossing) vs the lighter tier (a WARN quota and the
/// two advisory heads-ups). High = the full 8 grid (critical red); mid = step 4 (orange).
pub fn alert_step(alert: &Alert) -> u8 {
    if alert.is_critical() {
        8
    } else {
        4
    }
}

/// Draw the banner: one row per active alert — its dot-grid severity badge + the human sentence
/// (tinted by the ramp step, but the badge density is the cue, never color alone). A no-op for an
/// empty slice (alerts off OR clear). Pure of app/thread state — a headless egui pass exercises it.
pub fn draw(ui: &mut egui::Ui, alerts: &[Alert]) {
    if alerts.is_empty() {
        return;
    }
    ui.add_space(4.0);
    for alert in alerts {
        let step = alert_step(alert);
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            paint_severity_badge(ui, step);
            ui.add_space(6.0);
            ui.label(
                egui::RichText::new(alert_sentence(alert))
                    .monospace()
                    .color(color_of(glyph::step_fill_color(step))),
            );
        });
    }
    ui.add_space(2.0);
}

/// One alert's human sentence — mirrors the CLI's `alert_sentence` (quota copy is quota-extension
/// framing, NEVER money; budget/forecast copy is dollars, always `~`-hedged). Decimal stays in
/// core: every `$`/multiple routes through a core display helper, so the bar names no money type.
fn alert_sentence(alert: &Alert) -> String {
    match alert {
        Alert::Quota {
            tool,
            kind,
            fraction,
            reset_in_seconds,
            ..
        } => format!(
            "{} {} limit at {}, resets in {}",
            provider_label(*tool),
            alert_window_phrase(*kind),
            percent(*fraction),
            reset_countdown(*reset_in_seconds),
        ),
        Alert::Budget {
            scope,
            spent_usd,
            target_usd,
            over_by_usd,
        } => format!(
            "{} over by {}, spent {} of {}",
            alert_budget_scope(scope),
            format_over_by_usd(over_by_usd),
            format_money_usd(spent_usd, true),
            format_money_usd(target_usd, true),
        ),
        // Advisory (T17b): a projection, framed as such — never asserted as a present overrun.
        Alert::Forecast {
            projected_month_usd,
            target_usd,
            projected_over_by_usd,
        } => format!(
            "total budget projected over by {}, {} projected of {}",
            format_over_by_usd(projected_over_by_usd),
            format_money_usd(projected_month_usd, true),
            format_money_usd(target_usd, true),
        ),
        // Advisory (T17b): a spend spike vs the user's own norm. The "~Nx your $Y norm" multiple is
        // shown only when it reads honestly (mirrors the Anomalies tab); otherwise "well above".
        Alert::SpendSpike {
            date,
            value_usd,
            baseline_median_usd,
            magnitude,
        } => {
            let median_display = format_money_usd(baseline_median_usd, true);
            // The displayed $ baseline rounds to zero when below half a cent — keyed off the SAME
            // string the line shows, so the guard can never diverge from the rendered figure.
            let baseline_displays_zero = median_display == "~$0.00";
            let comparison =
                match anomaly_multiple_phrase(magnitude.as_ref(), baseline_displays_zero) {
                    Some(multiple) => format!("~{multiple}x your {median_display} norm"),
                    None => format!("well above your {median_display} norm"),
                };
            format!(
                "daily spend spike: {} on {}, {comparison}",
                format_money_usd(value_usd, true),
                date.format("%b %d"),
            )
        }
    }
}

/// The readable window phrase for a quota alert — quota-extension framing (mirrors the CLI's
/// `alert_window_phrase`).
fn alert_window_phrase(kind: LimitKind) -> &'static str {
    match kind {
        LimitKind::FiveHour => "5-hour",
        LimitKind::Weekly => "weekly",
        LimitKind::Daily => "daily",
        LimitKind::Monthly => "monthly",
        LimitKind::BillingCycle => "billing-cycle",
    }
}

/// The budget scope phrase for a budget alert ($ framing; mirrors the CLI's `alert_budget_scope`).
fn alert_budget_scope(scope: &BudgetScope) -> String {
    match scope {
        BudgetScope::Total => "total budget".to_string(),
        BudgetScope::Tool(tool) => format!("{tool} budget"),
    }
}

/// Paint a compact 3×3 dot-grid severity badge filled to `step` — the brand's 0–8 warning system
/// (§0), the same dot primitive as the tray mark + the meters (`glyph.rs`). The dot COUNT is the
/// grayscale-safe cue; the ramp tint is secondary.
fn paint_severity_badge(ui: &mut egui::Ui, step: u8) {
    let side = 16.0;
    let (rect, _response) = ui.allocate_exact_size(egui::Vec2::splat(side), egui::Sense::hover());
    let painter = ui.painter_at(rect);

    let filled = glyph::dots_filled(step);
    let mut lit = [false; 9];
    for &idx in glyph::FILL_ORDER.iter().take(filled) {
        lit[idx] = true;
    }
    let fill = color_of(glyph::step_fill_color(step));
    let empty = color_of(glyph::EMPTY_DOT);
    let radius = side * 0.095;
    let cols = [0.22_f32, 0.5, 0.78];
    let rows = [0.22_f32, 0.5, 0.78];
    for (r, &ry) in rows.iter().enumerate() {
        for (c, &cx) in cols.iter().enumerate() {
            let center = rect.min + egui::vec2(cx * side, ry * side);
            painter.circle_filled(center, radius, if lit[r * 3 + c] { fill } else { empty });
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::NaiveDate;
    use costroid_core::{AlertLevel, ProviderId};

    // The bar names no money type, even in tests: `Decimal` fields are built via `Default::default()`
    // (a zero `Decimal`, inferred — never `rust_decimal` named), the T19 precedent. The real money
    // FORMATTING (non-zero `~$N`, the `<$0.01`/`~Nx` guards) is value-tested in `costroid-core`'s
    // `format_money_usd`/`format_over_by_usd`/`anomaly_multiple_phrase` tests; these cover the bar's
    // sentence STRUCTURE + the dot-grid step split + the no-panic paint.
    fn date(y: i32, m: u32, d: u32) -> NaiveDate {
        match NaiveDate::from_ymd_opt(y, m, d) {
            Some(date) => date,
            None => panic!("valid date"),
        }
    }

    fn warn_quota() -> Alert {
        Alert::Quota {
            tool: ProviderId::ClaudeCode,
            kind: LimitKind::FiveHour,
            level: AlertLevel::Warn,
            fraction: 0.83,
            reset_in_seconds: 3600,
        }
    }

    fn over_budget() -> Alert {
        Alert::Budget {
            scope: BudgetScope::Total,
            spent_usd: Default::default(),
            target_usd: Default::default(),
            over_by_usd: Default::default(),
        }
    }

    #[test]
    fn step_is_high_for_critical_and_mid_for_warn_advisory() {
        let critical = Alert::Quota {
            tool: ProviderId::ClaudeCode,
            kind: LimitKind::Weekly,
            level: AlertLevel::Critical,
            fraction: 0.97,
            reset_in_seconds: 3600,
        };
        let spike = Alert::SpendSpike {
            date: date(2026, 6, 18),
            value_usd: Default::default(),
            baseline_median_usd: Default::default(),
            magnitude: None,
        };
        assert_eq!(alert_step(&critical), 8);
        assert_eq!(alert_step(&over_budget()), 8);
        assert_eq!(alert_step(&warn_quota()), 4);
        assert_eq!(
            alert_step(&spike),
            4,
            "advisory spike is the mid heads-up tier"
        );
    }

    #[test]
    fn quota_sentence_is_quota_extension_never_money() {
        let alert = Alert::Quota {
            tool: ProviderId::ClaudeCode,
            kind: LimitKind::Weekly,
            level: AlertLevel::Critical,
            fraction: 0.97,
            reset_in_seconds: 2 * 86_400 + 6 * 3600,
        };
        let sentence = alert_sentence(&alert);
        assert_eq!(sentence, "claude code weekly limit at 97%, resets in 2d 6h");
        assert!(
            !sentence.contains('$'),
            "quota copy is never money: {sentence}"
        );
    }

    #[test]
    fn budget_sentence_structure_routes_through_core_money() {
        // Zero fixtures verify the template + ordering; the `~$N` / `<$0.01` formatting is
        // value-tested in core. over_by = 0 -> the honest "<$0.01" sub-cent guard.
        let alert = Alert::Budget {
            scope: BudgetScope::Tool("codex".to_string()),
            spent_usd: Default::default(),
            target_usd: Default::default(),
            over_by_usd: Default::default(),
        };
        assert_eq!(
            alert_sentence(&alert),
            "codex budget over by <$0.01, spent ~$0.00 of ~$0.00"
        );
    }

    #[test]
    fn spend_spike_sentence_falls_back_to_well_above_over_a_zero_baseline() {
        let alert = Alert::SpendSpike {
            date: date(2026, 6, 18),
            value_usd: Default::default(),
            baseline_median_usd: Default::default(),
            magnitude: None,
        };
        assert_eq!(
            alert_sentence(&alert),
            "daily spend spike: ~$0.00 on Jun 18, well above your ~$0.00 norm"
        );
    }

    #[test]
    fn headless_draw_does_not_panic() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let alerts = [warn_quota(), over_budget()];
        for slice in [&alerts[..], &[]] {
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                draw(ui, slice);
            });
        }
    }
}
