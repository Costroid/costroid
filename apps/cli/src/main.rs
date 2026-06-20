#[cfg(feature = "power")]
mod bench;
#[cfg(feature = "connect")]
mod connect;
#[cfg(feature = "connect")]
mod reconcile;
mod render;
mod setup;
mod tui;

use anyhow::Result;
use clap::{Parser, Subcommand, ValueEnum};
use costroid_core::{GroupBy, NowOptions, Period, TrendsOptions};
use costroid_providers::HostEnv;
use render::{
    detect_render_options, render_frontier, render_statusline, render_trends, RenderMode,
};

#[cfg(feature = "connect")]
use costroid_connect::{ApiVendor, ConnectionRegistry, CredentialStore, SecretString};

#[derive(Debug, Parser)]
#[command(name = "costroid", version, about = "Local AI coding cost visibility")]
struct Cli {
    #[arg(long, global = true, help = "Render plain ASCII output without color")]
    plain: bool,

    #[arg(long, global = true, help = "Refresh the selected view in place")]
    live: bool,

    #[command(subcommand)]
    command: Option<Command>,
}

#[derive(Debug, Subcommand)]
enum Command {
    Trends(TrendsArgs),
    /// Cost-vs-quality frontier: where your API-billed models sit against published benchmarks.
    Frontier,
    /// Emit a compact one-line status (tmux / Starship / Claude Code `statusLine`).
    Statusline(StatuslineArgs),
    /// Wire Claude Code's `statusLine` to capture live 5h/7d quota into a local cache.
    SetupStatusline(SetupStatuslineArgs),
    Export(ExportArgs),
    /// Benchmark a local model's cost-per-token (estimated, or --measure with a wall meter).
    #[cfg(feature = "power")]
    Bench(BenchArgs),
    /// Import a foreign FOCUS export (v1.2) and re-emit it as Costroid's FOCUS 1.3 ledger
    /// (the `cloud_api` lane). Pure local parse — no network.
    Import(ImportArgs),
    /// Show active threshold alerts (opt-in; default off). `--check` is a cron-friendly exit code.
    Alerts(AlertsArgs),
    /// Connect a vendor's usage/billing API with your own admin key (stored in the OS keychain).
    #[cfg(feature = "connect")]
    Connect(VendorArgs),
    /// Disconnect a vendor: delete its key from the OS keychain and unlink it.
    #[cfg(feature = "connect")]
    Disconnect(VendorArgs),
    /// List connected vendors (local-only by default; --check re-validates each over the network).
    #[cfg(feature = "connect")]
    Connections(ConnectionsArgs),
    /// Reconcile your local cost estimate against a connected vendor's billed invoice.
    #[cfg(feature = "connect")]
    Reconcile(ReconcileArgs),
}

#[cfg(feature = "connect")]
#[derive(Debug, Parser)]
struct VendorArgs {
    /// Which billing vendor: anthropic | openai | gemini.
    #[arg(value_enum)]
    vendor: VendorArg,
}

#[cfg(feature = "connect")]
#[derive(Debug, Parser)]
struct ConnectionsArgs {
    /// Re-validate each connected vendor over the network (opt-in; default is local-only).
    #[arg(long)]
    check: bool,
}

#[cfg(feature = "connect")]
#[derive(Debug, Clone, Copy, ValueEnum)]
enum VendorArg {
    Anthropic,
    Openai,
    Gemini,
}

#[cfg(feature = "connect")]
#[derive(Debug, Parser)]
struct ReconcileArgs {
    /// Reconcile a single vendor (default: every connected billing vendor).
    #[arg(long, value_enum)]
    vendor: Option<ReconcileVendorArg>,

    /// The window of completed UTC days to reconcile.
    #[arg(long, value_enum, default_value_t = PeriodArg::Week)]
    period: PeriodArg,
}

/// Only Anthropic and OpenAI publish a sanctioned own-key cost API, so only they can be
/// reconciled (Gemini is always "unavailable"; the no-`--vendor` view shows it as such).
#[cfg(feature = "connect")]
#[derive(Debug, Clone, Copy, ValueEnum)]
enum ReconcileVendorArg {
    Anthropic,
    Openai,
}

#[cfg(feature = "connect")]
impl From<ReconcileVendorArg> for ApiVendor {
    fn from(value: ReconcileVendorArg) -> Self {
        match value {
            ReconcileVendorArg::Anthropic => ApiVendor::Anthropic,
            ReconcileVendorArg::Openai => ApiVendor::OpenAI,
        }
    }
}

#[cfg(feature = "connect")]
impl From<VendorArg> for ApiVendor {
    fn from(value: VendorArg) -> Self {
        match value {
            VendorArg::Anthropic => ApiVendor::Anthropic,
            VendorArg::Openai => ApiVendor::OpenAI,
            VendorArg::Gemini => ApiVendor::Gemini,
        }
    }
}

#[derive(Debug, Parser)]
struct StatuslineArgs {
    /// Capture Claude Code's `rate_limits` from stdin into the local cache, then exit
    /// without rendering. Used by the `setup-statusline` capture snippet.
    #[arg(long, conflicts_with = "wrap")]
    capture_only: bool,

    /// Wrap an existing status-line command: capture from stdin, then run the command on
    /// the identical input. Manual escape hatch — prefer `costroid setup-statusline`.
    #[arg(long, value_name = "COMMAND")]
    wrap: Option<String>,
}

#[derive(Debug, Parser)]
struct SetupStatuslineArgs {
    /// Undo a previous setup: restore the backed-up settings.json and remove the cache.
    #[arg(long)]
    undo: bool,
}

#[derive(Debug, Parser)]
struct TrendsArgs {
    #[arg(long, value_enum, default_value_t = PeriodArg::Week)]
    period: PeriodArg,

    #[arg(long, value_enum, default_value_t = GroupArg::Model)]
    group: GroupArg,
}

#[derive(Debug, Parser)]
struct ExportArgs {
    #[arg(long, value_enum, default_value_t = ExportFormat::Json)]
    format: ExportFormat,
}

#[derive(Debug, Parser)]
struct ImportArgs {
    /// The foreign file's serialization (FOCUS CSV or a JSON array of FOCUS rows).
    #[arg(long, value_enum, default_value_t = ImportFormat::FocusCsv)]
    format: ImportFormat,
    /// The source FOCUS spec version. `auto` detects it (defaulting to 1.2 with a stderr
    /// caveat when the file carries no version marker); `1.2` asserts it (no caveat).
    #[arg(long, value_enum, default_value_t = ImportVersion::Auto)]
    version: ImportVersion,
    /// The serialization of the re-emitted Costroid FOCUS 1.3 ledger.
    #[arg(long, value_enum, default_value_t = ExportFormat::Json)]
    out: ExportFormat,
    /// Optional user pricing-override file (JSON, the `pricing.v1.json` schema) layered over
    /// the bundled catalog when repricing usage-only rows. Omit to use the XDG default
    /// (`~/.config/costroid/pricing-override.json`, if present) or the bundled tiers only.
    #[arg(long)]
    pricing_override: Option<std::path::PathBuf>,
    /// Path to the foreign FOCUS export file.
    path: std::path::PathBuf,
}

#[cfg(feature = "power")]
#[derive(Debug, Parser)]
struct BenchArgs {
    /// The Gemma 4 model id (e.g. `gemma-4-26b-a4b`, `gemma-4-31b-dense`).
    #[arg(long, default_value = "gemma-4-26b-a4b")]
    model: String,
    /// The quantization (default: the model's manifest default, `Q4_K_M`).
    #[arg(long)]
    quant: Option<String>,
    /// The local runtime to drive in `--measure` mode (and label the estimate).
    #[arg(long, value_enum, default_value_t = RuntimeArg::Ollama)]
    runtime: RuntimeArg,
    /// Measure REAL power by running the model via the runtime subprocess (needs
    /// `--wall-meter-watts`). Without this flag, the cost is an estimate/what-if (no hardware).
    #[arg(long)]
    measure: bool,
    /// Path to the runtime binary (default: the runtime's name on `PATH`).
    #[arg(long)]
    binary: Option<String>,
    /// Generated (output) tokens — the decode budget for `--measure`, the scenario volume for
    /// the estimate.
    #[arg(long, default_value_t = 1024)]
    tokens_out: u64,
    /// Prompt (input) tokens for the estimate / the cost-per-1M denominator.
    #[arg(long, default_value_t = 256)]
    tokens_in: u64,
    /// Wall-meter reading in watts (true total-system draw) for `--measure` — the recommended
    /// measured source.
    #[arg(long)]
    wall_meter_watts: Option<f64>,
    /// Override the electricity rate (per kWh, in the profile currency) — a dated assumption (R8).
    #[arg(long)]
    electricity_rate: Option<f64>,
    /// Override the hardware purchase price (for amortization).
    #[arg(long)]
    hardware_price: Option<f64>,
    /// Override the hardware amortization lifetime, in seconds.
    #[arg(long)]
    hardware_lifetime_seconds: Option<f64>,
    /// Override the hardware profile id (default `strix-halo-128gb`).
    #[arg(long)]
    hardware_profile: Option<String>,
    /// The serialization of the emitted `local_inference` FOCUS row.
    #[arg(long, value_enum, default_value_t = ExportFormat::Json)]
    out: ExportFormat,
}

#[cfg(feature = "power")]
#[derive(Debug, Clone, Copy, ValueEnum)]
enum RuntimeArg {
    Ollama,
    #[value(name = "llama.cpp")]
    LlamaCpp,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
enum ImportFormat {
    FocusCsv,
    FocusJson,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
enum ImportVersion {
    Auto,
    #[value(name = "1.2")]
    V1_2,
}

#[derive(Debug, Parser)]
struct AlertsArgs {
    /// Cron mode: print at most one line and set the exit code (0 = clear, 1 = a crossing,
    /// 2 = a config / collect error). Quiet on a clear run (no output) — cron-friendly.
    #[arg(long)]
    check: bool,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
enum PeriodArg {
    Day,
    Week,
    Month,
    Year,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
enum GroupArg {
    Model,
    App,
    Total,
}

#[derive(Debug, Clone, Copy, ValueEnum)]
enum ExportFormat {
    Json,
    Csv,
}

fn main() -> Result<()> {
    let cli = Cli::parse();
    let render_options = detect_render_options(cli.plain);

    match &cli.command {
        Some(Command::Trends(args)) => {
            if render_options.mode == RenderMode::Plain {
                run_trends(args, render_options)?;
            } else {
                tui::run(
                    tui::StartScreen::Trends,
                    args.period.into(),
                    args.group.into(),
                    cli.live,
                    render_options,
                )?;
            }
        }
        Some(Command::Frontier) => {
            if render_options.mode == RenderMode::Plain {
                run_frontier(render_options)?;
            } else {
                tui::run(
                    tui::StartScreen::Frontier,
                    Period::Week,
                    GroupBy::Model,
                    cli.live,
                    render_options,
                )?;
            }
        }
        Some(Command::Statusline(args)) => {
            run_statusline(args, render_options)?;
        }
        Some(Command::SetupStatusline(args)) => {
            let env = HostEnv::detect();
            setup::run_setup_statusline(&env, args.undo)?;
        }
        Some(Command::Export(args)) => {
            run_export(args.format)?;
        }
        Some(Command::Import(args)) => {
            run_import(args)?;
        }
        #[cfg(feature = "power")]
        Some(Command::Bench(args)) => {
            bench::run_bench(args)?;
        }
        Some(Command::Alerts(args)) => {
            std::process::exit(run_alerts(args, render_options)?);
        }
        #[cfg(feature = "connect")]
        Some(Command::Connect(args)) => {
            std::process::exit(run_connect_command(args.vendor.into(), cli.plain)?);
        }
        #[cfg(feature = "connect")]
        Some(Command::Disconnect(args)) => {
            std::process::exit(run_disconnect_command(args.vendor.into(), cli.plain)?);
        }
        #[cfg(feature = "connect")]
        Some(Command::Connections(args)) => {
            std::process::exit(run_connections_command(args.check, cli.plain)?);
        }
        #[cfg(feature = "connect")]
        Some(Command::Reconcile(args)) => {
            std::process::exit(run_reconcile_command(args, cli.plain)?);
        }
        None => {
            if render_options.mode == RenderMode::Plain {
                run_now(render_options)?;
            } else {
                tui::run(
                    tui::StartScreen::Now,
                    Period::Week,
                    GroupBy::Model,
                    cli.live,
                    render_options,
                )?;
            }
        }
    }

    Ok(())
}

fn run_now(render_options: render::RenderOptions) -> Result<()> {
    let env = HostEnv::detect();
    let snapshot = costroid_core::collect_local_snapshot(&env)?;
    let summary = costroid_core::now_summary(&snapshot, NowOptions::default());
    // The opt-in alert banner (T17): computed only when enabled in config. A missing/malformed
    // config degrades to no alerts (no banner) — the dedicated `costroid alerts` command is the
    // place a config error surfaces, never the `now` view (it must keep rendering).
    let alerts = match costroid_config::load() {
        Ok(config) if config.alerts_enabled() => compute_alerts(&config, &snapshot, &summary),
        _ => Vec::new(),
    };
    print!(
        "{}",
        render::render_now_with_alerts(&summary, &alerts, render_options)
    );
    Ok(())
}

/// Compute the opt-in alerts for a snapshot: the T17 quota + budget crossings, plus the T17b
/// advisory sources (a TOTAL-budget projection + a spend spike) — each advisory view built ONLY
/// when its sub-flag is on, then fed to the pure `active_alerts` detector. The caller gates on
/// `alerts_enabled()` first; this stays self-contained (a disabled sub-flag passes `None`, so the
/// detector's output is byte-identical to T17). No network, no telemetry — pure-local.
fn compute_alerts(
    config: &costroid_config::Config,
    snapshot: &costroid_core::EngineSnapshot,
    summary: &costroid_core::NowSummary,
) -> Vec<costroid_core::Alert> {
    let budget = costroid_core::budget_view(snapshot, &config.budget_targets());
    let forecast = config
        .alerts_forecast_enabled()
        .then(|| costroid_core::forecast_view(snapshot));
    let anomalies = config
        .alerts_anomalies_enabled()
        .then(|| costroid_core::anomalies_view(snapshot));
    let advisory = costroid_core::AdvisoryAlerts {
        forecast: forecast.as_ref(),
        anomalies: anomalies.as_ref(),
    };
    costroid_core::active_alerts(summary, &budget, &config.alert_thresholds(), advisory)
}

/// `costroid alerts [--check]` (T17): opt-in threshold alerts, default OFF. The `enabled` config
/// flag is the master switch (also gates the inline `now` banner). Exit codes for `--check`: `0`
/// clear (or off), `1` a crossing, `2` a config / collect error (a distinct cron signal, never
/// conflated with a crossing). Pure-local — no network, no telemetry.
fn run_alerts(args: &AlertsArgs, render_options: render::RenderOptions) -> Result<i32> {
    let config = match costroid_config::load() {
        Ok(config) => config,
        Err(error) => {
            eprintln!("config: {error}");
            return Ok(2);
        }
    };
    if !config.alerts_enabled() {
        // Master switch off (the default): the human form says so; `--check` is silently clear.
        if !args.check {
            print!("{}", render::render_alerts_off(render_options));
        }
        return Ok(0);
    }
    let env = HostEnv::detect();
    let snapshot = match costroid_core::collect_local_snapshot(&env) {
        Ok(snapshot) => snapshot,
        Err(error) => {
            eprintln!("alerts: {error}");
            return Ok(2);
        }
    };
    let summary = costroid_core::now_summary(&snapshot, NowOptions::default());
    let alerts = compute_alerts(&config, &snapshot, &summary);
    if args.check {
        // Cron-friendly: quiet on a clear run, one line on a crossing; the exit code is the signal.
        if !alerts.is_empty() {
            println!("{}", render::alert_check_line(&alerts));
        }
        Ok(render::alerts_check_exit_code(&alerts))
    } else {
        print!("{}", render::render_alerts(&alerts, render_options));
        Ok(0)
    }
}

fn run_trends(args: &TrendsArgs, render_options: render::RenderOptions) -> Result<()> {
    let env = HostEnv::detect();
    let snapshot = costroid_core::collect_local_snapshot(&env)?;
    let summary = costroid_core::trends_summary(
        &snapshot,
        TrendsOptions {
            period: args.period.into(),
            group_by: args.group.into(),
        },
    );
    print!("{}", render_trends(&summary, render_options));
    Ok(())
}

fn run_frontier(render_options: render::RenderOptions) -> Result<()> {
    let env = HostEnv::detect();
    let snapshot = costroid_core::collect_local_snapshot(&env)?;
    let view = costroid_core::bench_view(&snapshot)?;
    print!("{}", render_frontier(&view, render_options));
    Ok(())
}

fn run_statusline(args: &StatuslineArgs, render_options: render::RenderOptions) -> Result<()> {
    // Capture-only: a side-effect, never a renderer. Read stdin once, capture, emit
    // nothing, and exit 0 always — a bad payload must never break the user's prompt.
    if args.capture_only {
        setup::capture_from_bytes(&setup::read_stdin());
        return Ok(());
    }
    // Manual wrap escape hatch: capture, then run the wrapped command on the same input.
    if let Some(command) = &args.wrap {
        return setup::run_wrap(command);
    }
    // Plain status line. When stdin is piped (Claude Code's `statusLine` JSON), capture
    // it opportunistically before rendering — path 2, where Costroid *is* the status
    // line. When stdin is interactive (tmux / Starship), never block on it.
    if !std::io::IsTerminal::is_terminal(&std::io::stdin()) {
        setup::capture_from_bytes(&setup::read_stdin());
    }
    let env = HostEnv::detect();
    // Render-something-on-failure: this exact command is what `setup-statusline`
    // installs as Claude Code's statusLine, so a collect error degrades to a blank
    // line + exit 0 — it must never take down the user's prompt (the --capture-only
    // and --wrap paths are already hardened the same way).
    let snapshot = match costroid_core::collect_local_snapshot(&env) {
        Ok(snapshot) => snapshot,
        Err(_) => {
            println!();
            return Ok(());
        }
    };
    let summary = costroid_core::now_summary(&snapshot, NowOptions::default());
    print!("{}", render_statusline(&summary, render_options));
    Ok(())
}

fn run_export(format: ExportFormat) -> Result<()> {
    let env = HostEnv::detect();
    let rows = costroid_core::focus_records_from_local_logs(&env)?;
    let output = match format {
        ExportFormat::Json => costroid_core::export_focus_json(rows)?,
        ExportFormat::Csv => costroid_core::export_focus_csv(&rows)?,
    };
    print!("{output}");
    Ok(())
}

fn run_import(args: &ImportArgs) -> Result<()> {
    use anyhow::Context;
    use costroid_providers::focus_import::{import_focus_csv, import_focus_json};

    let data = std::fs::read_to_string(&args.path)
        .with_context(|| format!("reading FOCUS import file {}", args.path.display()))?;

    let import = match args.format {
        ImportFormat::FocusCsv => import_focus_csv(&data),
        ImportFormat::FocusJson => import_focus_json(&data),
    }
    .with_context(|| format!("importing FOCUS file {}", args.path.display()))?;

    // Honesty (R6): when the version was ASSUMED (no marker) and the user did not assert
    // it, surface the caveat on stderr — never silently presume a version. `--version 1.2`
    // is the user asserting it, so the caveat is suppressed.
    if import.detection.assumed_default && matches!(args.version, ImportVersion::Auto) {
        eprintln!(
            "note: no FOCUS version marker found — assuming FOCUS {} \
             (pass --version 1.2 to assert it)",
            import.detection.version.as_str()
        );
    }

    // Optional user pricing-override (D5): explicit --pricing-override, else the XDG default
    // (missing = bundled tiers only). Layered over curated + LiteLLM when repricing usage-only
    // rows. Pure local file read — no network.
    let pricing_override = costroid_core::read_pricing_override(args.pricing_override.as_deref())
        .context("reading the pricing-override file")?;
    let rows = costroid_core::focus_records_from_v12_import_with_override(
        &import.events,
        &import.detection.version,
        pricing_override.as_deref(),
    )
    .context("normalizing imported FOCUS rows to the 1.3 ledger")?;

    let output = match args.out {
        ExportFormat::Json => costroid_core::export_focus_json(rows)?,
        ExportFormat::Csv => costroid_core::export_focus_csv(&rows)?,
    };
    print!("{output}");
    Ok(())
}

#[cfg(feature = "connect")]
fn connect_output_style(plain: bool) -> connect::OutputStyle {
    // Fold the em-dash for --plain / a non-UTF-8 terminal; keep it on a UTF-8 (braille) TTY.
    let mode = detect_render_options(plain).mode;
    connect::OutputStyle {
        ascii: mode != RenderMode::Braille,
    }
}

#[cfg(feature = "connect")]
fn run_connect_command(vendor: ApiVendor, plain: bool) -> Result<i32> {
    let style = connect_output_style(plain);
    let mut stdout = std::io::stdout().lock();
    if vendor == ApiVendor::Gemini {
        // gemini: print the pinned unavailable line, exit 0, never read/accept a key.
        return connect::gemini_connect(&mut stdout, style);
    }
    // Warn BEFORE the key is pasted (T9 pin §2.3/§6): an admin key is organization-wide.
    connect::print_connect_warning(&mut stdout, style, vendor)?;
    let key = read_admin_key(vendor)?;
    let store = CredentialStore::new()?;
    let registry = ConnectionRegistry::open()?;
    connect::run_connect(
        vendor,
        key,
        &connect::RealAdapters,
        &store,
        &registry,
        &mut stdout,
        style,
    )
}

#[cfg(feature = "connect")]
fn run_disconnect_command(vendor: ApiVendor, plain: bool) -> Result<i32> {
    let style = connect_output_style(plain);
    let mut stdout = std::io::stdout().lock();
    let store = CredentialStore::new()?;
    let registry = ConnectionRegistry::open()?;
    connect::run_disconnect(vendor, &store, &registry, &mut stdout, style)
}

#[cfg(feature = "connect")]
fn run_connections_command(check: bool, plain: bool) -> Result<i32> {
    let style = connect_output_style(plain);
    let mut stdout = std::io::stdout().lock();
    let store = CredentialStore::new()?;
    let registry = ConnectionRegistry::open()?;
    connect::run_connections(
        check,
        &connect::RealAdapters,
        &store,
        &registry,
        &mut stdout,
        style,
    )
}

/// `costroid reconcile`: load the local FOCUS rows from the same pipeline `now`/`trends`/
/// `export` use, then reconcile them against each connected vendor's billed-cost report.
/// Reuses T10a's stored key + authorized client — no new secret or network boundary; the
/// only network is the cost-report fetch on this explicit action (behind `--features
/// connect`). The default build links none of this.
#[cfg(feature = "connect")]
fn run_reconcile_command(args: &ReconcileArgs, plain: bool) -> Result<i32> {
    let options = detect_render_options(plain);
    let env = HostEnv::detect();
    let rows = costroid_core::focus_records_from_local_logs(&env)?;
    let store = CredentialStore::new()?;
    let registry = ConnectionRegistry::open()?;
    let window = reconcile::completed_window(args.period.into());
    let mut stdout = std::io::stdout().lock();
    reconcile::run_reconcile(
        args.vendor.map(Into::into),
        window,
        &rows,
        &connect::RealAdapters,
        &store,
        &registry,
        &mut stdout,
        options,
    )
}

/// Read the admin key from **stdin only** — never argv (leaks to `ps`), never env (leaks
/// to child processes and shell history). On a TTY: a hidden, no-echo prompt; on a pipe:
/// one line (so `echo "$KEY" | costroid connect anthropic` and secret-manager pipelines
/// work). Surrounding whitespace (e.g. a trailing newline) is trimmed **in place** and the
/// buffer is then **moved** into the `SecretString`, which owns and zeroizes it on drop —
/// so no separate, un-scrubbed plaintext copy of the key lingers. (One inherent remnant can
/// remain: `rpassword`/`read_line` hand back a plain `String`, and the `String → Box<str>`
/// conversion may shrink-realloc — the same "minimizes, not eliminates" limit the keychain
/// `retrieve` and `OpenAiAdapter::headers` carry.) The key is never echoed.
#[cfg(feature = "connect")]
fn read_admin_key(vendor: ApiVendor) -> Result<SecretString> {
    use std::io::IsTerminal;
    let mut raw = if std::io::stdin().is_terminal() {
        rpassword::prompt_password(format!(
            "Paste your {vendor} admin key (input hidden, not echoed): "
        ))?
    } else {
        use std::io::BufRead;
        let mut line = String::new();
        std::io::stdin().lock().read_line(&mut line)?;
        line
    };
    // Trim surrounding whitespace IN PLACE (no `.to_string()` copy of the plaintext), then
    // hand the one buffer to SecretString, which zeroizes it on drop.
    let lead = raw.len() - raw.trim_start().len();
    raw.drain(..lead);
    let end = raw.trim_end().len();
    raw.truncate(end);
    Ok(SecretString::from(raw))
}

impl From<PeriodArg> for Period {
    fn from(value: PeriodArg) -> Self {
        match value {
            PeriodArg::Day => Self::Day,
            PeriodArg::Week => Self::Week,
            PeriodArg::Month => Self::Month,
            PeriodArg::Year => Self::Year,
        }
    }
}

impl From<GroupArg> for GroupBy {
    fn from(value: GroupArg) -> Self {
        match value {
            GroupArg::Model => Self::Model,
            GroupArg::App => Self::App,
            GroupArg::Total => Self::Total,
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use clap::CommandFactory;

    #[test]
    fn cli_shape_is_valid() {
        Cli::command().debug_assert();
    }
}
