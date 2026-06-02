use std::collections::BTreeMap;
use std::env;
use std::io::{self, IsTerminal};

use chrono::{DateTime, Local};
use costroid_core::{
    AggregateTotals, CostLane, CostLaneSummary, GroupBy, LimitAvailability, LimitSummary,
    NowSummary, Period, PeriodRange, ProviderStatusKind, TrendsSummary,
};
use costroid_providers::{LimitKind, ProviderId};
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;

const COST_BAR_WIDTH: usize = 12;
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
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
enum Style {
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
        }
    }

    #[cfg(test)]
    fn braille(ansi: bool) -> Self {
        Self {
            mode: RenderMode::Braille,
            ansi,
        }
    }

    #[cfg(test)]
    fn ascii(ansi: bool) -> Self {
        Self {
            mode: RenderMode::Ascii,
            ansi,
        }
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
    match options.mode {
        RenderMode::Plain => render_now_plain(summary),
        RenderMode::Braille | RenderMode::Ascii => render_now_visual(summary, options),
    }
}

pub(crate) fn render_trends(summary: &TrendsSummary, options: RenderOptions) -> String {
    match options.mode {
        RenderMode::Plain => render_trends_plain(summary),
        RenderMode::Braille | RenderMode::Ascii => render_trends_visual(summary, options),
    }
}

pub(crate) fn render_statusline(summary: &NowSummary, options: RenderOptions) -> String {
    let api = sorted_lane_rows(&summary.current_costs, CostLane::Api);
    let api_total = sum_costs(&api);
    let spend = format_money(&api_total, Some("USD"), true);
    let api_state = if api.is_empty() { " no api" } else { "" };

    match options.mode {
        RenderMode::Plain => match most_constrained_limit(&summary.limits) {
            Some(limit) => format!(
                "costroid {spend},{} {}",
                if api.is_empty() { " no API usage," } else { "" },
                plain_limit_phrase(limit)
            ),
            None => format!(
                "costroid {spend}{}",
                if api.is_empty() { ", no API usage" } else { "" }
            ),
        },
        RenderMode::Braille | RenderMode::Ascii => match most_constrained_limit(&summary.limits) {
            Some(limit) => {
                let (fraction, reset) = limit_fraction_and_reset(limit);
                let meter = limit_meter(fraction, STATUS_BAR_WIDTH, options);
                let pct = percent(fraction);
                let cue = state_cue(limit_state(fraction));
                let reset = reset
                    .map(|seconds| format!(" {}", compact_reset(seconds)))
                    .unwrap_or_default();
                format!(
                    "{} {spend}{api_state}  {meter} {pct}{cue}{reset}",
                    mark(options)
                )
            }
            None => format!("{} {spend}{api_state}", mark(options)),
        },
    }
}

fn render_now_visual(summary: &NowSummary, options: RenderOptions) -> String {
    let mut out = String::new();
    let api = sorted_lane_rows(&summary.current_costs, CostLane::Api);
    let api_total = sum_costs(&api);

    push_line(
        &mut out,
        &format!(
            "{} costroid                                   this week  {}",
            mark(options),
            style(
                &format_money(&api_total, Some("USD"), true),
                Style::Strong,
                options
            )
        ),
    );
    push_rule(&mut out);
    push_line(&mut out, "limits");
    if summary.limits.is_empty() {
        push_line(&mut out, "  no local limit data found");
    } else {
        for limit in &summary.limits {
            push_line(&mut out, &render_limit_line(limit, options));
        }
    }
    push_rule(&mut out);
    push_line(&mut out, "api costs (this week)");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        push_cost_rows(&mut out, &api, options);
    }
    push_non_api_sections(&mut out, &summary.current_costs, options);
    push_rule(&mut out);
    push_line(&mut out, &insight_line(&api, &summary.limits));
    push_provider_notes(&mut out, &summary.providers);
    out
}

fn render_now_plain(summary: &NowSummary) -> String {
    let mut out = String::new();
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
    out
}

fn render_trends_visual(summary: &TrendsSummary, options: RenderOptions) -> String {
    let mut out = String::new();
    let api = sorted_lane_rows(&summary.totals, CostLane::Api);
    let api_total = sum_costs(&api);

    push_line(
        &mut out,
        &format!(
            "{} costroid                                   {}  {}",
            mark(options),
            period_scope(summary.period),
            style(
                &format_money(&api_total, Some("USD"), true),
                Style::Strong,
                options
            )
        ),
    );
    push_line(
        &mut out,
        &format!(
            "  {}            group: {}",
            period_tabs(summary.period),
            group_tabs(summary.group_by)
        ),
    );
    push_rule(&mut out);
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
    push_rule(&mut out);
    push_line(&mut out, "breakdown");
    if api.is_empty() {
        push_line(&mut out, "  no API usage in this period");
    } else {
        push_cost_rows(&mut out, &api, options);
    }
    push_non_api_sections(&mut out, &summary.totals, options);
    push_rule(&mut out);
    push_line(&mut out, &insight_line(&api, &[]));
    push_provider_notes(&mut out, &summary.providers);
    out
}

fn render_trends_plain(summary: &TrendsSummary) -> String {
    let mut out = String::new();
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
    out
}

fn push_non_api_sections(out: &mut String, rows: &[CostLaneSummary], options: RenderOptions) {
    let subscription = sorted_lane_rows(rows, CostLane::SubscriptionEstimate);
    if !subscription.is_empty() {
        push_rule(out);
        push_line(out, "subscription/local usage (not API bill)");
        push_cost_rows(out, &subscription, options);
    }

    let unknown = sorted_lane_rows(rows, CostLane::UnknownAccess);
    if !unknown.is_empty() {
        push_rule(out);
        push_line(out, "unknown-access usage (partial)");
        push_cost_rows(out, &unknown, options);
    }
}

fn push_plain_non_api_sections(out: &mut String, rows: &[CostLaneSummary]) {
    let subscription = sorted_lane_rows(rows, CostLane::SubscriptionEstimate);
    if !subscription.is_empty() {
        push_line(out, "subscription/local usage, not API bill:");
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

fn push_cost_rows(out: &mut String, rows: &[CostLaneSummary], options: RenderOptions) {
    let max = rows
        .iter()
        .map(|row| row.totals.billed_cost)
        .max()
        .unwrap_or_default();
    for row in rows {
        let bar = cost_bar(row.totals.billed_cost, max, COST_BAR_WIDTH, options);
        let money = style(
            &format_money(
                &row.totals.billed_cost,
                row.totals.currency.as_deref(),
                row.totals.estimated_rows > 0,
            ),
            Style::Strong,
            options,
        );
        let badge = pricing_badge(&row.totals);
        push_line(
            out,
            &format!(
                "  {:<18}  {}  {:>12}{}",
                display_group(&row.group.value),
                bar,
                money,
                badge
            ),
        );
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

fn render_limit_line(limit: &LimitSummary, options: RenderOptions) -> String {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    match &limit.availability {
        LimitAvailability::Available {
            used_fraction,
            reset_in_seconds,
            ..
        } => {
            let meter = limit_meter(*used_fraction, LIMIT_BAR_WIDTH, options);
            let cue = state_cue(limit_state(*used_fraction));
            format!(
                "  {:<12} {:<3} {}  {}{}  resets {}",
                tool,
                kind,
                meter,
                percent(*used_fraction),
                cue,
                reset_countdown(*reset_in_seconds)
            )
        }
        LimitAvailability::Partial {
            used_fraction,
            reset_in_seconds,
            reason,
            ..
        } => match used_fraction {
            Some(fraction) => {
                let meter = limit_meter(*fraction, LIMIT_BAR_WIDTH, options);
                let cue = state_cue(limit_state(*fraction));
                let reset = reset_in_seconds
                    .map(reset_countdown)
                    .map(|value| format!(" resets {value}"))
                    .unwrap_or_default();
                format!(
                    "  {:<12} {:<3} {}  {}{}  partial: {}{}",
                    tool,
                    kind,
                    meter,
                    percent(*fraction),
                    cue,
                    reason,
                    reset
                )
            }
            None => format!("  {:<12} {:<3} partial: {}", tool, kind, reason),
        },
        LimitAvailability::Unavailable { reason } => {
            format!("  {:<12} {:<3} unavailable: {}", tool, kind, reason)
        }
    }
}

fn plain_limit_line(limit: &LimitSummary) -> String {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    match &limit.availability {
        LimitAvailability::Available {
            used_fraction,
            reset_in_seconds,
            ..
        } => {
            let cue = plain_state_phrase(limit_state(*used_fraction));
            format!(
                "  {tool} {kind}: {} used{cue}, resets in {}",
                percent(*used_fraction),
                reset_countdown(*reset_in_seconds)
            )
        }
        LimitAvailability::Partial {
            used_fraction,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = used_fraction
                .map(|fraction| format!("{} used", percent(fraction)))
                .unwrap_or_else(|| "usage unknown".to_string());
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("  {tool} {kind}: partial, {usage}{reset}, {reason}")
        }
        LimitAvailability::Unavailable { reason } => {
            format!("  {tool} {kind}: unavailable, {reason}")
        }
    }
}

fn plain_limit_phrase(limit: &LimitSummary) -> String {
    match &limit.availability {
        LimitAvailability::Available {
            used_fraction,
            reset_in_seconds,
            ..
        } => format!(
            "{} {} {} used, resets in {}",
            provider_name(limit.tool),
            limit_kind(limit.kind),
            percent(*used_fraction),
            compact_reset(*reset_in_seconds)
        ),
        LimitAvailability::Partial {
            used_fraction,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = used_fraction
                .map(percent)
                .unwrap_or_else(|| "unknown usage".to_string());
            let reset = reset_in_seconds
                .map(compact_reset)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!(
                "{} {} partial, {usage}{reset}, {reason}",
                provider_name(limit.tool),
                limit_kind(limit.kind)
            )
        }
        LimitAvailability::Unavailable { reason } => format!(
            "{} {} unavailable, {reason}",
            provider_name(limit.tool),
            limit_kind(limit.kind)
        ),
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
        LimitAvailability::Available { used_fraction, .. }
        | LimitAvailability::Partial {
            used_fraction: Some(used_fraction),
            ..
        } => Some(*used_fraction),
        LimitAvailability::Partial {
            used_fraction: None,
            ..
        }
        | LimitAvailability::Unavailable { .. } => None,
    }
}

fn limit_fraction_and_reset(limit: &LimitSummary) -> (f64, Option<i64>) {
    match &limit.availability {
        LimitAvailability::Available {
            used_fraction,
            reset_in_seconds,
            ..
        } => (*used_fraction, Some(*reset_in_seconds)),
        LimitAvailability::Partial {
            used_fraction,
            reset_in_seconds,
            ..
        } => (used_fraction.unwrap_or(0.0), *reset_in_seconds),
        LimitAvailability::Unavailable { .. } => (0.0, None),
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

fn limit_meter(fraction: f64, width: usize, options: RenderOptions) -> String {
    let state = limit_state(fraction);
    let styled = match state {
        LimitState::Normal => Style::Strong,
        LimitState::Warn => Style::Warn,
        LimitState::Critical | LimitState::Over => Style::Critical,
    };
    positional_meter(fraction, width, options, styled)
}

fn cost_bar(amount: Decimal, max: Decimal, width: usize, options: RenderOptions) -> String {
    let fraction = if max > Decimal::ZERO {
        (amount / max).to_f64().unwrap_or(0.0)
    } else {
        0.0
    };
    positional_meter(fraction, width, options, Style::Strong)
}

fn positional_meter(
    fraction: f64,
    width: usize,
    options: RenderOptions,
    used_style: Style,
) -> String {
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
            style(&meter, used_style, options)
        }
        RenderMode::Braille => {
            let mut meter = String::with_capacity(width);
            meter.extend(std::iter::repeat_n(braille_full(), segments.full));
            if segments.partial {
                meter.push(braille_left_column());
            }
            meter.extend(std::iter::repeat_n(braille_light(), segments.remaining));
            style(&meter, used_style, options)
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

fn braille_cell(dots: &[u8]) -> char {
    let mut mask = 0_u32;
    for dot in dots {
        mask += match dot {
            1 => 1,
            2 => 2,
            3 => 4,
            4 => 8,
            5 => 16,
            6 => 32,
            7 => 64,
            8 => 128,
            _ => 0,
        };
    }
    char::from_u32(0x2800 + mask).unwrap_or('\u{2800}')
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

fn style(value: &str, style: Style, options: RenderOptions) -> String {
    if !options.ansi || options.mode == RenderMode::Plain {
        return value.to_string();
    }
    let code = match style {
        Style::Strong => "1",
        Style::Warn => "33;1",
        Style::Critical => "31;1",
    };
    format!("\x1b[{code}m{value}\x1b[0m")
}

fn push_line(out: &mut String, line: &str) {
    out.push_str(line);
    out.push('\n');
}

fn push_rule(out: &mut String) {
    push_line(
        out,
        "────────────────────────────────────────────────────────────────",
    );
}

fn push_provider_notes(out: &mut String, providers: &[costroid_core::ProviderStatus]) {
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
        ProviderStatusKind::Partial => "partial",
        ProviderStatusKind::Missing => "missing",
        ProviderStatusKind::Error => "error",
    }
}

fn limit_kind(kind: LimitKind) -> &'static str {
    match kind {
        LimitKind::FiveHour => "5h",
        LimitKind::Weekly => "wk",
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
                    used_fraction: 0.78,
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
                    used_fraction: 0.92,
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
                    used_fraction: Some(0.97),
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
}
