//! The eframe app shell: the toggle window, the worker-thread refresh wiring, and the
//! tray glue. T18 draws a MINIMAL placeholder (the `C⠉` mark + wordmark + freshness +
//! a refresh button) that proves the refresh loop works; the Overview meters (T19) and
//! the live panels (T20) fill the window next.

use std::time::Instant;

use crate::format;
use crate::glyph;
use crate::overview::{self, OverviewModel};
use crate::refresh::{due_for_refresh, Phase, RefreshState, RefreshWorker, REFRESH_INTERVAL};
use crate::severity::{most_constrained_available, Constraint};
use crate::tray::{self, TrayAction, TrayController};

/// Launch the taskbar. Blocks on the eframe event loop until the user quits.
pub fn run() -> anyhow::Result<()> {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title("Costroid")
            .with_inner_size([360.0, 250.0])
            .with_min_inner_size([300.0, 190.0]),
        // Remember window size/position across sessions (eframe persistence).
        persist_window: true,
        ..Default::default()
    };
    eframe::run_native(
        "costroid-bar",
        options,
        Box::new(|cc| Ok(Box::new(BarApp::new(cc)) as Box<dyn eframe::App>)),
    )
    .map_err(|err| anyhow::anyhow!("failed to start the taskbar: {err}"))
}

/// The brand's neutral colors as egui values. Shared with the Overview (`meter.rs` /
/// `overview.rs`) so the whole window renders in one palette.
pub(crate) fn color_of(rgba: [u8; 4]) -> egui::Color32 {
    egui::Color32::from_rgba_unmultiplied(rgba[0], rgba[1], rgba[2], rgba[3])
}

const CARBON: [u8; 4] = [0x0b, 0x0c, 0x0e, 0xff];
const SLATE: [u8; 4] = [0x16, 0x18, 0x1c, 0xff];
/// Primary ink (brand "Bone").
pub(crate) const BONE: [u8; 4] = [0xe9, 0xe7, 0xdf, 0xff];
/// Muted/secondary text (brand "Ash").
pub(crate) const ASH: [u8; 4] = [0x88, 0x87, 0x80, 0xff];
/// The "live"/active accent (brand "Signal" lime) — used sparingly (STEP6-TASKBAR-DESIGN
/// §0/§6: only the active/selected/"live" highlight). The active-tab/selected-row uses
/// land in T20; the Overview uses it only as the live header's thin accent rule.
pub(crate) const SIGNAL: [u8; 4] = [0xc8, 0xff, 0x3d, 0xff];

fn carbon_visuals() -> egui::Visuals {
    let mut visuals = egui::Visuals::dark();
    visuals.panel_fill = color_of(CARBON);
    visuals.window_fill = color_of(SLATE);
    visuals.override_text_color = Some(color_of(BONE));
    visuals
}

/// The plain data the shell renders — decoupled from the app/tray/threads so the draw
/// path is unit-testable with a headless egui pass.
struct ShellView {
    /// The most-constrained severity step (`0..=8`) the mark fills to.
    step: u8,
    /// The wordmark sub-line: freshness, "refreshing…", or "refresh failed — …".
    status: String,
    /// The Overview body (spend header + quota meters), or `None` before the first
    /// snapshot has loaded (the status line carries the loading state).
    overview: Option<OverviewModel>,
}

struct BarApp {
    worker: RefreshWorker,
    state: RefreshState,
    tray: TrayController,
    actions: std::sync::mpsc::Receiver<TrayAction>,
    visible: bool,
    quitting: bool,
}

impl BarApp {
    fn new(cc: &eframe::CreationContext<'_>) -> Self {
        let ctx = cc.egui_ctx.clone();
        crate::fonts::install(&ctx);
        ctx.set_visuals(carbon_visuals());

        let worker = RefreshWorker::spawn(ctx.clone());
        let mut state = RefreshState::new();
        // Kick the first collect immediately so the glance is fresh on open.
        worker.request();
        state.mark_requested();

        // Start the tray at idle; the first refresh updates it.
        let idle = glyph::render_tray(0);
        let tray = tray::spawn(&idle, &format::tooltip(None));
        let actions = tray::spawn_event_bridge(ctx);

        Self {
            worker,
            state,
            tray,
            actions,
            visible: true,
            quitting: false,
        }
    }

    /// The most-constrained window of the latest snapshot, if any.
    fn constraint(&self) -> Option<Constraint> {
        self.state
            .loaded()
            .and_then(|loaded| most_constrained_available(&loaded.summary))
    }

    fn shell_view(&self) -> ShellView {
        let constraint = self.constraint();
        ShellView {
            step: constraint.as_ref().map_or(0, Constraint::step),
            status: self.status_line(),
            overview: self
                .state
                .loaded()
                .map(|loaded| OverviewModel::from_summary(&loaded.summary)),
        }
    }

    fn status_line(&self) -> String {
        let generated = self
            .state
            .loaded()
            .map(|loaded| loaded.snapshot.generated_at.format("%H:%M").to_string());
        status_text(
            self.state.error(),
            self.state.has_data(),
            self.state.phase(),
            generated,
        )
    }

    /// Push the current glance (icon + tooltip) to the tray. Called once per refresh, so
    /// re-rasterizing the small 64×64 glyph each time is negligible.
    fn sync_tray(&self) {
        let constraint = self.constraint();
        let step = constraint.as_ref().map_or(0, Constraint::step);
        self.tray.update(
            &glyph::render_tray(step),
            &format::tooltip(constraint.as_ref()),
        );
    }

    fn request_refresh(&mut self) {
        self.worker.request();
        self.state.mark_requested();
    }

    fn set_visible(&mut self, ctx: &egui::Context, visible: bool) {
        self.visible = visible;
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(visible));
        if visible {
            ctx.send_viewport_cmd(egui::ViewportCommand::Focus);
            // Refresh on show so the glance is fresh.
            self.request_refresh();
        }
    }

    fn handle_action(&mut self, action: TrayAction, ctx: &egui::Context) {
        match action {
            TrayAction::Toggle => self.set_visible(ctx, !self.visible),
            TrayAction::Show => self.set_visible(ctx, true),
            TrayAction::Refresh => self.request_refresh(),
            TrayAction::Quit => self.quit(ctx),
        }
    }

    fn quit(&mut self, ctx: &egui::Context) {
        self.quitting = true;
        self.tray.shutdown();
        ctx.send_viewport_cmd(egui::ViewportCommand::Close);
    }
}

impl eframe::App for BarApp {
    // Non-drawing state work. eframe calls `logic` before each `ui` AND while the window
    // is hidden whenever a repaint is requested — so the auto-timer and tray actions keep
    // running off-screen (no painting happens here).
    fn logic(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        // 1. Apply any fresh worker outcomes, then resync the tray glance.
        let mut got_outcome = false;
        while let Some(outcome) = self.worker.poll() {
            self.state.apply(outcome, Instant::now());
            got_outcome = true;
        }
        if got_outcome {
            self.sync_tray();
        }

        // 2. Apply any tray actions.
        while let Ok(action) = self.actions.try_recv() {
            self.handle_action(action, ctx);
        }

        // 3. Closing the window hides it to the tray (when a tray exists); a real Quit
        //    comes from the tray menu. With no tray, allow the close so the app can exit.
        if ctx.input(|i| i.viewport().close_requested()) && self.tray.is_active() && !self.quitting
        {
            ctx.send_viewport_cmd(egui::ViewportCommand::CancelClose);
            self.set_visible(ctx, false);
        }

        // 4. The ~30 s auto-refresh timer (worker owns no clock; the UI decides cadence).
        if due_for_refresh(
            self.state.phase(),
            self.state.since_last_completed(),
            REFRESH_INTERVAL,
        ) {
            self.request_refresh();
        }

        // 5. Heartbeat so the auto-timer still fires while the window is hidden/idle.
        ctx.request_repaint_after(REFRESH_INTERVAL);
    }

    // Drawing. The given `ui` is already inside eframe's central panel (no margin/bg).
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let view = self.shell_view();
        if draw_shell(ui, &view) {
            self.request_refresh();
        }
    }
}

/// The wordmark sub-line text — pure, so it is unit-testable.
fn status_text(
    error: Option<&str>,
    has_data: bool,
    phase: Phase,
    generated_hhmm: Option<String>,
) -> String {
    if let Some(reason) = error {
        return format!("refresh failed — {reason}");
    }
    match (has_data, generated_hhmm) {
        (true, Some(stamp)) => format!("updated {stamp} · estimates"),
        (true, None) => "updated · estimates".to_owned(),
        (false, _) if phase == Phase::InFlight => "refreshing…".to_owned(),
        (false, _) => "starting…".to_owned(),
    }
}

/// Draw the window shell: the `C⠉` mark + wordmark + freshness status (the chrome T18 set
/// up), then the Overview body (spend header + quota meters), then the manual refresh.
/// Returns whether the refresh button was clicked. Pure of app/tray/thread state, so a
/// headless egui pass can exercise it.
fn draw_shell(ui: &mut egui::Ui, view: &ShellView) -> bool {
    ui.add_space(8.0);
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        draw_mark(ui, view.step);
        ui.add_space(10.0);
        ui.vertical(|ui| {
            ui.label(egui::RichText::new("costroid").strong().size(20.0));
            ui.label(egui::RichText::new(&view.status).color(color_of(ASH)));
        });
    });
    ui.add_space(10.0);
    ui.separator();
    ui.add_space(6.0);
    match &view.overview {
        Some(overview) => overview::draw(ui, overview),
        // Before the first snapshot lands the status line already says "starting…" /
        // "refreshing…"; show a quiet body note so the window is never blank.
        None => {
            ui.horizontal(|ui| {
                ui.add_space(8.0);
                ui.label(egui::RichText::new("loading local data…").color(color_of(ASH)));
            });
        }
    }
    ui.add_space(10.0);
    let mut clicked = false;
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        if ui.button("Refresh").clicked() {
            clicked = true;
        }
    });
    clicked
}

/// Paint the `C⠉` mark — the letter `C` plus the 3×3 dot grid filled to `step`, using the
/// same geometry as the tray bitmap (`glyph.rs`).
fn draw_mark(ui: &mut egui::Ui, step: u8) {
    let side = 44.0;
    let (rect, _response) = ui.allocate_exact_size(egui::Vec2::splat(side), egui::Sense::hover());
    let painter = ui.painter_at(rect);

    // The `C` (left third), drawn as text in the bundled mono font.
    painter.text(
        rect.left_center() + egui::vec2(rect.width() * 0.16, 0.0),
        egui::Align2::CENTER_CENTER,
        "C",
        egui::FontId::monospace(rect.height() * 0.72),
        color_of(glyph::MARK_INK),
    );

    // The dot grid (right portion), same fill order + colors as the rasterized icon.
    let filled = glyph::dots_filled(step);
    let mut lit = [false; 9];
    for &idx in glyph::FILL_ORDER.iter().take(filled) {
        lit[idx] = true;
    }
    let fill = color_of(glyph::step_fill_color(step));
    let empty = color_of(glyph::EMPTY_DOT);
    let radius = glyph::DOT_RADIUS * rect.width().min(rect.height());
    for (i, (u, v)) in glyph::dot_centers().iter().enumerate() {
        let center = rect.min + egui::vec2(u * rect.width(), v * rect.height());
        painter.circle_filled(center, radius, if lit[i] { fill } else { empty });
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn color_of_opaque_matches_rgb() {
        assert_eq!(color_of(BONE), egui::Color32::from_rgb(0xe9, 0xe7, 0xdf));
        assert_eq!(color_of(CARBON), egui::Color32::from_rgb(0x0b, 0x0c, 0x0e));
    }

    #[test]
    fn status_text_prioritizes_error() {
        let s = status_text(
            Some("could not read local data: boom"),
            true,
            Phase::Idle,
            Some("12:00".to_owned()),
        );
        assert_eq!(s, "refresh failed — could not read local data: boom");
    }

    #[test]
    fn status_text_shows_freshness_when_loaded() {
        let s = status_text(None, true, Phase::Idle, Some("09:41".to_owned()));
        assert_eq!(s, "updated 09:41 · estimates");
    }

    #[test]
    fn status_text_first_load_states() {
        assert_eq!(
            status_text(None, false, Phase::InFlight, None),
            "refreshing…"
        );
        assert_eq!(status_text(None, false, Phase::Idle, None), "starting…");
    }

    #[test]
    fn draw_shell_headless_tick_does_not_panic() {
        // A headless egui pass exercises the paint path with no window/GPU — both before a
        // snapshot loads (no overview) and with one present.
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        for overview in [None, Some(sample_overview())] {
            for step in [0u8, 4, 8] {
                let view = ShellView {
                    step,
                    status: "updated 12:00 · estimates".to_owned(),
                    overview: overview.clone(),
                };
                let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                    let _ = draw_shell(ui, &view);
                });
            }
        }
    }

    /// A small Overview model (one Available meter) for the headless shell test.
    fn sample_overview() -> OverviewModel {
        use chrono::DateTime;
        use costroid_core::{
            GroupBy, LimitAvailability, LimitKind, LimitMeasure, LimitSummary, NowSummary,
            PeriodRange, ProviderId,
        };
        let at = match DateTime::from_timestamp(1_900_000_000, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp"),
        };
        let summary = NowSummary {
            generated_at: at,
            cost_period: PeriodRange { start: at, end: at },
            group_by: GroupBy::Model,
            limits: vec![LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: None,
                kind: LimitKind::FiveHour,
                label: None,
                captured_at: at,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.5),
                    resets_at: at,
                    reset_in_seconds: 3600,
                },
            }],
            current_costs: Vec::new(),
            providers: Vec::new(),
        };
        OverviewModel::from_summary(&summary)
    }
}
