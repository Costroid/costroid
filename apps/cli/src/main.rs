mod render;

use anyhow::{bail, Result};
use clap::{Parser, Subcommand, ValueEnum};
use costroid_core::{GroupBy, NowOptions, Period, TrendsOptions};
use costroid_providers::HostEnv;
use render::{detect_render_options, render_now, render_statusline, render_trends};

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
    Statusline,
    Export(ExportArgs),
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
    if cli.live {
        bail!("--live is not implemented in M4; static rendering prints once and exits");
    }
    let render_options = detect_render_options(cli.plain);

    match &cli.command {
        Some(Command::Trends(args)) => {
            run_trends(args, render_options)?;
        }
        Some(Command::Statusline) => {
            run_statusline(render_options)?;
        }
        Some(Command::Export(args)) => {
            run_export(args.format)?;
        }
        None => {
            run_now(render_options)?;
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

fn run_statusline(render_options: render::RenderOptions) -> Result<()> {
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
