use std::io::{self, Stdout, Write};
use std::panic::{self, PanicHookInfo};
use std::process;
use std::sync::{Arc, Mutex};
use std::time::Duration as StdDuration;

use anyhow::{Context, Result};
use chrono::{DateTime, Duration, Utc};
use costroid_core::{EngineSnapshot, GroupBy, NowOptions, Period, TrendsOptions};
use costroid_providers::HostEnv;
use crossterm::cursor::{Hide, Show};
use crossterm::event::{self, Event, KeyCode, KeyEvent, KeyEventKind, KeyModifiers};
use crossterm::execute;
use crossterm::terminal::{
    disable_raw_mode, enable_raw_mode, EnterAlternateScreen, LeaveAlternateScreen,
};
use ratatui::backend::CrosstermBackend;
use ratatui::layout::{Alignment, Constraint, Direction, Layout, Rect};
use ratatui::style::{Color, Modifier, Style as RatatuiStyle};
use ratatui::text::{Line, Span, Text};
use ratatui::widgets::{Block, Borders, Clear, Paragraph, Wrap};
use ratatui::{Frame, Terminal};

use crate::render::{
    render_frontier_document, render_models_document, render_now_document,
    render_providers_document, render_trends_document, RenderOptions, SemanticStyle,
    StyledDocument, StyledLine,
};

const SPINNER_FRAMES: [char; 10] = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const ASCII_SPINNER_FRAMES: [char; 4] = ['|', '/', '-', '\\'];
const REDRAW_TICK: StdDuration = StdDuration::from_millis(80);
const COLLECT_INTERVAL: Duration = Duration::seconds(2);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum StartScreen {
    Now,
    Trends,
    Frontier,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Screen {
    Now,
    Trends,
    Providers,
    Models,
    Frontier,
}

/// The numbered tab cycle (Q1, §11.5): `1`-`6` jump straight to a tab, `Tab`/`BackTab`
/// cycle through them. Frontier is intentionally NOT in the cycle — it stays its own
/// `a`/`esc` overlay. Later Step 5 tabs (History/Budget…) append here, filling the reserved
/// `5`-`6` slots, with no further `handle_key` change (T12 appended Models at slot `4`).
const TAB_SCREENS: [Screen; 4] = [
    Screen::Now,
    Screen::Trends,
    Screen::Providers,
    Screen::Models,
];

/// Step left (`delta = -1`) or right (`delta = 1`) through [`TAB_SCREENS`], wrapping. A
/// screen outside the cycle (Frontier) returns to the first tab.
fn cycle_tab(current: Screen, delta: isize) -> Screen {
    match TAB_SCREENS.iter().position(|screen| *screen == current) {
        Some(index) => {
            let len = TAB_SCREENS.len() as isize;
            let next = (index as isize + delta).rem_euclid(len) as usize;
            TAB_SCREENS.get(next).copied().unwrap_or(current)
        }
        None => TAB_SCREENS[0],
    }
}

/// Map a `1`-`6` digit to its tab, if one exists yet (`1`-`4` are wired today — slot `4` is
/// Models; the rest are reserved and inert until a later tab fills them).
fn tab_for_digit(ch: char) -> Option<Screen> {
    let index = ch.to_digit(10)? as usize;
    index
        .checked_sub(1)
        .and_then(|zero_based| TAB_SCREENS.get(zero_based).copied())
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum AppAction {
    Continue,
    Refresh,
    Quit,
}

pub(crate) trait SnapshotCollector {
    fn collect(&mut self, env: &HostEnv) -> Result<EngineSnapshot>;
}

pub(crate) trait Clock {
    fn now(&self) -> DateTime<Utc>;
}

struct LocalCollector;

impl SnapshotCollector for LocalCollector {
    fn collect(&mut self, env: &HostEnv) -> Result<EngineSnapshot> {
        costroid_core::collect_local_snapshot(env).map_err(Into::into)
    }
}

struct SystemClock;

impl Clock for SystemClock {
    fn now(&self) -> DateTime<Utc> {
        Utc::now()
    }
}

#[derive(Debug, Clone, Copy)]
struct TuiConfig {
    start_screen: StartScreen,
    period: Period,
    group_by: GroupBy,
    live: bool,
    render_options: RenderOptions,
}

#[derive(Debug, Clone)]
struct App {
    screen: Screen,
    /// Where `a` was pressed from, so `esc`/`n` returns there.
    previous_screen: Screen,
    period: Period,
    group_by: GroupBy,
    filter: String,
    filter_editing: bool,
    help_open: bool,
    live: bool,
    loading: bool,
    spinner_index: usize,
    snapshot: Option<EngineSnapshot>,
    last_collect_at: Option<DateTime<Utc>>,
    status: Option<String>,
    render_options: RenderOptions,
    /// The Providers-tab connection lane, gathered once read-only over the existing
    /// keychain/registry (no network). Connect-gated: absent from the default build.
    #[cfg(feature = "connect")]
    connections: Vec<crate::render::ConnectionEntry>,
}

impl App {
    fn new(
        start_screen: StartScreen,
        period: Period,
        group_by: GroupBy,
        live: bool,
        render_options: RenderOptions,
    ) -> Self {
        let screen = match start_screen {
            StartScreen::Now => Screen::Now,
            StartScreen::Trends => Screen::Trends,
            StartScreen::Frontier => Screen::Frontier,
        };
        Self {
            screen,
            previous_screen: Screen::Now,
            period,
            group_by,
            filter: String::new(),
            filter_editing: false,
            help_open: false,
            live,
            loading: true,
            spinner_index: 0,
            snapshot: None,
            last_collect_at: None,
            status: None,
            render_options,
            #[cfg(feature = "connect")]
            connections: Vec::new(),
        }
    }

    fn refresh<C: SnapshotCollector>(
        &mut self,
        collector: &mut C,
        env: &HostEnv,
        now: DateTime<Utc>,
    ) {
        self.loading = true;
        match collector.collect(env) {
            Ok(mut snapshot) => {
                snapshot.generated_at = now;
                self.snapshot = Some(snapshot);
                self.last_collect_at = Some(now);
                self.status = Some("refreshed local logs".to_string());
            }
            Err(error) => {
                self.status = Some(format!("refresh failed: {error}"));
                self.last_collect_at = Some(now);
            }
        }
        self.loading = false;
    }

    fn should_auto_collect(&self, now: DateTime<Utc>) -> bool {
        if !self.live || self.loading {
            return false;
        }
        match self.last_collect_at {
            Some(last) => now - last >= COLLECT_INTERVAL,
            None => true,
        }
    }

    fn advance_spinner(&mut self) {
        self.spinner_index = self.spinner_index.wrapping_add(1);
    }

    fn handle_key(&mut self, key: KeyEvent) -> AppAction {
        if key.modifiers.contains(KeyModifiers::CONTROL) && key.code == KeyCode::Char('c') {
            return AppAction::Quit;
        }

        if self.filter_editing {
            return self.handle_filter_key(key);
        }

        match key.code {
            KeyCode::Char('q') => AppAction::Quit,
            KeyCode::Tab => {
                self.screen = cycle_tab(self.screen, 1);
                AppAction::Continue
            }
            KeyCode::BackTab => {
                self.screen = cycle_tab(self.screen, -1);
                AppAction::Continue
            }
            KeyCode::Char(ch @ '1'..='6') => {
                if let Some(screen) = tab_for_digit(ch) {
                    self.screen = screen;
                }
                AppAction::Continue
            }
            KeyCode::Char('d') if self.screen == Screen::Trends => {
                self.period = Period::Day;
                AppAction::Continue
            }
            KeyCode::Char('w') if self.screen == Screen::Trends => {
                self.period = Period::Week;
                AppAction::Continue
            }
            KeyCode::Char('m') if self.screen == Screen::Trends => {
                self.period = Period::Month;
                AppAction::Continue
            }
            KeyCode::Char('y') if self.screen == Screen::Trends => {
                self.period = Period::Year;
                AppAction::Continue
            }
            KeyCode::Char('g') if self.screen == Screen::Trends => {
                self.group_by = next_group(self.group_by);
                AppAction::Continue
            }
            KeyCode::Char('f') | KeyCode::Char('/') => {
                self.filter_editing = true;
                self.status = Some("type to filter model/app rows; enter applies".to_string());
                AppAction::Continue
            }
            KeyCode::Char('r') => AppAction::Refresh,
            KeyCode::Char('?') => {
                self.help_open = !self.help_open;
                AppAction::Continue
            }
            KeyCode::Char('a') if self.screen != Screen::Frontier => {
                self.previous_screen = self.screen;
                self.screen = Screen::Frontier;
                self.status = Some("cost-vs-quality frontier; no network or LLM call".to_string());
                AppAction::Continue
            }
            KeyCode::Char('n') if self.screen == Screen::Frontier => {
                self.screen = self.previous_screen;
                AppAction::Continue
            }
            KeyCode::Esc => {
                if self.help_open {
                    self.help_open = false;
                } else if self.screen == Screen::Frontier {
                    self.screen = self.previous_screen;
                }
                AppAction::Continue
            }
            _ => AppAction::Continue,
        }
    }

    fn handle_filter_key(&mut self, key: KeyEvent) -> AppAction {
        match key.code {
            KeyCode::Esc => {
                self.filter_editing = false;
                AppAction::Continue
            }
            KeyCode::Enter => {
                self.filter_editing = false;
                self.status = if self.filter.trim().is_empty() {
                    Some("filter cleared".to_string())
                } else {
                    Some(format!("filter: {}", self.filter))
                };
                AppAction::Continue
            }
            KeyCode::Backspace => {
                self.filter.pop();
                AppAction::Continue
            }
            KeyCode::Char(ch) => {
                self.filter.push(ch);
                AppAction::Continue
            }
            _ => AppAction::Continue,
        }
    }

    fn document_for_width(&self, width: u16, now: DateTime<Utc>) -> StyledDocument {
        let options = self.render_options.with_width(width as usize);
        match &self.snapshot {
            Some(snapshot) => {
                let mut snapshot = snapshot.clone();
                snapshot.generated_at = now;
                match self.screen {
                    Screen::Now => {
                        let mut summary = costroid_core::now_summary(
                            &snapshot,
                            NowOptions {
                                cost_period: Period::Week,
                                group_by: GroupBy::Model,
                            },
                        );
                        apply_now_filter(&mut summary, &self.filter);
                        render_now_document(&summary, options)
                    }
                    Screen::Trends => {
                        let mut summary = costroid_core::trends_summary(
                            &snapshot,
                            TrendsOptions {
                                period: self.period,
                                group_by: self.group_by,
                            },
                        );
                        apply_trends_filter(&mut summary, &self.filter);
                        render_trends_document(&summary, options)
                    }
                    Screen::Providers => {
                        #[allow(unused_mut)]
                        let mut doc = render_providers_document(
                            &snapshot.capabilities,
                            &snapshot.providers,
                            options,
                        );
                        // The connection lane (your own usage-API keys) is read-only over
                        // the existing keychain/registry — connect-gated, so the default
                        // build renders the local Capability/ProviderStatus alone.
                        #[cfg(feature = "connect")]
                        crate::render::push_provider_connection_lane(
                            &mut doc,
                            &self.connections,
                            options,
                        );
                        doc
                    }
                    Screen::Models => match costroid_core::models_view(&snapshot) {
                        Ok(view) => render_models_document(&view, options),
                        Err(error) => {
                            let mut doc = StyledDocument::new();
                            doc.push(StyledLine::plain(format!(
                                "models data unavailable: {error}"
                            )));
                            doc
                        }
                    },
                    Screen::Frontier => match costroid_core::bench_view(&snapshot) {
                        Ok(view) => render_frontier_document(&view, options),
                        Err(error) => {
                            let mut doc = StyledDocument::new();
                            doc.push(StyledLine::plain(format!(
                                "frontier data unavailable: {error}"
                            )));
                            doc
                        }
                    },
                }
            }
            None => loading_document(self, options),
        }
    }

    fn footer(&self) -> String {
        let left = match self.screen {
            Screen::Now => "now",
            Screen::Trends => "trends",
            Screen::Providers => "providers",
            Screen::Models => "models",
            Screen::Frontier => "frontier",
        };
        let live = if self.live { "live" } else { "manual" };
        let filter = if self.filter.trim().is_empty() {
            String::new()
        } else {
            format!(" filter:{}", self.filter)
        };
        let status = self
            .status
            .as_deref()
            .map(|value| format!(" | {value}"))
            .unwrap_or_default();
        let nav = match self.screen {
            Screen::Frontier => "esc back",
            Screen::Now | Screen::Trends | Screen::Providers | Screen::Models => {
                "1-4/tab switch | a frontier"
            }
        };
        format!("{left} | {live} | {nav} | r refresh | ? help | q quit{filter}{status}")
    }
}

pub(crate) fn run(
    start_screen: StartScreen,
    period: Period,
    group_by: GroupBy,
    live: bool,
    render_options: RenderOptions,
) -> Result<()> {
    let env = HostEnv::detect();
    let mut collector = LocalCollector;
    let clock = SystemClock;
    let config = TuiConfig {
        start_screen,
        period,
        group_by,
        live,
        render_options,
    };
    run_with_dependencies(&env, &mut collector, &clock, config)
}

fn run_with_dependencies<C: SnapshotCollector, K: Clock>(
    env: &HostEnv,
    collector: &mut C,
    clock: &K,
    config: TuiConfig,
) -> Result<()> {
    let mut session = TerminalSession::enter()?;
    let mut app = App::new(
        config.start_screen,
        config.period,
        config.group_by,
        config.live,
        config.render_options,
    );
    // Gather the Providers-tab connection lane once, read-only over the existing
    // keychain/registry (no network), so live refreshes never re-read the keychain.
    #[cfg(feature = "connect")]
    {
        app.connections = gather_connection_entries();
    }
    let now = clock.now();
    session
        .terminal
        .draw(|frame| draw_app(frame, &app, now))
        .context("draw loading screen")?;
    app.refresh(collector, env, now);

    loop {
        let now = clock.now();
        session
            .terminal
            .draw(|frame| draw_app(frame, &app, now))
            .context("draw TUI frame")?;

        if app.should_auto_collect(now) {
            app.loading = true;
            session
                .terminal
                .draw(|frame| draw_app(frame, &app, now))
                .context("draw refresh loading frame")?;
            app.refresh(collector, env, now);
            continue;
        }

        if event::poll(REDRAW_TICK).context("poll terminal events")? {
            match event::read().context("read terminal event")? {
                Event::Key(key) if is_actionable_key(key) => match app.handle_key(key) {
                    AppAction::Continue => {}
                    AppAction::Refresh => {
                        app.loading = true;
                        let refresh_now = clock.now();
                        session
                            .terminal
                            .draw(|frame| draw_app(frame, &app, refresh_now))
                            .context("draw manual refresh loading frame")?;
                        app.refresh(collector, env, refresh_now);
                    }
                    AppAction::Quit => break,
                },
                Event::Resize(_, _) => {}
                _ => {}
            }
        } else {
            app.advance_spinner();
        }
    }

    Ok(())
}

fn is_actionable_key(key: KeyEvent) -> bool {
    matches!(key.kind, KeyEventKind::Press | KeyEventKind::Repeat)
}

fn draw_app(frame: &mut Frame<'_>, app: &App, now: DateTime<Utc>) {
    let area = frame.area();
    let chunks = Layout::default()
        .direction(Direction::Vertical)
        .constraints([Constraint::Min(1), Constraint::Length(1)])
        .split(area);
    let doc = app.document_for_width(chunks[0].width, now);
    let paragraph = Paragraph::new(styled_document_to_text(&doc, app.render_options))
        .wrap(Wrap { trim: false });
    frame.render_widget(paragraph, chunks[0]);

    let footer = if app.filter_editing {
        format!("filter: {}", app.filter)
    } else if app.loading {
        format!("{} refreshing local logs", spinner(app))
    } else {
        app.footer()
    };
    frame.render_widget(Paragraph::new(footer), chunks[1]);

    if app.help_open {
        draw_help(frame, area);
    }
}

fn draw_help(frame: &mut Frame<'_>, area: Rect) {
    let popup = centered_rect(74, 14, area);
    let lines = vec![
        Line::from("1/2/3/4    now / trends / providers / models"),
        Line::from("tab/S-tab  cycle tabs"),
        Line::from("d/w/m/y    set trends period"),
        Line::from("g          cycle trends group"),
        Line::from("a          cost-vs-quality frontier"),
        Line::from("esc / n    back from frontier"),
        Line::from("f or /     filter model/app rows"),
        Line::from("r          refresh local logs"),
        Line::from("?          close help"),
        Line::from("q/Ctrl-C   quit"),
    ];
    let block = Block::default().title("help").borders(Borders::ALL);
    let paragraph = Paragraph::new(Text::from(lines))
        .block(block)
        .alignment(Alignment::Left);
    frame.render_widget(Clear, popup);
    frame.render_widget(paragraph, popup);
}

fn centered_rect(width: u16, height: u16, area: Rect) -> Rect {
    let width = width.min(area.width);
    let height = height.min(area.height);
    let x = area.x + area.width.saturating_sub(width) / 2;
    let y = area.y + area.height.saturating_sub(height) / 2;
    Rect {
        x,
        y,
        width,
        height,
    }
}

fn styled_document_to_text(
    document: &StyledDocument,
    render_options: RenderOptions,
) -> Text<'static> {
    Text::from(
        document
            .lines
            .iter()
            .map(|line| {
                Line::from(
                    line.spans
                        .iter()
                        .map(|span| {
                            Span::styled(
                                span.content.clone(),
                                ratatui_style(span.style, render_options),
                            )
                        })
                        .collect::<Vec<_>>(),
                )
            })
            .collect::<Vec<_>>(),
    )
}

fn ratatui_style(style: SemanticStyle, render_options: RenderOptions) -> RatatuiStyle {
    if !render_options.ansi {
        return RatatuiStyle::default();
    }
    match style {
        SemanticStyle::Plain => RatatuiStyle::default(),
        SemanticStyle::Strong => RatatuiStyle::default().add_modifier(Modifier::BOLD),
        SemanticStyle::Warn => RatatuiStyle::default()
            .fg(Color::Yellow)
            .add_modifier(Modifier::BOLD),
        SemanticStyle::Critical => RatatuiStyle::default()
            .fg(Color::Red)
            .add_modifier(Modifier::BOLD),
    }
}

fn loading_document(app: &App, render_options: RenderOptions) -> StyledDocument {
    let mut doc = StyledDocument::new();
    doc.push(StyledLine::plain(format!(
        "{} costroid",
        match render_options.mode {
            crate::render::RenderMode::Braille => "C⠉",
            crate::render::RenderMode::Ascii | crate::render::RenderMode::Plain => "costroid",
        }
    )));
    doc.push(StyledLine::plain(format!(
        "{} reading local provider logs",
        spinner(app)
    )));
    if let Some(status) = &app.status {
        doc.push(StyledLine::plain(status));
    }
    doc
}

fn spinner(app: &App) -> char {
    match app.render_options.mode {
        crate::render::RenderMode::Braille => {
            SPINNER_FRAMES[app.spinner_index % SPINNER_FRAMES.len()]
        }
        crate::render::RenderMode::Ascii | crate::render::RenderMode::Plain => {
            ASCII_SPINNER_FRAMES[app.spinner_index % ASCII_SPINNER_FRAMES.len()]
        }
    }
}

fn apply_now_filter(summary: &mut costroid_core::NowSummary, filter: &str) {
    if filter.trim().is_empty() {
        return;
    }
    retain_matching_rows(&mut summary.current_costs, filter, summary.group_by);
}

fn apply_trends_filter(summary: &mut costroid_core::TrendsSummary, filter: &str) {
    if filter.trim().is_empty() || summary.group_by == GroupBy::Total {
        return;
    }
    retain_matching_rows(&mut summary.totals, filter, summary.group_by);
    let query = filter.to_ascii_lowercase();
    summary.buckets.retain(|bucket| {
        bucket.group.value.to_ascii_lowercase().contains(&query)
            || display_value(&bucket.group.value).contains(&query)
    });
}

fn retain_matching_rows(
    rows: &mut Vec<costroid_core::CostLaneSummary>,
    filter: &str,
    group: GroupBy,
) {
    if group == GroupBy::Total {
        return;
    }
    let query = filter.to_ascii_lowercase();
    rows.retain(|row| {
        row.group.value.to_ascii_lowercase().contains(&query)
            || display_value(&row.group.value).contains(&query)
    });
}

fn display_value(value: &str) -> String {
    value
        .rsplit(['/', '\\'])
        .find(|part| !part.is_empty())
        .unwrap_or(value)
        .replace('-', " ")
        .to_ascii_lowercase()
}

fn next_group(group: GroupBy) -> GroupBy {
    match group {
        GroupBy::Model => GroupBy::App,
        GroupBy::App => GroupBy::Total,
        GroupBy::Total => GroupBy::Model,
    }
}

/// Read the per-vendor connection state for the Providers tab, read-only over the existing
/// keychain/registry — NO network, NEVER key material. Mirrors `connect.rs`'s
/// `run_connections` (no `--check`): the dual gate (`is_connected` AND `retrieve.is_some`,
/// the keychain being the source of truth for the secret's presence), the non-secret org
/// label, and Gemini's pinned "unavailable" message. Degrades to an empty/partial lane if
/// the keychain or registry is unreachable, never aborting the TUI.
#[cfg(feature = "connect")]
fn gather_connection_entries() -> Vec<crate::render::ConnectionEntry> {
    use costroid_connect::{ConnectionRegistry, CredentialStore};

    // Open the read-only handles, degrading to an empty lane if the keychain or registry is
    // unreachable (never aborting the TUI). The gate + label logic lives in the injectable
    // `connection_entries` below, which is unit-tested against a mock keychain.
    let store = match CredentialStore::new() {
        Ok(store) => store,
        Err(_) => return Vec::new(),
    };
    let registry = match ConnectionRegistry::open() {
        Ok(registry) => registry,
        Err(_) => return Vec::new(),
    };
    connection_entries(&store, &registry)
}

/// Build the per-vendor connection lane over an already-open keychain + registry — injectable
/// so it is unit-testable without touching the real OS keychain. The dual gate (`is_connected`
/// AND the key present in the keychain, the keychain being the source of truth for the
/// secret's presence), the non-secret org label, and Gemini's pinned "unavailable" message.
/// Read-only, NO network, NEVER key material; a per-vendor keychain/registry read error
/// degrades that vendor to "not connected" rather than aborting the lane.
#[cfg(feature = "connect")]
fn connection_entries(
    store: &costroid_connect::CredentialStore,
    registry: &costroid_connect::ConnectionRegistry,
) -> Vec<crate::render::ConnectionEntry> {
    use crate::render::{ConnectionEntry, ConnectionState};
    use costroid_connect::ApiVendor;

    let mut entries = Vec::new();
    for vendor in ApiVendor::ALL {
        let state = match vendor {
            // Gemini is a first-class "unavailable", never a network call; reuse the pinned
            // message verbatim (single-sourced in costroid-core).
            ApiVendor::Gemini => {
                ConnectionState::Unavailable(costroid_core::GEMINI_UNAVAILABLE_MESSAGE.to_string())
            }
            ApiVendor::Anthropic | ApiVendor::OpenAI => {
                // The dual gate (connect.rs): the registry marks it connected AND the key is
                // present in the keychain (the keychain is the source of truth). Read-only.
                let connected = registry.is_connected(vendor).unwrap_or(false)
                    && store
                        .retrieve(vendor)
                        .map(|key| key.is_some())
                        .unwrap_or(false);
                if connected {
                    let org = registry.label(vendor).ok().flatten().map(format_org_label);
                    ConnectionState::Connected { org }
                } else {
                    ConnectionState::NotConnected
                }
            }
        };
        entries.push(ConnectionEntry {
            vendor: vendor.to_string(),
            state,
        });
    }
    entries
}

/// The non-secret organization label, `name (id)` or just `name`. Never key material.
#[cfg(feature = "connect")]
fn format_org_label(label: costroid_connect::OrgLabel) -> String {
    match label.id {
        Some(id) => format!("{} ({})", label.name, id),
        None => label.name,
    }
}

type PanicHook = Box<dyn Fn(&PanicHookInfo<'_>) + Sync + Send + 'static>;

struct PanicHookGuard {
    previous: Arc<Mutex<Option<PanicHook>>>,
}

impl PanicHookGuard {
    fn install() -> Self {
        let previous = Arc::new(Mutex::new(Some(panic::take_hook())));
        let hook_previous = Arc::clone(&previous);
        panic::set_hook(Box::new(move |info| {
            restore_terminal();
            if let Ok(previous) = hook_previous.lock() {
                if let Some(previous) = previous.as_ref() {
                    previous(info);
                }
            }
            process::exit(101);
        }));
        Self { previous }
    }
}

impl Drop for PanicHookGuard {
    fn drop(&mut self) {
        if let Ok(mut previous) = self.previous.lock() {
            if let Some(previous) = previous.take() {
                panic::set_hook(previous);
            }
        }
    }
}

struct TerminalSession {
    terminal: Terminal<CrosstermBackend<Stdout>>,
    _panic_hook: PanicHookGuard,
}

impl TerminalSession {
    fn enter() -> Result<Self> {
        let panic_hook = PanicHookGuard::install();
        enable_raw_mode().context("enable terminal raw mode")?;
        let mut stdout = io::stdout();
        if let Err(error) = execute!(stdout, EnterAlternateScreen, Hide) {
            restore_terminal();
            return Err(error).context("enter alternate screen");
        }
        let backend = CrosstermBackend::new(stdout);
        let terminal = match Terminal::new(backend) {
            Ok(terminal) => terminal,
            Err(error) => {
                restore_terminal();
                return Err(error).context("create terminal backend");
            }
        };
        Ok(Self {
            terminal,
            _panic_hook: panic_hook,
        })
    }
}

impl Drop for TerminalSession {
    fn drop(&mut self) {
        restore_terminal();
    }
}

fn restore_terminal() {
    let _ = disable_raw_mode();
    let mut stdout = io::stdout();
    let _ = write_restore_sequences(&mut stdout);
}

fn write_restore_sequences<W: Write>(writer: &mut W) -> io::Result<()> {
    execute!(writer, LeaveAlternateScreen, Show)
}

#[cfg(test)]
mod tests {
    use std::path::PathBuf;

    use chrono::{LocalResult, TimeZone};
    use costroid_core::{CostLane, ProviderCapabilityView, ProviderStatus, ProviderStatusKind};
    use costroid_focus::{
        FocusAccessPath, FocusRecord, TokenType, UnpricedUsage, DEFAULT_BILLING_CURRENCY,
    };
    use costroid_providers::{
        AuthMethod, DataSource, LimitKind, LimitMeasure, LimitStatus, LimitWindow, ProviderId,
    };
    use ratatui::backend::TestBackend;

    use super::*;
    use crate::render::{RenderMode, StyledLine};

    fn utc(year: i32, month: u32, day: u32, hour: u32, minute: u32) -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(year, month, day, hour, minute, 0) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        }
    }

    fn render_options(mode: RenderMode, ansi: bool) -> RenderOptions {
        RenderOptions {
            mode,
            ansi,
            width: 64,
        }
    }

    fn test_env() -> HostEnv {
        HostEnv::new(PathBuf::from("/tmp/costroid-test"), Vec::new(), false)
    }

    fn sample_record(model: &str, cents: i64, project: &str) -> FocusRecord {
        let timestamp = utc(2026, 6, 2, 9, 0);
        let input = UnpricedUsage {
            timestamp,
            tool: "codex".to_string(),
            model: model.to_string(),
            token_type: TokenType::Output,
            token_count: 1_000_000,
            project: Some(project.to_string()),
            access_path: FocusAccessPath::Api,
            service_name: "OpenAI API".to_string(),
            service_provider_name: "OpenAI".to_string(),
            host_provider_name: "OpenAI".to_string(),
            invoice_issuer_name: "OpenAI".to_string(),
            billing_currency: DEFAULT_BILLING_CURRENCY.to_string(),
        };
        let mut record = match FocusRecord::unpriced_usage(input) {
            Ok(record) => record,
            Err(error) => panic!("test record should be valid: {error}"),
        };
        let cost = rust_decimal::Decimal::new(cents, 2);
        record.billed_cost = cost;
        record.effective_cost = cost;
        record.list_cost = cost;
        record.contracted_cost = cost;
        record.list_unit_price = Some(cost);
        record.contracted_unit_price = Some(cost);
        record.sku_price_id = Some(format!("{model}:output:standard"));
        record.x_pricing_status = "priced".to_string();
        record
    }

    fn sample_snapshot(now: DateTime<Utc>) -> EngineSnapshot {
        EngineSnapshot {
            generated_at: now,
            usage_events: Vec::new(),
            focus_rows: vec![
                sample_record("claude-opus-4.7", 2410, "alpha-app"),
                sample_record("gpt-5.5", 1130, "beta-app"),
            ],
            limit_windows: vec![LimitWindow {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                measure: Some(LimitMeasure::TokenFraction(0.92)),
                resets_at: Some(now + Duration::hours(2)),
                captured_at: now,
                status: LimitStatus::Verified,
                label: None,
            }],
            providers: vec![
                ProviderStatus {
                    provider: ProviderId::Codex,
                    status: ProviderStatusKind::Available,
                    files: 1,
                    usage_events: 2,
                    focus_rows: 2,
                    limit_windows: 1,
                    message: None,
                },
                ProviderStatus {
                    provider: ProviderId::Cursor,
                    status: ProviderStatusKind::Missing,
                    files: 0,
                    usage_events: 0,
                    focus_rows: 0,
                    limit_windows: 0,
                    message: Some("no local data found".to_string()),
                },
            ],
            capabilities: sample_capabilities(),
        }
    }

    fn sample_capabilities() -> Vec<ProviderCapabilityView> {
        vec![
            ProviderCapabilityView {
                provider: ProviderId::ClaudeCode,
                api_cost: DataSource::LocalArtifact,
                subscription_quota: DataSource::SanctionedHook,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: vec![LimitKind::FiveHour, LimitKind::Weekly],
            },
            ProviderCapabilityView {
                provider: ProviderId::Codex,
                api_cost: DataSource::LocalArtifact,
                subscription_quota: DataSource::LocalArtifact,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: vec![LimitKind::FiveHour, LimitKind::Weekly],
            },
            ProviderCapabilityView {
                provider: ProviderId::Cursor,
                api_cost: DataSource::Unavailable,
                subscription_quota: DataSource::Unavailable,
                model_mix: DataSource::LocalArtifact,
                auth: AuthMethod::None,
                quota_kinds: Vec::new(),
            },
        ]
    }

    fn empty_snapshot(now: DateTime<Utc>) -> EngineSnapshot {
        EngineSnapshot {
            generated_at: now,
            usage_events: Vec::new(),
            focus_rows: Vec::new(),
            limit_windows: Vec::new(),
            providers: vec![
                ProviderStatus {
                    provider: ProviderId::ClaudeCode,
                    status: ProviderStatusKind::Missing,
                    files: 0,
                    usage_events: 0,
                    focus_rows: 0,
                    limit_windows: 0,
                    message: Some("no local data found".to_string()),
                },
                ProviderStatus {
                    provider: ProviderId::Codex,
                    status: ProviderStatusKind::Missing,
                    files: 0,
                    usage_events: 0,
                    focus_rows: 0,
                    limit_windows: 0,
                    message: Some("no local data found".to_string()),
                },
            ],
            capabilities: Vec::new(),
        }
    }

    fn app_with_snapshot(screen: StartScreen, mode: RenderMode) -> App {
        let now = utc(2026, 6, 2, 9, 0);
        let mut app = App::new(
            screen,
            Period::Week,
            GroupBy::Model,
            false,
            render_options(mode, false),
        );
        app.loading = false;
        app.snapshot = Some(sample_snapshot(now));
        app.last_collect_at = Some(now);
        app
    }

    fn key(code: KeyCode) -> KeyEvent {
        KeyEvent::new(code, KeyModifiers::NONE)
    }

    fn ctrl_c() -> KeyEvent {
        KeyEvent::new(KeyCode::Char('c'), KeyModifiers::CONTROL)
    }

    fn frame_to_string(app: &App, width: u16, height: u16) -> String {
        let now = utc(2026, 6, 2, 9, 0);
        let backend = TestBackend::new(width, height);
        let mut terminal = match Terminal::new(backend) {
            Ok(terminal) => terminal,
            Err(error) => panic!("test terminal should be valid: {error}"),
        };
        match terminal.draw(|frame| draw_app(frame, app, now)) {
            Ok(_) => {}
            Err(error) => panic!("test frame should draw: {error}"),
        }
        buffer_to_string(terminal.backend().buffer())
    }

    fn buffer_to_string(buffer: &ratatui::buffer::Buffer) -> String {
        let mut out = String::new();
        for y in buffer.area.y..buffer.area.y + buffer.area.height {
            let mut line = String::new();
            for x in buffer.area.x..buffer.area.x + buffer.area.width {
                if let Some(cell) = buffer.cell((x, y)) {
                    line.push_str(cell.symbol());
                }
            }
            out.push_str(line.trim_end());
            out.push('\n');
        }
        out
    }

    #[test]
    fn state_keys_navigate_and_quit() {
        let mut app = App::new(
            StartScreen::Now,
            Period::Week,
            GroupBy::Model,
            false,
            render_options(RenderMode::Braille, false),
        );

        assert_eq!(app.screen, Screen::Now);
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Trends);
        assert_eq!(app.handle_key(key(KeyCode::Char('m'))), AppAction::Continue);
        assert_eq!(app.period, Period::Month);
        assert_eq!(app.handle_key(key(KeyCode::Char('g'))), AppAction::Continue);
        assert_eq!(app.group_by, GroupBy::App);
        assert_eq!(app.handle_key(key(KeyCode::Char('r'))), AppAction::Refresh);
        assert_eq!(app.handle_key(ctrl_c()), AppAction::Quit);
        assert_eq!(app.handle_key(key(KeyCode::Char('q'))), AppAction::Quit);
    }

    #[test]
    fn filter_and_frontier_navigation_state() {
        let mut app = app_with_snapshot(StartScreen::Trends, RenderMode::Braille);

        assert_eq!(app.handle_key(key(KeyCode::Char('/'))), AppAction::Continue);
        assert!(app.filter_editing);
        assert_eq!(app.handle_key(key(KeyCode::Char('o'))), AppAction::Continue);
        assert_eq!(app.handle_key(key(KeyCode::Char('p'))), AppAction::Continue);
        assert_eq!(app.handle_key(key(KeyCode::Enter)), AppAction::Continue);
        assert_eq!(app.filter, "op");
        assert!(!app.filter_editing);

        // `a` opens the frontier from trends; esc returns there. No network/LLM.
        assert_eq!(app.handle_key(key(KeyCode::Char('a'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Frontier);
        assert!(app
            .status
            .as_deref()
            .unwrap_or_default()
            .contains("no network"));
        assert_eq!(app.handle_key(key(KeyCode::Esc)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Trends);
    }

    #[test]
    fn a_opens_frontier_and_n_or_esc_returns_to_origin() {
        let mut app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);
        assert_eq!(app.handle_key(key(KeyCode::Char('a'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Frontier);
        // `a` is inert once on the frontier (no re-entry).
        assert_eq!(app.handle_key(key(KeyCode::Char('n'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Now);
    }

    #[test]
    fn numbered_keys_and_tab_cycle_reach_providers_and_models() {
        let mut app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);
        assert_eq!(app.screen, Screen::Now);

        // Numbered jumps go straight to a tab — including `4` (Models, appended in T12).
        assert_eq!(app.handle_key(key(KeyCode::Char('2'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Trends);
        assert_eq!(app.handle_key(key(KeyCode::Char('3'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Providers);
        assert_eq!(app.handle_key(key(KeyCode::Char('4'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Models);
        assert_eq!(app.handle_key(key(KeyCode::Char('1'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Now);
        // A still-reserved digit (no tab there yet) is inert.
        assert_eq!(app.handle_key(key(KeyCode::Char('5'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Now);

        // Tab cycles forward, wrapping Now -> Trends -> Providers -> Models -> Now.
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Trends);
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Providers);
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Models);
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Now);

        // BackTab cycles in reverse (Now -> Models).
        assert_eq!(app.handle_key(key(KeyCode::BackTab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Models);

        // Frontier is outside the cycle (an a/esc overlay); Tab returns to the first tab.
        assert_eq!(app.handle_key(key(KeyCode::Char('a'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Frontier);
        assert_eq!(app.handle_key(key(KeyCode::Tab)), AppAction::Continue);
        assert_eq!(app.screen, Screen::Now);
    }

    #[test]
    fn frame_snapshot_providers_surface() {
        let mut app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);
        assert_eq!(app.handle_key(key(KeyCode::Char('3'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Providers);
        let frame = frame_to_string(&app, 90, 26);

        assert!(frame.contains("claude code"));
        assert!(frame.contains("codex"));
        assert!(frame.contains("cursor"));
        assert!(frame.contains("api cost"));
        assert!(frame.contains("model mix"));
        assert!(frame.contains("from the statusLine capture"));
        // Cursor's both unavailable lanes render honestly — never "coming soon".
        assert!(frame.contains("no sanctioned source"));
        assert!(!frame.contains("coming soon"));
        // Connect is off in the default build: no connection lane.
        assert!(!frame.contains("connections (your own usage API keys)"));
    }

    #[test]
    fn frame_snapshot_models_surface() {
        let mut app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);
        assert_eq!(app.handle_key(key(KeyCode::Char('4'))), AppAction::Continue);
        assert_eq!(app.screen, Screen::Models);
        let frame = frame_to_string(&app, 90, 26);

        assert!(frame.contains("models"));
        // The two API-billed sample models, with the always-estimated spend + token mix.
        assert!(frame.contains("claude-opus-4.7"));
        assert!(frame.contains("gpt-5.5"));
        assert!(frame.contains("spent ~$"));
        assert!(frame.contains("tokens:"));
        assert!(frame.contains("frontier:"));
        // The cost-only hedge note is footnoted (an estimate, never an authoritative bill).
        assert!(frame.contains("cost-only comparison at equal token volume"));
    }

    #[test]
    fn live_cadence_recollects_every_two_seconds() {
        let now = utc(2026, 6, 2, 9, 0);
        let mut app = App::new(
            StartScreen::Now,
            Period::Week,
            GroupBy::Model,
            true,
            render_options(RenderMode::Braille, false),
        );
        app.loading = false;

        assert!(app.should_auto_collect(now));
        app.last_collect_at = Some(now);
        assert!(!app.should_auto_collect(now + Duration::seconds(1)));
        assert!(app.should_auto_collect(now + Duration::seconds(2)));
    }

    #[test]
    fn collector_seam_refreshes_without_live_logs() {
        struct FakeCollector {
            calls: usize,
            snapshot: EngineSnapshot,
        }

        impl SnapshotCollector for FakeCollector {
            fn collect(&mut self, _env: &HostEnv) -> Result<EngineSnapshot> {
                self.calls += 1;
                Ok(self.snapshot.clone())
            }
        }

        let now = utc(2026, 6, 2, 9, 0);
        let mut collector = FakeCollector {
            calls: 0,
            snapshot: sample_snapshot(now),
        };
        let mut app = App::new(
            StartScreen::Now,
            Period::Week,
            GroupBy::Model,
            false,
            render_options(RenderMode::Braille, false),
        );

        app.refresh(&mut collector, &test_env(), now);

        assert_eq!(collector.calls, 1);
        assert!(!app.loading);
        assert!(app.snapshot.is_some());
    }

    #[test]
    fn document_regenerates_for_frame_width() {
        let now = utc(2026, 6, 2, 9, 0);
        let app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);

        let narrow = app.document_for_width(64, now).render(app.render_options);
        let wide = app
            .document_for_width(80, now)
            .render(app.render_options.with_width(80));

        assert!(narrow.contains(&"─".repeat(64)));
        assert!(wide.contains(&"─".repeat(80)));
    }

    #[test]
    fn frame_snapshot_now_and_warning_state() {
        let app = app_with_snapshot(StartScreen::Now, RenderMode::Braille);
        let frame = frame_to_string(&app, 86, 18);

        assert!(frame.contains("C⠉ costroid"));
        assert!(frame.contains("limits"));
        assert!(frame.contains("api costs"));
        assert!(frame.contains("92% !"));
        assert!(frame.contains("provider cursor missing"));
    }

    #[test]
    fn frame_snapshot_trends_help_and_filter() {
        let mut app = app_with_snapshot(StartScreen::Trends, RenderMode::Braille);
        app.filter = "opus".to_string();
        let frame = frame_to_string(&app, 90, 20);

        assert!(frame.contains("claude opus"));
        assert!(!frame.contains("gpt 5.5"));

        app.help_open = true;
        let help_frame = frame_to_string(&app, 90, 20);
        assert!(help_frame.contains("help"));
        assert!(help_frame.contains("frontier"));
    }

    #[test]
    fn frame_snapshot_frontier_surface() {
        let app = app_with_snapshot(StartScreen::Frontier, RenderMode::Braille);
        let frame = frame_to_string(&app, 90, 26);

        assert!(frame.contains("cost vs quality"));
        assert!(frame.contains("DeepSWE"));
        assert!(frame.contains("dominated by gpt-5.5"));
    }

    #[test]
    fn frame_snapshot_loading_empty_and_ascii() {
        let now = utc(2026, 6, 2, 9, 0);
        let loading = App::new(
            StartScreen::Now,
            Period::Week,
            GroupBy::Model,
            false,
            render_options(RenderMode::Ascii, false),
        );
        let loading_frame = frame_to_string(&loading, 72, 8);
        assert!(loading_frame.contains("reading local provider logs"));
        assert!(loading_frame.contains("|"));

        let mut empty = App::new(
            StartScreen::Now,
            Period::Week,
            GroupBy::Model,
            false,
            render_options(RenderMode::Ascii, false),
        );
        empty.loading = false;
        empty.snapshot = Some(empty_snapshot(now));
        let empty_frame = frame_to_string(&empty, 90, 18);
        assert!(empty_frame.contains("no providers detected"));
        assert!(empty_frame.contains("under WSL"));

        let ascii = app_with_snapshot(StartScreen::Now, RenderMode::Ascii);
        let ascii_frame = frame_to_string(&ascii, 90, 18);
        assert!(ascii_frame.contains("["));
    }

    #[test]
    fn ratatui_style_mapping_honors_no_color() {
        let colored = ratatui_style(
            SemanticStyle::Warn,
            render_options(RenderMode::Braille, true),
        );
        let no_color = ratatui_style(
            SemanticStyle::Warn,
            render_options(RenderMode::Braille, false),
        );

        assert_eq!(colored.fg, Some(Color::Yellow));
        assert!(colored.add_modifier.contains(Modifier::BOLD));
        assert_eq!(no_color, RatatuiStyle::default());
    }

    #[test]
    fn restore_sequences_leave_alt_screen_and_show_cursor() {
        let mut bytes = Vec::new();

        match write_restore_sequences(&mut bytes) {
            Ok(()) => {}
            Err(error) => panic!("restore sequences should write to buffer: {error}"),
        }

        let output = String::from_utf8_lossy(&bytes);
        assert!(output.contains("\x1b[?1049l"));
        assert!(output.contains("\x1b[?25h"));
    }

    #[test]
    fn styled_document_to_text_preserves_semantic_spans() {
        let mut document = StyledDocument::new();
        let mut line = StyledLine::new();
        line.push_plain("plain ");
        line.push_styled("warn", SemanticStyle::Warn);
        document.push(line);

        let text = styled_document_to_text(&document, render_options(RenderMode::Braille, true));

        assert_eq!(text.lines.len(), 1);
        assert_eq!(text.lines[0].spans.len(), 2);
        assert_eq!(text.lines[0].spans[1].style.fg, Some(Color::Yellow));
    }

    #[test]
    fn filter_keeps_matching_lanes_only() {
        let now = utc(2026, 6, 2, 9, 0);
        let mut summary = costroid_core::trends_summary(
            &sample_snapshot(now),
            TrendsOptions {
                period: Period::Week,
                group_by: GroupBy::Model,
            },
        );

        apply_trends_filter(&mut summary, "opus");

        assert!(summary
            .totals
            .iter()
            .all(|row| row.group.value.contains("opus") || row.lane != CostLane::Api));
        assert!(summary
            .buckets
            .iter()
            .all(|bucket| bucket.group.value.contains("opus")));
    }
}

/// Connect-gated tests for the Providers-tab connection lane (`connection_entries` +
/// `format_org_label`), driven over a MOCK OS keychain + a temp registry — zero real
/// keychain, zero network. Compiled only under `--features connect-test-support` (the same
/// tier as the T10a Layer-1 connect-action tests in `src/connect.rs`).
#[cfg(all(test, feature = "connect-test-support"))]
mod connection_lane_tests {
    use super::{connection_entries, format_org_label};
    use crate::render::ConnectionState;
    use costroid_connect::test_support::install_mock_keychain;
    use costroid_connect::{
        ApiVendor, ConnectionRegistry, CredentialStore, OrgLabel, SecretString,
    };

    // The workspace clippy lints deny `.unwrap()`/`.expect()` even in tests.
    #[track_caller]
    fn okv<T, E: std::fmt::Debug>(result: Result<T, E>) -> T {
        match result {
            Ok(value) => value,
            Err(err) => panic!("expected Ok, got Err: {err:?}"),
        }
    }

    /// An auto-cleaned temp dir for the registry file (no `tempfile` dep, to keep the
    /// forbidden-crates graph clean), mirroring `connect.rs`'s own test helper.
    struct TempDir {
        path: std::path::PathBuf,
    }
    impl TempDir {
        fn new(tag: &str) -> Self {
            static COUNTER: std::sync::atomic::AtomicU32 = std::sync::atomic::AtomicU32::new(0);
            let n = COUNTER.fetch_add(1, std::sync::atomic::Ordering::Relaxed);
            let path =
                std::env::temp_dir().join(format!("costroid-t11-{tag}-{}-{n}", std::process::id()));
            let _ = std::fs::remove_dir_all(&path);
            okv(std::fs::create_dir_all(&path));
            Self { path }
        }
    }
    impl Drop for TempDir {
        fn drop(&mut self) {
            let _ = std::fs::remove_dir_all(&self.path);
        }
    }

    #[test]
    fn connection_lane_reflects_the_dual_gate_label_and_gemini_message() {
        install_mock_keychain();
        let store = okv(CredentialStore::new());
        let dir = TempDir::new("conn-lane");
        let registry = ConnectionRegistry::at(dir.path.join("connections.json"));

        // Anthropic: key in the keychain AND registry-connected with a label → Connected{org}.
        okv(store.store(
            ApiVendor::Anthropic,
            &SecretString::from("sk-ant-admin-FAKE-T11".to_string()),
        ));
        okv(registry.mark_connected_with_label(
            ApiVendor::Anthropic,
            Some(OrgLabel {
                name: "Acme".to_string(),
                id: Some("org-123".to_string()),
            }),
        ));
        // OpenAI: marked connected in the registry but NO key in the keychain → the dual gate
        // must still report NotConnected (the keychain is the source of truth, not the mark).
        okv(registry.mark_connected(ApiVendor::OpenAI));

        let entries = connection_entries(&store, &registry);
        assert_eq!(entries.len(), 3, "one entry per vendor (ApiVendor::ALL)");

        // Order follows ApiVendor::ALL: anthropic, openai, gemini.
        assert_eq!(entries[0].vendor, "anthropic");
        assert_eq!(
            entries[0].state,
            ConnectionState::Connected {
                org: Some("Acme (org-123)".to_string())
            }
        );

        assert_eq!(entries[1].vendor, "openai");
        assert_eq!(
            entries[1].state,
            ConnectionState::NotConnected,
            "registry-connected but no key → NotConnected (keychain is source of truth)"
        );

        assert_eq!(entries[2].vendor, "gemini");
        assert_eq!(
            entries[2].state,
            ConnectionState::Unavailable(costroid_core::GEMINI_UNAVAILABLE_MESSAGE.to_string()),
            "gemini reuses the pinned message verbatim, never a network call"
        );
    }

    #[test]
    fn format_org_label_renders_name_with_and_without_id() {
        assert_eq!(
            format_org_label(OrgLabel {
                name: "Acme".to_string(),
                id: Some("org-123".to_string()),
            }),
            "Acme (org-123)"
        );
        assert_eq!(
            format_org_label(OrgLabel {
                name: "Acme".to_string(),
                id: None,
            }),
            "Acme"
        );
    }
}
