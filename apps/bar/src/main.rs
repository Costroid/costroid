//! costroid-bar — the Costroid taskbar (Step 6). A tray glance + a small toggle-window
//! cockpit for AI-coding-tool cost and limits, a pure consumer of `costroid-core`: every
//! figure the engine already computes, from local data, no network, no telemetry.
//!
//! T18 stood up the running shell (tray + window + worker-thread refresh); T19 added the
//! Overview meters; T20 adds the tab strip, the opt-in alert banner, and the four live panels
//! (Budget/Forecast/Anomalies/Providers) over the shared `costroid-config` schema.

mod anomalies;
mod app;
mod banner;
mod budget;
mod fonts;
mod forecast;
mod format;
mod glyph;
mod meter;
mod overview;
mod providers;
mod refresh;
mod severity;
mod tabs;
mod tray;

use anyhow::Result;

fn main() -> Result<()> {
    // Minimal arg handling (no clap dep): version / help, else launch the GUI.
    let mut args = std::env::args().skip(1);
    if let Some(arg) = args.next() {
        match arg.as_str() {
            "--version" | "-V" => {
                println!("costroid-bar {}", env!("CARGO_PKG_VERSION"));
                return Ok(());
            }
            "--help" | "-h" => {
                print_help();
                return Ok(());
            }
            // A one-shot, no-GUI self-check that exercises the full local data path and exits — the
            // runtime no-network proof drives this headless (scripts/offline_acceptance.sh).
            "--self-check" => {
                return app::self_check();
            }
            other => {
                eprintln!("costroid-bar: unrecognized argument '{other}'. Try --help.");
                std::process::exit(2);
            }
        }
    }
    app::run()
}

fn print_help() {
    println!(
        "costroid-bar — the Costroid taskbar (tray glance + live cockpit).\n\n\
         USAGE:\n    costroid-bar [--version] [--help] [--self-check]\n\n\
         --self-check runs a one-shot, no-GUI local-data pass (collect + render models) and\n\
         exits — used by the offline-acceptance proof. Run with no arguments to launch the\n\
         tray + window. Quotas, spend, and alerts\n\
         come from local data via costroid-core — no network, no telemetry. Connect and\n\
         reconcile stay in the `costroid` CLI; deep analysis (trends / models / history /\n\
         frontier) lives there too."
    );
}
