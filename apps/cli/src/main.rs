mod render;
mod setup;
mod tui;

use anyhow::Result;
use clap::{Parser, Subcommand, ValueEnum};
use costroid_core::{GroupBy, NowOptions, Period, TrendsOptions};
use costroid_providers::HostEnv;
use render::{
    detect_render_options, render_frontier, render_now, render_statusline, render_trends,
    RenderMode,
};

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
    print!("{}", render_now(&summary, render_options));
    Ok(())
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
    let snapshot = costroid_core::collect_local_snapshot(&env)?;
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
