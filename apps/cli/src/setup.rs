//! `costroid setup-statusline` + the `statusline` capture writer (Step 2 / T5).
//!
//! This wires Claude Code's `statusLine` hook to tee its live `rate_limits` block into
//! Costroid's no-secret local cache, and provides the `statusline --capture-only`
//! side-effect that performs the capture. The matching *reader* lives in
//! `costroid-providers` (T4); the *rendering* of the captured quota lands in T6.
//!
//! Security envelope (ARCHITECTURE §8/§10): the cache holds only two percentages, two
//! reset stamps, and a capture time — never a token, prompt, credential, or content. No
//! network call, no credential read. `setup-statusline` edits only Claude Code's
//! `settings.json`, always backing it up first and restorable via `--undo`.

use std::fs;
use std::io::{Read, Write};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::sync::atomic::{AtomicU64, Ordering};

use anyhow::{bail, Context, Result};
use chrono::{DateTime, Utc};
use costroid_providers::HostEnv;
use serde_json::{Map, Value};

/// Versioned idempotency sentinel injected into a wrapped `statusLine` command (path 1).
/// Re-running `setup-statusline` detects it and is a no-op; `--undo` keys off it too. The
/// version lets a future injection format migrate cleanly.
const SENTINEL: &str = "# costroid:statusline-capture v1";

/// The command Costroid installs when there is no existing `statusLine` (path 2). Its
/// presence is itself an idempotency marker.
const COSTROID_STATUSLINE_CMD: &str = "costroid statusline";

/// Backup written next to `settings.json` before the first edit.
const BACKUP_NAME: &str = "settings.json.costroid-bak";

// ---------------------------------------------------------------------------
// Capture writer (`statusline --capture-only`, and opportunistic plain capture)
// ---------------------------------------------------------------------------

/// Shape Claude Code's stdin session object into the no-secret cache value, or `None`
/// when there is nothing trustworthy to write (unparseable input, no `rate_limits`
/// block, or no recognized window). Only `used_percentage` + `resets_at` are copied from
/// each window — every other field (including anything sensitive) is dropped. The values
/// are passed through verbatim; the T4 reader sanitizes them.
pub fn build_cache_value(input: &[u8], captured_at: DateTime<Utc>) -> Option<Value> {
    let root: Value = serde_json::from_slice(input).ok()?;
    let rate_limits = root.get("rate_limits")?;
    let mut cache = Map::new();
    if let Some(window) = clean_window(rate_limits.get("five_hour")) {
        cache.insert("five_hour".to_string(), window);
    }
    if let Some(window) = clean_window(rate_limits.get("seven_day")) {
        cache.insert("seven_day".to_string(), window);
    }
    if cache.is_empty() {
        return None; // a rate_limits block with no usable window: write nothing
    }
    cache.insert(
        "captured_at".to_string(),
        Value::String(captured_at.to_rfc3339()),
    );
    Some(Value::Object(cache))
}

/// Copy only the two allowed fields from one rate-limit window. `None` when the window is
/// absent, not an object, or carries neither field — the security floor that keeps the
/// cache to "two percentages + two reset stamps" (ARCHITECTURE §10).
fn clean_window(window: Option<&Value>) -> Option<Value> {
    let window = window?.as_object()?;
    let mut clean = Map::new();
    // Shape-check the VALUES too, not just the keys: the percentage must be a number
    // and the reset stamp a number or string (epoch or RFC3339). If a future Claude
    // Code build ever ships an object/array under an allowed key, it is dropped —
    // nothing non-scalar (and so nothing sensitive) can transit into the cache.
    if let Some(pct) = window.get("used_percentage").filter(|v| v.is_number()) {
        clean.insert("used_percentage".to_string(), pct.clone());
    }
    if let Some(reset) = window
        .get("resets_at")
        .filter(|v| v.is_number() || v.is_string())
    {
        clean.insert("resets_at".to_string(), reset.clone());
    }
    if clean.is_empty() {
        return None;
    }
    Some(Value::Object(clean))
}

/// A unique temp sibling for `path` (same directory, so the rename stays atomic):
/// `<file>.<pid>.<n>.tmp`. A FIXED temp name would let two concurrent writers (e.g. two
/// Claude Code sessions both firing `--capture-only`) interleave truncate/write/rename
/// and publish a torn file at the final path.
fn unique_tmp_sibling(path: &Path) -> PathBuf {
    static COUNTER: AtomicU64 = AtomicU64::new(0);
    let serial = COUNTER.fetch_add(1, Ordering::Relaxed);
    let mut tmp = path.as_os_str().to_owned();
    tmp.push(format!(".{}.{serial}.tmp", std::process::id()));
    PathBuf::from(tmp)
}

/// Atomic-write the cache JSON: unique temp file in the same directory, then rename, so
/// a concurrent reader never sees a torn file. Creates the parent directory as needed.
fn write_cache_atomic(cache_path: &Path, value: &Value) -> Result<()> {
    if let Some(parent) = cache_path.parent() {
        fs::create_dir_all(parent)
            .with_context(|| format!("creating cache dir {}", parent.display()))?;
    }
    let bytes = serde_json::to_vec_pretty(value).context("serializing cache")?;
    let tmp = unique_tmp_sibling(cache_path);
    fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    fs::rename(&tmp, cache_path)
        .with_context(|| format!("renaming into {}", cache_path.display()))?;
    Ok(())
}

/// Capture `input` into the cache at `cache_path`. Returns whether anything was written.
/// Decoupled from path resolution so it is unit-testable with an explicit temp path.
pub fn capture_to_path(
    input: &[u8],
    cache_path: &Path,
    captured_at: DateTime<Utc>,
) -> Result<bool> {
    match build_cache_value(input, captured_at) {
        Some(value) => {
            write_cache_atomic(cache_path, &value)?;
            Ok(true)
        }
        None => Ok(false),
    }
}

/// Best-effort capture from raw stdin bytes — the `statusline --capture-only` side-effect
/// (and the opportunistic capture in the plain status line). Resolves the cache path,
/// writes if there is something to write, and **swallows every error**: a bad payload, an
/// unwritable cache, or a missing base dir must never break the user's status line.
pub fn capture_from_bytes(input: &[u8]) {
    let Some(cache_path) = costroid_providers::claude_rate_limits_cache_path() else {
        return;
    };
    let _ = capture_to_path(input, &cache_path, Utc::now());
}

/// Read all of stdin into memory once. Returns empty on error — a capture side-effect
/// must never fail its caller.
pub fn read_stdin() -> Vec<u8> {
    let mut buf = Vec::new();
    let _ = std::io::stdin().lock().read_to_end(&mut buf);
    buf
}

// ---------------------------------------------------------------------------
// Manual wrap escape hatch (`statusline --wrap '<command>'`)
// ---------------------------------------------------------------------------

/// `costroid statusline --wrap '<command>'` — the hazardous manual escape hatch (brief §2
/// path 3) for a `statusLine` the user can't edit. Reads stdin once, tees a copy to the
/// capture side-effect, then runs `<command>` on the identical bytes with its output
/// inherited. Always succeeds (exit 0): if the wrapped command can't run, a blank line is
/// printed so the user's status line degrades, never breaks.
pub fn run_wrap(command: &str) -> Result<()> {
    let input = read_stdin();
    capture_from_bytes(&input);
    if run_wrapped(command, &input).is_err() {
        // Render-something-on-failure: never take down the status line.
        println!();
    }
    Ok(())
}

/// Run `command` via the shell, feeding it `input` on stdin and inheriting its stdout so
/// the rendered status line passes straight through.
fn run_wrapped(command: &str, input: &[u8]) -> Result<()> {
    let mut child = Command::new("sh")
        .arg("-c")
        .arg(command)
        .stdin(Stdio::piped())
        .spawn()
        .with_context(|| format!("spawning wrapped status line: {command}"))?;
    if let Some(mut stdin) = child.stdin.take() {
        // A wrapped command that ignores stdin may close it early; a broken pipe here is
        // not fatal — it still renders.
        let _ = stdin.write_all(input);
    }
    let status = child.wait().context("waiting on wrapped status line")?;
    if !status.success() {
        bail!("wrapped status line exited with {status}");
    }
    Ok(())
}

// ---------------------------------------------------------------------------
// `setup-statusline` — settings.json wiring
// ---------------------------------------------------------------------------

/// The path-1 capture snippet: read stdin once, tee a copy to the capture side-effect,
/// then feed the identical bytes to the user's original renderer. The sentinel on its own
/// line makes the edit detectable (idempotency) and reversible.
///
/// The original is wrapped in a `{ … }` group on its own line(s) so the pipe feeds the
/// WHOLE original command: embedded verbatim (no escaping needed), a multi-line original
/// still receives stdin on whichever line consumes it, and a leading `#` comments out
/// only its own line — never the pipe itself.
fn capture_snippet(original_command: &str) -> String {
    format!(
        "{SENTINEL}\ninput=$(cat); printf '%s' \"$input\" | {COSTROID_STATUSLINE_CMD} \
         --capture-only; printf '%s' \"$input\" | {{\n{original_command}\n}}"
    )
}

/// Recover the original command a path-1 snippet wrapped, for the no-backup `--undo`
/// fallback. Understands the current `{ … }` group form and the flat pre-fix form
/// (`… --capture-only; printf '%s' "$input" | <original>`), both under the v1 sentinel.
fn original_from_snippet(command: &str) -> Option<String> {
    if !command.contains(SENTINEL) {
        return None;
    }
    if let Some((_, tail)) = command.split_once("| {\n") {
        return tail.strip_suffix("\n}").map(str::to_string);
    }
    command
        .rsplit_once("\"$input\" | ")
        .map(|(_, original)| original.to_string())
        .filter(|original| !original.is_empty())
}

/// What the pure settings transform decided to do.
#[derive(Debug, PartialEq, Eq)]
enum SetupOutcome {
    /// No `statusLine` existed → Costroid became the status line (path 2).
    BecameStatusline,
    /// An existing `statusLine` was wrapped with the capture snippet (path 1).
    WrappedExisting,
    /// Already wired (sentinel or the costroid command present) → no-op.
    AlreadyWired,
}

/// Pure settings transform. Preserves every unknown key; only ever reads/writes
/// `statusLine`. Returns the (possibly unchanged) settings and what it did.
fn apply_setup(mut settings: Value) -> (Value, SetupOutcome) {
    if !settings.is_object() {
        settings = Value::Object(Map::new());
    }
    match current_statusline_command(&settings) {
        Some(cmd) if is_wired(&cmd) => (settings, SetupOutcome::AlreadyWired),
        Some(original) => {
            let snippet = capture_snippet(&original);
            set_statusline_command(&mut settings, &snippet);
            (settings, SetupOutcome::WrappedExisting)
        }
        None => {
            set_statusline_command(&mut settings, COSTROID_STATUSLINE_CMD);
            (settings, SetupOutcome::BecameStatusline)
        }
    }
}

/// Whether a `statusLine` command already carries Costroid's wiring (either path).
fn is_wired(command: &str) -> bool {
    command.contains(SENTINEL) || command.trim() == COSTROID_STATUSLINE_CMD
}

/// Extract the existing `statusLine` command. Claude Code's documented shape is an object
/// `{ "type": "command", "command": "…" }`; a bare string form is tolerated defensively.
fn current_statusline_command(settings: &Value) -> Option<String> {
    match settings.get("statusLine") {
        Some(Value::String(cmd)) => Some(cmd.clone()),
        Some(Value::Object(obj)) => obj
            .get("command")
            .and_then(Value::as_str)
            .map(str::to_string),
        _ => None,
    }
}

/// Set `statusLine.command`, preserving sibling fields (`type`, `padding`, …) on an
/// existing object and creating a well-formed `{ type: "command", command }` otherwise.
fn set_statusline_command(settings: &mut Value, command: &str) {
    let Some(root) = settings.as_object_mut() else {
        return;
    };
    match root.get_mut("statusLine") {
        Some(Value::Object(obj)) => {
            obj.insert("command".to_string(), Value::String(command.to_string()));
            if !obj.contains_key("type") {
                obj.insert("type".to_string(), Value::String("command".to_string()));
            }
        }
        _ => {
            let mut obj = Map::new();
            obj.insert("type".to_string(), Value::String("command".to_string()));
            obj.insert("command".to_string(), Value::String(command.to_string()));
            root.insert("statusLine".to_string(), Value::Object(obj));
        }
    }
}

/// Remove Costroid's `statusLine` wiring when there is no backup to restore from.
/// Returns whether anything changed. A non-Costroid status line is left untouched.
///
/// A path-1 snippet (sentinel-bearing) embeds the user's ORIGINAL renderer — deleting
/// the key would discard it, so the original is parsed back out of the snippet and
/// restored instead. Only the exact path-2 command (`costroid statusline`, nothing of
/// the user's inside) removes the key. An unparseable sentinel-bearing command is left
/// untouched rather than destroyed.
fn strip_wiring(settings: &mut Value) -> bool {
    let Some(cmd) = current_statusline_command(settings) else {
        return false;
    };
    if cmd.trim() == COSTROID_STATUSLINE_CMD {
        return match settings.as_object_mut() {
            Some(obj) => obj.remove("statusLine").is_some(),
            None => false,
        };
    }
    if cmd.contains(SENTINEL) {
        if let Some(original) = original_from_snippet(&cmd) {
            set_statusline_command(settings, &original);
            return true;
        }
    }
    false
}

/// Serialize settings pretty-printed (round-tripped — unknown keys preserved). Note: the
/// workspace `serde_json` is BTreeMap-backed, so unrelated keys are written in sorted
/// order; the backup preserves the original verbatim. Written via a unique temp file +
/// rename, like the cache: a crash mid-write must never leave a torn `settings.json`
/// (which would make a re-run refuse to touch it).
fn write_settings(path: &Path, value: &Value) -> Result<()> {
    let mut bytes = serde_json::to_vec_pretty(value).context("serializing settings.json")?;
    bytes.push(b'\n');
    let tmp = unique_tmp_sibling(path);
    fs::write(&tmp, &bytes).with_context(|| format!("writing {}", tmp.display()))?;
    fs::rename(&tmp, path).with_context(|| format!("renaming into {}", path.display()))?;
    Ok(())
}

/// What `setup_at` did, for messaging + tests.
#[derive(Debug, PartialEq, Eq)]
enum SetupReport {
    BecameStatusline,
    WrappedExisting,
    AlreadyWired,
    Restored,
    StrippedWiring,
    NothingToUndo,
}

/// File-level orchestration for one `settings.json`, decoupled from `HostEnv` and the
/// cache path so it is unit-testable with explicit temp paths. Never panics; a malformed
/// `settings.json` is refused (never clobbered), an absent one is a fresh object.
fn setup_at(settings_path: &Path, backup_path: &Path, undo: bool) -> Result<SetupReport> {
    if undo {
        return undo_at(settings_path, backup_path);
    }

    let existed = settings_path.exists();
    let settings = if existed {
        let bytes = fs::read(settings_path)
            .with_context(|| format!("reading {}", settings_path.display()))?;
        match serde_json::from_slice::<Value>(&bytes) {
            Ok(value) => value,
            Err(err) => bail!(
                "{} is not valid JSON ({err}); refusing to overwrite it. Fix or remove \
                 it, then re-run `costroid setup-statusline`.",
                settings_path.display()
            ),
        }
    } else {
        Value::Object(Map::new())
    };

    let (updated, outcome) = apply_setup(settings);
    match outcome {
        SetupOutcome::AlreadyWired => Ok(SetupReport::AlreadyWired),
        SetupOutcome::WrappedExisting | SetupOutcome::BecameStatusline => {
            // Back up the original before the first write — never clobber a pristine copy
            // on a later run (only if it existed and there is no backup yet).
            if existed && !backup_path.exists() {
                fs::copy(settings_path, backup_path)
                    .with_context(|| format!("backing up to {}", backup_path.display()))?;
            }
            write_settings(settings_path, &updated)?;
            Ok(match outcome {
                SetupOutcome::WrappedExisting => SetupReport::WrappedExisting,
                _ => SetupReport::BecameStatusline,
            })
        }
    }
}

/// `--undo`: restore the backed-up `settings.json` (or strip our wiring when there is no
/// backup, e.g. a fresh path-2 file).
fn undo_at(settings_path: &Path, backup_path: &Path) -> Result<SetupReport> {
    if backup_path.exists() {
        fs::copy(backup_path, settings_path)
            .with_context(|| format!("restoring {}", settings_path.display()))?;
        fs::remove_file(backup_path)
            .with_context(|| format!("removing {}", backup_path.display()))?;
        return Ok(SetupReport::Restored);
    }
    if settings_path.exists() {
        let bytes = fs::read(settings_path)
            .with_context(|| format!("reading {}", settings_path.display()))?;
        if let Ok(mut value) = serde_json::from_slice::<Value>(&bytes) {
            if strip_wiring(&mut value) {
                // If stripping leaves nothing, the file held only our wiring — it was a
                // fresh path-2 file we created, so remove it rather than leaving `{}`.
                if value.as_object().is_some_and(Map::is_empty) {
                    fs::remove_file(settings_path)
                        .with_context(|| format!("removing {}", settings_path.display()))?;
                } else {
                    write_settings(settings_path, &value)?;
                }
                return Ok(SetupReport::StrippedWiring);
            }
        }
    }
    Ok(SetupReport::NothingToUndo)
}

/// The single root Claude Code reads `settings.json` from: the first *existing* root in
/// `HostEnv::claude_roots()` order (so a set `CLAUDE_CONFIG_DIR` — listed first — wins
/// when it exists). `None` when no candidate exists, which `run_setup_statusline` turns
/// into actionable guidance rather than creating config in a guessed location.
fn resolve_config_root(env: &HostEnv) -> Option<PathBuf> {
    env.claude_roots().into_iter().find(|root| root.exists())
}

fn print_no_root(env: &HostEnv) {
    eprintln!("No Claude Code config directory found. Looked in:");
    for root in env.claude_roots() {
        eprintln!("  {}", root.display());
    }
    eprintln!(
        "Run Claude Code once, or set CLAUDE_CONFIG_DIR, then re-run \
         `costroid setup-statusline`."
    );
}

fn print_post_setup() {
    println!("Live 5h/7d quota appears after your next Claude Code response (Pro/Max plans).");
    println!("Undo anytime: costroid setup-statusline --undo");
}

/// Remove the capture cache, if present and resolvable. Best-effort.
fn remove_cache() {
    if let Some(cache_path) = costroid_providers::claude_rate_limits_cache_path() {
        if cache_path.exists() && fs::remove_file(&cache_path).is_ok() {
            println!("Removed capture cache {}", cache_path.display());
        }
    }
}

/// Entry point for `costroid setup-statusline [--undo]`.
pub fn run_setup_statusline(env: &HostEnv, undo: bool) -> Result<()> {
    let Some(root) = resolve_config_root(env) else {
        print_no_root(env);
        return Ok(());
    };
    let settings_path = root.join("settings.json");
    let backup_path = root.join(BACKUP_NAME);
    println!("Claude config root: {}", root.display());

    let report = setup_at(&settings_path, &backup_path, undo)?;

    if undo {
        match report {
            SetupReport::Restored => {
                println!("Restored settings.json from {}", backup_path.display());
            }
            SetupReport::StrippedWiring => {
                println!("Removed Costroid statusLine wiring from settings.json.");
            }
            _ => println!("No Costroid statusLine wiring found — nothing to undo."),
        }
        remove_cache();
        return Ok(());
    }

    match report {
        SetupReport::AlreadyWired => {
            println!("Already wired ({SENTINEL}) — nothing to do.");
        }
        SetupReport::WrappedExisting => {
            if backup_path.exists() {
                println!("Backed up settings.json → {}", backup_path.display());
            }
            println!("Wired Costroid quota capture into your existing statusLine.");
            print_post_setup();
        }
        SetupReport::BecameStatusline => {
            if backup_path.exists() {
                println!("Backed up settings.json → {}", backup_path.display());
            }
            println!("Set Costroid as your Claude Code statusLine (`{COSTROID_STATUSLINE_CMD}`).");
            print_post_setup();
        }
        _ => {}
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    const RAW_STDIN: &str = include_str!("../../../fixtures/claude-code/statusline-stdin.json");

    /// A fixed instant so tests never depend on the wall clock.
    fn fixed_time() -> DateTime<Utc> {
        DateTime::parse_from_rfc3339("2026-06-06T12:00:00Z")
            .map(|dt| dt.with_timezone(&Utc))
            .unwrap_or_else(|_| Utc::now())
    }

    fn test_dir(tag: &str) -> PathBuf {
        let dir = std::env::temp_dir().join(format!("costroid-t5-{}-{}", tag, std::process::id()));
        let _ = fs::remove_dir_all(&dir);
        if let Err(err) = fs::create_dir_all(&dir) {
            panic!("creating test dir: {err}");
        }
        dir
    }

    fn cleanup(dir: &Path) {
        let _ = fs::remove_dir_all(dir);
    }

    // --- capture writer ---

    #[test]
    fn build_cache_keeps_only_allowed_fields() {
        let value = match build_cache_value(RAW_STDIN.as_bytes(), fixed_time()) {
            Some(value) => value,
            None => panic!("expected a cache value from valid rate_limits"),
        };
        let obj = match value.as_object() {
            Some(obj) => obj,
            None => panic!("cache must be a JSON object"),
        };
        assert_eq!(
            obj.get("captured_at").and_then(Value::as_str),
            Some("2026-06-06T12:00:00+00:00")
        );
        for key in ["five_hour", "seven_day"] {
            let window = match obj.get(key).and_then(Value::as_object) {
                Some(window) => window,
                None => panic!("{key} window missing"),
            };
            let mut keys: Vec<&str> = window.keys().map(String::as_str).collect();
            keys.sort_unstable();
            assert_eq!(
                keys,
                vec!["resets_at", "used_percentage"],
                "{key} leaked fields beyond the security floor"
            );
        }
        // No secret / extraneous field ever reaches the cache.
        let serialized = value.to_string();
        assert!(!serialized.contains("secret"));
        assert!(!serialized.contains("should-be-dropped"));
        assert!(!serialized.contains("session"));
        assert!(!serialized.contains("leftover"));
    }

    #[test]
    fn build_cache_none_without_rate_limits() {
        assert!(build_cache_value(br#"{"model":{"id":"x"}}"#, fixed_time()).is_none());
    }

    #[test]
    fn build_cache_none_on_malformed_or_empty_input() {
        assert!(build_cache_value(b"not json at all", fixed_time()).is_none());
        assert!(build_cache_value(b"", fixed_time()).is_none());
    }

    #[test]
    fn build_cache_none_when_windows_have_no_usable_fields() {
        let input = br#"{"rate_limits":{"five_hour":{"other":1}}}"#;
        assert!(build_cache_value(input, fixed_time()).is_none());
    }

    #[test]
    fn capture_to_path_writes_then_skips_bad_input() {
        let dir = test_dir("capture");
        let cache = dir.join("claude-rate-limits.json");

        // Good input writes a readable cache.
        let wrote = match capture_to_path(RAW_STDIN.as_bytes(), &cache, fixed_time()) {
            Ok(wrote) => wrote,
            Err(err) => panic!("capture failed: {err}"),
        };
        assert!(wrote);
        assert!(cache.exists());

        // Bad input writes nothing and is not an error (the exit-0 contract).
        let untouched = dir.join("none.json");
        let wrote = match capture_to_path(b"garbage", &untouched, fixed_time()) {
            Ok(wrote) => wrote,
            Err(err) => panic!("bad input must not error: {err}"),
        };
        assert!(!wrote);
        assert!(!untouched.exists());

        cleanup(&dir);
    }

    // --- setup transform ---

    #[test]
    fn setup_becomes_statusline_when_none() {
        let (updated, outcome) = apply_setup(Value::Object(Map::new()));
        assert_eq!(outcome, SetupOutcome::BecameStatusline);
        assert_eq!(
            current_statusline_command(&updated).as_deref(),
            Some(COSTROID_STATUSLINE_CMD)
        );
    }

    #[test]
    fn setup_wraps_existing_statusline_preserving_keys() {
        let mut settings = Map::new();
        let mut sl = Map::new();
        sl.insert("type".into(), Value::String("command".into()));
        sl.insert("command".into(), Value::String("ccusage statusline".into()));
        settings.insert("statusLine".into(), Value::Object(sl));
        settings.insert("theme".into(), Value::String("dark".into()));

        let (updated, outcome) = apply_setup(Value::Object(settings));
        assert_eq!(outcome, SetupOutcome::WrappedExisting);
        let cmd = match current_statusline_command(&updated) {
            Some(cmd) => cmd,
            None => panic!("command must be present"),
        };
        assert!(cmd.contains(SENTINEL));
        assert!(cmd.contains("ccusage statusline")); // original preserved inside
        assert!(cmd.contains("--capture-only"));
        // Unknown keys survive the round-trip.
        assert_eq!(updated.get("theme").and_then(Value::as_str), Some("dark"));
    }

    #[test]
    fn setup_is_idempotent() {
        // Path 2 then re-run → AlreadyWired, unchanged.
        let (once, _) = apply_setup(Value::Object(Map::new()));
        let (twice, outcome) = apply_setup(once.clone());
        assert_eq!(outcome, SetupOutcome::AlreadyWired);
        assert_eq!(once, twice);

        // Path 1 then re-run → AlreadyWired, unchanged (sentinel detected).
        let mut settings = Map::new();
        let mut sl = Map::new();
        sl.insert("command".into(), Value::String("ccusage statusline".into()));
        settings.insert("statusLine".into(), Value::Object(sl));
        let (wrapped, _) = apply_setup(Value::Object(settings));
        let (again, outcome) = apply_setup(wrapped.clone());
        assert_eq!(outcome, SetupOutcome::AlreadyWired);
        assert_eq!(wrapped, again);
    }

    #[test]
    fn strip_wiring_removes_path2_only() {
        let (mut path2, _) = apply_setup(Value::Object(Map::new()));
        assert!(strip_wiring(&mut path2));
        assert!(path2.get("statusLine").is_none());

        // A non-costroid status line is left untouched.
        let mut settings = Map::new();
        let mut sl = Map::new();
        sl.insert("command".into(), Value::String("ccusage statusline".into()));
        settings.insert("statusLine".into(), Value::Object(sl));
        let mut other = Value::Object(settings);
        assert!(!strip_wiring(&mut other));
        assert!(other.get("statusLine").is_some());
    }

    #[test]
    fn snippet_group_form_survives_multiline_and_comment_originals() {
        // The original is wrapped in a `{ … }` group on its own line(s), so a
        // multi-line original (or one starting with a #-comment) still receives the
        // piped stdin — the flat form piped only into its FIRST line.
        let original = "# my statusline\nccusage statusline --fancy";
        let snippet = capture_snippet(original);
        assert!(snippet.contains("| {\n"));
        assert!(snippet.ends_with("\n}"));
        assert!(snippet.contains(original));
        // And --undo's no-backup fallback recovers the original verbatim.
        assert_eq!(original_from_snippet(&snippet).as_deref(), Some(original));
    }

    #[test]
    fn original_from_snippet_understands_the_flat_legacy_form() {
        // Pre-fix installs carry the flat (ungrouped) snippet; --undo must still
        // recover their original renderer.
        let legacy = format!(
            "{SENTINEL}\ninput=$(cat); printf '%s' \"$input\" | {COSTROID_STATUSLINE_CMD} \
             --capture-only; printf '%s' \"$input\" | ccusage statusline"
        );
        assert_eq!(
            original_from_snippet(&legacy).as_deref(),
            Some("ccusage statusline")
        );
    }

    #[test]
    fn strip_wiring_restores_the_wrapped_original_for_path1() {
        // A path-1 snippet embeds the user's original renderer — undo without a backup
        // must RESTORE it, never delete the statusLine key (that would discard the
        // user's own command).
        let mut settings = Map::new();
        let mut sl = Map::new();
        sl.insert("command".into(), Value::String("ccusage statusline".into()));
        settings.insert("statusLine".into(), Value::Object(sl));
        let (mut wrapped, _) = apply_setup(Value::Object(settings));
        assert!(strip_wiring(&mut wrapped));
        assert_eq!(
            current_statusline_command(&wrapped).as_deref(),
            Some("ccusage statusline")
        );
    }

    #[test]
    fn clean_window_drops_non_scalar_values() {
        // An object-shaped used_percentage (a hypothetical future upstream schema
        // change) must never be persisted to the no-secret cache; scalar values pass.
        let input = br#"{"rate_limits":{
            "five_hour":{"used_percentage":{"secret":"leak"},"resets_at":1781000000},
            "seven_day":{"used_percentage":41.5,"resets_at":"2026-06-12T00:00:00Z"}}}"#;
        let value = match build_cache_value(input, fixed_time()) {
            Some(value) => value,
            None => panic!("seven_day still yields a cache"),
        };
        assert!(!value.to_string().contains("secret"));
        // five_hour kept only its scalar resets_at — the object pct was dropped.
        assert!(value.pointer("/five_hour/used_percentage").is_none());
        assert!(value.pointer("/five_hour/resets_at").is_some());
        // seven_day kept both scalars (number pct + string reset).
        assert!(value.pointer("/seven_day/used_percentage").is_some());
        assert!(value.pointer("/seven_day/resets_at").is_some());
    }

    #[test]
    fn unique_tmp_siblings_never_collide() {
        let path = Path::new("/tmp/example/settings.json");
        let first = unique_tmp_sibling(path);
        let second = unique_tmp_sibling(path);
        assert_ne!(first, second, "two writers must never share a temp path");
        assert_eq!(
            first.parent(),
            path.parent(),
            "same directory keeps the rename atomic"
        );
    }

    // --- file orchestration (backup / undo / malformed) ---

    #[test]
    fn setup_at_roundtrip_backup_and_undo() {
        let dir = test_dir("setupat");
        let settings = dir.join("settings.json");
        let backup = dir.join(BACKUP_NAME);

        let original =
            r#"{"statusLine":{"type":"command","command":"ccusage statusline"},"theme":"dark"}"#;
        if let Err(err) = fs::write(&settings, original) {
            panic!("seeding settings: {err}");
        }

        let report = match setup_at(&settings, &backup, false) {
            Ok(report) => report,
            Err(err) => panic!("setup_at: {err}"),
        };
        assert_eq!(report, SetupReport::WrappedExisting);
        assert!(backup.exists());

        // Re-run is a no-op.
        let report = match setup_at(&settings, &backup, false) {
            Ok(report) => report,
            Err(err) => panic!("setup_at re-run: {err}"),
        };
        assert_eq!(report, SetupReport::AlreadyWired);

        // Undo restores the exact original bytes and removes the backup.
        let report = match setup_at(&settings, &backup, true) {
            Ok(report) => report,
            Err(err) => panic!("undo: {err}"),
        };
        assert_eq!(report, SetupReport::Restored);
        assert!(!backup.exists());
        let restored = fs::read_to_string(&settings).unwrap_or_default();
        assert_eq!(restored, original);

        cleanup(&dir);
    }

    #[test]
    fn setup_at_refuses_malformed_settings() {
        let dir = test_dir("malformed");
        let settings = dir.join("settings.json");
        let malformed = "{ not valid json ";
        if let Err(err) = fs::write(&settings, malformed) {
            panic!("seeding: {err}");
        }
        let result = setup_at(&settings, &dir.join(BACKUP_NAME), false);
        assert!(
            result.is_err(),
            "malformed settings.json must be refused, not clobbered"
        );
        // The malformed file is left untouched.
        let after = fs::read_to_string(&settings).unwrap_or_default();
        assert_eq!(after, malformed);
        cleanup(&dir);
    }

    #[test]
    fn setup_at_fresh_file_then_undo_strips() {
        let dir = test_dir("fresh");
        let settings = dir.join("settings.json");
        let backup = dir.join(BACKUP_NAME);

        // No settings.json yet → path 2, no backup created.
        let report = match setup_at(&settings, &backup, false) {
            Ok(report) => report,
            Err(err) => panic!("setup_at: {err}"),
        };
        assert_eq!(report, SetupReport::BecameStatusline);
        assert!(settings.exists());
        assert!(!backup.exists());

        // Undo with no backup strips our wiring; since the file held only our wiring (we
        // created it fresh), it is removed entirely rather than left as `{}`.
        let report = match setup_at(&settings, &backup, true) {
            Ok(report) => report,
            Err(err) => panic!("undo: {err}"),
        };
        assert_eq!(report, SetupReport::StrippedWiring);
        assert!(!settings.exists(), "a fresh path-2 file is removed on undo");
        cleanup(&dir);
    }
}
