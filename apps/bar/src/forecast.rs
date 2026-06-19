//! The Forecast panel — `forecast_view`, rendered in the brand's painted dot language.
//!
//! Mirrors the CLI's `render_forecast_*` SEMANTICS: a month-end $ projection (or the honest
//! below-floor "insufficient data" state), the per-UTC-day actual-spend sparkline (painted dots in
//! the brand's cold cyan-blue data ramp — "logs, data, raw compute", §6), and one honest quota-ETA
//! line per window (a projected weekday, a "resets first", or a typed "unavailable" — degrade,
//! never a confident wrong ETA). Every figure is a labeled estimate; the spend normalization for
//! the sparkline routes through `costroid_core::forecast_daily_fractions`, so the bar names no money
//! type. An advisory panel: no amber/red (that is reserved for the near-limit/over-budget state).

use chrono::Datelike;
use costroid_core::{
    forecast_daily_fractions, format_money_usd, ForecastView, LimitKind, QuotaEta, QuotaEtaOutcome,
    QuotaEtaUnavailable, SpendForecast,
};

use crate::app::{color_of, ASH, BONE, DATA_CYAN};
use crate::format::provider_label;

const FORECAST_NO_USAGE: &str = "no API usage recorded — nothing to forecast yet";

/// Draw the Forecast panel. Pure of app/thread state — a headless egui pass exercises it. The
/// persistent header status carries the "estimates" caveat, so the panel drops the long scope +
/// estimate notes the CLI keeps (lean taskbar); every `$` stays `~`-hedged + `(estimated)`-tagged.
pub fn draw(ui: &mut egui::Ui, view: &ForecastView) {
    draw_header(ui, view);

    if view.no_api_usage {
        text_line(ui, FORECAST_NO_USAGE, ASH);
    } else {
        for line in spend_lines(view) {
            text_line(ui, &line, BONE);
        }
        let fractions = forecast_daily_fractions(&view.daily_actuals);
        if !fractions.is_empty() {
            ui.add_space(2.0);
            paint_sparkline(ui, &fractions);
        }
    }

    ui.add_space(4.0);
    draw_quota_section(ui, view);
}

fn draw_header(ui: &mut egui::Ui, view: &ForecastView) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("forecast")
                .monospace()
                .color(color_of(ASH)),
        );
        if let Some(money) = header_money(view) {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new(money)
                    .monospace()
                    .strong()
                    .color(color_of(BONE)),
            );
        }
    });
}

/// The header figure: the projected month-end total when we can project, else the spend-to-date —
/// or `None` when there is no API usage at all (never a fabricated `~$0.00` above the empty state).
/// Mirrors the CLI's `forecast_header_money`. The bar receives the finished string, never a Decimal.
fn header_money(view: &ForecastView) -> Option<String> {
    if view.no_api_usage {
        return None;
    }
    let amount = match &view.spend {
        SpendForecast::Projected {
            projected_month_usd,
            ..
        } => projected_month_usd,
        SpendForecast::InsufficientData {
            spend_to_date_usd, ..
        } => spend_to_date_usd,
    };
    Some(format_money_usd(amount, true))
}

/// The two spend text lines (projected / spend-so-far, or the honest below-floor state). Always
/// `~`-hedged + `(estimated)`-tagged. Mirrors the CLI's `forecast_spend_lines`.
fn spend_lines(view: &ForecastView) -> Vec<String> {
    match &view.spend {
        SpendForecast::Projected {
            projected_month_usd,
            spend_to_date_usd,
            days_elapsed,
            days_in_month,
        } => vec![
            format!(
                "projected {} by {} (estimated)",
                format_money_usd(projected_month_usd, true),
                month_end_label(view, *days_in_month),
            ),
            format!(
                "spend so far {} over {} of {} days (estimated)",
                format_money_usd(spend_to_date_usd, true),
                days_elapsed,
                days_in_month,
            ),
        ],
        SpendForecast::InsufficientData {
            spend_to_date_usd,
            days_elapsed,
            days_in_month,
            min_days,
        } => vec![
            format!(
                "insufficient data to project - {} of {} days elapsed (need {}+) (estimated)",
                days_elapsed, days_in_month, min_days,
            ),
            format!(
                "spend so far {} over {} of {} days (estimated)",
                format_money_usd(spend_to_date_usd, true),
                days_elapsed,
                days_in_month,
            ),
        ],
    }
}

/// The last calendar day of the projection's (UTC) month, e.g. `Jun 30`. Mirrors the CLI's
/// `forecast_month_end_label`; falls back to a generic label (never a panic) on an impossible date.
fn month_end_label(view: &ForecastView, days_in_month: u32) -> String {
    let today = view.generated_at.date_naive();
    match chrono::NaiveDate::from_ymd_opt(today.year(), today.month(), days_in_month) {
        Some(end) => end.format("%b %d").to_string(),
        None => "month end".to_string(),
    }
}

/// The quota section: a `quota:` header then one honest line per window (or "no quota windows
/// tracked"). Mirrors the CLI's `push_forecast_quota_section`.
fn draw_quota_section(ui: &mut egui::Ui, view: &ForecastView) {
    if view.quota_etas.is_empty() {
        text_line(ui, "quota: no quota windows tracked", ASH);
        return;
    }
    text_line(ui, "quota:", ASH);
    for eta in &view.quota_etas {
        text_line(ui, &quota_line(eta), ASH);
    }
}

/// One quota window's forecast line. Mirrors the CLI's `forecast_quota_line` (the projected hit
/// weekday is UTC-labeled; the unavailable arms name the typed reason).
fn quota_line(eta: &QuotaEta) -> String {
    let window = format!("{} {}", provider_label(eta.tool), kind_word(eta.kind));
    match &eta.outcome {
        QuotaEtaOutcome::ProjectedHit { at, .. } => {
            format!(
                "{window}: projected to hit ~{} (UTC, estimated)",
                at.format("%A")
            )
        }
        QuotaEtaOutcome::ResetsFirst { .. } => {
            format!("{window}: resets before you hit it (estimated)")
        }
        QuotaEtaOutcome::Unavailable { reason } => {
            format!("{window}: ETA unavailable ({})", unavailable_text(*reason))
        }
    }
}

fn kind_word(kind: LimitKind) -> &'static str {
    match kind {
        LimitKind::FiveHour => "5h",
        LimitKind::Weekly => "weekly",
        LimitKind::Daily => "daily",
        LimitKind::Monthly => "monthly",
        LimitKind::BillingCycle => "billing cycle",
    }
}

fn unavailable_text(reason: QuotaEtaUnavailable) -> &'static str {
    match reason {
        QuotaEtaUnavailable::ReadingNotProjectable => "no fresh verified usage reading",
        QuotaEtaUnavailable::WindowJustStarted => "window just started",
    }
}

/// Paint the daily-actuals sparkline: one column per day, a bottom-up stack of up to 4 painted dots
/// lit to the day's normalized height, in the brand's cold cyan-blue data ramp (§6). Cost data is
/// never amber (amber is for limits); the cyan ink reads as "data/compute", and the dot height
/// carries the magnitude. `fractions` are the `0..=1` normalized day spends from core.
fn paint_sparkline(ui: &mut egui::Ui, fractions: &[f64]) {
    const ROWS: usize = 4;
    let columns = fractions.len().max(1);
    let cell_w = 6.0_f32;
    let width = (columns as f32 * cell_w).min(ui.available_width().max(cell_w));
    let height = 22.0_f32;
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        let (rect, response) =
            ui.allocate_exact_size(egui::vec2(width, height), egui::Sense::hover());
        // The painted dots carry no text — name the sparkline for AccessKit (T21). The projected /
        // spend-to-date figures are accessible labels in the lines above.
        let days = fractions.len();
        response.widget_info(|| {
            egui::WidgetInfo::labeled(
                egui::WidgetType::Image,
                true,
                format!("daily api spend sparkline, last {days} days"),
            )
        });
        let painter = ui.painter_at(rect);
        let lit = color_of(DATA_CYAN);
        let dim = color_of(crate::glyph::EMPTY_DOT);
        let col_w = rect.width() / columns as f32;
        let row_h = rect.height() / ROWS as f32;
        let radius = (col_w.min(row_h) * 0.5 * 0.62).max(0.5);
        for (index, &fraction) in fractions.iter().enumerate() {
            let height_dots = spark_height(fraction, ROWS);
            let cx = rect.left() + (index as f32 + 0.5) * col_w;
            for row in 0..ROWS {
                // Bottom-up: row 0 is the lowest dot.
                let cy = rect.bottom() - (row as f32 + 0.5) * row_h;
                let on = row < height_dots;
                painter.circle_filled(egui::pos2(cx, cy), radius, if on { lit } else { dim });
            }
        }
    });
}

/// A day's sparkline dot-height (`0..=rows`): `round(fraction*rows)`, but any nonzero spend lights
/// at least one dot (min-visibility, mirroring the TUI sparkline's `v>0 => >=1`).
fn spark_height(fraction: f64, rows: usize) -> usize {
    if !fraction.is_finite() || fraction <= 0.0 {
        return 0;
    }
    let raw = (fraction * rows as f64).round() as usize;
    raw.clamp(1, rows)
}

/// A single indented monospace text line.
fn text_line(ui: &mut egui::Ui, text: &str, ink: [u8; 4]) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(egui::RichText::new(text).monospace().color(color_of(ink)));
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::{DateTime, NaiveDate, Utc};
    use costroid_core::{ForecastDay, ProviderId};

    fn ts() -> DateTime<Utc> {
        match DateTime::from_timestamp(1_900_000_000, 0) {
            Some(dt) => dt,
            None => panic!("valid ts"),
        }
    }

    fn day(d: u32) -> ForecastDay {
        ForecastDay {
            date: match NaiveDate::from_ymd_opt(2026, 6, d) {
                Some(date) => date,
                None => panic!("valid date"),
            },
            spent_usd: Default::default(),
        }
    }

    fn base(spend: SpendForecast, no_api: bool, etas: Vec<QuotaEta>) -> ForecastView {
        ForecastView {
            generated_at: ts(),
            no_api_usage: no_api,
            spend,
            daily_actuals: vec![day(1), day(2), day(3)],
            quota_etas: etas,
        }
    }

    #[test]
    fn spark_height_min_visibility_and_clamp() {
        assert_eq!(spark_height(0.0, 4), 0);
        assert_eq!(spark_height(0.01, 4), 1, "any nonzero lights >= 1 dot");
        assert_eq!(spark_height(1.0, 4), 4);
        assert_eq!(spark_height(f64::NAN, 4), 0);
    }

    #[test]
    fn quota_line_degrades_honestly() {
        let projectable = QuotaEta {
            tool: ProviderId::ClaudeCode,
            kind: LimitKind::Weekly,
            outcome: QuotaEtaOutcome::Unavailable {
                reason: QuotaEtaUnavailable::ReadingNotProjectable,
            },
        };
        let line = quota_line(&projectable);
        assert!(line.contains("ETA unavailable"), "line: {line}");
        assert!(line.contains("no fresh verified usage reading"));
    }

    #[test]
    fn header_money_is_none_when_no_api_usage() {
        let view = base(
            SpendForecast::InsufficientData {
                spend_to_date_usd: Default::default(),
                days_elapsed: 0,
                days_in_month: 30,
                min_days: 3,
            },
            true,
            Vec::new(),
        );
        assert!(header_money(&view).is_none());
    }

    #[test]
    fn headless_draw_covers_projected_insufficient_and_empty() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let projected = base(
            SpendForecast::Projected {
                projected_month_usd: Default::default(),
                spend_to_date_usd: Default::default(),
                days_elapsed: 9,
                days_in_month: 30,
            },
            false,
            vec![QuotaEta {
                tool: ProviderId::ClaudeCode,
                kind: LimitKind::Weekly,
                outcome: QuotaEtaOutcome::Unavailable {
                    reason: QuotaEtaUnavailable::WindowJustStarted,
                },
            }],
        );
        let insufficient = base(
            SpendForecast::InsufficientData {
                spend_to_date_usd: Default::default(),
                days_elapsed: 1,
                days_in_month: 30,
                min_days: 3,
            },
            false,
            Vec::new(),
        );
        let mut empty = insufficient.clone();
        empty.no_api_usage = true;
        empty.daily_actuals = Vec::new();
        for v in [projected, insufficient, empty] {
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                draw(ui, &v);
            });
        }
    }
}
