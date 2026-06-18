//! costroid-bar — the Costroid taskbar (Step 6). A tray glance + a small toggle-window
//! cockpit for AI-coding-tool cost and limits, a pure consumer of `costroid-core`: every
//! figure the engine already computes, from local data, no network, no telemetry.
//!
//! T18 stands up the running shell (tray + window + worker-thread refresh); the Overview
//! meters (T19) and the live panels (T20) fill it in next.

mod app;
mod fonts;
mod format;
mod glyph;
mod meter;
mod overview;
mod refresh;
mod severity;
mod tray;

// Mirror apps/cli's off-by-default `connect` feature so a future (T20) read-only
// Providers connection display can link `costroid-connect`. T18 wires no connect action
// and no network — this only proves the gate compiles both ways.
#[cfg(feature = "connect")]
use costroid_connect as _;

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
         USAGE:\n    costroid-bar [--version] [--help]\n\n\
         Run with no arguments to launch the tray + window. Quotas, spend, and alerts\n\
         come from local data via costroid-core — no network, no telemetry. Connect and\n\
         reconcile stay in the `costroid` CLI; deep analysis (trends / models / history /\n\
         frontier) lives there too."
    );
}
