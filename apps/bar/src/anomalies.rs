//! The Anomalies panel — `anomalies_view`, rendered in the brand's painted dot language.
//!
//! Mirrors the CLI's `render_anomalies_*` SEMANTICS: proactive, non-alarmist callouts vs the user's
//! OWN recent history — a daily spend spike (API-lane $) and a model-mix shift (all-lane token
//! share), each `~`-hedged + `(estimated)`-tagged with the compared `N-day` window named — plus the
//! honest transient "no usage" / thin-history "N of M days" / clean states and the deferred
//! quota-burn footnote (local data keeps no multi-day quota history, never faked). An ADVISORY
//! panel: no amber/red (that is reserved for the near-limit/over-budget state); each callout carries
//! a painted dot marker (never color, never a glyph the bundled font may lack). The Decimal money /
//! share / multiple formatting all routes through core, so the bar names no money type.

use costroid_core::{
    anomaly_multiple_phrase, decimal_share_percent, format_money_usd, AnomaliesView, Anomaly,
    AnomalySignal,
};

use crate::app::{color_of, ASH, BONE, DATA_CYAN};

const ANOMALIES_NO_USAGE: &str = "no usage recorded yet — callouts need a few days of history";

/// Draw the Anomalies panel. Pure of app/thread state — a headless egui pass exercises it. The
/// persistent header status carries the "estimates" caveat, so the panel drops the long scope +
/// estimate + quota-deferred footnotes the CLI keeps (lean taskbar); each callout stays `~`-hedged.
pub fn draw(ui: &mut egui::Ui, view: &AnomaliesView) {
    draw_header(ui, view);
    draw_body(ui, view);
}

fn draw_header(ui: &mut egui::Ui, view: &AnomaliesView) {
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        ui.label(
            egui::RichText::new("anomalies")
                .monospace()
                .color(color_of(ASH)),
        );
        let label = header_label(view);
        if !label.is_empty() {
            ui.add_space(8.0);
            ui.label(
                egui::RichText::new(label)
                    .monospace()
                    .strong()
                    .color(color_of(BONE)),
            );
        }
    });
}

/// The right-hand header label: the count of callouts when there are any, else empty (no fabricated
/// figure above an empty/insufficient state). Mirrors the CLI's `anomalies_header_label`.
fn header_label(view: &AnomaliesView) -> String {
    if !view.enough_history || view.anomalies.is_empty() {
        String::new()
    } else {
        format!("{} flagged", view.anomalies.len())
    }
}

/// The body: the transient no-usage state, the thin-history state, the clean state, or one marked
/// callout per anomaly. Mirrors the CLI's `push_anomalies_body` (no_usage FIRST).
fn draw_body(ui: &mut egui::Ui, view: &AnomaliesView) {
    if view.no_usage {
        text_line(ui, ANOMALIES_NO_USAGE, ASH);
        return;
    }
    if !view.enough_history {
        text_line(
            ui,
            &format!(
                "not enough history yet - {} of {} days (estimated)",
                view.history_days, view.min_history_days
            ),
            ASH,
        );
        return;
    }
    if view.anomalies.is_empty() {
        text_line(
            ui,
            &format!(
                "no anomalies - usage in line with your {}-day norm (estimated)",
                view.history_days
            ),
            ASH,
        );
        return;
    }
    for anomaly in &view.anomalies {
        draw_callout(ui, &anomaly_callout(anomaly));
    }
}

/// One anomaly's proactive, hedged callout — always `~`-hedged + `(estimated)`-tagged with the
/// compared `N-day` window named. The "~N.Nx your norm" multiple is shown only when it reads
/// honestly (via core's `anomaly_multiple_phrase`); otherwise the descriptive "well above" /
/// "up from" / "down from" phrasing keeps the line from contradicting its own displayed baseline.
/// Mirrors the CLI's `anomaly_line` (minus the marker glyph — the bar paints its own dot marker).
fn anomaly_callout(anomaly: &Anomaly) -> String {
    match &anomaly.signal {
        AnomalySignal::SpendSpike { date } => {
            let median_display = format_money_usd(&anomaly.baseline_median, true);
            let baseline_displays_zero = median_display == "~$0.00";
            let comparison =
                match anomaly_multiple_phrase(anomaly.magnitude.as_ref(), baseline_displays_zero) {
                    Some(multiple) => format!(
                        "~{multiple}x your {median_display} {}-day median",
                        anomaly.baseline_days
                    ),
                    None => format!(
                        "well above your {median_display} {}-day median",
                        anomaly.baseline_days
                    ),
                };
            format!(
                "spend spike: {} on {}, {comparison} (estimated)",
                format_money_usd(&anomaly.value, true),
                date.format("%b %d"),
            )
        }
        AnomalySignal::ModelMixShift { model } => {
            let today_share = decimal_share_percent(&anomaly.value);
            let median_share = decimal_share_percent(&anomaly.baseline_median);
            // `>` on `Decimal` is the std `PartialOrd` operator — no money type is named.
            let comparison = if anomaly.value > anomaly.baseline_median {
                let baseline_displays_zero = median_share == "0%";
                match anomaly_multiple_phrase(anomaly.magnitude.as_ref(), baseline_displays_zero) {
                    Some(multiple) => format!(
                        "~{multiple}x your {median_share} {}-day median",
                        anomaly.baseline_days
                    ),
                    None => format!(
                        "up from your {median_share} {}-day median",
                        anomaly.baseline_days
                    ),
                }
            } else {
                format!(
                    "down from your {median_share} {}-day median",
                    anomaly.baseline_days
                )
            };
            format!("model mix shift: {model} at {today_share} of tokens, {comparison} (estimated)")
        }
    }
}

/// Draw one callout: a small painted data-cyan dot marker + the hedged sentence (the proactive
/// insight voice). The marker is PAINTED (not a `◆` glyph the bundled JetBrains Mono may lack), and
/// cyan reads as "data/analytical insight" — NOT amber/red (anomalies are advisory, never an alarm).
fn draw_callout(ui: &mut egui::Ui, text: &str) {
    ui.add_space(2.0);
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        let (rect, _response) =
            ui.allocate_exact_size(egui::Vec2::splat(8.0), egui::Sense::hover());
        ui.painter_at(rect)
            .circle_filled(rect.center(), 2.4, color_of(DATA_CYAN));
        ui.add_space(4.0);
        ui.label(egui::RichText::new(text).monospace().color(color_of(BONE)));
    });
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

    fn ts() -> DateTime<Utc> {
        match DateTime::from_timestamp(1_900_000_000, 0) {
            Some(dt) => dt,
            None => panic!("valid ts"),
        }
    }

    fn spike() -> Anomaly {
        Anomaly {
            signal: AnomalySignal::SpendSpike {
                date: match NaiveDate::from_ymd_opt(2026, 6, 18) {
                    Some(date) => date,
                    None => panic!("valid date"),
                },
            },
            // Zero `Decimal` fixtures (the bar names no money type); value formatting is core-tested.
            value: Default::default(),
            baseline_median: Default::default(),
            deviation: Default::default(),
            magnitude: None,
            baseline_days: 7,
        }
    }

    fn mix() -> Anomaly {
        Anomaly {
            signal: AnomalySignal::ModelMixShift {
                model: "claude-opus-4-8".to_string(),
            },
            value: Default::default(),
            baseline_median: Default::default(),
            deviation: Default::default(),
            magnitude: None,
            baseline_days: 14,
        }
    }

    fn view(
        history: usize,
        min: usize,
        enough: bool,
        no_usage: bool,
        anomalies: Vec<Anomaly>,
    ) -> AnomaliesView {
        AnomaliesView {
            generated_at: ts(),
            history_days: history,
            min_history_days: min,
            baseline_days: 14,
            enough_history: enough,
            no_usage,
            anomalies,
        }
    }

    #[test]
    fn header_label_is_empty_unless_there_are_callouts() {
        assert_eq!(header_label(&view(0, 7, false, true, Vec::new())), "");
        assert_eq!(header_label(&view(10, 7, true, false, Vec::new())), "");
        assert_eq!(
            header_label(&view(10, 7, true, false, vec![spike()])),
            "1 flagged"
        );
    }

    #[test]
    fn spike_callout_structure_routes_through_core() {
        // Zero fixtures: structure + the honest "well above" fallback over a zero baseline.
        let line = anomaly_callout(&spike());
        assert!(
            line.starts_with("spend spike: ~$0.00 on Jun 18,"),
            "line: {line}"
        );
        assert!(line.contains("well above your ~$0.00 7-day median"));
        assert!(line.ends_with("(estimated)"));
    }

    #[test]
    fn mix_callout_names_the_model_and_share() {
        let line = anomaly_callout(&mix());
        assert!(
            line.contains("model mix shift: claude-opus-4-8 at 0% of tokens"),
            "line: {line}"
        );
        assert!(line.contains("14-day median"));
        assert!(line.ends_with("(estimated)"));
    }

    #[test]
    fn headless_draw_covers_every_state() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let states = [
            view(0, 7, false, true, Vec::new()),            // no usage
            view(3, 7, false, false, Vec::new()),           // thin history
            view(10, 7, true, false, Vec::new()),           // clean
            view(10, 7, true, false, vec![spike(), mix()]), // callouts
        ];
        for v in states {
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                draw(ui, &v);
            });
        }
    }
}
