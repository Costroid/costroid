use std::collections::BTreeMap;
use std::env;
use std::io::{self, IsTerminal};

use chrono::{DateTime, Local, Utc};
use costroid_core::{
    AggregateTotals, BenchFrontier, BenchView, CostLane, CostLaneSummary, FrontierPoint,
    FrontierStanding, GroupBy, LimitAvailability, LimitSummary, NowSummary, OverlayModel, Period,
    PeriodRange, ProviderCapabilityView, ProviderStatus, ProviderStatusKind, RepricingDelta,
    RepricingStatus, TrendsSummary,
};
use costroid_providers::{AuthMethod, DataSource, LimitKind, LimitMeasure, ProviderId};
use rust_decimal::prelude::ToPrimitive;
use rust_decimal::Decimal;

// The reconciliation renderer (T10c) is a pure function of the core `CostReconciliation`
// type, so it compiles + snapshot-tests in the default suite, but it is only *called* from
// the connect-gated `reconcile` command — gate the imports + functions on
// `any(feature = "connect", test)` so the default non-test build carries no dead code.
#[cfg(any(feature = "connect", test))]
use costroid_core::{
    AmountConfidence, BilledAbsence, CostReconciliation, DayReconciliation, ModelReconciliation,
    ReconciledReportStatus, UsdAmount, VendorBilled, VendorReportUnavailable,
};

const COST_BAR_WIDTH: usize = 12;
const DEFAULT_RENDER_WIDTH: usize = 64;
const LIMIT_BAR_WIDTH: usize = 12;
const STATUS_BAR_WIDTH: usize = 4;
const WARN_FRACTION: f64 = 0.80;
const CRITICAL_FRACTION: f64 = 0.95;
/// A reading at least this old (capture time vs. the summary's `generated_at`) carries an
/// always-on "as of HH:MM" freshness stamp (STATUSLINE-CAPTURE-BRIEF §8): every Claude
/// reading is a cached push and every Codex window is only as fresh as its latest rollout
/// entry, so a hours-old reading must never render as a bare, confident meter. Tunable.
const LIMIT_FRESHNESS_STAMP_MINUTES: i64 = 10;
/// The distinct, color-free flag for a cross-check-failed (`Unverified`) reading — shown
/// INSTEAD of the confident `!`/`!!` state cue so a near-max unverified reading never
/// reads as a confident alarm (brief §8). Survives `--plain` / `NO_COLOR`.
const UNVERIFIED_CUE: &str = " ? unverified";
/// The push-only chat-under-report caveat carried by Claude `Available`/`Unverified`
/// lines (brief §8): claude.ai chat shares the 5h/7d limit but is invisible to the cache,
/// so the meter can read low.
const CLAUDE_CHAT_CAVEAT: &str =
    "reflects Claude Code's view; claude.ai chat usage may make true usage higher.";

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
// Reconciliation surface (T10c) — `costroid reconcile`. Renders T9c's
// `CostReconciliation` HONESTLY: signed variance per UTC day + model; TYPED vendor-side
// absence as text, NEVER a fabricated $0; every caveat footnoted; the local figure ALWAYS
// labeled an estimate (`~`). It is a pure function of the core type (no `costroid-connect`
// dependency), so its snapshots run in the default test suite; the connect-gated `reconcile`
// command (apps/cli/src/reconcile.rs) fetches the report and calls it. Same monochrome voice
// as the frontier — no amber (reserved for near-limit); the over/under direction is carried
// as TEXT, never color. Gated `any(feature = "connect", test)` so the default non-test build
// (which never calls it) carries no dead code.
// ---------------------------------------------------------------------------

/// Render one vendor's reconciliation section to a string in `options`' mode.
#[cfg(any(feature = "connect", test))]
pub(crate) fn render_reconciliation(
    vendor: &str,
    window_label: &str,
    recon: &CostReconciliation,
    options: RenderOptions,
) -> String {
    render_reconciliation_document(vendor, window_label, recon, options).render(options)
}

#[cfg(any(feature = "connect", test))]
pub(crate) fn render_reconciliation_document(
    vendor: &str,
    window_label: &str,
    recon: &CostReconciliation,
    options: RenderOptions,
) -> StyledDocument {
    let mut out = StyledDocument::new();

    // Header: the vendor + section totals (estimate always `~`-marked; the invoice total
    // only when a report was available).
    let (total_est, total_billed) = reconciliation_totals(recon);
    let money = match total_billed {
        Some(billed) => format!(
            "est {} / inv {}",
            format_money(&total_est, Some("USD"), true),
            format_money(&billed, Some("USD"), false),
        ),
        None => format!("est {}", format_money(&total_est, Some("USD"), true)),
    };
    if options.mode == RenderMode::Plain {
        // Plain follows the established `costroid <screen>` convention (a clean header, like
        // now/trends/frontier's plain builders) — never the visual builder's `costroid
        // costroid` doubling, which would read awkwardly to a screen reader.
        push_line(&mut out, &format!("costroid reconcile  {vendor}  {money}"));
    } else {
        push_header_line(&mut out, mark(options), vendor, money, options);
    }
    push_line(
        &mut out,
        &fold_ascii(&format!("estimate vs invoice — {window_label}"), options),
    );
    // The standing hedge (DESIGN-SYSTEM voice): the local figure is an estimate; the vendor
    // invoice is the source of truth.
    push_line(
        &mut out,
        "Local figures are estimates (your tokens x current prices); the vendor invoice is the source of truth.",
    );

    // Report-level unavailability (Gemini, not-connected, auth failure, …): name the typed
    // reason once up top, then STILL surface the local estimate day by day below — every
    // vendor cell renders typed-absent, never a fabricated $0.
    if let ReconciledReportStatus::Unavailable(reason) = &recon.report {
        push_line(
            &mut out,
            &fold_ascii(
                &format!(
                    "vendor invoice unavailable: {}",
                    report_unavailable_text(reason, vendor)
                ),
                options,
            ),
        );
    }

    push_reconcile_rule(&mut out, options);

    if recon.days.is_empty() {
        push_line(
            &mut out,
            "No local usage recorded for this vendor in this window.",
        );
    }
    for day in &recon.days {
        push_reconciliation_day(&mut out, day, vendor, options);
    }

    push_reconciliation_footnotes(&mut out, recon, options);
    out
}

/// One UTC day (its total) plus its per-model breakdown, indented.
#[cfg(any(feature = "connect", test))]
fn push_reconciliation_day(
    out: &mut StyledDocument,
    day: &DayReconciliation,
    vendor: &str,
    options: RenderOptions,
) {
    let line = format!(
        "{date}  est {est}   {inv}   {var}",
        date = day.date,
        est = format_money(&day.local_estimate.as_usd(), Some("USD"), true),
        inv = reconcile_billed_cell(&day.vendor_billed, vendor),
        var = reconcile_variance_cell(
            day.variance,
            day.variance_pct,
            billed_usd(&day.vendor_billed),
            options
        ),
    );
    push_line(out, &fold_ascii(&line, options));
    for model in &day.by_model {
        push_reconciliation_model(out, model, vendor, options);
    }
}

#[cfg(any(feature = "connect", test))]
fn push_reconciliation_model(
    out: &mut StyledDocument,
    model: &ModelReconciliation,
    vendor: &str,
    options: RenderOptions,
) {
    // Mark a row whose vendor figure is best-effort (OpenAI per-model, derived from line
    // items) with `*`; the footnote explains it.
    let marker = if matches!(model.confidence, Some(AmountConfidence::DerivedBestEffort)) {
        " *"
    } else {
        ""
    };
    let line = format!(
        "    {name:<22} est {est}   {inv}   {var}{marker}",
        name = model.model,
        est = format_money(&model.local_estimate.as_usd(), Some("USD"), true),
        inv = reconcile_billed_cell(&model.vendor_billed, vendor),
        var = reconcile_variance_cell(
            model.variance,
            model.variance_pct,
            billed_usd(&model.vendor_billed),
            options
        ),
    );
    push_line(out, &fold_ascii(&line, options));
}

/// The vendor-billed cell: a dollar invoice figure when billed, otherwise the TYPED absence
/// reason as TEXT — never a fabricated `$0`. (Any em-dash in a reason message is folded by
/// the caller's `fold_ascii` on the whole line.)
#[cfg(any(feature = "connect", test))]
fn reconcile_billed_cell(billed: &VendorBilled, vendor: &str) -> String {
    match billed {
        VendorBilled::Billed(amount) => {
            format!("inv {}", format_money(&amount.as_usd(), Some("USD"), false))
        }
        VendorBilled::Unavailable(absence) => match absence {
            BilledAbsence::DayNotCovered => "report doesn't cover this day".to_string(),
            BilledAbsence::ModelNotInReport => "not attributed by the vendor".to_string(),
            BilledAbsence::ReportUnavailable(reason) => report_unavailable_text(reason, vendor),
        },
    }
}

/// The billed dollar figure when the vendor side is present, else `None` (a typed absence —
/// where the variance is also `None`). The variance cell uses it to detect a sub-cent
/// denominator (one that displays as `$0.00`) so a percentage doesn't explode against it.
#[cfg(any(feature = "connect", test))]
fn billed_usd(billed: &VendorBilled) -> Option<Decimal> {
    match billed {
        VendorBilled::Billed(amount) => Some(amount.as_usd()),
        VendorBilled::Unavailable(_) => None,
    }
}

/// The typed reason a whole report is unavailable. `NotConnected` becomes an actionable
/// "connect <vendor> first"; every other reason uses its single-sourced core message.
#[cfg(any(feature = "connect", test))]
fn report_unavailable_text(reason: &VendorReportUnavailable, vendor: &str) -> String {
    match reason {
        VendorReportUnavailable::NotConnected => format!("connect {vendor} first"),
        other => other.message(),
    }
}

/// The signed-variance cell: `+$X over (+P%)` / `-$X under (-P%)` / `exact`, or the typed
/// "—" when the vendor side is absent (no fabricated delta). The percentage is ROUNDED here,
/// at the render boundary — full `Decimal` precision is preserved upstream by the engine.
/// `billed_usd` is the vendor figure the percentage is relative to (its denominator); it's
/// used only to catch the sub-cent-denominator case below.
#[cfg(any(feature = "connect", test))]
fn reconcile_variance_cell(
    variance: Option<UsdAmount>,
    variance_pct: Option<Decimal>,
    billed_usd: Option<Decimal>,
    options: RenderOptions,
) -> String {
    let variance = match variance {
        Some(variance) => variance.as_usd(),
        // Typed vendor-side absence → "—" (folded to "-" outside braille), never "$0.00".
        None => return dash(options).to_string(),
    };
    if variance.is_zero() {
        return "exact".to_string();
    }
    let over = variance > Decimal::ZERO;
    let word = if over { "over" } else { "under" };
    let abs = variance.abs();
    // A genuinely non-zero variance whose dollar magnitude rounds below a cent: show
    // "<$0.01" (no numeric sign — the over/under word carries direction) so the row never
    // reads as a misleading "+$0.00 over". Real vendor bills are sub-cent (Anthropic bills
    // fractions of a cent), so this is reachable, not theoretical.
    let variance_subcent = abs.round_dp(2).is_zero();
    let money = if variance_subcent {
        format!("<$0.01 {word}")
    } else {
        let sign = if over { "+" } else { "-" };
        format!("{sign}{} {word}", format_money(&abs, Some("USD"), false))
    };
    match variance_pct {
        // A ≥ $0.01 variance against a billed figure that itself rounds below a cent (so the
        // invoice cell shows "$0.00") makes the percentage explode against an effectively-
        // invisible denominator (e.g. "+$1.40 over (+69950.0%)" beside "inv $0.00"). Name the
        // denominator's magnitude instead — parallel to the "(vs $0 billed)" case below. A
        // sub-cent variance keeps its percentage: those magnitudes are coherent and small.
        Some(_)
            if !variance_subcent
                && matches!(billed_usd, Some(billed) if !billed.is_zero() && billed.round_dp(2).is_zero()) =>
        {
            format!("{money} (vs <$0.01 billed)")
        }
        Some(pct) => format!("{money} ({})", format_signed_pct(pct)),
        // The vendor billed $0 → the percentage is undefined; the signed dollar still stands.
        None => format!("{money} (vs $0 billed)"),
    }
}

/// A percentage rounded to one decimal at the render boundary, with an explicit leading sign
/// and a uniform single fractional digit (so `-100.0%` lines up with `+20.0%`). The sign is
/// taken from the **unrounded** value, so a tiny negative that rounds to `0.0` still reads
/// `-0.0%` (sign-consistent with its `under`) rather than flipping to `+0.0%`.
#[cfg(any(feature = "connect", test))]
fn format_signed_pct(pct: Decimal) -> String {
    let mut magnitude = pct.abs().round_dp(1);
    magnitude.rescale(1);
    let sign = if pct.is_sign_negative() { "-" } else { "+" };
    format!("{sign}{magnitude}%")
}

/// The reconciliation section rule: an ASCII `-` outside braille (Plain is the accessibility
/// floor and must be pure ASCII, unlike the shared `push_rule` which keeps `─` in Plain).
#[cfg(any(feature = "connect", test))]
fn push_reconcile_rule(out: &mut StyledDocument, options: RenderOptions) {
    let glyph = if options.mode == RenderMode::Braille {
        "─"
    } else {
        "-"
    };
    push_line(out, &glyph.repeat(options.width.max(1)));
}

/// Section totals: estimated dollars (always summed) and billed dollars (only when a report
/// was available — `None` folds the invoice total out of the header).
#[cfg(any(feature = "connect", test))]
fn reconciliation_totals(recon: &CostReconciliation) -> (Decimal, Option<Decimal>) {
    let total_est = recon
        .days
        .iter()
        .fold(Decimal::ZERO, |acc, day| acc + day.local_estimate.as_usd());
    let total_billed = match recon.report {
        ReconciledReportStatus::Available => Some(recon.days.iter().fold(
            Decimal::ZERO,
            |acc, day| match &day.vendor_billed {
                VendorBilled::Billed(amount) => acc + amount.as_usd(),
                VendorBilled::Unavailable(_) => acc,
            },
        )),
        ReconciledReportStatus::Unavailable(_) => None,
    };
    (total_est, total_billed)
}

/// The carried-through caveats, footnoted (they survive on `CostReconciliation.caveats`).
#[cfg(any(feature = "connect", test))]
fn push_reconciliation_footnotes(
    out: &mut StyledDocument,
    recon: &CostReconciliation,
    options: RenderOptions,
) {
    if recon.caveats.per_model_derived_best_effort {
        push_line(
            out,
            &fold_ascii(
                "* OpenAI per-model figures are best-effort (derived from line items).",
                options,
            ),
        );
    }
    if recon.caveats.priority_tier_absent {
        push_line(
            out,
            &fold_ascii(
                "Note: Anthropic Priority-Tier spend isn't in this report — the bill may be higher.",
                options,
            ),
        );
    }
    // When a report is available but doesn't span every local day, the header `inv` total
    // covers only the spanned days while `est` covers all of them — footnote it so the
    // headline pair isn't misread as a real over-estimate.
    let some_day_uncovered = matches!(recon.report, ReconciledReportStatus::Available)
        && recon.days.iter().any(|day| {
            matches!(
                day.vendor_billed,
                VendorBilled::Unavailable(BilledAbsence::DayNotCovered)
            )
        });
    if some_day_uncovered {
        push_line(
            out,
            "Note: the invoice total covers only the days this report spans; days outside it show \"report doesn't cover this day\".",
        );
    }
}

/// The absence/placeholder dash — an em-dash on a braille TTY, an ASCII hyphen otherwise.
#[cfg(any(feature = "connect", test))]
fn dash(options: RenderOptions) -> &'static str {
    if options.mode == RenderMode::Braille {
        "—"
    } else {
        "-"
    }
}

/// Fold the handful of non-ASCII glyphs reconciliation copy can carry (`—`, `…`, `×`, `·`)
/// to ASCII for the Ascii/Plain modes — Plain is the accessibility floor (every byte ASCII),
/// and Ascii targets non-UTF-8 terminals — while a braille TTY keeps them.
#[cfg(any(feature = "connect", test))]
fn fold_ascii(value: &str, options: RenderOptions) -> String {
    if options.mode == RenderMode::Braille {
        value.to_string()
    } else {
        value
            .replace('—', "-")
            .replace('…', "...")
            .replace('×', "x")
            .replace('·', "-")
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
                "{} {} {}   as of {}",
                frontier.name,
                // The em-dash is not ASCII — substitute in the Ascii fallback mode.
                if options.mode == RenderMode::Ascii {
                    "-"
                } else {
                    "—"
                },
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
            push_line(&mut out, &format!("  {}", point_line(point, options)));
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
    push_line(
        &mut out,
        &mode_insight(frontier_insight_line(view), options),
    );
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
                "{} - {}, source {}, as of {}",
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

fn point_line(point: &FrontierPoint, options: RenderOptions) -> String {
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
        .map(|note| {
            // The em-dash is not ASCII — substitute in the Ascii fallback mode.
            let separator = if options.mode == RenderMode::Ascii {
                "--"
            } else {
                "—"
            };
            format!("  {separator} {note}")
        })
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
        .map(|note| format!(" -- {note}"))
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
    // An unpriced baseline carries a placeholder $0/undercount — render the gap,
    // never the misleading dollar figure or a comparison against it.
    if !model.fully_priced {
        return StyledLine::plain(format!(
            "  {}  spend not fully priced (frontier comparison unavailable)",
            model.model_id
        ));
    }
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
    if !model.fully_priced {
        return format!(
            "{}: spend not fully priced (frontier comparison unavailable)",
            model.model_id
        );
    }
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
            "; {} costs about {} less at equal volume",
            delta.target_model_id, money
        )
    } else if delta.delta_usd > Decimal::ZERO {
        format!(
            "; {} costs about {} more at equal volume",
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

#[cfg(test)]
pub(crate) fn render_providers(
    capabilities: &[ProviderCapabilityView],
    statuses: &[ProviderStatus],
    options: RenderOptions,
) -> String {
    render_providers_document(capabilities, statuses, options).render(options)
}

/// The Providers tab (T11): per provider, each data lane's declared source (§2b
/// `Capability`), how it authenticates, the quota windows it can report, and its detection
/// health — i.e. *what is available, what is unavailable, and why*. The first production
/// consumer of the `Capability` descriptor. Honest by construction: a lane with no clean
/// source renders "no sanctioned source", never a fabricated one.
///
/// The connect-gated connection lane (your own usage-API keys) is appended separately by
/// [`push_provider_connection_lane`] under `--features connect`; the default build renders
/// the local `Capability`/`ProviderStatus` alone.
pub(crate) fn render_providers_document(
    capabilities: &[ProviderCapabilityView],
    statuses: &[ProviderStatus],
    options: RenderOptions,
) -> StyledDocument {
    match options.mode {
        RenderMode::Plain => render_providers_plain_document(capabilities, statuses),
        RenderMode::Braille | RenderMode::Ascii => {
            render_providers_visual_document(capabilities, statuses, options)
        }
    }
}

fn render_providers_visual_document(
    capabilities: &[ProviderCapabilityView],
    statuses: &[ProviderStatus],
    options: RenderOptions,
) -> StyledDocument {
    let mut out = StyledDocument::new();
    let mut header = StyledLine::new();
    header.push_plain(format!("{} costroid", mark(options)));
    header.push_styled("  providers", SemanticStyle::Strong);
    out.push(header);

    if capabilities.is_empty() {
        push_rule(&mut out, options);
        push_line(&mut out, "no providers to describe");
        return out;
    }

    for capability in capabilities {
        push_rule(&mut out, options);
        let status = find_status(statuses, capability.provider);
        let mut head = StyledLine::new();
        head.push_styled(provider_name(capability.provider), SemanticStyle::Strong);
        head.push_plain(format!(" ({})", provider_state_word(status)));
        out.push(head);
        push_line(
            &mut out,
            &format!("  api cost   {}", data_source_phrase(capability.api_cost)),
        );
        push_line(
            &mut out,
            &format!("  quota      {}", quota_phrase(capability)),
        );
        push_line(
            &mut out,
            &format!("  model mix  {}", data_source_phrase(capability.model_mix)),
        );
        push_line(
            &mut out,
            &format!("  auth       {}", auth_phrase(capability.auth)),
        );
        if let Some(note) = status.and_then(|status| status.message.as_deref()) {
            push_line(&mut out, &format!("  note: {note}"));
        }
    }
    out
}

fn render_providers_plain_document(
    capabilities: &[ProviderCapabilityView],
    statuses: &[ProviderStatus],
) -> StyledDocument {
    let mut out = StyledDocument::new();
    push_line(&mut out, "costroid providers");
    if capabilities.is_empty() {
        push_line(&mut out, "no providers to describe");
        return out;
    }
    for capability in capabilities {
        let status = find_status(statuses, capability.provider);
        push_line(
            &mut out,
            &format!(
                "{} ({}):",
                provider_name(capability.provider),
                provider_state_word(status)
            ),
        );
        push_line(
            &mut out,
            &format!("  api cost: {}", data_source_phrase(capability.api_cost)),
        );
        push_line(&mut out, &format!("  quota: {}", quota_phrase(capability)));
        push_line(
            &mut out,
            &format!("  model mix: {}", data_source_phrase(capability.model_mix)),
        );
        push_line(
            &mut out,
            &format!("  auth: {}", auth_phrase(capability.auth)),
        );
        if let Some(note) = status.and_then(|status| status.message.as_deref()) {
            push_line(&mut out, &format!("  note: {note}"));
        }
    }
    out
}

fn find_status(statuses: &[ProviderStatus], provider: ProviderId) -> Option<&ProviderStatus> {
    statuses.iter().find(|status| status.provider == provider)
}

/// The detection-health word for a provider, joining its `ProviderStatus` (if collected)
/// to its capability. A provider with no status row (never collected) reads "not detected"
/// rather than a fabricated state.
fn provider_state_word(status: Option<&ProviderStatus>) -> &'static str {
    match status {
        Some(status) => provider_status(status.status),
        None => "not detected",
    }
}

/// Author-written human copy for a [`DataSource`] (T11 card). Pure ASCII so the Providers
/// tab renders identically in `--plain`; phrased to match `cursor_detected_message`
/// ("no sanctioned source").
fn data_source_phrase(source: DataSource) -> &'static str {
    match source {
        DataSource::LocalArtifact => "from local logs",
        DataSource::SanctionedHook => "from the statusLine capture; run setup-statusline",
        // Tiers 2-3 of the auth ladder both surface as the user's connected credential.
        DataSource::SanctionedOauth | DataSource::ApiKey => "via your connected key",
        DataSource::Unavailable => "no sanctioned source",
    }
}

/// Author-written human copy for an [`AuthMethod`] (T11 card). Pure ASCII.
fn auth_phrase(auth: AuthMethod) -> &'static str {
    match auth {
        AuthMethod::None => "no login required",
        AuthMethod::Oauth => "sanctioned OAuth",
        AuthMethod::ApiKey => "your own API key",
    }
}

/// The quota line: the subscription-quota source plus the windows the provider can report.
/// An empty `quota_kinds` carries no window suffix (e.g. Cursor, which keeps no local quota
/// window), so the source phrase ("no sanctioned source") stands alone.
fn quota_phrase(capability: &ProviderCapabilityView) -> String {
    let source = data_source_phrase(capability.subscription_quota);
    if capability.quota_kinds.is_empty() {
        source.to_string()
    } else {
        let kinds = capability
            .quota_kinds
            .iter()
            .map(|kind| limit_kind(*kind))
            .collect::<Vec<_>>()
            .join(", ");
        format!("{source} ({kinds})")
    }
}

/// A single billing-vendor's connection state for the Providers tab's connection lane,
/// gathered read-only over the existing keychain/registry (no network). Carries only the
/// non-secret org label — NEVER key material. Connect-gated: the default build never
/// compiles it, so it links no `costroid-connect` symbols.
#[cfg(feature = "connect")]
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) struct ConnectionEntry {
    pub(crate) vendor: String,
    pub(crate) state: ConnectionState,
}

#[cfg(feature = "connect")]
#[derive(Debug, Clone, PartialEq, Eq)]
pub(crate) enum ConnectionState {
    /// Linked: the key is present in the OS keychain AND the registry marks it connected
    /// (the dual gate). `org` is the non-secret organization label, if captured.
    Connected {
        org: Option<String>,
    },
    NotConnected,
    /// No sanctioned source (e.g. Gemini): carries the pinned unavailable message verbatim.
    Unavailable(String),
}

/// Append the connect-gated connection lane (your own usage-API keys) to the Providers tab.
/// Called only under `--features connect`; renders the org label + connected/not state,
/// NEVER key material. Pure ASCII in `--plain`/Ascii (the pinned Gemini message and any
/// server-supplied org label are folded via [`fold_for_ascii`]); the em-dash is kept only
/// on a UTF-8 (Braille) TTY.
#[cfg(feature = "connect")]
pub(crate) fn push_provider_connection_lane(
    out: &mut StyledDocument,
    connections: &[ConnectionEntry],
    options: RenderOptions,
) {
    if connections.is_empty() {
        return;
    }
    // Plain mode delimits sections by labels alone (no rules), like the other plain docs;
    // the visual modes get the horizontal rule.
    if options.mode != RenderMode::Plain {
        push_rule(out, options);
    }
    push_line(out, "connections (your own usage API keys)");
    let dash = if options.mode == RenderMode::Braille {
        "—"
    } else {
        "-"
    };
    for entry in connections {
        let detail = match &entry.state {
            ConnectionState::Connected { org: Some(org) } => {
                format!("connected {dash} organization {org}")
            }
            ConnectionState::Connected { org: None } => "connected".to_string(),
            ConnectionState::NotConnected => "not connected".to_string(),
            ConnectionState::Unavailable(message) => message.clone(),
        };
        push_line(
            out,
            &fold_for_ascii(&format!("  {:<10} {detail}", entry.vendor), options),
        );
    }
}

/// Strip control characters always, and for `--plain`/Ascii fold the known glyphs (em-dash,
/// ellipsis) and map any remaining non-ASCII to `?` — mirroring the connect command's
/// `emit`, so the connection lane is guaranteed pure ASCII there regardless of a
/// server-supplied org label. Braille keeps printable Unicode.
#[cfg(feature = "connect")]
fn fold_for_ascii(value: &str, options: RenderOptions) -> String {
    let sanitized: String = value.chars().filter(|ch| !ch.is_control()).collect();
    match options.mode {
        RenderMode::Braille => sanitized,
        RenderMode::Ascii | RenderMode::Plain => sanitized
            .replace('—', "-")
            .replace('…', "...")
            .chars()
            .map(|ch| if ch.is_ascii() { ch } else { '?' })
            .collect(),
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
                // An unverified window may be the most-constrained pick (brief §8); it must
                // carry the `? unverified` cue and a neutral meter, never a confident `!!`.
                let unverified = matches!(limit.availability, LimitAvailability::Unverified { .. });
                let cue = if unverified {
                    UNVERIFIED_CUE.to_string()
                } else {
                    state_cue(limit_state(fraction)).to_string()
                };
                let reset = reset
                    .map(|seconds| format!(" {}", compact_reset(seconds)))
                    .unwrap_or_default();
                let mut line = StyledLine::new();
                line.push_plain(format!("{} {spend}{api_state}  ", mark(options)));
                line.spans.push(limit_meter_with_confidence(
                    fraction,
                    unverified,
                    STATUS_BAR_WIDTH,
                    options,
                ));
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
            out.push(render_limit_line(limit, summary.generated_at, options));
            if let Some(caveat) = claude_caveat(limit) {
                push_line(&mut out, &format!("    {caveat}"));
            }
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
    push_line(
        &mut out,
        &mode_insight(insight_line(&api, &summary.limits), options),
    );
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
            push_line(&mut out, &plain_limit_line(limit, summary.generated_at));
            if let Some(caveat) = claude_caveat(limit) {
                push_line(&mut out, &format!("    {caveat}"));
            }
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
    push_line(&mut out, &mode_insight(insight_line(&api, &[]), options));
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

/// The token-fraction carried by a [`LimitMeasure`], if any. `Spend` measures meter a
/// dollar pool, not a fraction, so they return `None` and are never selected as the
/// "most constrained" window (they render their own dollar line instead).
fn measure_fraction(measure: &LimitMeasure) -> Option<f64> {
    match measure {
        LimitMeasure::TokenFraction(fraction) => Some(*fraction),
        LimitMeasure::Spend { .. } => None,
    }
}

/// The always-on "as of HH:MM" freshness stamp (brief §8): once a reading is at least
/// [`LIMIT_FRESHNESS_STAMP_MINUTES`] old it must carry an age signal so it never renders
/// as a bare, confident meter. Empty for a still-fresh reading. Formatted in **UTC** so
/// snapshots stay timezone-deterministic (the suite avoids env mutation).
fn freshness_stamp(captured_at: DateTime<Utc>, generated_at: DateTime<Utc>) -> String {
    // The UNIX-epoch sentinel means "no observation instant recorded" (a cache or
    // rollout entry with a missing/unparseable capture time). Stamping it would render
    // a bogus, confident "as of 00:00" — disclose the unknown age instead.
    if captured_at.timestamp() == 0 {
        return "  capture time unknown".to_string();
    }
    if (generated_at - captured_at).num_minutes() >= LIMIT_FRESHNESS_STAMP_MINUTES {
        format!("  as of {}", captured_at.format("%H:%M"))
    } else {
        String::new()
    }
}

/// The Claude chat-under-report caveat for a Claude window that shows usage, else `None`.
/// The now-screen builders render it as an indented sub-note line. Covers `Available` /
/// `Unverified` (the meter reads low — brief §8) AND `Estimated` (the volume shown is
/// Claude-Code-only and must not be misread as account-wide — brief §6 requires the
/// "excludes claude.ai chat" disclosure on the absent→estimate fallback).
fn claude_caveat(limit: &LimitSummary) -> Option<&'static str> {
    let shows_usage = matches!(
        limit.availability,
        LimitAvailability::Available { .. }
            | LimitAvailability::Unverified { .. }
            | LimitAvailability::Estimated { .. }
    );
    (limit.tool == ProviderId::ClaudeCode && shows_usage).then_some(CLAUDE_CHAT_CAVEAT)
}

/// A meter span that never alarm-colors an `unverified` reading — an unverified near-max
/// must draw as a neutral bar, not a confident red one (brief §8). Verified readings keep
/// the state-based color from [`limit_meter_span`].
fn limit_meter_with_confidence(
    fraction: f64,
    unverified: bool,
    width: usize,
    options: RenderOptions,
) -> StyledSpan {
    if unverified {
        StyledSpan::styled(
            positional_meter_text(fraction, width, options),
            SemanticStyle::Strong,
        )
    } else {
        limit_meter_span(fraction, width, options)
    }
}

/// Format a `Spend` dollar pool as "$used / $included used", or "$used used" when there is
/// no published allowance — never fabricate a denominator (brief §6/§8).
fn spend_text(used_usd: &Decimal, included_usd: &Option<Decimal>) -> String {
    match included_usd {
        Some(included) => format!(
            "{} / {} used",
            format_money(used_usd, Some("USD"), false),
            format_money(included, Some("USD"), false)
        ),
        None => format!("{} used", format_money(used_usd, Some("USD"), false)),
    }
}

/// The `Estimated` window's local token volume (thousands-grouped for readability).
fn estimated_volume_text(volume_tokens: u64) -> String {
    format!("{} tokens", with_thousands(&volume_tokens.to_string()))
}

/// The estimated dollar value suffix for an `Estimated` window: "(~$value, estimated)"
/// when priced, or "(estimated)" alone when the model is unpriced — never a guessed price.
fn estimated_value_suffix(estimated_usd: &Option<Decimal>) -> String {
    match estimated_usd {
        Some(value) => format!(" ({}, estimated)", format_money(value, Some("USD"), true)),
        None => " (estimated)".to_string(),
    }
}

fn render_limit_line(
    limit: &LimitSummary,
    generated_at: DateTime<Utc>,
    options: RenderOptions,
) -> StyledLine {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    let stamp = freshness_stamp(limit.captured_at, generated_at);
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure {
            LimitMeasure::TokenFraction(fraction) => {
                let fraction = *fraction;
                let cue = state_cue(limit_state(fraction));
                let mut line = StyledLine::new();
                line.push_plain(format!("  {tool:<12} {kind:<3} "));
                line.spans
                    .push(limit_meter_span(fraction, LIMIT_BAR_WIDTH, options));
                line.push_plain(format!(
                    "  {}{}  resets {}{}",
                    percent(fraction),
                    cue,
                    reset_countdown(*reset_in_seconds),
                    stamp
                ));
                line
            }
            LimitMeasure::Spend {
                used_usd,
                included_usd,
            } => StyledLine::plain(format!(
                "  {tool:<12} {kind:<3} {}  resets {}{}",
                spend_text(used_usd, included_usd),
                reset_countdown(*reset_in_seconds),
                stamp
            )),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!("  resets {value}"))
                .unwrap_or_default();
            // A Partial reading still carries an observation instant — give it the same
            // age signal as Available/Unverified (an arbitrarily old % must never render
            // with zero age cue; measure-less Partial has no reading to age).
            match measure {
                Some(LimitMeasure::TokenFraction(fraction)) => {
                    let fraction = *fraction;
                    let cue = state_cue(limit_state(fraction));
                    let mut line = StyledLine::new();
                    line.push_plain(format!("  {tool:<12} {kind:<3} "));
                    line.spans
                        .push(limit_meter_span(fraction, LIMIT_BAR_WIDTH, options));
                    line.push_plain(format!(
                        "  {}{}  partial: {}{}{}",
                        percent(fraction),
                        cue,
                        reason,
                        reset,
                        stamp
                    ));
                    line
                }
                Some(LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                }) => StyledLine::plain(format!(
                    "  {tool:<12} {kind:<3} {}  partial: {}{}{}",
                    spend_text(used_usd, included_usd),
                    reason,
                    reset,
                    stamp
                )),
                None => {
                    StyledLine::plain(format!("  {tool:<12} {kind:<3} partial: {reason}{reset}"))
                }
            }
        }
        LimitAvailability::Unverified {
            measure,
            reset_in_seconds,
            ..
        } => {
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!("  resets {value}"))
                .unwrap_or_default();
            match measure {
                LimitMeasure::TokenFraction(fraction) => {
                    let fraction = *fraction;
                    let mut line = StyledLine::new();
                    line.push_plain(format!("  {tool:<12} {kind:<3} "));
                    line.spans.push(limit_meter_with_confidence(
                        fraction,
                        true,
                        LIMIT_BAR_WIDTH,
                        options,
                    ));
                    line.push_plain(format!(
                        "  {}{}{}{}",
                        percent(fraction),
                        UNVERIFIED_CUE,
                        reset,
                        stamp
                    ));
                    line
                }
                LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                } => StyledLine::plain(format!(
                    "  {tool:<12} {kind:<3} {}{}{}{}",
                    spend_text(used_usd, included_usd),
                    UNVERIFIED_CUE,
                    reset,
                    stamp
                )),
            }
        }
        LimitAvailability::Estimated {
            volume_tokens,
            estimated_usd,
        } => StyledLine::plain(format!(
            "  {tool:<12} {kind:<3} usage: {}{} {} quota % unavailable",
            estimated_volume_text(*volume_tokens),
            estimated_value_suffix(estimated_usd),
            // The em-dash is not ASCII — substitute in the Ascii fallback mode.
            if options.mode == RenderMode::Ascii {
                "--"
            } else {
                "—"
            }
        )),
        LimitAvailability::Unavailable { reason } => {
            StyledLine::plain(format!("  {tool:<12} {kind:<3} unavailable: {reason}"))
        }
    }
}

fn plain_limit_line(limit: &LimitSummary, generated_at: DateTime<Utc>) -> String {
    let tool = provider_name(limit.tool);
    let kind = limit_kind(limit.kind);
    let stamp = freshness_stamp(limit.captured_at, generated_at);
    match &limit.availability {
        LimitAvailability::Available {
            measure,
            reset_in_seconds,
            ..
        } => match measure {
            LimitMeasure::TokenFraction(fraction) => {
                let cue = plain_state_phrase(limit_state(*fraction));
                format!(
                    "  {tool} {kind}: {} used{cue}, resets in {}{stamp}",
                    percent(*fraction),
                    reset_countdown(*reset_in_seconds)
                )
            }
            LimitMeasure::Spend {
                used_usd,
                included_usd,
            } => format!(
                "  {tool} {kind}: {}, resets in {}{stamp}",
                spend_text(used_usd, included_usd),
                reset_countdown(*reset_in_seconds)
            ),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = match measure {
                Some(LimitMeasure::TokenFraction(fraction)) => format!(
                    "{} used{}",
                    percent(*fraction),
                    plain_state_phrase(limit_state(*fraction))
                ),
                Some(LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                }) => spend_text(used_usd, included_usd),
                None => "usage unknown".to_string(),
            };
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            // A Partial reading still carries an observation instant — give it the same
            // age signal as Available/Unverified (an arbitrarily old % must never render
            // with zero age cue; measure-less Partial has no reading to age).
            let stamp = if measure.is_some() {
                stamp
            } else {
                String::new()
            };
            format!("  {tool} {kind}: partial, {usage}{reset}, {reason}{stamp}")
        }
        LimitAvailability::Unverified {
            measure,
            reset_in_seconds,
            ..
        } => {
            let usage = match measure {
                LimitMeasure::TokenFraction(fraction) => format!("{} used", percent(*fraction)),
                LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                } => spend_text(used_usd, included_usd),
            };
            let reset = reset_in_seconds
                .map(reset_countdown)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("  {tool} {kind}: {usage}{UNVERIFIED_CUE}{reset}{stamp}")
        }
        LimitAvailability::Estimated {
            volume_tokens,
            estimated_usd,
        } => format!(
            "  {tool} {kind}: usage {}{}, quota % unavailable",
            estimated_volume_text(*volume_tokens),
            estimated_value_suffix(estimated_usd)
        ),
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
        } => match measure {
            LimitMeasure::TokenFraction(fraction) => format!(
                "{tool} {kind} {} used{}, resets in {}",
                percent(*fraction),
                plain_state_phrase(limit_state(*fraction)),
                compact_reset(*reset_in_seconds)
            ),
            LimitMeasure::Spend {
                used_usd,
                included_usd,
            } => format!(
                "{tool} {kind} {}, resets in {}",
                spend_text(used_usd, included_usd),
                compact_reset(*reset_in_seconds)
            ),
        },
        LimitAvailability::Partial {
            measure,
            reset_in_seconds,
            reason,
            ..
        } => {
            let usage = match measure {
                Some(LimitMeasure::TokenFraction(fraction)) => format!(
                    "{}{}",
                    percent(*fraction),
                    plain_state_phrase(limit_state(*fraction))
                ),
                Some(LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                }) => spend_text(used_usd, included_usd),
                None => "unknown usage".to_string(),
            };
            let reset = reset_in_seconds
                .map(compact_reset)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("{tool} {kind} partial, {usage}{reset}, {reason}")
        }
        LimitAvailability::Unverified {
            measure,
            reset_in_seconds,
            ..
        } => {
            let usage = match measure {
                LimitMeasure::TokenFraction(fraction) => format!("{} used", percent(*fraction)),
                LimitMeasure::Spend {
                    used_usd,
                    included_usd,
                } => spend_text(used_usd, included_usd),
            };
            let reset = reset_in_seconds
                .map(compact_reset)
                .map(|value| format!(", resets in {value}"))
                .unwrap_or_default();
            format!("{tool} {kind} {usage}{UNVERIFIED_CUE}{reset}")
        }
        LimitAvailability::Estimated {
            volume_tokens,
            estimated_usd,
        } => format!(
            "{tool} {kind} usage {}{}, quota % unavailable",
            estimated_volume_text(*volume_tokens),
            estimated_value_suffix(estimated_usd)
        ),
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
        // An Unverified window IS eligible to be the most-constrained pick (brief §8); the
        // statusline carries its `? unverified` cue so a maxed-looking reading is never
        // shown as confident. `Estimated` has no fraction → excluded, like `Unavailable`.
        LimitAvailability::Unverified { measure, .. } => measure_fraction(measure),
        LimitAvailability::Partial { measure: None, .. }
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
        LimitAvailability::Unverified {
            measure,
            reset_in_seconds,
            ..
        } => (measure_fraction(measure).unwrap_or(0.0), *reset_in_seconds),
        LimitAvailability::Estimated { .. } | LimitAvailability::Unavailable { .. } => (0.0, None),
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
    // RenderMode::Ascii targets terminals that may not render multi-byte glyphs
    // (chosen when braille capability — incl. a UTF-8 locale — is absent), so the
    // horizontal rule must be pure ASCII there.
    let glyph = if options.mode == RenderMode::Ascii {
        "-"
    } else {
        "─"
    };
    push_line(out, &glyph.repeat(options.width.max(1)));
}

/// Swap the ◆ insight marker for a pure-ASCII one in [`RenderMode::Ascii`] (plain mode
/// replaces it with "insight:" separately).
fn mode_insight(line: String, options: RenderOptions) -> String {
    if options.mode == RenderMode::Ascii {
        line.replace('◆', "*")
    } else {
        line
    }
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
            // An unverified near-max must not read as a confident insight (brief §8) — flag it.
            let flag = if matches!(limit.availability, LimitAvailability::Unverified { .. }) {
                " (unverified)"
            } else {
                ""
            };
            return format!(
                "◆ {} {} at {}{}, resets {}.",
                provider_name(limit.tool),
                limit_kind(limit.kind),
                percent(fraction),
                flag,
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
    // Short window labels (fit the `{kind:<3}` column). Daily/Monthly/BillingCycle
    // have no producer yet (no adapter emits them); revisit their abbreviations when
    // the first one lands (e.g. a Cursor billing-cycle window).
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
    use chrono::{Duration, LocalResult, NaiveDate, TimeZone, Utc};
    use costroid_core::{
        reconcile_cost, AggregateTotals, CostLaneSummary, CostLineItem, CostReportCaveats,
        CostReportOutcome, GroupKey, LimitSummary, LocalCostEstimate, NowSummary, PeriodRange,
        PricingCoverage, ProviderStatus, TrendsSummary, VendorCostDay, VendorCostReport,
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

    /// A bucket start the way production builds them — a LOCAL midnight (core's
    /// `start_of_period_local`) — so the trends snapshot's `%Y-%m-%d` label (formatted
    /// in Local, the documented product behavior) is the same date in every timezone
    /// the suite runs in. A UTC-midnight fixture would render the previous day in any
    /// UTC-negative zone and break the snapshot.
    fn local_midnight_utc(year: i32, month: u32, day: u32) -> DateTime<Utc> {
        match Local.with_ymd_and_hms(year, month, day, 0, 0, 0) {
            LocalResult::Single(value) | LocalResult::Ambiguous(value, _) => {
                value.with_timezone(&Utc)
            }
            LocalResult::None => panic!("test local midnight should exist"),
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
                // Mirrors the descriptive label `unavailable_limit` emits since the
                // F26 fix (never the redundant "unavailable: unavailable").
                label: Some("no captured reading; run `costroid setup-statusline`".to_string()),
                captured_at: now,
                availability: LimitAvailability::Unavailable {
                    reason: "no captured reading; run `costroid setup-statusline`".to_string(),
                },
            },
            LimitSummary {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::FiveHour,
                label: None,
                captured_at: now,
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
                captured_at: now,
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
                captured_at: now,
                availability: LimitAvailability::Partial {
                    measure: Some(LimitMeasure::TokenFraction(0.97)),
                    resets_at: None,
                    reset_in_seconds: None,
                    reason: "limit data incomplete".to_string(),
                },
            },
        ]
    }

    /// Every availability arm + both `LimitMeasure` shapes in one fixture so the snapshots
    /// prove T6 renders all five distinctly and uniformly across providers (brief §8/§9).
    /// All "live" readings are captured 30 min before `generated_at` so the always-on
    /// "as of HH:MM" stamp fires (threshold is 10 min); UTC keeps the stamp deterministic.
    fn all_arms_limits() -> Vec<LimitSummary> {
        let now = utc(2026, 6, 2, 9, 0);
        let captured = now - Duration::minutes(30);
        vec![
            // Available, TokenFraction — Claude: meter + stamp + chat caveat.
            LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: Some("max".to_string()),
                kind: LimitKind::FiveHour,
                label: None,
                captured_at: captured,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.55),
                    resets_at: now + Duration::hours(2),
                    reset_in_seconds: 2 * 60 * 60,
                },
            },
            // Unverified, TokenFraction — Claude near-max, cross-check failed: neutral meter +
            // "? unverified" (never "!!") + stamp + caveat.
            LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: Some("max".to_string()),
                kind: LimitKind::Weekly,
                label: None,
                captured_at: captured,
                availability: LimitAvailability::Unverified {
                    measure: LimitMeasure::TokenFraction(0.96),
                    resets_at: Some(now + Duration::hours(50)),
                    reset_in_seconds: Some(50 * 60 * 60),
                },
            },
            // Available, TokenFraction — Codex: proves the stamp + render are uniform, not
            // Claude-only (no caveat — caveat is Claude-only).
            LimitSummary {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::FiveHour,
                label: None,
                captured_at: captured,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.40),
                    resets_at: now + Duration::minutes(41),
                    reset_in_seconds: 41 * 60,
                },
            },
            // Available, Spend — a dollar credit pool with NO meter, NEVER a fabricated %.
            LimitSummary {
                tool: ProviderId::Cursor,
                plan: Some("pro".to_string()),
                kind: LimitKind::Monthly,
                label: None,
                captured_at: captured,
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::Spend {
                        used_usd: Decimal::new(1850, 2),
                        included_usd: Some(Decimal::new(2000, 2)),
                    },
                    resets_at: now + Duration::days(12),
                    reset_in_seconds: 12 * 24 * 60 * 60,
                },
            },
            // Partial, TokenFraction — incomplete but not flagged (reset unknown).
            LimitSummary {
                tool: ProviderId::Cursor,
                plan: Some("pro".to_string()),
                kind: LimitKind::Weekly,
                label: None,
                captured_at: captured,
                availability: LimitAvailability::Partial {
                    measure: Some(LimitMeasure::TokenFraction(0.88)),
                    resets_at: None,
                    reset_in_seconds: None,
                    reason: "limit data incomplete".to_string(),
                },
            },
            // Estimated WITH a priced value — volume + ~$value, quota % unavailable, no meter.
            LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: Some("max".to_string()),
                kind: LimitKind::Daily,
                label: None,
                captured_at: now,
                availability: LimitAvailability::Estimated {
                    volume_tokens: 1_234_567,
                    estimated_usd: Some(Decimal::new(1234, 2)),
                },
            },
            // Estimated WITHOUT a value — unpriced model → volume alone, never a guessed price.
            LimitSummary {
                tool: ProviderId::Codex,
                plan: Some("plus".to_string()),
                kind: LimitKind::Daily,
                label: None,
                captured_at: now,
                availability: LimitAvailability::Estimated {
                    volume_tokens: 5_000,
                    estimated_usd: None,
                },
            },
            // Unavailable — unchanged arm, no meter, no stamp.
            LimitSummary {
                tool: ProviderId::Cursor,
                plan: None,
                kind: LimitKind::BillingCycle,
                label: Some("no sanctioned source".to_string()),
                captured_at: now,
                availability: LimitAvailability::Unavailable {
                    reason: "no sanctioned source".to_string(),
                },
            },
        ]
    }

    fn all_arms_now() -> NowSummary {
        let mut summary = priced_now();
        summary.limits = all_arms_limits();
        summary
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

    /// The three providers' honest capability descriptors (mirroring the as-built
    /// `capability()` impls) for the Providers tab (T11).
    fn provider_capabilities_fixture() -> Vec<ProviderCapabilityView> {
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

    /// Detection health spanning the arms: Claude available (no note), Codex missing (a
    /// "no local data" note), Cursor detected (the BETA detect-and-defer note). The Cursor
    /// row pins the card's "detected, never coming soon" requirement.
    fn provider_statuses_for_tab() -> Vec<ProviderStatus> {
        vec![
            ProviderStatus {
                provider: ProviderId::ClaudeCode,
                status: ProviderStatusKind::Available,
                files: 2,
                usage_events: 4,
                focus_rows: 9,
                limit_windows: 2,
                message: None,
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
            ProviderStatus {
                provider: ProviderId::Cursor,
                status: ProviderStatusKind::Detected,
                files: 1,
                usage_events: 0,
                focus_rows: 0,
                limit_windows: 0,
                message: Some(
                    "BETA - model Composer 2.5 Fast (composer-2.5), logged in; \
                     usage unavailable - no sanctioned source; \
                     quota unavailable - no sanctioned source"
                        .to_string(),
                ),
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
        let week_one = range(local_midnight_utc(2026, 6, 1), 7);
        let week_two = range(local_midnight_utc(2026, 6, 8), 7);
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

    // ----- T6: the five availability arms + Spend, across all four render modes -----

    #[test]
    fn snapshot_now_all_arms_braille() {
        insta::assert_snapshot!(render_now(&all_arms_now(), RenderOptions::braille(true)));
    }

    #[test]
    fn snapshot_now_all_arms_braille_no_ansi() {
        insta::assert_snapshot!(render_now(&all_arms_now(), RenderOptions::braille(false)));
    }

    #[test]
    fn snapshot_now_all_arms_ascii() {
        insta::assert_snapshot!(render_now(&all_arms_now(), RenderOptions::ascii(false)));
    }

    #[test]
    fn snapshot_now_all_arms_plain() {
        insta::assert_snapshot!(render_now(&all_arms_now(), RenderOptions::plain()));
    }

    #[test]
    fn now_all_arms_render_honestly_in_plain() {
        // The Done-when checks, pinned against drift: plain has no ANSI; Unverified is
        // flagged (never a confident phrase); the freshness stamp appears (UTC, so it is
        // deterministic); the Claude caveat is reachable; Spend shows a dollar pool with no
        // fabricated %; Estimated shows volume + value + "quota % unavailable" and no meter.
        let output = render_now(&all_arms_now(), RenderOptions::plain());
        assert!(
            !output.contains('\u{1b}'),
            "plain now must contain no ANSI escapes: {output}"
        );
        assert!(
            output.contains("96% used ? unverified"),
            "unverified reading must be flagged, never confident: {output}"
        );
        assert!(
            output.contains("as of 08:30"),
            "an aged reading must carry the freshness stamp: {output}"
        );
        assert!(
            output.contains(
                "reflects Claude Code's view; claude.ai chat usage may make true usage higher."
            ),
            "Claude quota lines must carry the chat caveat: {output}"
        );
        assert!(
            output.contains("$18.50 / $20.00 used"),
            "Spend must render a dollar pool, never a fabricated %: {output}"
        );
        assert!(
            output.contains("usage 1,234,567 tokens (~$12.34, estimated), quota % unavailable"),
            "Estimated must show volume + value + unavailable-%: {output}"
        );
        assert!(
            output.contains("usage 5,000 tokens (estimated), quota % unavailable"),
            "unpriced Estimated must show volume alone, never a guessed price: {output}"
        );
    }

    #[test]
    fn now_all_arms_spend_draws_no_meter_in_braille() {
        // A Spend window has no fraction, so it must NOT draw a braille meter — the dollar
        // line stands alone (brief §6/§8).
        let output = render_now(&all_arms_now(), RenderOptions::braille(true));
        let spend_line = output
            .lines()
            .find(|line| line.contains("$18.50 / $20.00 used"))
            .unwrap_or_else(|| panic!("spend line should render: {output}"));
        assert!(
            !spend_line.contains('⣿') && !spend_line.contains('⣀'),
            "spend line must not draw a meter: {spend_line}"
        );
    }

    #[test]
    fn statusline_flags_unverified_when_most_constrained() {
        // The 0.96 Unverified window is the highest fraction, so it is the most-constrained
        // pick — the one-liner must carry the `? unverified` cue, never a confident `!!`
        // (the exact confident-wrong-number failure brief §0/§8 forbids).
        let braille = render_statusline(&all_arms_now(), RenderOptions::braille(true));
        assert!(
            braille.contains("96% ? unverified"),
            "statusline must flag the unverified pick: {braille}"
        );
        assert!(
            !braille.contains("96% !!"),
            "unverified must never render as a confident alarm: {braille}"
        );
        let plain = render_statusline(&all_arms_now(), RenderOptions::plain());
        assert!(plain.contains("? unverified"), "plain statusline: {plain}");
        assert!(
            !plain.contains('\u{1b}'),
            "plain statusline must contain no ANSI: {plain}"
        );
    }

    #[test]
    fn snapshot_statusline_unverified_braille() {
        insta::assert_snapshot!(render_statusline(
            &all_arms_now(),
            RenderOptions::braille(true)
        ));
    }

    #[test]
    fn snapshot_statusline_unverified_plain() {
        insta::assert_snapshot!(render_statusline(&all_arms_now(), RenderOptions::plain()));
    }

    #[test]
    fn freshness_stamp_appears_only_past_the_threshold() {
        let now = utc(2026, 6, 2, 9, 0);
        // Fresh reading (under 10 min) → no stamp; aged reading → "as of HH:MM" in UTC.
        assert_eq!(freshness_stamp(now - Duration::minutes(5), now), "");
        assert_eq!(
            freshness_stamp(now - Duration::minutes(10), now),
            "  as of 08:50"
        );
    }

    #[test]
    fn epoch_sentinel_discloses_unknown_capture_time_never_midnight() {
        // A reading whose capture instant was never recorded (the UNIX-epoch sentinel)
        // must disclose the unknown age — never render the bogus, confident
        // "as of 00:00" the bare arithmetic would produce.
        let now = utc(2026, 6, 2, 9, 0);
        let stamp = freshness_stamp(Utc.timestamp_nanos(0), now);
        assert_eq!(stamp, "  capture time unknown");
        assert!(!stamp.contains("as of"));
    }

    #[test]
    fn plain_paths_carry_the_state_cue() {
        // In --plain (and the plain statusline) there is no color at all, so the
        // textual cue is the ONLY near-limit signal — Available AND Partial must
        // carry it, exactly like the styled paths' `!`/`!!`.
        let generated = utc(2026, 6, 2, 9, 0);
        let available = LimitSummary {
            tool: ProviderId::Codex,
            plan: None,
            kind: LimitKind::FiveHour,
            label: None,
            captured_at: generated,
            availability: LimitAvailability::Available {
                measure: LimitMeasure::TokenFraction(0.97),
                resets_at: generated + Duration::hours(1),
                reset_in_seconds: 3600,
            },
        };
        assert!(plain_limit_line(&available, generated).contains("(critical)"));
        assert!(plain_limit_phrase(&available).contains("(critical)"));

        let partial = LimitSummary {
            availability: LimitAvailability::Partial {
                measure: Some(LimitMeasure::TokenFraction(0.97)),
                resets_at: None,
                reset_in_seconds: None,
                reason: "limit data incomplete".to_string(),
            },
            ..available
        };
        assert!(plain_limit_line(&partial, generated).contains("(critical)"));
        assert!(plain_limit_phrase(&partial).contains("(critical)"));
    }

    #[test]
    fn ascii_mode_output_is_pure_ascii() {
        // RenderMode::Ascii is the fallback for terminals without braille capability —
        // including non-UTF-8 locales — so every Ascii-mode surface (now, trends,
        // frontier with and without API usage, statusline) must be pure ASCII
        // (no ─ rule, no ◆ marker, no em-dash, no braille scatter/meter glyphs).
        let now = render_now(&all_arms_now(), RenderOptions::ascii(false));
        assert!(now.is_ascii(), "ascii now screen must be pure ASCII: {now}");
        let trends = render_trends(&priced_trends(), RenderOptions::ascii(false));
        assert!(
            trends.is_ascii(),
            "ascii trends screen must be pure ASCII: {trends}"
        );
        let frontier = render_frontier(&used_gpt(), RenderOptions::ascii(false));
        assert!(
            frontier.is_ascii(),
            "ascii frontier screen must be pure ASCII: {frontier}"
        );
        let frontier_no_api = render_frontier(&bench_view_for(&[]), RenderOptions::ascii(false));
        assert!(
            frontier_no_api.is_ascii(),
            "ascii frontier screen without API usage must be pure ASCII: {frontier_no_api}"
        );
        let statusline = render_statusline(&all_arms_now(), RenderOptions::ascii(false));
        assert!(
            statusline.is_ascii(),
            "ascii statusline must be pure ASCII: {statusline}"
        );
        let providers = render_providers(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::ascii(false),
        );
        assert!(
            providers.is_ascii(),
            "ascii providers tab must be pure ASCII: {providers}"
        );
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
                "BETA - model Composer 2.5 Fast (composer-2.5), logged in; \
                 usage unavailable - no sanctioned source; quota unavailable - no sanctioned source"
                    .to_string(),
            ),
        }];

        let output = render_now(&summary, RenderOptions::plain());
        assert!(
            output.contains(
                "provider cursor detected: BETA - model Composer 2.5 Fast (composer-2.5), logged in"
            ),
            "plain now should render the cursor detected note: {output}"
        );
        assert!(output.contains("usage unavailable - no sanctioned source"));
        assert!(output.contains("quota unavailable - no sanctioned source"));
        assert!(
            !output.contains('\u{1b}'),
            "plain output must not contain ANSI escapes"
        );
        assert!(
            output.is_ascii(),
            "plain output must be pure ASCII: {output}"
        );
    }

    #[test]
    fn plain_mode_output_is_pure_ascii() {
        // Plain mode is the accessibility floor (screen readers, dumb pipes, non-UTF-8
        // terminals), so every Costroid-generated byte in it must be ASCII.
        // Provider-supplied names (models, projects) pass through verbatim — the
        // fixtures are ASCII, so a failure here means a Costroid string regressed.
        let outputs = [
            render_now(&all_arms_now(), RenderOptions::plain()),
            render_now(&priced_now(), RenderOptions::plain()),
            render_now(&subscription_only_now(), RenderOptions::plain()),
            render_trends(&priced_trends(), RenderOptions::plain()),
            render_statusline(&priced_now(), RenderOptions::plain()),
            render_statusline(&subscription_only_now(), RenderOptions::plain()),
            render_frontier(&used_gpt(), RenderOptions::plain()),
            render_frontier(&bench_view_for(&[]), RenderOptions::plain()),
            render_providers(
                &provider_capabilities_fixture(),
                &provider_statuses_for_tab(),
                RenderOptions::plain(),
            ),
        ];
        for output in outputs {
            assert!(
                output.is_ascii(),
                "plain output must be pure ASCII: {output}"
            );
        }
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
    fn snapshot_providers_braille() {
        insta::assert_snapshot!(render_providers(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::braille(false)
        ));
    }

    #[test]
    fn snapshot_providers_ascii() {
        insta::assert_snapshot!(render_providers(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::ascii(false)
        ));
    }

    #[test]
    fn snapshot_providers_plain() {
        insta::assert_snapshot!(render_providers(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::plain()
        ));
    }

    #[test]
    fn providers_render_is_honest_and_cursor_is_detected_never_coming_soon() {
        let output = render_providers(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::plain(),
        );
        // Author-written DataSource/AuthMethod copy.
        assert!(output.contains("api cost: from local logs"));
        assert!(
            output.contains("quota: from the statusLine capture; run setup-statusline (5h, wk)")
        );
        assert!(output.contains("auth: no login required"));
        // Codex's quota is local, with both windows.
        assert!(output.contains("quota: from local logs (5h, wk)"));
        // Cursor: detected (never Missing/"coming soon"), both unavailable lanes honest.
        assert!(output.contains("cursor (detected):"));
        assert!(output.contains("api cost: no sanctioned source"));
        assert!(output.contains("quota: no sanctioned source"));
        assert!(!output.contains("coming soon"));
        // Detection health: the missing Codex carries its note; the available Claude none.
        assert!(output.contains("note: no local data found"));
    }

    #[test]
    fn providers_document_is_monochrome() {
        // The Providers tab is informational, so amber/red (Warn/Critical, reserved for the
        // near-limit/over-budget state) must never appear — bold (Strong) headers are an
        // allowed non-color cue, mirroring `frontier_document_is_monochrome`.
        let doc = render_providers_document(
            &provider_capabilities_fixture(),
            &provider_statuses_for_tab(),
            RenderOptions::braille(true),
        );
        for line in &doc.lines {
            for span in &line.spans {
                assert!(
                    !matches!(span.style, SemanticStyle::Warn | SemanticStyle::Critical),
                    "providers tab must be monochrome (amber/red are reserved): {span:?}"
                );
            }
        }
    }

    #[cfg(feature = "connect")]
    #[test]
    fn connection_lane_renders_state_without_key_material_and_folds_ascii() {
        use costroid_core::GEMINI_UNAVAILABLE_MESSAGE;

        let connections = vec![
            ConnectionEntry {
                vendor: "anthropic".to_string(),
                state: ConnectionState::Connected {
                    org: Some("Acme (org-123)".to_string()),
                },
            },
            ConnectionEntry {
                vendor: "openai".to_string(),
                state: ConnectionState::NotConnected,
            },
            ConnectionEntry {
                vendor: "gemini".to_string(),
                state: ConnectionState::Unavailable(GEMINI_UNAVAILABLE_MESSAGE.to_string()),
            },
        ];
        let capabilities = provider_capabilities_fixture();
        let statuses = provider_statuses_for_tab();

        // Braille (UTF-8 TTY): keeps the em-dash glyph; shows org label, connected/not, and
        // the pinned Gemini message verbatim — and never any key material.
        let braille_opts = RenderOptions::braille(false);
        let mut braille = render_providers_document(&capabilities, &statuses, braille_opts);
        push_provider_connection_lane(&mut braille, &connections, braille_opts);
        let braille = braille.render(braille_opts);
        assert!(braille.contains("connections (your own usage API keys)"));
        assert!(braille.contains("connected — organization Acme (org-123)"));
        assert!(braille.contains("openai     not connected"));
        assert!(braille.contains(GEMINI_UNAVAILABLE_MESSAGE));

        // Plain + Ascii fold to pure ASCII (em-dash -> '-'), even with the pinned message.
        for options in [RenderOptions::plain(), RenderOptions::ascii(false)] {
            let mut doc = render_providers_document(&capabilities, &statuses, options);
            push_provider_connection_lane(&mut doc, &connections, options);
            let rendered = doc.render(options);
            assert!(
                rendered.is_ascii(),
                "connection lane must be pure ASCII: {rendered}"
            );
            assert!(rendered.contains("connected - organization Acme (org-123)"));
            assert!(rendered.contains("unavailable - no sanctioned static-key usage API"));
        }
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
            capabilities: Vec::new(),
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

    // ---- reconciliation surface (T10c) --------------------------------------

    #[track_caller]
    fn dec(literal: &str) -> Decimal {
        match Decimal::from_str_exact(literal) {
            Ok(value) => value,
            Err(err) => panic!("bad fixture decimal {literal:?}: {err:?}"),
        }
    }

    fn usd(literal: &str) -> UsdAmount {
        UsdAmount::from_usd(dec(literal))
    }

    fn naive(year: i32, month: u32, day: u32) -> NaiveDate {
        match NaiveDate::from_ymd_opt(year, month, day) {
            Some(date) => date,
            None => panic!("bad fixture date"),
        }
    }

    fn local_with(entries: &[(NaiveDate, &str, &str)]) -> LocalCostEstimate {
        let mut estimate = LocalCostEstimate::new();
        for (date, model, dollars) in entries {
            if let Err(err) = estimate.add(*date, model, usd(dollars)) {
                panic!("fixture add failed: {err:?}");
            }
        }
        estimate
    }

    fn available(days: Vec<VendorCostDay>, caveats: CostReportCaveats) -> CostReportOutcome {
        CostReportOutcome::Available(VendorCostReport { days, caveats })
    }

    /// A vendor cost day from line items (panicking on the impossible money-overflow path —
    /// the workspace lints forbid `unwrap`/`expect` even in tests).
    fn vendor_day(date: NaiveDate, items: Vec<CostLineItem>) -> VendorCostDay {
        match VendorCostDay::from_line_items(date, items) {
            Ok(day) => day,
            Err(err) => panic!("fixture vendor day failed: {err:?}"),
        }
    }

    fn line_item(model: &str, dollars: &str, confidence: AmountConfidence) -> CostLineItem {
        CostLineItem {
            label: model.to_string(),
            amount: usd(dollars),
            model: Some(model.to_string()),
            cost_type: Some("tokens".to_string()),
            service_tier: Some("standard".to_string()),
            confidence,
        }
    }

    /// An Anthropic-shaped reconciliation exercising every honest state: an over-estimate
    /// day with a per-model split, a local day the vendor report doesn't cover (typed
    /// absence, never $0), and a model the vendor billed but Costroid never saw (a real
    /// local $0). Carries the Priority-Tier caveat.
    fn anthropic_reconciliation() -> CostReconciliation {
        let d_covered = naive(2026, 6, 14);
        let d_uncovered = naive(2026, 6, 13);
        let local = local_with(&[
            (d_covered, "claude-opus-4-8", "3.00"),
            (d_covered, "claude-sonnet-4-6", "1.20"),
            (d_uncovered, "claude-opus-4-8", "1.00"),
        ]);
        let outcome = available(
            vec![vendor_day(
                d_covered,
                vec![
                    line_item("claude-opus-4-8", "3.00", AmountConfidence::Exact),
                    line_item("claude-sonnet-4-6", "1.00", AmountConfidence::Exact),
                    line_item("claude-ghost-9", "0.50", AmountConfidence::Exact),
                ],
            )],
            CostReportCaveats {
                priority_tier_absent: true,
                per_model_derived_best_effort: false,
            },
        );
        reconcile_cost(&local, &outcome)
    }

    /// An OpenAI-shaped reconciliation: a best-effort per-model figure (marked + footnoted)
    /// and an under-estimate.
    fn openai_reconciliation() -> CostReconciliation {
        let day = naive(2026, 6, 14);
        let local = local_with(&[(day, "gpt-5.5", "1.00")]);
        let outcome = available(
            vec![vendor_day(
                day,
                vec![line_item(
                    "gpt-5.5",
                    "1.50",
                    AmountConfidence::DerivedBestEffort,
                )],
            )],
            CostReportCaveats {
                priority_tier_absent: false,
                per_model_derived_best_effort: true,
            },
        );
        reconcile_cost(&local, &outcome)
    }

    /// A not-connected reconciliation: the report is unavailable, but the local estimate
    /// still surfaces day by day beside the typed reason (never a fabricated delta).
    fn not_connected_reconciliation() -> CostReconciliation {
        let local = local_with(&[(naive(2026, 6, 14), "claude-opus-4-8", "2.00")]);
        reconcile_cost(
            &local,
            &CostReportOutcome::Unavailable(VendorReportUnavailable::NotConnected),
        )
    }

    /// Gemini: a first-class unavailable with no local usage — the pinned message, no delta.
    fn gemini_reconciliation() -> CostReconciliation {
        reconcile_cost(
            &LocalCostEstimate::new(),
            &CostReportOutcome::Unavailable(VendorReportUnavailable::NoSanctionedStaticKeyApi),
        )
    }

    /// Exercises three honest states the other fixtures don't: a model the vendor billed
    /// **$0** (variance shown, percentage undefined → "(vs $0 billed)"); a locally-estimated
    /// model the covered day does **not** attribute (`ModelNotInReport`); and a genuinely
    /// non-zero **sub-cent** variance (whose dollar magnitude rounds below a cent).
    fn mixed_states_reconciliation() -> CostReconciliation {
        let d = naive(2026, 6, 14);
        let local = local_with(&[
            (d, "billed-zero", "1.00"),
            (d, "unattributed", "0.40"),
            (d, "sub-cent", "0.001"),
        ]);
        let outcome = available(
            vec![vendor_day(
                d,
                vec![
                    line_item("billed-zero", "0", AmountConfidence::Exact),
                    line_item("sub-cent", "0.002", AmountConfidence::Exact),
                ],
            )],
            CostReportCaveats::default(),
        );
        reconcile_cost(&local, &outcome)
    }

    const WINDOW: &str = "2026-06-08 to 2026-06-14 (UTC, completed days)";

    #[test]
    fn snapshot_reconcile_anthropic_braille() {
        insta::assert_snapshot!(render_reconciliation(
            "anthropic",
            WINDOW,
            &anthropic_reconciliation(),
            RenderOptions::braille(false),
        ));
    }

    #[test]
    fn snapshot_reconcile_anthropic_ascii() {
        insta::assert_snapshot!(render_reconciliation(
            "anthropic",
            WINDOW,
            &anthropic_reconciliation(),
            RenderOptions::ascii(false),
        ));
    }

    #[test]
    fn snapshot_reconcile_anthropic_plain() {
        insta::assert_snapshot!(render_reconciliation(
            "anthropic",
            WINDOW,
            &anthropic_reconciliation(),
            RenderOptions::plain(),
        ));
    }

    #[test]
    fn snapshot_reconcile_openai_plain() {
        insta::assert_snapshot!(render_reconciliation(
            "openai",
            WINDOW,
            &openai_reconciliation(),
            RenderOptions::plain(),
        ));
    }

    #[test]
    fn snapshot_reconcile_not_connected_plain() {
        insta::assert_snapshot!(render_reconciliation(
            "openai",
            WINDOW,
            &not_connected_reconciliation(),
            RenderOptions::plain(),
        ));
    }

    #[test]
    fn snapshot_reconcile_gemini_plain() {
        insta::assert_snapshot!(render_reconciliation(
            "gemini",
            WINDOW,
            &gemini_reconciliation(),
            RenderOptions::plain(),
        ));
    }

    #[test]
    fn snapshot_reconcile_mixed_states_plain() {
        insta::assert_snapshot!(render_reconciliation(
            "openai",
            WINDOW,
            &mixed_states_reconciliation(),
            RenderOptions::plain(),
        ));
    }

    #[test]
    fn reconcile_renders_the_zero_billed_unattributed_and_subcent_states() {
        let output = render_reconciliation(
            "openai",
            WINDOW,
            &mixed_states_reconciliation(),
            RenderOptions::plain(),
        );
        // A model the vendor billed $0 → the dollar variance stands; the percentage is
        // undefined (never a divide-by-zero or a fabricated %).
        assert!(
            output.contains("+$1.00 over (vs $0 billed)"),
            "zero-billed model: {output}"
        );
        // A locally-estimated model a covered day doesn't attribute → typed text, no $0.
        assert!(output.contains("not attributed by the vendor"));
        // A genuinely non-zero sub-cent variance never reads as a misleading "$0.00".
        assert!(
            output.contains("<$0.01 under (-50.0%)"),
            "sub-cent variance: {output}"
        );
        assert!(
            !output.contains("$0.00 under") && !output.contains("$0.00 over"),
            "no misleading $0.00 direction cell: {output}"
        );
    }

    #[test]
    fn reconcile_renders_honest_states_in_plain() {
        let output = render_reconciliation(
            "anthropic",
            WINDOW,
            &anthropic_reconciliation(),
            RenderOptions::plain(),
        );
        // Local figure labeled an estimate; invoice shown un-tilded.
        assert!(output.contains("est ~$4.20"));
        // Signed variance carries the over/under direction as TEXT (never color alone).
        assert!(
            output.contains("over"),
            "expected an over-estimate row: {output}"
        );
        // Exact match renders "exact", not "+$0.00".
        assert!(output.contains("exact"));
        // Typed vendor-side absence renders as text, NEVER a fabricated $0.
        assert!(output.contains("report doesn't cover this day"));
        assert!(!output.contains("$0.00 under") && !output.contains("inv $0.00 "));
        // A vendor-billed model Costroid never saw → a REAL local $0 against the billed figure.
        assert!(output.contains("claude-ghost-9"));
        assert!(output.contains("est ~$0.00"));
        // The Priority-Tier caveat survives onto the screen.
        assert!(output.contains("Priority-Tier spend isn't in this report"));
    }

    #[test]
    fn reconcile_openai_marks_and_footnotes_best_effort() {
        let output = render_reconciliation(
            "openai",
            WINDOW,
            &openai_reconciliation(),
            RenderOptions::plain(),
        );
        // The under-estimate is signed + worded.
        assert!(output.contains("under"));
        // The best-effort per-model row is marked and footnoted.
        assert!(output.contains(" *"));
        assert!(output.contains("OpenAI per-model figures are best-effort"));
    }

    #[test]
    fn reconcile_not_connected_surfaces_estimate_and_remediation() {
        let output = render_reconciliation(
            "openai",
            WINDOW,
            &not_connected_reconciliation(),
            RenderOptions::plain(),
        );
        assert!(output.contains("vendor invoice unavailable: connect openai first"));
        // The local estimate still surfaces (never hidden behind the unavailable invoice).
        assert!(output.contains("est ~$2.00"));
        // No fabricated delta against a missing invoice.
        assert!(!output.contains("over") && !output.contains("under"));
    }

    #[test]
    fn reconcile_gemini_uses_the_pinned_unavailable_message() {
        let output = render_reconciliation(
            "gemini",
            WINDOW,
            &gemini_reconciliation(),
            RenderOptions::plain(),
        );
        // Plain folds the message's em-dash to ASCII, so compare the folded pinned string.
        assert!(output.contains(&costroid_core::GEMINI_UNAVAILABLE_MESSAGE.replace('—', "-")));
        assert!(output.contains("No local usage recorded"));
    }

    #[test]
    fn reconcile_ascii_and_plain_output_is_pure_ascii() {
        // The accessibility floor: Ascii (non-UTF-8 terminals) and Plain (screen readers)
        // must be pure ASCII — the em-dash subtitle, the absence "—", and any reason-message
        // em-dash all fold.
        for recon in [
            anthropic_reconciliation(),
            openai_reconciliation(),
            not_connected_reconciliation(),
            gemini_reconciliation(),
            mixed_states_reconciliation(),
        ] {
            let ascii =
                render_reconciliation("anthropic", WINDOW, &recon, RenderOptions::ascii(false));
            assert!(
                ascii.is_ascii(),
                "ascii reconcile must be pure ASCII: {ascii}"
            );
            let plain = render_reconciliation("anthropic", WINDOW, &recon, RenderOptions::plain());
            assert!(
                plain.is_ascii(),
                "plain reconcile must be pure ASCII: {plain}"
            );
            // Plain is the accessibility floor — no ANSI escapes either.
            assert!(
                !plain.contains('\u{1b}'),
                "plain reconcile must carry no ANSI escapes: {plain}"
            );
        }
    }
}
