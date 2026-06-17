//! End-to-end exit-code contract for `costroid alerts --check` (T17). Spawns the built binary
//! against an isolated, fixture-only config dir (no network — alerts is pure-local) and pins the
//! cron contract: **0 = clear / off**, **2 = a config error** (a distinct signal, never conflated
//! with a crossing). Exit **1 = a crossing** is unit-tested via `alerts_check_exit_code` in
//! `render.rs` (and proven by the detector tests in core); forcing a real crossing end-to-end would
//! need planted provider logs, which is out of scope for this exit-code contract test.
//!
//! No `unwrap`/`expect` (workspace clippy denies them even in tests) — failures `panic!` with
//! context, mirroring `tests/offline.rs`.

use std::fs;
use std::path::{Path, PathBuf};
use std::process::{Command, Output};

/// A unique throwaway config dir under the temp dir (no `tempfile` dep). Both `XDG_CONFIG_HOME`
/// and `HOME` are pointed here in the spawned process, so a real user config is never read.
fn unique_config_dir(tag: &str) -> PathBuf {
    use std::sync::atomic::{AtomicU32, Ordering};
    static COUNTER: AtomicU32 = AtomicU32::new(0);
    let n = COUNTER.fetch_add(1, Ordering::Relaxed);
    let dir = std::env::temp_dir().join(format!(
        "costroid-alerts-cli-{}-{tag}-{n}",
        std::process::id()
    ));
    if let Err(err) = fs::create_dir_all(dir.join("costroid")) {
        panic!("temp config dir should create: {err}");
    }
    dir
}

fn write_config(dir: &Path, contents: &str) {
    if let Err(err) = fs::write(dir.join("costroid").join("config.toml"), contents) {
        panic!("fixture config should write: {err}");
    }
}

fn run_alerts(dir: &Path, args: &[&str]) -> Output {
    let mut command = Command::new(env!("CARGO_BIN_EXE_costroid"));
    command
        .args(args)
        .env("XDG_CONFIG_HOME", dir)
        .env("HOME", dir);
    match command.output() {
        Ok(output) => output,
        Err(err) => panic!("costroid binary should run: {err}"),
    }
}

#[test]
fn alerts_check_off_exits_zero_and_is_silent() {
    // No config file => alerts default OFF => clear: exit 0, no stdout (cron-friendly quiet).
    let dir = unique_config_dir("off");
    let output = run_alerts(&dir, &["alerts", "--check"]);
    assert_eq!(
        output.status.code(),
        Some(0),
        "default-off --check must exit 0 (clear); stderr={}",
        String::from_utf8_lossy(&output.stderr)
    );
    assert!(
        output.stdout.is_empty(),
        "a clear/off --check must print nothing: {:?}",
        String::from_utf8_lossy(&output.stdout)
    );
    let _ = fs::remove_dir_all(&dir);
}

#[test]
fn alerts_check_malformed_config_exits_two() {
    // A present-but-malformed config is neither clear nor a crossing — it is a distinct error
    // signal (exit 2), so a cron consumer can tell a real misconfiguration from a quota crossing.
    let dir = unique_config_dir("malformed");
    write_config(&dir, "[alerts\nenabled = true\n"); // unterminated table header
    let output = run_alerts(&dir, &["alerts", "--check"]);
    assert_eq!(
        output.status.code(),
        Some(2),
        "a malformed config must exit 2 (distinct from a crossing); stderr={}",
        String::from_utf8_lossy(&output.stderr)
    );
    let _ = fs::remove_dir_all(&dir);
}

#[test]
fn alerts_human_off_state_is_printed_and_exits_zero() {
    // The human form is honest about being off and points at the config file.
    let dir = unique_config_dir("human-off");
    let output = run_alerts(&dir, &["alerts", "--plain"]);
    assert_eq!(output.status.code(), Some(0));
    let stdout = String::from_utf8_lossy(&output.stdout);
    assert!(
        stdout.contains("alerts are off"),
        "human off state must say so: {stdout}"
    );
    let _ = fs::remove_dir_all(&dir);
}
