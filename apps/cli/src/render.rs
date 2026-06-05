use std::collections::BTreeMap;
use std::env;
use std::io::{self, IsTerminal};

use chrono::{DateTime, Local};
use costroid_core::{
    AggregateTotals, BenchFrontier, BenchView, CostLane, CostLaneSummary, FrontierPoint,
    FrontierStanding, GroupBy, LimitAvailability, LimitSummary, NowSummary, OverlayModel, Period,
    PeriodRange, ProviderStatusKind, RepricingDelta, RepricingStatus, TrendsSummary,
};
use costroid_providers::{LimitKind, LimitMeasure, ProviderId};
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;

const COST_BAR_WIDTH: usize = 12;
const DEFAULT_RENDER_WIDTH: usize = 64;
const LIMIT_BAR_WIDTH: usize = 12;
const STATUS_BAR_WIDTH: usize = 4;
const WARN_FRACTION: f64 = 0.80;
const CRITICAL_FRACTION: f64 = 0.95;

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum RenderMode {
    Braille,
    Ascii,
    Plain,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) struct RenderOptions {
    pub mode: RenderMode,
    pub ansi: bool,
    pub width: usize,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub(crate) enum SemanticStyle {
    Plain,
    Strong,
    Warn,
    Critical,
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum LimitState {
    Normal,
    Warn,
    Critical,
    Over,
}

impl RenderOptions {
    pub(crate) fn plain() -> Self {
        Self {
            mode: RenderMode::Plain,
            ansi: false,
            width: DEFAULT_RENDER_WIDTH,
        }
    }

    pub(crate) fn with_width(mut self, width: usize) -> Self {
        self.width = width.max(1);
        self
    }

    #[cfg(test)]
    fn braille(ansi: bool) -> Self {
        Self {
            mode: RenderMode::Braille,
            ansi,
            width: DEFAULT_RENDER_WIDTH,
        }
    }

    #[cfg(test)]
    fn ascii(ansi: bool) -> Self {
        Self {
            mode: RenderMode::Ascii,
            ansi,
            width: DEFAULT_RENDER_WIDTH,
        }
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct StyledDocument {
    pub(crate) lines: Vec<StyledLine>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct StyledLine {
    pub(crate) spans: Vec<StyledSpan>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct StyledSpan {
    pub(crate) content: String,
    pub(crate) style: SemanticStyle,
}

impl StyledDocument {
    pub(crate) fn new() -> Self {
        Self { lines: Vec::new() }
    }

    pub(crate) fn push(&mut self, line: StyledLine) {
        self.lines.push(line);
    }

    pub(crate) fn render(&self, options: RenderOptions) -> String {
        let mut out = String::new();
        for line in &self.lines {
            out.push_str(&line.render(options));
            out.push('\n');
        }
        out
    }
}

impl StyledLine {
    pub(crate) fn new() -> Self {
        Self { spans: Vec::new() }
    }

    pub(crate) fn plain(content: impl Into<String>) -> Self {
        Self {
            spans: vec![StyledSpan::plain(content)],
        }
    }

    pub(crate) fn push_plain(&mut self, content: impl Into<String>) {
        self.spans.push(StyledSpan::plain(content));
    }

    pub(crate) fn push_styled(&mut self, content: impl Into<String>, style: SemanticStyle) {
        self.spans.push(StyledSpan {
            content: content.into(),
            style,
        });
    }

    pub(crate) fn render(&self, options: RenderOptions) -> String {
        let mut out = String::new();
        for span in &self.spans {
            out.push_str(&span.render(options));
        }
        out
    }
}

impl StyledSpan {
    pub(crate) fn plain(content: impl Into<String>) -> Self {
        Self {
            content: content.into(),
            style: SemanticStyle::Plain,
        }
    }

    fn styled(content: impl Into<String>, style: SemanticStyle) -> Self {
        Self {
            content: content.into(),
            style,
        }
    }

    fn render(&self, options: RenderOptions) -> String {
        if !options.ansi || options.mode == RenderMode::Plain || self.style == SemanticStyle::Plain
        {
            return self.content.clone();
        }
        let code = match self.style {
            SemanticStyle::Plain => return self.content.clone(),
            SemanticStyle::Strong => "1",
            SemanticStyle::Warn => "33;1",
            SemanticStyle::Critical => "31;1",
        };
        format!("\x1b[{code}m{}\x1b[0m", self.content)
    }
}

#[derive(Debug, Clone, PartialEq, Eq)]
struct EnvSnapshot {
    term: Option<String>,
    lang: Option<String>,
    lc_all: Option<String>,
    lc_ctype: Option<String>,
    no_color: Option<String>,
}

impl EnvSnapshot {
    fn current() -> Self {
        Self {
            term: env::var("TERM").ok(),
            lang: env::var("LANG").ok(),
            lc_all: env::var("LC_ALL").ok(),
            lc_ctype: env::var("LC_CTYPE").ok(),
            no_color: env::var("NO_COLOR").ok(),
        }
    }
}

pub(crate) fn detect_render_options(plain: bool) -> RenderOptions {
    select_render_options(plain, io::stdout().is_terminal(), &EnvSnapshot::current())
}

fn select_render_options(plain: bool, stdout_is_tty: bool, env: &EnvSnapshot) -> RenderOptions {
    if plain || !stdout_is_tty {
        return RenderOptions::plain();
    }

    let no_color = env
        .no_color
        .as_deref()
        .map(str::trim)
        .is_some_and(|value| !value.is_empty());
    let mode = if braille_capable(env) {
        RenderMode::Braille
    } else {
        RenderMode::Ascii
    };

    RenderOptions {
        mode,
        ansi: !no_color,
        width: DEFAULT_RENDER_WIDTH,
    }
}

fn braille_capable(env: &EnvSnapshot) -> bool {
    if env
        .term
        .as_deref()
        .map(str::trim)
        .filter(|value| !value.is_empty())
        .map(|term| term.eq_ignore_ascii_case("dumb"))
        .unwrap_or(true)
    {
        return false;
    }

    match locale_value(env) {
        Some(locale) => {
            let locale = locale.to_ascii_uppercase();
            locale.contains("UTF-8") || locale.contains("UTF8")
        }
        None => true,
    }
}

fn locale_value(env: &EnvSnapshot) -> Option<&str> {
    [&env.lc_all, &env.lc_ctype, &env.lang]
        .into_iter()
        .filter_map(Option::as_deref)
        .map(str::trim)
        .find(|value| !value.is_empty())
}

pub(crate) fn render_now(summary: &NowSummary, options: RenderOptions) -> String {
    render_now_document(summary, options).render(options)
}

pub(crate) fn render_trends(summary: &TrendsSummary, options: RenderOptions) -> String {
    render_trends_document(summary, options).render(options)
}

pub(crate) fn render_frontier(view: &BenchView, options: RenderOptions) -> String {
    render_frontier_document(view, options).render(options)
}

pub(crate) fn render_frontier_document(view: &BenchView, options: RenderOptions) -> StyledDocument {
    match options.mode {
        RenderMode::Plain => render_frontier_plain_document(view),
        RenderMode::Braille | RenderMode::Ascii => render_frontier_visual_document(view, options),
    }
}

// ---------------------------------------------------------------------------
// Frontier surface — the cost-vs-quality scatter. Monochrome (Plain/Strong only;
// never Warn — amber is reserved for the near-limit state). Informs, never prescribes.
// ---------------------------------------------------------------------------

const SCATTER_HEIGHT: usize = 6;
/// Braille 2x4 dot layout: `[row][col]` → dot number (left col 1,2,3,7; right 4,5,6,8).
const SCATTER_DOT_AT: [[u8; 2]; 4] = [[1, 4], [2, 5], [3, 6], [7, 8]];

/// A single benchmark point in plot space. The rasterizer is pure geometry — it knows
/// nothing about models, names, or money.
struct PlotPoint {
    x: f64,
    y: f64,
    on_frontier: bool,
}

fn render_frontier_visual_document(view: &BenchView, options: RenderOptions) -> StyledDocument {
    let mut out = StyledDocument::new();
    push_header_line(
        &mut out,
        mark(options),
        "cost vs quality",
        format_money(&total_overlay_spend(view), Some("USD"), true),
        options,
    );

    for frontier in &view.frontiers {
        push_rule(&mut out, options);
        push_line(
            &mut out,
            &format!(
                "{} — {}   as of {}",
                frontier.name,
                role_label(&frontier.role),
                frontier.as_of
            ),
        );
        push_line(&mut out, &format!("  {}", frontier.cost_note));
        push_line(&mut out, &format!("  source: {}", frontier.source));
        let points = plot_points(frontier);
        let width = scatter_width(options);
        for row in scatter_rows(&points, width, SCATTER_HEIGHT, options) {
            push_line(&mut out, &format!("  {row}"));
        }
        push_line(&mut out, "  x: cost/task ->   y: score (high = top)");
        for point in &frontier.points {
            push_line(&mut out, &format!("  {}", point_line(point)));
        }
    }

    push_rule(&mut out, options);
    push_line(&mut out, "your models (API-billed):");
    if view.no_api_usage || view.overlay.is_empty() {
        push_line(&mut out, "  no API-billed usage to compare");
    } else {
        for model in &view.overlay {
            out.push(overlay_line(model));
        }
    }

    push_rule(&mut out, options);
    push_line(&mut out, &frontier_insight_line(view));
    push_provider_notes(&mut out, &view.providers);
    push_empty_provider_guidance(&mut out, &view.providers);
    out
}

fn render_frontier_plain_document(view: &BenchView) -> StyledDocument {
    let mut out = StyledDocument::new();
    push_line(&mut out, "costroid frontier");
    for frontier in &view.frontiers {
        push_line(
            &mut out,
            &format!(
                "{} — {}, source {}, as of {}",
                frontier.name,
                role_label(&frontier.role),
                frontier.source,
                frontier.as_of
            ),
        );
        push_line(&mut out, &format!("  caveat: {}", frontier.cost_note));
        for point in &frontier.points {
            push_line(&mut out, &format!("  {}", plain_point_line(point, view)));
        }
    }
    if view.no_api_usage || view.overlay.is_empty() {
        push_line(&mut out, "your models: no API-billed usage to compare");
    } else {
        push_line(&mut out, "your models (API-billed):");
        for model in &view.overlay {
            push_line(&mut out, &format!("  {}", plain_overlay_line(model)));
        }
    }
    push_line(&mut out, &plain_frontier_insight_line(view));
    push_provider_notes(&mut out, &view.providers);
    push_empty_provider_guidance(&mut out, &view.providers);
    out
}

fn total_overlay_spend(view: &BenchView) -> Decimal {
    view.overlay
        .iter()
        .fold(Decimal::ZERO, |total, model| total + model.billed_cost)
}

fn role_label(role: &str) -> String {
    match role {
        "primary" => "primary (neutral)".to_string(),
        "corroborating" => "corroborating (vendor)".to_string(),
        other => other.to_string(),
    }
}

fn plot_points(frontier: &BenchFrontier) -> Vec<PlotPoint> {
    frontier
        .points
        .iter()
        .filter_map(|point| {
            let cost = point.cost_per_task_usd?;
            Some(PlotPoint {
                x: cost.to_f64().unwrap_or(0.0),
                y: point.score_pct.to_f64().unwrap_or(0.0),
                on_frontier: point.standing == FrontierStanding::OnFrontier,
            })
        })
        .collect()
}

fn point_line(point: &FrontierPoint) -> String {
    let cost = match point.cost_per_task_usd {
        Some(value) => format!("@ {}", format_money(&value, Some("USD"), true)),
        None => "@ cost n/a".to_string(),
    };
    let standing = match &point.standing {
        FrontierStanding::OnFrontier => "on frontier".to_string(),
        FrontierStanding::Dominated { by } => format!("off (dominated by {by})"),
        FrontierStanding::CostUnknown => "score only".to_string(),
    };
    let note = point
        .note
        .as_deref()
        .map(|note| format!("  — {note}"))
        .unwrap_or_default();
    format!(
        "{}  {}% {}  {}{}",
        point.label,
        score_text(point),
        cost,
        standing,
        note
    )
}

fn plain_point_line(point: &FrontierPoint, view: &BenchView) -> String {
    let cost = match point.cost_per_task_usd {
        Some(value) => format!("{}/task", format_money(&value, Some("USD"), true)),
        None => "n/a".to_string(),
    };
    let standing = match &point.standing {
        FrontierStanding::OnFrontier => "yes".to_string(),
        FrontierStanding::Dominated { by } => format!("no (dominated by {by})"),
        FrontierStanding::CostUnknown => "n/a (no published cost)".to_string(),
    };
    let spend = view
        .overlay
        .iter()
        .find(|model| model.model_id == point.model_id)
        .map(|model| format_money(&model.billed_cost, Some("USD"), true))
        .unwrap_or_else(|| "none".to_string());
    let note = point
        .note
        .as_deref()
        .map(|note| format!(" — {note}"))
        .unwrap_or_default();
    format!(
        "{}: score {}%, cost {}, on frontier: {}, your API spend: {}{}",
        point.label,
        score_text(point),
        cost,
        standing,
        spend,
        note
    )
}

fn score_text(point: &FrontierPoint) -> String {
    point.score_pct.normalize().to_string()
}

fn overlay_line(model: &OverlayModel) -> StyledLine {
    let mut line = StyledLine::new();
    line.push_plain(format!("  {}  spent ", model.model_id));
    line.push_styled(
        format_money(&model.billed_cost, Some("USD"), true),
        SemanticStyle::Strong,
    );
    if let Some(delta) = best_delta(model) {
        let phrase = delta_phrase(delta);
        if !phrase.is_empty() {
            line.push_plain(phrase);
        }
    }
    line
}

fn plain_overlay_line(model: &OverlayModel) -> String {
    let mut line = format!(
        "{}: spent {}",
        model.model_id,
        format_money(&model.billed_cost, Some("USD"), true)
    );
    if let Some(delta) = best_delta(model) {
        line.push_str(&delta_phrase(delta));
    }
    line
}

/// The most-favorable computed re-pricing comparison (largest saving), if any.
fn best_delta(model: &OverlayModel) -> Option<&RepricingDelta> {
    model
        .repricing
        .iter()
        .filter(|delta| delta.status == RepricingStatus::Computed)
        .min_by(|left, right| left.delta_usd.cmp(&right.delta_usd))
}

/// Cost-only, equal-volume phrasing. INFORM, never PRESCRIBE — states a cost fact,
/// never "switch to X".
fn delta_phrase(delta: &RepricingDelta) -> String {
    let money = format_money(&delta.delta_usd.abs(), Some("USD"), true);
    if delta.delta_usd < Decimal::ZERO {
        format!(
            " · {} costs about {} less at equal volume",
            delta.target_model_id, money
        )
    } else if delta.delta_usd > Decimal::ZERO {
        format!(
            " · {} costs about {} more at equal volume",
            delta.target_model_id, money
        )
    } else {
        String::new()
    }
}

fn frontier_insight_line(view: &BenchView) -> String {
    if view.no_api_usage || view.overlay.is_empty() {
        return "◆ no API-billed usage to compare against the frontier. (estimated)".to_string();
    }
    let top = view
        .overlay
        .iter()
        .max_by(|left, right| left.billed_cost.cmp(&right.billed_cost));
    match top {
        Some(model) => {
            if let Some(delta) = best_delta(model) {
                if delta.delta_usd < Decimal::ZERO {
                    return format!(
                        "◆ {} drove most of your API spend; {} sits cheaper on the frontier at equal volume. (estimated)",
                        model.model_id, delta.target_model_id
                    );
                }
            }
            if model
                .appearances
                .iter()
                .any(|appearance| appearance.standing == FrontierStanding::OnFrontier)
            {
                format!(
                    "◆ {} drove most of your API spend and already sits on the frontier. (estimated)",
                    model.model_id
                )
            } else {
                format!(
                    "◆ {} drove most of your API spend. (estimated)",
                    model.model_id
                )
            }
        }
        None => "◆ no API-billed usage to compare against the frontier. (estimated)".to_string(),
    }
}

fn plain_frontier_insight_line(view: &BenchView) -> String {
    frontier_insight_line(view).replace('◆', "insight:")
}

fn scatter_width(options: RenderOptions) -> usize {
    options.width.saturating_sub(4).clamp(20, 40)
}

fn scatter_rows(
    points: &[PlotPoint],
    width: usize,
    height: usize,
    options: RenderOptions,
) -> Vec<String> {
    match options.mode {
        RenderMode::Braille => braille_scatter(points, width, height, true),
        _ => ascii_scatter(points, width, height),
    }
}

/// Hand-rasterized braille scatter (no Ratatui `Canvas`, consistent with the sparkline).
/// A `Vec<u8>` of `w*h` cells, one braille bitmask byte each; points set dots in a
/// `w*2 × h*4` dot grid (y inverted so high score is at top); frontier points are bold
/// full cells, optionally joined by a thin connector.
fn braille_scatter(
    points: &[PlotPoint],
    w_cells: usize,
    h_cells: usize,
    draw_line: bool,
) -> Vec<String> {
    let w = w_cells.max(1);
    let h = h_cells.max(1);
    if points.is_empty() {
        return vec![braille_blank().to_string().repeat(w); h];
    }
    let dot_w = w * 2;
    let dot_h = h * 4;
    let mut buf = vec![0_u8; w * h];
    let (x_min, x_max) = min_max(points.iter().map(|point| point.x));
    let (y_min, y_max) = min_max(points.iter().map(|point| point.y));

    for point in points {
        let dx = axis_index(point.x, x_min, x_max, dot_w, false);
        let dy = axis_index(point.y, y_min, y_max, dot_h, true);
        set_scatter_dot(&mut buf, w, h, dx, dy);
    }

    let mut frontier_dots: Vec<(usize, usize)> = points
        .iter()
        .filter(|point| point.on_frontier)
        .map(|point| {
            (
                axis_index(point.x, x_min, x_max, dot_w, false),
                axis_index(point.y, y_min, y_max, dot_h, true),
            )
        })
        .collect();
    frontier_dots.sort_by_key(|&(dx, _)| dx);
    for &(dx, dy) in &frontier_dots {
        let cx = (dx / 2).min(w - 1);
        let cy = (dy / 4).min(h - 1);
        buf[cy * w + cx] = 0xFF;
    }
    if draw_line {
        for pair in frontier_dots.windows(2) {
            scatter_line(&mut buf, w, h, pair[0], pair[1]);
        }
    }

    (0..h)
        .map(|cy| {
            (0..w)
                .map(|cx| byte_to_braille(buf[cy * w + cx]))
                .collect::<String>()
        })
        .collect()
}

fn ascii_scatter(points: &[PlotPoint], w_cells: usize, h_cells: usize) -> Vec<String> {
    let w = w_cells.max(1);
    let h = h_cells.max(1);
    let mut grid = vec![vec![' '; w]; h];
    if !points.is_empty() {
        let (x_min, x_max) = min_max(points.iter().map(|point| point.x));
        let (y_min, y_max) = min_max(points.iter().map(|point| point.y));
        for point in points {
            let cx = axis_index(point.x, x_min, x_max, w, false);
            let cy = axis_index(point.y, y_min, y_max, h, true);
            let glyph = if point.on_frontier { '#' } else { '.' };
            if !(grid[cy][cx] == '#' && glyph == '.') {
                grid[cy][cx] = glyph;
            }
        }
    }
    grid.into_iter()
        .map(|row| row.into_iter().collect())
        .collect()
}

fn set_scatter_dot(buf: &mut [u8], w: usize, h: usize, dx: usize, dy: usize) {
    let cx = (dx / 2).min(w - 1);
    let cy = (dy / 4).min(h - 1);
    let dot = SCATTER_DOT_AT[dy % 4][dx % 2];
    buf[cy * w + cx] |= bit_for_dot(dot);
}

fn scatter_line(buf: &mut [u8], w: usize, h: usize, a: (usize, usize), b: (usize, usize)) {
    let mut x0 = a.0 as isize;
    let mut y0 = a.1 as isize;
    let x1 = b.0 as isize;
    let y1 = b.1 as isize;
    let dx = (x1 - x0).abs();
    let dy = -(y1 - y0).abs();
    let sx = if x0 < x1 { 1 } else { -1 };
    let sy = if y0 < y1 { 1 } else { -1 };
    let mut err = dx + dy;
    loop {
        let px = usize::try_from(x0).unwrap_or(0);
        let py = usize::try_from(y0).unwrap_or(0);
        set_scatter_dot(buf, w, h, px, py);
        if x0 == x1 && y0 == y1 {
            break;
        }
        let e2 = 2 * err;
        if e2 >= dy {
            err += dy;
            x0 += sx;
        }
        if e2 <= dx {
            err += dx;
            y0 += sy;
        }
    }
}

fn min_max(values: impl Iterator<Item = f64>) -> (f64, f64) {
    let mut lo = f64::INFINITY;
    let mut hi = f64::NEG_INFINITY;
    for value in values {
        if value < lo {
            lo = value;
        }
        if value > hi {
            hi = value;
        }
    }
    if lo.is_finite() && hi.is_finite() {
        (lo, hi)
    } else {
        (0.0, 1.0)
    }
}

fn axis_index(value: f64, lo: f64, hi: f64, n: usize, invert: bool) -> usize {
    let max_index = n.saturating_sub(1) as f64;
    let fraction = if hi > lo {
        (value - lo) / (hi - lo)
    } else {
        0.5
    };
    let fraction = if invert { 1.0 - fraction } else { fraction };
    (fraction * max_index).round().clamp(0.0, max_index) as usize
}

pub(crate) fn render_statusline(summary: &NowSummary, options: RenderOptions) -> String {
    render_statusline_line(summary, options).render(options)
}

pub(crate) fn render_now_document(summary: &NowSummary, options: RenderOptions) -> StyledDocument {
    match options.mode {
        RenderMode::Plain => render_now_plain_document(summary),
        RenderMode::Braille | RenderMode::Ascii => render_now_visual_document(summary, options),
    }
}

pub(crate) fn render_trends_document(
    summary: &TrendsSummary,
    options: RenderOptions,
) -> StyledDocument {
    match options.mode {
        RenderMode::Plain => render_trends_plain_document(summary),
        RenderMode::Braille | RenderMode::Ascii => render_trends_visual_document(summary, options),
    }
}

fn render_statusline_line(summary: &NowSummary, options: RenderOptions) -> StyledLine {
    let api = sorted_lane_rows(&summary.current_costs, CostLane::Api);
    let api_total = sum_costs(&api);
    let spend = format_money(&api_total, Some("USD"), true);
    let api_state = if api.is_empty() { " no api" } else { "" };

    match options.mode {
        RenderMode::Plain => match most_constrained_limit(&summary.limits) {
            Some(limit) => StyledLine::plain(format!(
                "costroid {spend},{} {}",
                if api.is_empty() { " no API usage," } else { "" },
                plain_limit_phrase(limit)
            )),
            None => StyledLine::plain(format!(
                "costroid {spend}{}",
                if api.is_empty() { ", no API usage" } else { "" }
            )),
        },
        RenderMode::Braille | RenderMode::Ascii => match most_constrained_limit(&summary.limits) {
            Some(limit) => {
                let (fraction, reset) = limit_fraction_and_reset(limit);
                let pct = percent(fraction);
                let cue = state_cue(limit_state(fraction));
                let reset = reset
                    .map(|seconds| format!(" {}", compact_reset(seconds)))
                    .unwrap_or_default();
                let mut line = StyledLine::new();
                line.push_plain(format!("{} {spend}{api_state}  ", mark(options)));
                line.spans
                    .push(limit_meter_span(fraction, STATUS_BAR_WIDTH, options));
                line.push_plain(format!(" {pct}{cue}{reset}"));
                line
            }
            None => StyledLine::plain(format!("{} {spend}{api_state}", mark(options))),
        },
    }
}

fn render_now_visual_document(summary: &NowSummary, options: RenderOptions) -> StyledDocument {
    let mut out = StyledDocument::new();
    let api = sorted_lane_rows(&summary.current_costs, CostLane::Api);
    let api_total = sum_costs(&api);

    push_header_line(
        &mut out,
        mark(options),
        "this week",
        format_money(&api_total, Some("USD"), true),
        options,
    );
    push_rule(&mut out, options);
    push_line(&mut out, "limits");
    if summary.limits.is_empty() {
        push_line(&mut out, "  no local limit data found");
    } else {
        for limit in &summary.limits {
            out.push(render_limit_line(limit, options));
        }
    }
    push_rule(&mut out, options);
    push_line(&mut out, "api costs (this week)");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        push_cost_rows(&mut out, &api, options);
    }
    push_non_api_sections(&mut out, &summary.current_costs, options);
    push_rule(&mut out, options);
    push_line(&mut out, &insight_line(&api, &summary.limits));
    push_provider_notes(&mut out, &summary.providers);
    push_empty_provider_guidance(&mut out, &summary.providers);
    out
}

fn render_now_plain_document(summary: &NowSummary) -> StyledDocument {
    let mut out = StyledDocument::new();
    let api = sorted_lane_rows(&summary.current_costs, CostLane::Api);
    let api_total = sum_costs(&api);

    push_line(&mut out, "costroid now");
    push_line(
        &mut out,
        &format!(
            "period: this week, estimated API spend: {}",
            format_money(&api_total, Some("USD"), true)
        ),
    );
    push_line(&mut out, "limits:");
    if summary.limits.is_empty() {
        push_line(&mut out, "  no local limit data found");
    } else {
        for limit in &summary.limits {
            push_line(&mut out, &plain_limit_line(limit));
        }
    }
    push_line(&mut out, "api costs this week:");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        for row in &api {
            push_line(&mut out, &plain_cost_line(row));
        }
    }
    push_plain_non_api_sections(&mut out, &summary.current_costs);
    push_line(&mut out, &plain_insight_line(&api, &summary.limits));
    push_provider_notes(&mut out, &summary.providers);
    push_empty_provider_guidance(&mut out, &summary.providers);
    out
}

fn render_trends_visual_document(
    summary: &TrendsSummary,
    options: RenderOptions,
) -> StyledDocument {
    let mut out = StyledDocument::new();
    let api = sorted_lane_rows(&summary.totals, CostLane::Api);
    let api_total = sum_costs(&api);

    push_header_line(
        &mut out,
        mark(options),
        period_scope(summary.period),
        format_money(&api_total, Some("USD"), true),
        options,
    );
    push_line(
        &mut out,
        &format!(
            "  {}            group: {}",
            period_tabs(summary.period),
            group_tabs(summary.group_by)
        ),
    );
    push_rule(&mut out, options);
    push_line(
        &mut out,
        &format!("  api spend / {}", period_bucket_label(summary.period)),
    );
    let values = api_bucket_values(summary);
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        push_line(&mut out, &format!("  {}", sparkline(&values, options)));
        push_line(
            &mut out,
            &format!("  {}", sparkline_labels(summary.period, values.len())),
        );
    }
    push_rule(&mut out, options);
    push_line(&mut out, "breakdown");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        push_cost_rows(&mut out, &api, options);
    }
    push_non_api_sections(&mut out, &summary.totals, options);
    push_rule(&mut out, options);
    push_line(&mut out, &insight_line(&api, &[]));
    push_provider_notes(&mut out, &summary.providers);
    push_empty_provider_guidance(&mut out, &summary.providers);
    out
}

fn render_trends_plain_document(summary: &TrendsSummary) -> StyledDocument {
    let mut out = StyledDocument::new();
    let api = sorted_lane_rows(&summary.totals, CostLane::Api);
    let api_total = sum_costs(&api);

    push_line(&mut out, "costroid trends");
    push_line(
        &mut out,
        &format!(
            "period: {}, group: {}, estimated API spend: {}",
            period_name(summary.period),
            group_name(summary.group_by),
            format_money(&api_total, Some("USD"), true)
        ),
    );
    push_line(&mut out, "api spend buckets:");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        for (range, value) in plain_api_bucket_values(summary) {
            push_line(
                &mut out,
                &format!(
                    "  {}: {}",
                    format_bucket_start(&range),
                    format_money(&value, Some("USD"), true)
                ),
            );
        }
    }
    push_line(&mut out, "breakdown:");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        for row in &api {
            push_line(&mut out, &plain_cost_line(row));
        }
    }
    push_plain_non_api_sections(&mut out, &summary.totals);
    push_line(&mut out, &plain_insight_line(&api, &[]));
    push_provider_notes(&mut out, &summary.providers);
    push_empty_provider_guidance(&mut out, &summary.providers);
    out
}

fn push_non_api_sections(
    out: &mut StyledDocument,
    rows: &[CostLaneSummary],
    options: RenderOptions,
) {
    let subscription = sorted_lane_rows(rows, CostLane::SubscriptionEstimate);
    if !subscription.is_empty() {
        push_rule(out, options);
        push_line(
            out,
            "subscription API-equivalent value (estimate, not bill)",
        );
        push_cost_rows(out, &subscription, options);
    }

    let unknown = sorted_lane_rows(rows, CostLane::UnknownAccess);
    if !unknown.is_empty() {
        push_rule(out, options);
        push_line(out, "unknown-access usage (partial)");
        push_cost_rows(out, &unknown, options);
    }
}

fn push_plain_non_api_sections(out: &mut StyledDocument, rows: &[CostLaneSummary]) {
    let subscription = sorted_lane_rows(rows, CostLane::SubscriptionEstimate);
    if !subscription.is_empty() {
        push_line(out, "subscription API-equivalent value, estimate not bill:");
        for row in &subscription {
            push_line(out, &plain_cost_line(row));
        }
    }

    let unknown = sorted_lane_rows(rows, CostLane::UnknownAccess);
    if !unknown.is_empty() {
        push_line(out, "unknown-access usage, partial:");
        for row in &unknown {
            push_line(out, &plain_cost_line(row));
        }
    }
}

fn push_cost_rows(out: &mut StyledDocument, rows: &[CostLaneSummary], options: RenderOptions) {
    let max = rows
        .iter()
        .map(|row| row.totals.billed_cost)
        .max()
        .unwrap_or_default();
    for row in rows {
        let money = format_money(
            &row.totals.billed_cost,
            row.totals.currency.as_deref(),
            row.totals.estimated_rows > 0,
        );
        let rendered_money_len = StyledSpan::styled(money.clone(), SemanticStyle::Strong)
            .render(options)
            .len();
        let badge = pricing_badge(&row.totals);
        let mut line = StyledLine::new();
        line.push_plain(format!("  {:<18}  ", display_group(&row.group.value)));
        line.spans.push(cost_bar_span(
            row.totals.billed_cost,
            max,
            cost_bar_width(options),
            options,
        ));
        line.push_plain(format!(
            "  {}",
            " ".repeat(12_usize.saturating_sub(rendered_money_len))
        ));
        line.push_styled(money, SemanticStyle::Strong);
        line.push_plain(badge);
        out.push(line);
    }
}

fn plain_cost_line(row: &CostLaneSummary) -> String {
    format!(
        "  {}: {}, {} tokens{}",
        display_group(&row.group.value),
        format_money(
            &row.totals.billed_cost,
            row.totals.currency.as_deref(),
            row.totals.estimated_rows > 0
        ),
        row.totals.tokens.total(),
        pricing_badge_plain(&row.totals)
    )
}

fn sorted_lane_rows(rows: &[CostLaneSummary], lane: CostLane) -> Vec<CostLaneSummary> {
    let mut selected = rows
        .iter()
        .filter(|row| row.lane == lane)
        .cloned()
        .collect::<Vec<_>>();
    selected.sort_by(|left, right| {
        right
            .totals
            .billed_cost
            .cmp(&left.totals.billed_cost)
            .then_with(|| left.group.value.cmp(&right.group.value))
    });
    selected
}

fn sum_costs(rows: &[CostLaneSummary]) -> Decimal {
    rows.iter()
        .fold(Decimal::ZERO, |total, row| total + row.totals.billed_cost)
}

/// The token-fraction carried by a [`LimitMeasure`], if any. `Spend` measures have no
/// fraction (`None`) and fall to a placeholder line until T6 renders them properly.
fn measure_fraction(measure: &LimitMeasure) -> Option<f64> {
    match measure {
        LimitMeasure::TokenFraction(fraction) => Some(*fraction),
        LimitMeasure::Spend { .. } => None,
    }
}

/// T2 placeholder text for limit shapes T6 will format properly — `Spend` measures and
/// the new `Unverified`/`Estimated` availability arms. No producer emits these in T2,
/// so this never reaches a user; it exists only to keep the `match`es exhaustive and
/// the build green (PRODUCT-PLAN §11.5 D1). Real measure-aware rendering is T6's.
const LIMIT_RENDER_PENDING: &str = "limit detail pending";

fn render_limit_line(limit: &LimitSummary, options: RenderOptions) -> StyledLine {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    let pending = || StyledLine::plain(format!("  {tool:<12} {kind:<3} {LIMIT_RENDER_PENDING}"));
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure_fraction(measure) {
            Some(used_fraction) => {
                let cue = state_cue(limit_state(used_fraction));
                let mut line = StyledLine::new();
                line.push_plain(format!("  {tool:<12} {kind:<3} "));
                line.spans
                    .push(limit_meter_span(used_fraction, LIMIT_BAR_WIDTH, options));
                line.push_plain(format!(
                    "  {}{}  resets {}",
                    percent(used_fraction),
                    cue,
                    reset_countdown(*reset_in_seconds)
                ));
                line
            }
            None => pending(),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => match measure.as_ref().and_then(measure_fraction) {
            Some(fraction) => {
                let cue = state_cue(limit_state(fraction));
                let reset = reset_in_seconds
                    .map(reset_countdown)
                    .map(|value| format!(" resets {value}"))
                    .unwrap_or_default();
                let mut line = StyledLine::new();
                line.push_plain(format!("  {tool:<12} {kind:<3} "));
                line.spans
                    .push(limit_meter_span(fraction, LIMIT_BAR_WIDTH, options));
                line.push_plain(format!(
                    "  {}{}  partial: {}{}",
                    percent(fraction),
                    cue,
                    reason,
                    reset
                ));
                line
            }
            None => StyledLine::plain(format!("  {tool:<12} {kind:<3} partial: {reason}")),
        },
        // T6 renders these; T2 keeps the build green with a basic placeholder line.
        LimitAvailability::Unverified { .. } | LimitAvailability::Estimated { .. } => pending(),
        LimitAvailability::Unavailable { reason } => {
            StyledLine::plain(format!("  {tool:<12} {kind:<3} unavailable: {reason}"))
        }
    }
}

fn plain_limit_line(limit: &LimitSummary) -> String {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure_fraction(measure) {
            Some(used_fraction) => {
                let cue = plain_state_phrase(limit_state(used_fraction));
                format!(
                    "  {tool} {kind}: {} used{cue}, resets in {}",
                    percent(used_fraction),
                    reset_countdown(*reset_in_seconds)
                )
            }
            None => format!("  {tool} {kind}: {LIMIT_RENDER_PENDING}"),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = measure
                .as_ref()
                .and_then(measure_fraction)
                .map(|fraction| format!("{} used", percent(fraction)))
                .unwrap_or_else(|| "usage unknown".to_string());
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("  {tool} {kind}: partial, {usage}{reset}, {reason}")
        }
        // T6 renders these; T2 keeps the build green with a basic placeholder line.
        LimitAvailability::Unverified { .. } | LimitAvailability::Estimated { .. } => {
            format!("  {tool} {kind}: {LIMIT_RENDER_PENDING}")
        }
        LimitAvailability::Unavailable { reason } => {
            format!("  {tool} {kind}: unavailable, {reason}")
        }
    }
}

fn plain_limit_phrase(limit: &LimitSummary) -> String {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure_fraction(measure) {
            Some(used_fraction) => format!(
                "{tool} {kind} {} used, resets in {}",
                percent(used_fraction),
                compact_reset(*reset_in_seconds)
            ),
            None => format!("{tool} {kind} {LIMIT_RENDER_PENDING}"),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = measure
                .as_ref()
                .and_then(measure_fraction)
                .map(percent)
                .unwrap_or_else(|| "unknown usage".to_string());
            let reset = reset_in_seconds
                .map(compact_reset)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("{tool} {kind} partial, {usage}{reset}, {reason}")
        }
        // T6 renders these; T2 keeps the build green with a basic placeholder phrase.
        LimitAvailability::Unverified { .. } | LimitAvailability::Estimated { .. } => {
            format!("{tool} {kind} {LIMIT_RENDER_PENDING}")
        }
        LimitAvailability::Unavailable { reason } => {
            format!("{tool} {kind} unavailable, {reason}")
        }
    }
}

fn most_constrained_limit(limits: &[LimitSummary]) -> Option<&LimitSummary> {
    limits.iter().filter(has_fraction).max_by(|left, right| {
        let left_fraction = limit_fraction(left).unwrap_or(0.0);
        let right_fraction = limit_fraction(right).unwrap_or(0.0);
        left_fraction.total_cmp(&right_fraction)
    })
}

fn has_fraction(limit: &&LimitSummary) -> bool {
    limit_fraction(limit).is_some()
}

fn limit_fraction(limit: &LimitSummary) -> Option<f64> {
    match &limit.availability {
        LimitAvailability::Available { measure, .. } => measure_fraction(measure),
        LimitAvailability::Partial {
            measure: Some(measure),
            ..
        } => measure_fraction(measure),
        // The new Unverified/Estimated arms don't feed the "most constrained" pick in
        // T2 (no producer emits them yet); T6 decides how they surface.
        LimitAvailability::Partial { measure: None, .. }
        | LimitAvailability::Unverified { .. }
        | LimitAvailability::Estimated { .. }
        | LimitAvailability::Unavailable { .. } => None,
    }
}

fn limit_fraction_and_reset(limit: &LimitSummary) -> (f64, Option<i64>) {
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => (
            measure_fraction(measure).unwrap_or(0.0),
            Some(*reset_in_seconds),
        ),
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            ..
        } => (
            measure.as_ref().and_then(measure_fraction).unwrap_or(0.0),
            *reset_in_seconds,
        ),
        LimitAvailability::Unverified { .. }
        | LimitAvailability::Estimated { .. }
        | LimitAvailability::Unavailable { .. } => (0.0, None),
    }
}

fn limit_state(fraction: f64) -> LimitState {
    if fraction >= 1.0 {
        LimitState::Over
    } else if fraction >= CRITICAL_FRACTION {
        LimitState::Critical
    } else if fraction >= WARN_FRACTION {
        LimitState::Warn
    } else {
        LimitState::Normal
    }
}

fn state_cue(state: LimitState) -> &'static str {
    match state {
        LimitState::Normal => "",
        LimitState::Warn => " !",
        LimitState::Critical => " !!",
        LimitState::Over => " !! OVER",
    }
}

fn plain_state_phrase(state: LimitState) -> &'static str {
    match state {
        LimitState::Normal => "",
        LimitState::Warn => " (near limit)",
        LimitState::Critical => " (critical)",
        LimitState::Over => " (over limit)",
    }
}

#[cfg(test)]
fn limit_meter(fraction: f64, width: usize, options: RenderOptions) -> String {
    limit_meter_span(fraction, width, options).render(options)
}

fn limit_meter_span(fraction: f64, width: usize, options: RenderOptions) -> StyledSpan {
    let state = limit_state(fraction);
    let styled = match state {
        LimitState::Normal => SemanticStyle::Strong,
        LimitState::Warn => SemanticStyle::Warn,
        LimitState::Critical | LimitState::Over => SemanticStyle::Critical,
    };
    StyledSpan::styled(positional_meter_text(fraction, width, options), styled)
}

fn cost_bar_span(
    amount: Decimal,
    max: Decimal,
    width: usize,
    options: RenderOptions,
) -> StyledSpan {
    let fraction = if max > Decimal::ZERO {
        (amount / max).to_f64().unwrap_or(0.0)
    } else {
        0.0
    };
    StyledSpan::styled(
        positional_meter_text(fraction, width, options),
        SemanticStyle::Strong,
    )
}

fn positional_meter_text(fraction: f64, width: usize, options: RenderOptions) -> String {
    let segments = meter_segments(fraction, width);
    match options.mode {
        RenderMode::Plain => String::new(),
        RenderMode::Ascii => {
            let mut meter = String::with_capacity(width + 2);
            meter.push('[');
            meter.extend(std::iter::repeat_n('#', segments.full));
            if segments.partial {
                meter.push('+');
            }
            meter.extend(std::iter::repeat_n('-', segments.remaining));
            meter.push(']');
            meter
        }
        RenderMode::Braille => {
            let mut meter = String::with_capacity(width);
            meter.extend(std::iter::repeat_n(braille_full(), segments.full));
            if segments.partial {
                meter.push(braille_left_column());
            }
            meter.extend(std::iter::repeat_n(braille_light(), segments.remaining));
            meter
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
struct MeterSegments {
    full: usize,
    partial: bool,
    remaining: usize,
}

fn meter_segments(fraction: f64, width: usize) -> MeterSegments {
    if width == 0 {
        return MeterSegments {
            full: 0,
            partial: false,
            remaining: 0,
        };
    }
    let clamped = fraction.clamp(0.0, 1.0);
    if clamped >= 1.0 {
        return MeterSegments {
            full: width,
            partial: false,
            remaining: 0,
        };
    }
    if clamped <= 0.0 {
        return MeterSegments {
            full: 0,
            partial: false,
            remaining: width,
        };
    }

    let exact = clamped * width as f64;
    let mut full = exact.floor() as usize;
    let mut partial = exact - full as f64 >= 0.5;
    if full == 0 && !partial {
        partial = true;
    }
    if partial && full >= width {
        partial = false;
    }
    let used = full + usize::from(partial);
    if used > width {
        full = width;
        partial = false;
    }

    MeterSegments {
        full,
        partial,
        remaining: width.saturating_sub(full + usize::from(partial)),
    }
}

fn sparkline(values: &[Decimal], options: RenderOptions) -> String {
    match options.mode {
        RenderMode::Plain => String::new(),
        RenderMode::Ascii => ascii_sparkline(values),
        RenderMode::Braille => braille_sparkline(values),
    }
}

fn ascii_sparkline(values: &[Decimal]) -> String {
    let max = values.iter().copied().max().unwrap_or_default();
    let ramp = ['.', ':', '-', '=', '+', '*', '#'];
    values
        .iter()
        .map(|value| {
            if max <= Decimal::ZERO || *value <= Decimal::ZERO {
                '.'
            } else {
                let fraction = (*value / max).to_f64().unwrap_or(0.0);
                let index =
                    ((fraction * (ramp.len() - 1) as f64).round() as usize).min(ramp.len() - 1);
                ramp[index]
            }
        })
        .collect()
}

fn braille_sparkline(values: &[Decimal]) -> String {
    let max = values.iter().copied().max().unwrap_or_default();
    values
        .iter()
        .map(|value| {
            if max <= Decimal::ZERO || *value <= Decimal::ZERO {
                braille_blank()
            } else {
                let fraction = (*value / max).to_f64().unwrap_or(0.0);
                let height = ((fraction * 4.0).round() as u8).clamp(1, 4);
                braille_bar_height(height)
            }
        })
        .collect()
}

fn api_bucket_values(summary: &TrendsSummary) -> Vec<Decimal> {
    plain_api_bucket_values(summary)
        .into_iter()
        .map(|(_, value)| value)
        .collect()
}

fn plain_api_bucket_values(summary: &TrendsSummary) -> Vec<(PeriodRange, Decimal)> {
    let mut buckets = BTreeMap::<PeriodRange, Decimal>::new();
    for bucket in summary
        .buckets
        .iter()
        .filter(|bucket| bucket.lane == CostLane::Api)
    {
        let total = buckets.entry(bucket.period.clone()).or_default();
        *total += bucket.totals.billed_cost;
    }
    buckets.into_iter().collect()
}

fn mark(options: RenderOptions) -> &'static str {
    match options.mode {
        RenderMode::Braille => "C⠉",
        RenderMode::Ascii | RenderMode::Plain => "costroid",
    }
}

/// Bit for a braille dot number (1-8) in the U+2800 cell mask.
fn bit_for_dot(dot: u8) -> u8 {
    match dot {
        1 => 1,
        2 => 2,
        3 => 4,
        4 => 8,
        5 => 16,
        6 => 32,
        7 => 64,
        8 => 128,
        _ => 0,
    }
}

fn braille_cell(dots: &[u8]) -> char {
    let mut mask = 0_u8;
    for dot in dots {
        mask |= bit_for_dot(*dot);
    }
    byte_to_braille(mask)
}

/// A filled braille cell mask → its glyph (base U+2800 + the 8-bit dot mask).
fn byte_to_braille(mask: u8) -> char {
    char::from_u32(0x2800 + u32::from(mask)).unwrap_or('\u{2800}')
}

fn braille_blank() -> char {
    braille_cell(&[])
}

fn braille_full() -> char {
    braille_cell(&[1, 2, 3, 4, 5, 6, 7, 8])
}

fn braille_light() -> char {
    braille_cell(&[7, 8])
}

fn braille_left_column() -> char {
    braille_cell(&[1, 2, 3, 7])
}

fn braille_bar_height(height: u8) -> char {
    match height {
        0 => braille_blank(),
        1 => braille_cell(&[7, 8]),
        2 => braille_cell(&[3, 6, 7, 8]),
        3 => braille_cell(&[2, 3, 5, 6, 7, 8]),
        _ => braille_full(),
    }
}

fn push_line(out: &mut StyledDocument, line: &str) {
    out.push(StyledLine::plain(line));
}

fn push_rule(out: &mut StyledDocument, options: RenderOptions) {
    push_line(out, &"─".repeat(options.width.max(1)));
}

fn push_header_line(
    out: &mut StyledDocument,
    mark: &str,
    scope: &str,
    money: String,
    options: RenderOptions,
) {
    let mut line = StyledLine::new();
    if options.width == DEFAULT_RENDER_WIDTH {
        line.push_plain(format!(
            "{mark} costroid                                   {scope}  "
        ));
    } else {
        let left = format!("{mark} costroid");
        let right = format!("{scope}  ");
        let spacing = options
            .width
            .saturating_sub(visible_width(&left) + visible_width(&right) + visible_width(&money))
            .max(1);
        line.push_plain(format!("{left}{}{right}", " ".repeat(spacing)));
    }
    line.push_styled(money, SemanticStyle::Strong);
    out.push(line);
}

fn visible_width(value: &str) -> usize {
    value.chars().count()
}

fn cost_bar_width(options: RenderOptions) -> usize {
    if options.width <= DEFAULT_RENDER_WIDTH {
        COST_BAR_WIDTH
    } else {
        (COST_BAR_WIDTH + (options.width - DEFAULT_RENDER_WIDTH) / 4).min(32)
    }
}

fn push_provider_notes(out: &mut StyledDocument, providers: &[costroid_core::ProviderStatus]) {
    for provider in providers
        .iter()
        .filter(|provider| provider.status != ProviderStatusKind::Available)
    {
        let message = provider
            .message
            .as_deref()
            .unwrap_or("local data incomplete");
        push_line(
            out,
            &format!(
                "provider {} {}: {}",
                provider_name(provider.provider),
                provider_status(provider.status),
                message
            ),
        );
    }
}

fn push_empty_provider_guidance(
    out: &mut StyledDocument,
    providers: &[costroid_core::ProviderStatus],
) {
    if providers.iter().any(|provider| {
        matches!(
            provider.status,
            ProviderStatusKind::Available | ProviderStatusKind::Detected
        )
    }) {
        return;
    }

    push_line(out, "no providers detected");
    push_line(
        out,
        "looked for Claude Code, Codex, and Cursor local logs under the usual home/config dirs.",
    );
    push_line(
        out,
        "under WSL, Costroid also checks Windows paths like /mnt/c/Users/<you>/...",
    );
    push_line(
        out,
        "set CLAUDE_CONFIG_DIR or CODEX_HOME if your logs live elsewhere.",
    );
}

fn insight_line(api: &[CostLaneSummary], limits: &[LimitSummary]) -> String {
    if let Some(limit) = most_constrained_limit(limits) {
        let fraction = limit_fraction(limit).unwrap_or(0.0);
        if fraction >= WARN_FRACTION {
            return format!(
                "◆ {} {} at {}, resets {}.",
                provider_name(limit.tool),
                limit_kind(limit.kind),
                percent(fraction),
                limit_fraction_and_reset(limit)
                    .1
                    .map(reset_countdown)
                    .unwrap_or_else(|| "unknown".to_string())
            );
        }
    }

    match api.first() {
        Some(row) => format!(
            "◆ {} drove most of your API spend in this period. (estimated)",
            display_group(&row.group.value)
        ),
        None => "◆ no API usage in this period. (estimated)".to_string(),
    }
}

fn plain_insight_line(api: &[CostLaneSummary], limits: &[LimitSummary]) -> String {
    insight_line(api, limits).replace('◆', "insight:")
}

fn pricing_badge(totals: &AggregateTotals) -> String {
    let label = pricing_badge_plain(totals);
    if label.is_empty() {
        String::new()
    } else {
        format!("  [{}]", label.trim_start_matches(", "))
    }
}

fn pricing_badge_plain(totals: &AggregateTotals) -> String {
    let missing = totals.pricing_coverage.missing_price_rows;
    let unknown = totals.pricing_coverage.unknown_model_rows;
    let priced = totals.pricing_coverage.priced_rows;
    if unknown > 0 {
        if priced > 0 || missing > 0 {
            ", unknown model/partial".to_string()
        } else {
            ", unknown model/unpriced".to_string()
        }
    } else if missing > 0 {
        if priced > 0 {
            ", partial pricing".to_string()
        } else {
            ", unpriced/partial".to_string()
        }
    } else {
        String::new()
    }
}

fn format_money(amount: &Decimal, currency: Option<&str>, estimated: bool) -> String {
    let prefix = if estimated { "~" } else { "" };
    let rounded = amount.round_dp(2).to_string();
    let (whole, fraction) = match rounded.split_once('.') {
        Some((whole, fraction)) => (whole, fraction),
        None => (rounded.as_str(), ""),
    };
    let fraction = two_decimal_digits(fraction);
    let whole = with_thousands(whole);
    match currency.unwrap_or("USD") {
        "USD" => format!("{prefix}${whole}.{fraction}"),
        other => format!("{prefix}{other} {whole}.{fraction}"),
    }
}

fn two_decimal_digits(value: &str) -> String {
    let mut fraction = value.chars().take(2).collect::<String>();
    while fraction.len() < 2 {
        fraction.push('0');
    }
    fraction
}

fn with_thousands(value: &str) -> String {
    let (sign, digits) = value
        .strip_prefix('-')
        .map(|digits| ("-", digits))
        .unwrap_or(("", value));
    let mut reversed = String::new();
    for (index, ch) in digits.chars().rev().enumerate() {
        if index > 0 && index % 3 == 0 {
            reversed.push(',');
        }
        reversed.push(ch);
    }
    let grouped = reversed.chars().rev().collect::<String>();
    format!("{sign}{grouped}")
}

fn percent(fraction: f64) -> String {
    format!("{:.0}%", (fraction * 100.0).round())
}

fn reset_countdown(seconds: i64) -> String {
    if seconds <= 0 {
        return "<1m".to_string();
    }
    let minutes = seconds / 60;
    if minutes < 1 {
        "<1m".to_string()
    } else if minutes < 60 {
        format!("{minutes}m")
    } else {
        let hours = minutes / 60;
        let remaining_minutes = minutes % 60;
        if hours < 24 {
            if remaining_minutes == 0 {
                format!("{hours}h")
            } else {
                format!("{hours}h {remaining_minutes}m")
            }
        } else {
            let days = hours / 24;
            let remaining_hours = hours % 24;
            if remaining_hours == 0 {
                format!("{days}d")
            } else {
                format!("{days}d {remaining_hours}h")
            }
        }
    }
}

fn compact_reset(seconds: i64) -> String {
    reset_countdown(seconds).replace(' ', "")
}

fn display_group(value: &str) -> String {
    let last = value
        .rsplit(['/', '\\'])
        .find(|part| !part.is_empty())
        .unwrap_or(value);
    last.replace('-', " ")
}

fn provider_name(provider: ProviderId) -> &'static str {
    match provider {
        ProviderId::ClaudeCode => "claude code",
        ProviderId::Codex => "codex",
        ProviderId::Cursor => "cursor",
    }
}

fn provider_status(status: ProviderStatusKind) -> &'static str {
    match status {
        ProviderStatusKind::Available => "available",
        ProviderStatusKind::Detected => "detected",
        ProviderStatusKind::Partial => "partial",
        ProviderStatusKind::Missing => "missing",
        ProviderStatusKind::Error => "error",
    }
}

fn limit_kind(kind: LimitKind) -> &'static str {
    // Short window labels (fit the `{kind:<3}` column). The new kinds get minimal
    // placeholder abbreviations here so the build stays green; T6 owns any refinement.
    match kind {
        LimitKind::FiveHour => "5h",
        LimitKind::Weekly => "wk",
        LimitKind::Daily => "1d",
        LimitKind::Monthly => "mo",
        LimitKind::BillingCycle => "cyc",
    }
}

fn period_scope(period: Period) -> &'static str {
    match period {
        Period::Day => "today",
        Period::Week => "this week",
        Period::Month => "this month",
        Period::Year => "this year",
    }
}

fn period_name(period: Period) -> &'static str {
    match period {
        Period::Day => "day",
        Period::Week => "week",
        Period::Month => "month",
        Period::Year => "year",
    }
}

fn period_bucket_label(period: Period) -> &'static str {
    match period {
        Period::Day => "day",
        Period::Week => "week",
        Period::Month => "month",
        Period::Year => "year",
    }
}

fn group_name(group: GroupBy) -> &'static str {
    match group {
        GroupBy::Model => "model",
        GroupBy::App => "app",
        GroupBy::Total => "total",
    }
}

fn period_tabs(active: Period) -> String {
    [Period::Day, Period::Week, Period::Month, Period::Year]
        .into_iter()
        .map(|period| {
            if period == active {
                format!("({})", period_name(period))
            } else {
                format!("[{}]", period_name(period))
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn group_tabs(active: GroupBy) -> String {
    [GroupBy::Model, GroupBy::App, GroupBy::Total]
        .into_iter()
        .map(|group| {
            if group == active {
                format!("({})", group_name(group))
            } else {
                group_name(group).to_string()
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

fn sparkline_labels(period: Period, len: usize) -> String {
    if len == 0 {
        return String::new();
    }
    match period {
        Period::Day => "start      now".to_string(),
        Period::Week => "mon       sun".to_string(),
        Period::Month => (1..=len)
            .map(|index| format!("w{index}"))
            .collect::<Vec<_>>()
            .join("   "),
        Period::Year => "jan       dec".to_string(),
    }
}

fn format_bucket_start(range: &PeriodRange) -> String {
    let local: DateTime<Local> = range.start.with_timezone(&Local);
    local.format("%Y-%m-%d").to_string()
}

#[cfg(test)]
mod tests {
    use chrono::{Duration, LocalResult, TimeZone, Utc};
    use costroid_core::{
        AggregateTotals, CostLaneSummary, GroupKey, LimitSummary, NowSummary, PeriodRange,
        PricingCoverage, ProviderStatus, TrendsSummary,
    };
    use rust_decimal::Decimal;

    use super::*;

    fn utc(year: i32, month: u32, day: u32, hour: u32, minute: u32) -> DateTime<Utc> {
        match Utc.with_ymd_and_hms(year, month, day, hour, minute, 0) {
            LocalResult::Single(value) => value,
            LocalResult::Ambiguous(_, _) | LocalResult::None => {
                panic!("test timestamp should be valid")
            }
        }
    }

    fn range(start: DateTime<Utc>, days: i64) -> PeriodRange {
        PeriodRange {
            start,
            end: start + Duration::days(days),
        }
    }

    fn totals(cost_cents: i64, priced: usize, missing: usize, unknown: usize) -> AggregateTotals {
        AggregateTotals {
            row_count: priced + missing + unknown,
            billed_cost: Decimal::new(cost_cents, 2),
            effective_cost: Decimal::new(cost_cents, 2),
            currency: Some("USD".to_string()),
            multiple_currencies: false,
            tokens: costroid_core::TokenTotals {
                input: 10,
                output: 20,
                cache_read: 30,
                cache_write: 0,
            },
            pricing_coverage: PricingCoverage {
                priced_rows: priced,
                missing_price_rows: missing,
                unknown_model_rows: unknown,
            },
            estimated_rows: priced + missing + unknown,
        }
    }

    fn row(group: &str, lane: CostLane, cost_cents: i64) -> CostLaneSummary {
        CostLaneSummary {
            group: GroupKey {
                kind: GroupBy::Model,
                value: group.to_string(),
            },
            lane,
            totals: totals(cost_cents, 3, 0, 0),
        }
    }

    fn unpriced_row(group: &str, lane: CostLane) -> CostLaneSummary {
        CostLaneSummary {
            group: GroupKey {
                kind: GroupBy::Model,
                value: group.to_string(),
            },
            lane,
            totals: totals(0, 0, 3, 0),
        }
    }

    fn unknown_model_row(group: &str, lane: CostLane) -> CostLaneSummary {
        CostLaneSummary {
            group: GroupKey {
                kind: GroupBy::Model,
                value: group.to_string(),
            },
            lane,
            totals: totals(0, 0, 0, 3),
        }
    }

    fn limits() -> Vec<LimitSummary> {
        let now = utc(2026, 6, 2, 9, 0);
        vec![
            LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: None,
                kind: LimitKind::FiveHour,
                label: Some("unavailable".to_string()),
                availability: LimitAvailability::Unavailable {
                    reason: "unavailable".to_string(),
                },
            },
            LimitSummary {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::FiveHour,
                label: None,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.78),
                    resets_at: now + Duration::minutes(41),
                    reset_in_seconds: 41 * 60,
                },
            },
            LimitSummary {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Weekly,
                label: None,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.92),
                    resets_at: now + Duration::hours(54),
                    reset_in_seconds: 54 * 60 * 60,
                },
            },
            LimitSummary {
                tool: ProviderId::Cursor,
                plan: None,
                kind: LimitKind::Weekly,
                label: None,
                availability: LimitAvailability::Partial {
                    measure: Some(LimitMeasure::TokenFraction(0.97)),
                    resets_at: None,
                    reset_in_seconds: None,
                    reason: "limit data incomplete".to_string(),
                },
            },
        ]
    }

    fn provider_statuses() -> Vec<ProviderStatus> {
        vec![
            ProviderStatus {
                provider: ProviderId::ClaudeCode,
                status: ProviderStatusKind::Available,
                files: 1,
                usage_events: 1,
                focus_rows: 3,
                limit_windows: 2,
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
        ]
    }

    fn priced_now() -> NowSummary {
        let now = utc(2026, 6, 2, 9, 0);
        NowSummary {
            generated_at: now,
            cost_period: range(utc(2026, 6, 1, 0, 0), 7),
            group_by: GroupBy::Model,
            limits: limits(),
            current_costs: vec![
                row("sonnet-4.6", CostLane::Api, 678),
                row("gpt-5.5", CostLane::Api, 1130),
                row("claude-opus-4.7", CostLane::Api, 2410),
                unpriced_row("gpt-5.5", CostLane::SubscriptionEstimate),
                unknown_model_row("mystery-model", CostLane::UnknownAccess),
            ],
            providers: provider_statuses(),
        }
    }

    fn subscription_only_now() -> NowSummary {
        let mut summary = priced_now();
        summary.current_costs = vec![unpriced_row("gpt-5.5", CostLane::SubscriptionEstimate)];
        summary
    }

    fn priced_trends() -> TrendsSummary {
        let now = utc(2026, 6, 2, 9, 0);
        let week_one = range(utc(2026, 6, 1, 0, 0), 7);
        let week_two = range(utc(2026, 6, 8, 0, 0), 7);
        TrendsSummary {
            generated_at: now,
            period: Period::Month,
            group_by: GroupBy::Model,
            buckets: vec![
                costroid_core::TrendBucket {
                    period: week_one,
                    group: GroupKey {
                        kind: GroupBy::Model,
                        value: "claude-opus-4.7".to_string(),
                    },
                    lane: CostLane::Api,
                    totals: totals(9600, 3, 0, 0),
                },
                costroid_core::TrendBucket {
                    period: week_two,
                    group: GroupKey {
                        kind: GroupBy::Model,
                        value: "gpt-5.5".to_string(),
                    },
                    lane: CostLane::Api,
                    totals: totals(4500, 3, 0, 0),
                },
            ],
            totals: vec![
                row("sonnet-4.6", CostLane::Api, 2700),
                row("gpt-5.5", CostLane::Api, 4500),
                row("claude-opus-4.7", CostLane::Api, 9600),
                unpriced_row("gpt-5.5", CostLane::SubscriptionEstimate),
            ],
            providers: provider_statuses(),
        }
    }

    #[test]
    fn cli_mode_selection_keeps_braille_when_no_color_is_set() {
        let env = EnvSnapshot {
            term: Some("xterm-256color".to_string()),
            lang: Some("en_US.UTF-8".to_string()),
            lc_all: None,
            lc_ctype: None,
            no_color: Some("1".to_string()),
        };
        let options = select_render_options(false, true, &env);

        assert_eq!(options.mode, RenderMode::Braille);
        assert!(!options.ansi);
    }

    #[test]
    fn cli_mode_selection_plain_wins_for_pipes_and_flag() {
        let env = EnvSnapshot {
            term: Some("xterm-256color".to_string()),
            lang: Some("en_US.UTF-8".to_string()),
            lc_all: None,
            lc_ctype: None,
            no_color: None,
        };

        assert_eq!(
            select_render_options(true, true, &env),
            RenderOptions::plain()
        );
        assert_eq!(
            select_render_options(false, false, &env),
            RenderOptions::plain()
        );
    }

    #[test]
    fn cli_mode_selection_ascii_is_for_braille_incapability() {
        let env = EnvSnapshot {
            term: Some("dumb".to_string()),
            lang: Some("en_US.UTF-8".to_string()),
            lc_all: None,
            lc_ctype: None,
            no_color: None,
        };
        let options = select_render_options(false, true, &env);

        assert_eq!(options.mode, RenderMode::Ascii);
        assert!(options.ansi);
    }

    #[test]
    fn braille_codepoint_math_matches_design_system() {
        assert_eq!(braille_cell(&[1, 4]), '⠉');
        assert_eq!(braille_full(), '⣿');
        assert_eq!(braille_left_column(), '⡇');
        assert_eq!(braille_light(), '⣀');
    }

    #[test]
    fn meter_segments_keep_braille_fill_visible_without_ansi() {
        let meter = limit_meter(0.42, 6, RenderOptions::braille(false));

        assert_eq!(meter, "⣿⣿⡇⣀⣀⣀");
    }

    #[test]
    fn thresholds_have_non_color_cues() {
        assert_eq!(state_cue(limit_state(0.79)), "");
        assert_eq!(state_cue(limit_state(0.80)), " !");
        assert_eq!(state_cue(limit_state(0.95)), " !!");
        assert_eq!(state_cue(limit_state(1.0)), " !! OVER");
    }

    #[test]
    fn reset_countdown_uses_compact_two_unit_format() {
        assert_eq!(reset_countdown(30), "<1m");
        assert_eq!(reset_countdown(46 * 60), "46m");
        assert_eq!(reset_countdown((2 * 60 + 14) * 60), "2h 14m");
        assert_eq!(reset_countdown((3 * 24 + 4) * 60 * 60), "3d 4h");
    }

    #[test]
    fn money_formatting_marks_estimates() {
        assert_eq!(
            format_money(&Decimal::new(2410, 2), Some("USD"), true),
            "~$24.10"
        );
        assert_eq!(
            format_money(&Decimal::new(184000, 2), Some("USD"), false),
            "$1,840.00"
        );
    }

    #[test]
    fn snapshot_now_braille() {
        insta::assert_snapshot!(render_now(&priced_now(), RenderOptions::braille(true)));
    }

    #[test]
    fn snapshot_now_braille_no_ansi() {
        insta::assert_snapshot!(render_now(&priced_now(), RenderOptions::braille(false)));
    }

    #[test]
    fn snapshot_now_ascii() {
        insta::assert_snapshot!(render_now(&priced_now(), RenderOptions::ascii(false)));
    }

    #[test]
    fn snapshot_now_plain() {
        insta::assert_snapshot!(render_now(&priced_now(), RenderOptions::plain()));
    }

    #[test]
    fn plain_now_renders_cursor_detected_note_without_color() {
        // Pins the Cursor detect-and-defer note in `--plain` output: the BETA / model /
        // deferred-live wording must render as plain text against drift (§9.7), and —
        // because accessibility forbids relying on color — carry NO ANSI escapes. The
        // message string mirrors what `costroid-core` builds for a detected Cursor.
        let mut summary = priced_now();
        summary.providers = vec![ProviderStatus {
            provider: ProviderId::Cursor,
            status: ProviderStatusKind::Detected,
            files: 1,
            usage_events: 0,
            focus_rows: 0,
            limit_windows: 0,
            message: Some(
                "BETA — model Composer 2.5 Fast (composer-2.5), logged in; \
                 usage unavailable — live (Phase 2); quota unavailable — live (Phase 2)"
                    .to_string(),
            ),
        }];

        let output = render_now(&summary, RenderOptions::plain());
        assert!(
            output.contains(
                "provider cursor detected: BETA — model Composer 2.5 Fast (composer-2.5), logged in"
            ),
            "plain now should render the cursor detected note: {output}"
        );
        assert!(output.contains("usage unavailable — live (Phase 2)"));
        assert!(output.contains("quota unavailable — live (Phase 2)"));
        assert!(
            !output.contains('\u{1b}'),
            "plain output must not contain ANSI escapes"
        );
    }

    #[test]
    fn snapshot_now_subscription_only_has_explicit_empty_api_state() {
        insta::assert_snapshot!(render_now(
            &subscription_only_now(),
            RenderOptions::braille(false)
        ));
    }

    #[test]
    fn snapshot_trends_braille_with_ansi() {
        insta::assert_snapshot!(render_trends(
            &priced_trends(),
            RenderOptions::braille(true)
        ));
    }

    #[test]
    fn snapshot_trends_braille() {
        insta::assert_snapshot!(render_trends(
            &priced_trends(),
            RenderOptions::braille(false)
        ));
    }

    #[test]
    fn snapshot_trends_ascii() {
        insta::assert_snapshot!(render_trends(&priced_trends(), RenderOptions::ascii(false)));
    }

    #[test]
    fn snapshot_trends_plain() {
        insta::assert_snapshot!(render_trends(&priced_trends(), RenderOptions::plain()));
    }

    #[test]
    fn snapshot_statusline_braille_with_ansi() {
        insta::assert_snapshot!(render_statusline(
            &priced_now(),
            RenderOptions::braille(true)
        ));
    }

    #[test]
    fn snapshot_statusline_braille() {
        insta::assert_snapshot!(render_statusline(
            &priced_now(),
            RenderOptions::braille(false)
        ));
    }

    #[test]
    fn snapshot_statusline_ascii() {
        insta::assert_snapshot!(render_statusline(
            &priced_now(),
            RenderOptions::ascii(false)
        ));
    }

    #[test]
    fn snapshot_statusline_plain() {
        insta::assert_snapshot!(render_statusline(&priced_now(), RenderOptions::plain()));
    }

    #[test]
    fn snapshot_statusline_plain_empty_api() {
        insta::assert_snapshot!(render_statusline(
            &subscription_only_now(),
            RenderOptions::plain()
        ));
    }

    // ----- frontier surface -----

    fn frontier_event(
        model: &str,
        access: costroid_providers::AccessPath,
        input: u64,
        output: u64,
    ) -> costroid_providers::UsageEvent {
        costroid_providers::UsageEvent {
            tool: costroid_providers::ProviderId::Codex,
            model: model.to_string(),
            timestamp: utc(2026, 6, 2, 9, 0),
            input_tokens: input,
            output_tokens: output,
            cache_read_tokens: 0,
            cache_write_tokens: 0,
            project: Some("/work/proj".to_string()),
            access_path: access,
        }
    }

    fn bench_view_for(events: &[costroid_providers::UsageEvent]) -> BenchView {
        let focus_rows = match costroid_core::focus_records_from_usage(events) {
            Ok(rows) => rows,
            Err(err) => panic!("events should price: {err}"),
        };
        let snapshot = costroid_core::EngineSnapshot {
            generated_at: utc(2026, 6, 2, 9, 0),
            usage_events: Vec::new(),
            focus_rows,
            limit_windows: Vec::new(),
            providers: Vec::new(),
        };
        match costroid_core::bench_view(&snapshot) {
            Ok(view) => view,
            Err(err) => panic!("bench view should build: {err}"),
        }
    }

    fn used_gpt() -> BenchView {
        bench_view_for(&[frontier_event(
            "gpt-5.5",
            costroid_providers::AccessPath::Api,
            1_000_000,
            1_000_000,
        )])
    }

    #[test]
    fn snapshot_frontier_plain() {
        insta::assert_snapshot!(render_frontier(&used_gpt(), RenderOptions::plain()));
    }

    #[test]
    fn snapshot_frontier_plain_no_api() {
        insta::assert_snapshot!(render_frontier(
            &bench_view_for(&[]),
            RenderOptions::plain()
        ));
    }

    #[test]
    fn snapshot_frontier_braille() {
        insta::assert_snapshot!(render_frontier(&used_gpt(), RenderOptions::braille(true)));
    }

    #[test]
    fn plain_frontier_has_no_ansi() {
        let output = render_frontier(&used_gpt(), RenderOptions::plain());
        assert!(
            !output.contains('\u{1b}'),
            "plain frontier must not contain ANSI escapes: {output}"
        );
    }

    #[test]
    fn braille_scatter_maps_known_points() {
        // Top-left point lands in cell 0 (dot 1 = ⠁); bottom-right in cell 1 (dot 8 = ⢀).
        let top_left = PlotPoint {
            x: 0.0,
            y: 100.0,
            on_frontier: false,
        };
        let bottom_right = PlotPoint {
            x: 10.0,
            y: 0.0,
            on_frontier: false,
        };
        assert_eq!(
            braille_scatter(&[top_left, bottom_right], 2, 1, false),
            vec!["⠁⢀".to_string()]
        );

        // A degenerate axis (all same x) must not panic and keeps the grid shape.
        let same_x = vec![
            PlotPoint {
                x: 5.0,
                y: 1.0,
                on_frontier: false,
            },
            PlotPoint {
                x: 5.0,
                y: 2.0,
                on_frontier: false,
            },
        ];
        let rows = braille_scatter(&same_x, 3, 2, false);
        assert_eq!(rows.len(), 2);
        assert!(rows.iter().all(|row| row.chars().count() == 3));
    }

    #[test]
    fn frontier_document_is_monochrome() {
        let doc = render_frontier_document(&used_gpt(), RenderOptions::braille(true));
        for line in &doc.lines {
            for span in &line.spans {
                assert!(
                    !matches!(span.style, SemanticStyle::Warn | SemanticStyle::Critical),
                    "frontier must be monochrome (amber/red are reserved): {span:?}"
                );
            }
        }
    }

    #[test]
    fn frontier_insight_uses_advisory_voice() {
        let line = frontier_insight_line(&used_gpt());
        assert!(
            line.starts_with('◆'),
            "insight should use the ◆ marker: {line}"
        );
        assert!(
            line.contains("(estimated)") || line.contains('~'),
            "insight should hedge: {line}"
        );
        let lower = line.to_lowercase();
        assert!(
            !lower.contains("switch to"),
            "insight must not prescribe: {line}"
        );
        assert!(
            !lower.contains("you should"),
            "insight must not prescribe: {line}"
        );
        assert!(plain_frontier_insight_line(&used_gpt()).starts_with("insight:"));
    }

    #[test]
    fn frontier_no_api_states_nothing_to_compare() {
        let output = render_frontier(&bench_view_for(&[]), RenderOptions::plain());
        assert!(output.contains("no API-billed usage to compare"));
        // Reference frontier still renders both benchmarks with their dominance verdicts.
        assert!(output.contains("dominated by gpt-5.5"));
    }
}
