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
    render_now_document, render_trends_document, RenderOptions, SemanticStyle, StyledDocument,
    StyledLine,
};

const SPINNER_FRAMES: [char; 10] = ['⠋', '⠙', '⠹', '⠸', '⠼', '⠴', '⠦', '⠧', '⠇', '⠏'];
const ASCII_SPINNER_FRAMES: [char; 4] = ['|', '/', '-', '\\'];
const REDRAW_TICK: StdDuration = StdDuration::from_millis(80);
const COLLECT_INTERVAL: Duration = Duration::seconds(2);

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum StartScreen {
    Now,
    Trends,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Screen {
    Now,
    Trends,
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
}

impl App {
    fn new(
        start_screen: StartScreen,
        period: Period,
        group_by: GroupBy,
        live: bool,
        render_options: RenderOptions,
    ) -> Self {
        Self {
            screen: match start_screen {
                StartScreen::Now => Screen::Now,
                StartScreen::Trends => Screen::Trends,
            },
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
                self.screen = match self.screen {
                    Screen::Now => Screen::Trends,
                    Screen::Trends => Screen::Now,
                };
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
            KeyCode::Char('a') => {
                self.status = Some(
                    "ask/recommendations arrive in Phase 4; no network or LLM call was made"
                        .to_string(),
                );
                AppAction::Continue
            }
            KeyCode::Esc => {
                self.help_open = false;
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
                }
            }
            None => loading_document(self, options),
        }
    }

    fn footer(&self) -> String {
        let left = match self.screen {
            Screen::Now => "now",
            Screen::Trends => "trends",
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
        format!("{left} | {live} | tab switch | r refresh | ? help | q quit{filter}{status}")
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
    let popup = centered_rect(74, 12, area);
    let lines = vec![
        Line::from("d/w/m/y  set trends period"),
        Line::from("g        cycle trends group"),
        Line::from("tab      switch now/trends"),
        Line::from("f or /   filter model/app rows"),
        Line::from("r        refresh local logs"),
        Line::from("a        Phase 4 ask/recommendations note"),
        Line::from("?        close help"),
        Line::from("q/Ctrl-C quit"),
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
    use costroid_core::{CostLane, ProviderStatus, ProviderStatusKind};
    use costroid_focus::{
        FocusAccessPath, FocusRecord, TokenType, UnpricedUsage, DEFAULT_BILLING_CURRENCY,
    };
    use costroid_providers::{LimitKind, LimitWindow, ProviderId};
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
        HostEnv::new(PathBuf::from("/tmp/costroid-test"), None, false)
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
                used_fraction: Some(0.92),
                resets_at: Some(now + Duration::hours(2)),
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
        }
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
    fn filter_and_phase_four_stub_are_visible_state() {
        let mut app = app_with_snapshot(StartScreen::Trends, RenderMode::Braille);

        assert_eq!(app.handle_key(key(KeyCode::Char('/'))), AppAction::Continue);
        assert!(app.filter_editing);
        assert_eq!(app.handle_key(key(KeyCode::Char('o'))), AppAction::Continue);
        assert_eq!(app.handle_key(key(KeyCode::Char('p'))), AppAction::Continue);
        assert_eq!(app.handle_key(key(KeyCode::Enter)), AppAction::Continue);
        assert_eq!(app.filter, "op");
        assert!(!app.filter_editing);

        assert_eq!(app.handle_key(key(KeyCode::Char('a'))), AppAction::Continue);
        assert!(app
            .status
            .as_deref()
            .unwrap_or_default()
            .contains("Phase 4"));
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
        assert!(help_frame.contains("Phase 4"));
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
