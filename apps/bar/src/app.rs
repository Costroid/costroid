//! The eframe app shell: the persistent spend+meters+banner header, the tab strip over the four
//! live panels, the worker-thread refresh wiring, the config state, and the tray glue.
//!
//! The window is the live cockpit (DESIGN-SYSTEM): a persistent header (period spend +
//! the painted quota meters + the opt-in alert banner) above a `[Overview] [Budget] [Forecast]
//! [Anomalies] [Providers]` tab strip that switches the lower region. Every figure the core already
//! computes — the bar is a pure consumer, no new network, no telemetry. One `Cockpit` model is
//! rebuilt per refresh (from the snapshot + the read-only `[budget]`/`[alerts]` config) and the draw
//! is a pure function of it, so the whole window is headless-testable.

use std::time::Instant;

use costroid_config::Config;
use costroid_core::{
    active_alerts, anomalies_view, budget_view, forecast_view, AdvisoryAlerts, Alert,
    AnomaliesView, BudgetView, ForecastView, ProviderCapabilityView, ProviderStatus,
};

use crate::anomalies;
use crate::banner;
use crate::budget;
use crate::forecast;
use crate::format;
use crate::glyph;
use crate::overview::{self, NowBreakdown, OverviewModel};
use crate::providers;
use crate::refresh::{
    due_for_refresh, Loaded, Phase, RefreshState, RefreshWorker, REFRESH_INTERVAL,
};
use crate::severity::{most_constrained_available, Constraint};
use crate::tabs::{self, Tab};
use crate::tray::{self, TrayAction, TrayController};

/// Launch the taskbar. Blocks on the eframe event loop until the user quits.
pub fn run() -> anyhow::Result<()> {
    let options = eframe::NativeOptions {
        viewport: egui::ViewportBuilder::default()
            .with_title("Costroid")
            .with_inner_size([380.0, 420.0])
            .with_min_inner_size([320.0, 240.0]),
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

/// A one-shot, no-GUI self-check (`costroid-bar --self-check`). It exercises the bar's full
/// data path — `collect_local_snapshot` + `now_summary` + every panel's `*_view` compute (via
/// `Cockpit::build`) + the read-only config and (under `--features connect`) the keychain
/// connection lane — then prints a summary and exits, WITHOUT opening a window. This is what the
/// runtime no-network proof drives (`scripts/offline_acceptance.sh`): the data path is where any
/// accidental egress would happen, and running it under strace/netns proves no `AF_INET` socket.
/// It needs no display, so it runs headless in CI; the full window/AccessKit path's no-network
/// property rests on the per-binary static allowlist (`apps/cli/tests/offline.rs`) — the
/// AccessKit subtree links no network/TLS/telemetry crate — plus an optional xvfb GUI run.
pub fn self_check() -> anyhow::Result<()> {
    let env = costroid_core::HostEnv::detect();
    let snapshot = costroid_core::collect_local_snapshot(&env)
        .map_err(|err| anyhow::anyhow!("could not read local data: {err}"))?;
    let summary = costroid_core::now_summary(&snapshot, costroid_core::NowOptions::default());
    let loaded = Loaded { snapshot, summary };

    let (config, config_status) = load_config();
    let cockpit = Cockpit::build(&loaded, &config);
    // The display-only connection lane is a read-only keychain/registry join — NO network. Touch
    // it so the self-check covers that path too (it is a no-op in the default build).
    #[cfg(feature = "connect")]
    let connections = providers::gather_connections().len();
    #[cfg(not(feature = "connect"))]
    let connections = 0usize;

    println!(
        "costroid-bar self-check ok — {} limit window(s), {} active alert(s), {} connection \
         entr(ies), connect={}{}",
        cockpit.overview.meters.len(),
        cockpit.alerts.len(),
        connections,
        cfg!(feature = "connect"),
        match config_status {
            Some(status) => format!(" ({status})"),
            None => String::new(),
        },
    );
    Ok(())
}

/// The brand's neutral colors as egui values. Shared with the Overview/panels so the whole window
/// renders in one palette.
pub(crate) fn color_of(rgba: [u8; 4]) -> egui::Color32 {
    egui::Color32::from_rgba_unmultiplied(rgba[0], rgba[1], rgba[2], rgba[3])
}

/// The darkest brand ground ("Carbon") — also the high-contrast ink on a Signal-lime chip.
pub(crate) const CARBON: [u8; 4] = [0x0b, 0x0c, 0x0e, 0xff];
const SLATE: [u8; 4] = [0x16, 0x18, 0x1c, 0xff];
/// Primary ink (brand "Bone").
pub(crate) const BONE: [u8; 4] = [0xe9, 0xe7, 0xdf, 0xff];
/// Muted/secondary text (brand "Ash").
pub(crate) const ASH: [u8; 4] = [0x88, 0x87, 0x80, 0xff];
/// The "live"/active accent (brand "Signal" lime) — used sparingly (DESIGN-SYSTEM:
/// only the active/selected/"live" highlight — the active tab + the Overview's header rule).
pub(crate) const SIGNAL: [u8; 4] = [0xc8, 0xff, 0x3d, 0xff];
/// The cold cyan-blue data ramp's mid tone (brand "COSTROID·CLI" `#378ADD` — "logs, data, raw
/// compute"). Used for cost/spend data viz (the Forecast sparkline + per-model share bars); never
/// amber (amber is limits).
pub(crate) const DATA_CYAN: [u8; 4] = [0x37, 0x8a, 0xdd, 0xff];

/// The brand's categorical per-model hue ramp — the true-color equivalents of the CLI's xterm-256
/// `SERIES_PALETTE` (38/79/75/141/215/210): azure, aquamarine, cornflower, medium-purple, sand-gold,
/// salmon. Six dark-ground-friendly hues cycled by a model's spend rank, so the taskbar's per-model
/// coloring matches the terminal edge-to-edge (DESIGN-SYSTEM "Terminal palette" / the §0 brand
/// system, evolved 2026-06-19). Signal-lime is intentionally ABSENT — it stays the reserved
/// active/"live" accent, never a data series. The leading dot + the model name carry the identity
/// even without color (never color alone).
pub(crate) const SERIES: [[u8; 4]; 6] = [
    [0x00, 0xaf, 0xd7, 0xff], // azure       (xterm 38)
    [0x5f, 0xd7, 0xaf, 0xff], // aquamarine  (79)
    [0x5f, 0xaf, 0xff, 0xff], // cornflower  (75)
    [0xaf, 0x87, 0xff, 0xff], // medium-purple (141)
    [0xff, 0xaf, 0x5f, 0xff], // sand-gold   (215)
    [0xff, 0x87, 0x87, 0xff], // salmon      (210)
];

/// The `SERIES` hue for the `n`-th model (cycled), as an egui color. Mirrors the CLI's
/// `series_color_index`, so the two surfaces share one per-model palette.
pub(crate) fn series_color(index: usize) -> egui::Color32 {
    color_of(SERIES[index % SERIES.len()])
}

/// A small filled "chip": a rounded-rect tint of `fg` with `text` painted in `fg` — the colored
/// state / pace tag (Providers health, Budget pace). The word itself is the non-color cue (never
/// color alone), and the chip is named for AccessKit (T21) since its text is painted, not a label.
/// Laid out inline (allocates its own exact size); returns after painting.
pub(crate) fn chip(ui: &mut egui::Ui, text: &str, fg: [u8; 4]) {
    let fg_color = color_of(fg);
    let galley =
        ui.painter()
            .layout_no_wrap(text.to_owned(), egui::FontId::monospace(11.0), fg_color);
    let pad = egui::vec2(6.0, 2.0);
    let (rect, response) = ui.allocate_exact_size(galley.size() + pad * 2.0, egui::Sense::hover());
    response.widget_info(|| egui::WidgetInfo::labeled(egui::WidgetType::Label, true, text));
    let painter = ui.painter_at(rect);
    // A low-alpha tint of the fg reads as a subtle badge over the Carbon ground.
    painter.rect_filled(rect, 3.0, color_of([fg[0], fg[1], fg[2], 0x2e]));
    painter.galley(rect.min + pad, galley, fg_color);
}

/// A "healthy"/positive state color (the warning ramp's green) — distinct from Signal-lime so it
/// never competes with the reserved active/"live" accent. Paired with a state/pace WORD by callers.
pub(crate) const HEALTHY: [u8; 4] = [0x5b, 0xd1, 0x7a, 0xff];
/// A "caution" state color (the warning ramp's amber/orange) — Budget ahead-of-pace, a partial
/// provider. Always paired with its word.
pub(crate) const CAUTION: [u8; 4] = [0xe8, 0x92, 0x3d, 0xff];
/// A "critical" state color (the warning ramp's red) — over budget, a provider error. Paired with
/// its word.
pub(crate) const CRITICAL: [u8; 4] = [0xe0, 0x53, 0x3d, 0xff];

fn carbon_visuals() -> egui::Visuals {
    let mut visuals = egui::Visuals::dark();
    visuals.panel_fill = color_of(CARBON);
    visuals.window_fill = color_of(SLATE);
    visuals.override_text_color = Some(color_of(BONE));
    visuals
}

/// The render-ready cockpit model — every panel's pure data, rebuilt once per refresh from one
/// snapshot + the read-only config. The draw is a pure function of this, so the window is
/// headless-testable. No `Decimal` is named here (money stays in core's view types / display
/// strings).
pub(crate) struct Cockpit {
    /// The persistent header: period spend + the painted quota meters.
    overview: OverviewModel,
    /// The Overview tab's lower region: the now per-model cost breakdown + provider notes.
    breakdown: NowBreakdown,
    /// The opt-in alert banner's active crossings (empty when alerts are off OR clear).
    alerts: Vec<Alert>,
    budget: BudgetView,
    forecast: ForecastView,
    anomalies: AnomaliesView,
    capabilities: Vec<ProviderCapabilityView>,
    statuses: Vec<ProviderStatus>,
    /// The display-only connection lane (read-only keychain/registry, no network), filled by the
    /// app after build under `--features connect`. Empty in the default build.
    #[cfg(feature = "connect")]
    connections: Vec<providers::ConnectionEntry>,
}

impl Cockpit {
    /// Build the cockpit from one snapshot + the user config. The advisory forecast/anomaly views
    /// are always computed (the panels show them regardless of the alert flags); they feed the
    /// banner ONLY when their opt-in sub-flag is on, exactly mirroring the CLI's `compute_alerts`.
    fn build(loaded: &Loaded, config: &Config) -> Cockpit {
        let summary = &loaded.summary;
        let snapshot = &loaded.snapshot;
        let budget = budget_view(snapshot, &config.budget_targets());
        let forecast = forecast_view(snapshot);
        let anomalies = anomalies_view(snapshot);
        let alerts = if config.alerts_enabled() {
            let advisory = AdvisoryAlerts {
                forecast: config.alerts_forecast_enabled().then_some(&forecast),
                anomalies: config.alerts_anomalies_enabled().then_some(&anomalies),
            };
            active_alerts(summary, &budget, &config.alert_thresholds(), advisory)
        } else {
            Vec::new()
        };
        Cockpit {
            overview: OverviewModel::from_summary(summary),
            breakdown: NowBreakdown::from_summary(summary),
            alerts,
            budget,
            forecast,
            anomalies,
            capabilities: snapshot.capabilities.clone(),
            statuses: snapshot.providers.clone(),
            #[cfg(feature = "connect")]
            connections: Vec::new(),
        }
    }
}

/// The plain data the shell renders — decoupled from the app/tray/threads so the draw is
/// unit-testable with a headless egui pass.
struct ShellView<'a> {
    /// The most-constrained severity step (`0..=8`) the mark fills to.
    step: u8,
    /// The wordmark sub-line: freshness, "refreshing…", or "refresh failed — …".
    status: String,
    /// A config-load error (a malformed `config.toml`), shown as an in-window status line — never
    /// a crash (DESIGN-SYSTEM).
    config_status: Option<String>,
    /// The selected tab.
    tab: Tab,
    /// The cockpit, or `None` before the first snapshot has loaded.
    cockpit: Option<&'a Cockpit>,
}

/// What the user did this frame in the window chrome (the refresh button, a tab click).
#[derive(Default)]
struct ShellAction {
    refresh_clicked: bool,
    tab_clicked: Option<Tab>,
}

struct BarApp {
    worker: RefreshWorker,
    state: RefreshState,
    tray: TrayController,
    actions: std::sync::mpsc::Receiver<TrayAction>,
    visible: bool,
    quitting: bool,
    /// The read-only `[budget]`/`[alerts]` config (zero-config default when absent), reloaded on a
    /// manual refresh; a malformed file degrades to the default + `config_status`.
    config: Config,
    config_status: Option<String>,
    tab: Tab,
    cockpit: Option<Cockpit>,
    /// The display-only connection lane, gathered read-only on startup + manual refresh (no
    /// network). Default build: an empty, unused field.
    #[cfg(feature = "connect")]
    connections: Vec<providers::ConnectionEntry>,
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

        let (config, config_status) = load_config();

        Self {
            worker,
            state,
            tray,
            actions,
            visible: true,
            quitting: false,
            config,
            config_status,
            tab: Tab::Overview,
            cockpit: None,
            #[cfg(feature = "connect")]
            connections: providers::gather_connections(),
        }
    }

    /// The most-constrained window of the latest snapshot, if any.
    fn constraint(&self) -> Option<Constraint> {
        self.state
            .loaded()
            .and_then(|loaded| most_constrained_available(&loaded.summary))
    }

    fn shell_view(&self) -> ShellView<'_> {
        ShellView {
            step: self.constraint().as_ref().map_or(0, Constraint::step),
            status: self.status_line(),
            config_status: self.config_status.clone(),
            tab: self.tab,
            cockpit: self.cockpit.as_ref(),
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

    /// Rebuild the cockpit from the latest snapshot + the current config + the gathered connections.
    fn rebuild_cockpit(&mut self) {
        if let Some(loaded) = self.state.loaded() {
            // `mut` is only used under `--features connect` (to fill the connection lane); the
            // default build never mutates it.
            #[cfg_attr(not(feature = "connect"), allow(unused_mut))]
            let mut cockpit = Cockpit::build(loaded, &self.config);
            #[cfg(feature = "connect")]
            {
                cockpit.connections = self.connections.clone();
            }
            self.cockpit = Some(cockpit);
        }
    }

    /// Push the current glance (icon + tooltip) to the tray. Called once per refresh.
    fn sync_tray(&self) {
        let constraint = self.constraint();
        let step = constraint.as_ref().map_or(0, Constraint::step);
        self.tray.update(
            &glyph::render_tray(step),
            &format::tooltip(constraint.as_ref()),
        );
    }

    /// An automatic refresh (the ~30 s timer): re-collect the snapshot only — config + connections
    /// are NOT re-read off the timer (battery-friendly; DESIGN-SYSTEM).
    fn request_auto_refresh(&mut self) {
        self.worker.request();
        self.state.mark_requested();
    }

    /// A user-initiated refresh (the button / `r` / tray "Refresh now" / window-show): re-read the
    /// config and re-gather the read-only connection lane, then re-collect the snapshot. A malformed
    /// config degrades to `config_status`, never a crash.
    fn request_manual_refresh(&mut self) {
        let (config, config_status) = load_config();
        self.config = config;
        self.config_status = config_status;
        #[cfg(feature = "connect")]
        {
            self.connections = providers::gather_connections();
        }
        self.request_auto_refresh();
    }

    fn set_visible(&mut self, ctx: &egui::Context, visible: bool) {
        self.visible = visible;
        ctx.send_viewport_cmd(egui::ViewportCommand::Visible(visible));
        if visible {
            ctx.send_viewport_cmd(egui::ViewportCommand::Focus);
            // Opening the window is user-initiated: refresh config + data so the glance is fresh.
            self.request_manual_refresh();
        }
    }

    fn handle_action(&mut self, action: TrayAction, ctx: &egui::Context) {
        match action {
            TrayAction::Toggle => self.set_visible(ctx, !self.visible),
            TrayAction::Show => self.set_visible(ctx, true),
            TrayAction::Refresh => self.request_manual_refresh(),
            TrayAction::Quit => self.quit(ctx),
        }
    }

    /// `q` while the window is shown: hide to the tray when one exists (quit-to-tray), else exit.
    fn quit_or_hide(&mut self, ctx: &egui::Context) {
        if self.tray.is_active() {
            self.set_visible(ctx, false);
        } else {
            self.quit(ctx);
        }
    }

    fn quit(&mut self, ctx: &egui::Context) {
        self.quitting = true;
        self.tray.shutdown();
        ctx.send_viewport_cmd(egui::ViewportCommand::Close);
    }

    /// Read tab-navigation / refresh / quit keys (digit 1–5, arrows, Tab, `r`, `q`), consuming each
    /// so egui's own focus handling never double-acts on them. Mirrors the TUI keybindings.
    fn handle_keys(&mut self, ctx: &egui::Context) {
        let mut nav: Option<Tab> = None;
        let mut refresh = false;
        let mut hide = false;
        ctx.input_mut(|input| {
            use egui::{Key, Modifiers};
            let digits = [
                (Key::Num1, 1usize),
                (Key::Num2, 2),
                (Key::Num3, 3),
                (Key::Num4, 4),
                (Key::Num5, 5),
            ];
            for (key, digit) in digits {
                if input.consume_key(Modifiers::NONE, key) {
                    if let Some(tab) = Tab::from_digit(digit) {
                        nav = Some(tab);
                    }
                }
            }
            if input.consume_key(Modifiers::NONE, Key::ArrowRight)
                || input.consume_key(Modifiers::NONE, Key::Tab)
            {
                nav = Some(self.tab.next());
            }
            if input.consume_key(Modifiers::NONE, Key::ArrowLeft)
                || input.consume_key(Modifiers::SHIFT, Key::Tab)
            {
                nav = Some(self.tab.prev());
            }
            if input.consume_key(Modifiers::NONE, Key::R) {
                refresh = true;
            }
            if input.consume_key(Modifiers::NONE, Key::Q) {
                hide = true;
            }
        });
        if let Some(tab) = nav {
            self.tab = tab;
        }
        if refresh {
            self.request_manual_refresh();
        }
        if hide {
            self.quit_or_hide(ctx);
        }
    }
}

impl eframe::App for BarApp {
    // Non-drawing state work. Runs before each `ui` AND while the window is hidden whenever a
    // repaint is requested — so the auto-timer and tray actions keep running off-screen.
    fn logic(&mut self, ctx: &egui::Context, _frame: &mut eframe::Frame) {
        // 1. Apply any fresh worker outcomes, then rebuild the cockpit + resync the tray.
        let mut got_outcome = false;
        while let Some(outcome) = self.worker.poll() {
            self.state.apply(outcome, Instant::now());
            got_outcome = true;
        }
        if got_outcome {
            self.rebuild_cockpit();
            self.sync_tray();
        }

        // 2. Apply any tray actions.
        while let Ok(action) = self.actions.try_recv() {
            self.handle_action(action, ctx);
        }

        // 3. Closing the window hides it to the tray (when a tray exists); a real Quit comes from
        //    the tray menu. With no tray, allow the close so the app can exit.
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
            self.request_auto_refresh();
        }

        // 5. Heartbeat so the auto-timer still fires while the window is hidden/idle.
        ctx.request_repaint_after(REFRESH_INTERVAL);
    }

    // Drawing. The given `ui` is already inside eframe's central panel.
    fn ui(&mut self, ui: &mut egui::Ui, _frame: &mut eframe::Frame) {
        let ctx = ui.ctx().clone();
        self.handle_keys(&ctx);
        let view = self.shell_view();
        let action = draw_shell(ui, &view);
        if let Some(tab) = action.tab_clicked {
            self.tab = tab;
        }
        if action.refresh_clicked {
            self.request_manual_refresh();
        }
    }
}

/// Load the user config, mapping a load error to an in-window status string (never a crash). A
/// missing file is the zero-config default (no budgets, alerts off) — not an error.
fn load_config() -> (Config, Option<String>) {
    match costroid_config::load() {
        Ok(config) => (config, None),
        Err(error) => (Config::default(), Some(format!("config: {error}"))),
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
        // A failed refresh KEEPS the last-good cockpit on screen (`RefreshState::apply` retains
        // `loaded` on an error), so the `$` panels — Budget especially, whose figures carry no
        // inline `(estimated)` tag — keep rendering. The "· estimates" honesty caveat must ride
        // every state in which a dollar figure can show (CLAUDE.md: local cost is always an
        // estimate), so keep it here whenever stale data is still up. The no-data load states below
        // have no cockpit (no `$` panels), so they carry no caveat.
        return if has_data {
            format!("refresh failed — {reason} · estimates")
        } else {
            format!("refresh failed — {reason}")
        };
    }
    match (has_data, generated_hhmm) {
        (true, Some(stamp)) => format!("updated {stamp} · estimates"),
        (true, None) => "updated · estimates".to_owned(),
        (false, _) if phase == Phase::InFlight => "refreshing…".to_owned(),
        (false, _) => "starting…".to_owned(),
    }
}

/// Draw the window: the fixed mark+wordmark+status header (with the refresh button + any config
/// error), then the scrollable cockpit — the persistent spend+meters+banner header, the tab strip,
/// and the selected panel. Returns what the user clicked. Pure of app/tray/thread state.
fn draw_shell(ui: &mut egui::Ui, view: &ShellView) -> ShellAction {
    let mut action = ShellAction::default();

    ui.add_space(6.0);
    ui.horizontal(|ui| {
        ui.add_space(8.0);
        draw_mark(ui, view.step);
        ui.add_space(10.0);
        ui.vertical(|ui| {
            ui.add_space(2.0);
            ui.label(egui::RichText::new("costroid").strong().size(18.0));
            ui.label(
                egui::RichText::new(&view.status)
                    .monospace()
                    .size(11.0)
                    .color(color_of(ASH)),
            );
        });
        ui.with_layout(egui::Layout::right_to_left(egui::Align::Center), |ui| {
            ui.add_space(8.0);
            if draw_refresh_button(ui) {
                action.refresh_clicked = true;
            }
        });
    });
    if let Some(config_status) = &view.config_status {
        ui.horizontal(|ui| {
            ui.add_space(8.0);
            // A malformed config is a degraded state, not a "live" one — render it in muted Ash
            // (the secondary ink the status sub-line uses), never Signal-lime, which the brand
            // reserves for the active/selected/"live" highlight (pin §0/§6). The message text
            // carries the meaning, so this stays never-color-alone.
            ui.label(
                egui::RichText::new(config_status)
                    .monospace()
                    .size(11.0)
                    .color(color_of(ASH)),
            );
        });
    }
    ui.add_space(8.0);
    ui.separator();

    egui::ScrollArea::vertical()
        .auto_shrink([false, false])
        .show(ui, |ui| match view.cockpit {
            None => {
                ui.add_space(6.0);
                ui.horizontal(|ui| {
                    ui.add_space(8.0);
                    ui.label(egui::RichText::new("loading local data…").color(color_of(ASH)));
                });
            }
            Some(cockpit) => {
                // Persistent header: period spend + the painted quota meters, then the banner.
                overview::draw(ui, &cockpit.overview);
                banner::draw(ui, &cockpit.alerts);
                ui.add_space(6.0);
                ui.separator();
                ui.add_space(4.0);
                if let Some(tab) = tabs::draw_strip(ui, view.tab) {
                    action.tab_clicked = Some(tab);
                }
                ui.add_space(6.0);
                draw_panel(ui, view.tab, cockpit);
                ui.add_space(10.0);
                draw_key_hints(ui);
            }
        });

    action
}

/// The persistent key-hint footer — keys in Signal-lime (the active/"live" accent), labels in muted
/// Ash, dim `·` separators. Colorful but quiet; the words carry the meaning, so it never relies on
/// color alone. The honesty caveat ("estimates") rides the header status line, not here.
fn draw_key_hints(ui: &mut egui::Ui) {
    const HINTS: [(&str, &str); 3] = [("1-5", "tabs"), ("r", "refresh"), ("q", "hide")];
    ui.horizontal(|ui| {
        ui.spacing_mut().item_spacing.x = 4.0;
        ui.add_space(8.0);
        for (index, (key, label)) in HINTS.iter().enumerate() {
            if index > 0 {
                ui.label(
                    egui::RichText::new("·")
                        .monospace()
                        .size(10.0)
                        .color(color_of(ASH)),
                );
            }
            ui.label(
                egui::RichText::new(*key)
                    .monospace()
                    .size(10.0)
                    .strong()
                    .color(color_of(SIGNAL)),
            );
            ui.label(
                egui::RichText::new(*label)
                    .monospace()
                    .size(10.0)
                    .color(color_of(ASH)),
            );
        }
    });
}

/// Dispatch the lower region to the selected tab's panel.
fn draw_panel(ui: &mut egui::Ui, tab: Tab, cockpit: &Cockpit) {
    match tab {
        Tab::Overview => overview::draw_breakdown(ui, &cockpit.breakdown),
        Tab::Budget => budget::draw(ui, &cockpit.budget),
        Tab::Forecast => forecast::draw(ui, &cockpit.forecast),
        Tab::Anomalies => anomalies::draw(ui, &cockpit.anomalies),
        Tab::Providers => {
            providers::draw(ui, &cockpit.capabilities, &cockpit.statuses);
            #[cfg(feature = "connect")]
            providers::draw_connection_lane(ui, &cockpit.connections);
        }
    }
}

/// A painted refresh affordance — a circular arrow drawn with the same painter idiom as the `C⠉`
/// mark (`draw_mark`), NOT a typeset glyph. The bundled JetBrains Mono has no refresh-arrow glyph
/// (U+27F3 `⟳` / U+21BB `↻` / U+21BA `↺` are all absent from its cmap) and `fonts.rs` installs it as
/// the *only* family with no fallback, so a glyph button would render tofu — exactly the failure mode
/// the crate paints around ("paint, don't typeset" — `fonts.rs` / pin §13). Returns whether clicked.
fn draw_refresh_button(ui: &mut egui::Ui) -> bool {
    let side = 22.0;
    let (rect, response) = ui.allocate_exact_size(egui::Vec2::splat(side), egui::Sense::click());
    let response = response.on_hover_text("Refresh now");
    // The button is painted (no glyph), so name it for AccessKit as a button (T21).
    response
        .widget_info(|| egui::WidgetInfo::labeled(egui::WidgetType::Button, true, "Refresh now"));
    let painter = ui.painter_at(rect);

    // Brighten on hover (Bone), else the muted Ash the header chrome uses.
    let ink = color_of(if response.hovered() { BONE } else { ASH });
    let center = rect.center();
    let radius = side * 0.30;

    // A ~290° open arc as a painted polyline, leaving a gap for the arrowhead.
    let start = 150.0_f32.to_radians();
    let sweep = 290.0_f32.to_radians();
    let steps = 24;
    let points: Vec<egui::Pos2> = (0..=steps)
        .map(|i| {
            let theta = start + sweep * (i as f32 / steps as f32);
            center + egui::vec2(theta.cos(), theta.sin()) * radius
        })
        .collect();
    painter.add(egui::Shape::line(points, egui::Stroke::new(1.6, ink)));

    // The arrowhead at the arc's end, pointing along the sweep (the tangent direction).
    let end = start + sweep;
    let at = center + egui::vec2(end.cos(), end.sin()) * radius;
    let tangent = egui::vec2(-end.sin(), end.cos());
    let radial = egui::vec2(end.cos(), end.sin());
    let head = side * 0.18;
    let tip = at + tangent * head;
    let left = at - tangent * (head * 0.2) + radial * (head * 0.7);
    let right = at - tangent * (head * 0.2) - radial * (head * 0.7);
    painter.add(egui::Shape::convex_polygon(
        vec![tip, left, right],
        ink,
        egui::Stroke::NONE,
    ));

    response.clicked()
}

/// Paint the `C⠉` mark — the letter `C` plus the 3×3 dot grid filled to `step`, using the same
/// geometry as the tray bitmap (`glyph.rs`).
fn draw_mark(ui: &mut egui::Ui, step: u8) {
    let side = 44.0;
    let (rect, response) = ui.allocate_exact_size(egui::Vec2::splat(side), egui::Sense::hover());
    // The mark is painted (no text), so its meaning — the most-constrained limit severity — is
    // attached as an AccessKit name (T21).
    response.widget_info(|| {
        egui::WidgetInfo::labeled(
            egui::WidgetType::Label,
            true,
            format!("Costroid — most-constrained limit, severity {step} of 8"),
        )
    });
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
    use chrono::{DateTime, Utc};
    use costroid_core::{
        EngineSnapshot, GroupBy, LimitAvailability, LimitKind, LimitMeasure, LimitSummary,
        NowOptions, NowSummary, PeriodRange, ProviderId,
    };

    fn ts(secs: i64) -> DateTime<Utc> {
        match DateTime::from_timestamp(secs, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp"),
        }
    }

    #[test]
    fn color_of_opaque_matches_rgb() {
        assert_eq!(color_of(BONE), egui::Color32::from_rgb(0xe9, 0xe7, 0xdf));
        assert_eq!(color_of(CARBON), egui::Color32::from_rgb(0x0b, 0x0c, 0x0e));
    }

    #[test]
    fn status_text_prioritizes_error() {
        // A failed refresh over stale data still shows the cockpit's $ panels, so the error line
        // keeps the "· estimates" honesty caveat (cockpit Some ⇒ has_data true).
        let s = status_text(
            Some("could not read local data: boom"),
            true,
            Phase::Idle,
            Some("12:00".to_owned()),
        );
        assert_eq!(
            s,
            "refresh failed — could not read local data: boom · estimates"
        );
        // A first-load error (no data yet, no cockpit, no $ panels) carries no estimate caveat.
        let s = status_text(Some("boom"), false, Phase::Idle, None);
        assert_eq!(s, "refresh failed — boom");
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

    /// A snapshot with one Available limit window (no API rows) — enough to build a cockpit.
    fn sample_loaded() -> Loaded {
        let at = ts(1_900_000_000);
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
        let snapshot = EngineSnapshot {
            generated_at: at,
            usage_events: Vec::new(),
            focus_rows: Vec::new(),
            limit_windows: Vec::new(),
            providers: Vec::new(),
            capabilities: Vec::new(),
        };
        Loaded { snapshot, summary }
    }

    fn sample_cockpit() -> Cockpit {
        Cockpit::build(&sample_loaded(), &Config::default())
    }

    #[test]
    fn cockpit_build_with_default_config_raises_no_alerts() {
        // Default config = alerts OFF → the banner slice is empty (no fabricated crossing).
        let cockpit = sample_cockpit();
        assert!(cockpit.alerts.is_empty());
        assert_eq!(cockpit.overview.meters.len(), 1);
    }

    #[test]
    fn draw_shell_headless_tick_does_not_panic() {
        // A headless egui pass exercises the whole shell — before a snapshot loads (no cockpit) and
        // with one present — across every tab.
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        let cockpit = sample_cockpit();
        for cockpit_ref in [None, Some(&cockpit)] {
            for tab in Tab::ALL {
                let view = ShellView {
                    step: 4,
                    status: "updated 12:00 · estimates".to_owned(),
                    config_status: Some("config: invalid config ...".to_owned()),
                    tab,
                    cockpit: cockpit_ref,
                };
                let _ = ctx.run_ui(egui::RawInput::default(), |ui| {
                    let _ = draw_shell(ui, &view);
                });
            }
        }
    }

    #[test]
    fn accesskit_tree_announces_the_painted_widgets() {
        // T21 a11y smoke: with AccessKit enabled, a headless pass builds the accessibility tree
        // without panic AND the painted widgets (the mark, the refresh button, the quota meter, the
        // alert badge) carry names — so a screen reader announces each. egui core always builds the
        // tree once `enable_accesskit` is called (the eframe `accesskit` feature only wires the
        // platform AT-SPI backend), so this runs regardless of the feature flag.
        use costroid_core::{Alert, AlertLevel, LimitKind, ProviderId};
        let ctx = egui::Context::default();
        crate::fonts::install(&ctx);
        ctx.enable_accesskit();

        let mut cockpit = sample_cockpit();
        // Force one active alert so the banner badge is painted + named.
        cockpit.alerts.push(Alert::Quota {
            tool: ProviderId::ClaudeCode,
            kind: LimitKind::FiveHour,
            level: AlertLevel::Critical,
            fraction: 0.97,
            reset_in_seconds: 3600,
        });
        let view = ShellView {
            step: 7,
            status: "updated 12:00 · estimates".to_owned(),
            config_status: None,
            tab: Tab::Overview,
            cockpit: Some(&cockpit),
        };

        let output = ctx.run_ui(egui::RawInput::default(), |ui| {
            let _ = draw_shell(ui, &view);
        });
        let update = match output.platform_output.accesskit_update {
            Some(update) => update,
            None => panic!("the AccessKit tree must build when accesskit is enabled"),
        };
        assert!(
            update.nodes.len() > 5,
            "expected a populated a11y tree, got {} nodes",
            update.nodes.len()
        );
        // Collect every node's accessible text. egui maps a `Label`-role widget's text onto the
        // node `value`, and other roles (Button/ProgressIndicator/Image) onto the node `label`.
        let mut text = String::new();
        for (_, node) in &update.nodes {
            if let Some(label) = node.label() {
                text.push_str(label);
                text.push('\n');
            }
            if let Some(value) = node.value() {
                text.push_str(value);
                text.push('\n');
            }
        }
        assert!(
            text.contains("most-constrained limit"),
            "the painted mark must be named:\n{text}"
        );
        assert!(
            text.contains("Refresh now"),
            "the painted refresh button must be named:\n{text}"
        );
        assert!(
            text.contains("claude code"),
            "the painted quota meter must carry its line:\n{text}"
        );
        assert!(
            text.contains("critical alert"),
            "the painted alert badge must announce its severity:\n{text}"
        );
    }

    #[test]
    fn now_options_default_is_weekly() {
        // The bar collects with NowOptions::default() (Week) — the breakdown header says "this week".
        assert_eq!(
            NowOptions::default().cost_period,
            costroid_core::Period::Week
        );
    }
}
