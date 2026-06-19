//! The cockpit tab strip: `[Overview] [Budget] [Forecast] [Anomalies] [Providers]`.
//!
//! The strip sits over the persistent spend+meters+banner header and switches the lower
//! window region between the five live views (STEP6-TASKBAR-DESIGN §4). The active tab is the
//! brand's **Signal-lime** accent — the one sparing decorative use of lime (§0/§6: lime marks
//! the active/selected/"live" element). Lime is **decorative, never the sole signal**: the
//! active tab is also `strong`-weighted, so the selection survives grayscale. Keyboard nav
//! (digit 1–5 / arrows / Tab) mirrors the TUI; it is driven by `app.rs`, this module owns the
//! `Tab` model + the strip paint.

use crate::app::{color_of, ASH, CARBON, SIGNAL};

/// One of the five live cockpit views (STEP6-TASKBAR-DESIGN §1, scope "Glance + live
/// cockpit"). `Trends`/`Models`/`History`/`Frontier` are the deferred post-0.6.0 fast-follow.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Tab {
    Overview,
    Budget,
    Forecast,
    Anomalies,
    Providers,
}

impl Tab {
    /// Every tab, left-to-right strip order (also the digit-key order `1..=5`).
    pub const ALL: [Tab; 5] = [
        Tab::Overview,
        Tab::Budget,
        Tab::Forecast,
        Tab::Anomalies,
        Tab::Providers,
    ];

    /// The strip label.
    pub fn label(self) -> &'static str {
        match self {
            Tab::Overview => "Overview",
            Tab::Budget => "Budget",
            Tab::Forecast => "Forecast",
            Tab::Anomalies => "Anomalies",
            Tab::Providers => "Providers",
        }
    }

    /// The tab selected by a digit key (`1`..=`5`), or `None` for any other digit.
    pub fn from_digit(digit: usize) -> Option<Tab> {
        Tab::ALL.get(digit.checked_sub(1)?).copied()
    }

    fn position(self) -> usize {
        Tab::ALL.iter().position(|&t| t == self).unwrap_or(0)
    }

    /// The next tab (wraps Providers → Overview) — Right-arrow / Tab.
    pub fn next(self) -> Tab {
        Tab::ALL[(self.position() + 1) % Tab::ALL.len()]
    }

    /// The previous tab (wraps Overview → Providers) — Left-arrow / Shift-Tab.
    pub fn prev(self) -> Tab {
        Tab::ALL[(self.position() + Tab::ALL.len() - 1) % Tab::ALL.len()]
    }
}

/// Paint the tab strip, the active tab a filled Signal-lime chip. Returns a tab if one was clicked.
/// Pure of app/thread state, so a headless egui pass can exercise it.
pub fn draw_strip(ui: &mut egui::Ui, selected: Tab) -> Option<Tab> {
    let mut clicked = None;
    ui.horizontal_wrapped(|ui| {
        ui.spacing_mut().item_spacing.x = 4.0;
        ui.add_space(8.0);
        for tab in Tab::ALL {
            if draw_tab(ui, tab, tab == selected) {
                clicked = Some(tab);
            }
        }
    });
    clicked
}

/// One tab: the active one is a filled Signal-lime chip with dark (Carbon) ink — a reverse-video
/// selection whose FILL (a shape change, not the lime alone) is the never-color-alone cue; inactive
/// tabs are muted Ash with a quiet lime hover tint. Painted (not a `selectable_label`) so the active
/// chip can be the brand lime; named + selected-flagged for AccessKit (T21). Returns whether clicked.
fn draw_tab(ui: &mut egui::Ui, tab: Tab, active: bool) -> bool {
    let label = tab.label();
    let fg = if active { CARBON } else { ASH };
    let galley = ui.painter().layout_no_wrap(
        label.to_owned(),
        egui::FontId::monospace(12.0),
        color_of(fg),
    );
    let pad = egui::vec2(7.0, 3.0);
    let (rect, response) = ui.allocate_exact_size(galley.size() + pad * 2.0, egui::Sense::click());
    response.widget_info(|| {
        egui::WidgetInfo::selected(egui::WidgetType::SelectableLabel, true, active, label)
    });
    let painter = ui.painter_at(rect);
    if active {
        painter.rect_filled(rect, 4.0, color_of(SIGNAL));
    } else if response.hovered() {
        painter.rect_filled(rect, 4.0, color_of([SIGNAL[0], SIGNAL[1], SIGNAL[2], 0x22]));
    }
    painter.galley(rect.min + pad, galley, color_of(fg));
    response.clicked()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn from_digit_maps_one_through_five() {
        assert_eq!(Tab::from_digit(1), Some(Tab::Overview));
        assert_eq!(Tab::from_digit(5), Some(Tab::Providers));
        assert_eq!(Tab::from_digit(0), None);
        assert_eq!(Tab::from_digit(6), None);
    }

    #[test]
    fn next_and_prev_wrap() {
        assert_eq!(Tab::Overview.prev(), Tab::Providers);
        assert_eq!(Tab::Providers.next(), Tab::Overview);
        assert_eq!(Tab::Overview.next(), Tab::Budget);
        assert_eq!(Tab::Budget.prev(), Tab::Overview);
        // A full cycle returns to the start.
        let mut tab = Tab::Overview;
        for _ in 0..Tab::ALL.len() {
            tab = tab.next();
        }
        assert_eq!(tab, Tab::Overview);
    }

    #[test]
    fn labels_are_ascii() {
        for tab in Tab::ALL {
            assert!(tab.label().is_ascii());
        }
    }

    #[test]
    fn draw_strip_headless_tick_does_not_panic() {
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        for selected in Tab::ALL {
            let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                let _ = draw_strip(ui, selected);
            });
        }
    }
}
