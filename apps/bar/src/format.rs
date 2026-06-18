//! Plain-text formatting for the tray tooltip and the window's mark line.
//!
//! Mirrors the CLI's voice (`apps/cli/src/render.rs`) — provider name, window label,
//! percent, the compact reset countdown, and the "as of HH:MM" freshness stamp — so the
//! taskbar reads the same as `costroid`. (These small formatters are duplicated rather
//! than shared because the CLI's are private to `apps/cli`; consolidating them into core
//! is a possible T19 cleanup.)

use chrono::{DateTime, Utc};
use costroid_core::{LimitAvailability, LimitKind, ProviderId};

use crate::severity::Constraint;

/// Age past which an in-window quota reading carries the "as of HH:MM" stamp — mirrors the
/// CLI's `LIMIT_FRESHNESS_STAMP_MINUTES` (brief §8): a reading at least this many minutes
/// older than the render must show its age, so an hours-old cached number never renders as
/// a bare, confident meter.
pub const LIMIT_FRESHNESS_STAMP_MINUTES: i64 = 10;

/// Lower-case provider name, matching `render.rs::provider_name`.
pub fn provider_label(provider: ProviderId) -> &'static str {
    match provider {
        ProviderId::ClaudeCode => "claude code",
        ProviderId::Codex => "codex",
        ProviderId::Cursor => "cursor",
    }
}

/// Short window label, matching `render.rs::limit_kind`.
pub fn kind_label(kind: LimitKind) -> &'static str {
    match kind {
        LimitKind::FiveHour => "5h",
        LimitKind::Weekly => "wk",
        LimitKind::Daily => "1d",
        LimitKind::Monthly => "mo",
        LimitKind::BillingCycle => "cyc",
    }
}

/// `"92%"`, matching `render.rs::percent`.
pub fn percent(fraction: f64) -> String {
    format!("{:.0}%", (fraction * 100.0).round())
}

/// Compact, two-largest-non-zero-units reset countdown, matching
/// `render.rs::reset_countdown`.
pub fn reset_countdown(seconds: i64) -> String {
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

/// The capture-time stamp for a reading: `"as of HH:MM"` (UTC, matching the CLI), or
/// `"capture time unknown"` for the UNIX-epoch sentinel (no observation instant recorded
/// — never a confident `as of 00:00`, matching `render.rs::freshness_stamp`).
pub fn as_of(captured_at: DateTime<Utc>) -> String {
    if captured_at.timestamp() == 0 {
        "capture time unknown".to_string()
    } else {
        format!("as of {}", captured_at.format("%H:%M"))
    }
}

/// The in-window freshness stamp for a quota reading: `"as of HH:MM"` (UTC) once the
/// reading is at least [`LIMIT_FRESHNESS_STAMP_MINUTES`] older than the render, `""` while
/// still fresh, or `"capture time unknown"` for the UNIX-epoch sentinel (no observation
/// instant recorded — never a confident `as of 00:00`). Mirrors the CLI's `freshness_stamp`
/// (brief §8); unlike the always-on [`as_of`] the tray tooltip uses, this one is empty for a
/// fresh reading so the in-window meter only stamps an aged number.
pub fn freshness_stamp(captured_at: DateTime<Utc>, generated_at: DateTime<Utc>) -> String {
    if captured_at.timestamp() == 0 {
        return "capture time unknown".to_string();
    }
    if (generated_at - captured_at).num_minutes() >= LIMIT_FRESHNESS_STAMP_MINUTES {
        format!("as of {}", captured_at.format("%H:%M"))
    } else {
        String::new()
    }
}

/// Group a (possibly signed) integer string with `,` thousands separators (e.g. `"12345"`
/// → `"12,345"`, `"-1000"` → `"-1,000"`). Mirrors the CLI's `with_thousands` exactly,
/// including stripping a leading `-` before grouping so the sign never gets a stray comma.
/// Used for the `Estimated` arm's local token volume (always non-negative), but the sign
/// handling keeps it a faithful mirror.
pub fn with_thousands(value: &str) -> String {
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
    let grouped: String = reversed.chars().rev().collect();
    format!("{sign}{grouped}")
}

/// The tray tooltip: the precise most-constrained line, e.g.
/// `"claude code 5h — 92% used · resets in 41m · as of 15:32"`, or an honest idle line
/// when no window is fresh-`Available` (STEP6-TASKBAR-DESIGN §3).
pub fn tooltip(constraint: Option<&Constraint>) -> String {
    match constraint {
        Some(c) => constraint_line(c),
        None => "costroid — no live quota reading".to_string(),
    }
}

/// The one-line description of a constrained window, shared by the tooltip and the
/// window header.
pub fn constraint_line(constraint: &Constraint) -> String {
    let limit = &constraint.limit;
    let tool = provider_label(limit.tool);
    let kind = kind_label(limit.kind);
    let pct = percent(constraint.fraction);
    let stamp = as_of(limit.captured_at);
    match &limit.availability {
        LimitAvailability::Available {
            reset_in_seconds, ..
        } => format!(
            "{tool} {kind} — {pct} used · resets in {} · {stamp}",
            reset_countdown(*reset_in_seconds)
        ),
        // `most_constrained_available` only ever yields the `Available` arm, so this is
        // unreachable in practice; render honestly rather than panic if it ever changes.
        _ => format!("{tool} {kind} — {pct} used · {stamp}"),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use costroid_core::{LimitMeasure, LimitSummary};

    fn ts(secs: i64) -> DateTime<Utc> {
        match DateTime::from_timestamp(secs, 0) {
            Some(dt) => dt,
            None => panic!("invalid test timestamp {secs}"),
        }
    }

    #[test]
    fn percent_rounds_like_the_cli() {
        assert_eq!(percent(0.0), "0%");
        assert_eq!(percent(0.925), "93%");
        assert_eq!(percent(1.0), "100%");
    }

    #[test]
    fn reset_countdown_uses_compact_two_units() {
        assert_eq!(reset_countdown(0), "<1m");
        assert_eq!(reset_countdown(30), "<1m");
        assert_eq!(reset_countdown(41 * 60), "41m");
        assert_eq!(reset_countdown(2 * 3600 + 14 * 60), "2h 14m");
        assert_eq!(reset_countdown(3 * 86400 + 4 * 3600), "3d 4h");
        assert_eq!(reset_countdown(3 * 3600), "3h");
    }

    #[test]
    fn freshness_stamp_only_past_the_threshold() {
        // 1970-01-01 08:00:00 UTC captured; render 8 minutes later → still fresh, no stamp.
        let captured = ts(8 * 3600);
        assert_eq!(freshness_stamp(captured, ts(8 * 3600 + 8 * 60)), "");
        // 12 minutes later → past the 10-minute threshold → stamped at capture time.
        assert_eq!(
            freshness_stamp(captured, ts(8 * 3600 + 12 * 60)),
            "as of 08:00"
        );
        // The epoch sentinel discloses the unknown age, never a confident "as of 00:00".
        assert_eq!(freshness_stamp(ts(0), ts(8 * 3600)), "capture time unknown");
    }

    #[test]
    fn with_thousands_groups_in_threes() {
        assert_eq!(with_thousands("0"), "0");
        assert_eq!(with_thousands("999"), "999");
        assert_eq!(with_thousands("1000"), "1,000");
        assert_eq!(with_thousands("1234567"), "1,234,567");
        // The sign is stripped before grouping (mirrors the CLI), so a 3*k-digit negative
        // never gets a stray comma right after the minus.
        assert_eq!(with_thousands("-123"), "-123");
        assert_eq!(with_thousands("-1234"), "-1,234");
    }

    #[test]
    fn as_of_handles_real_and_sentinel_times() {
        // 1970-01-01 00:00:00 UTC + 55_500s = 15:25 UTC.
        assert_eq!(as_of(ts(55_500)), "as of 15:25");
        assert_eq!(as_of(ts(0)), "capture time unknown");
    }

    #[test]
    fn tooltip_idle_is_honest() {
        assert_eq!(tooltip(None), "costroid — no live quota reading");
    }

    #[test]
    fn tooltip_constraint_reads_like_the_brand() {
        let constraint = Constraint {
            limit: LimitSummary {
                tool: ProviderId::ClaudeCode,
                plan: None,
                kind: LimitKind::FiveHour,
                label: None,
                captured_at: ts(55_500), // 15:25 UTC
                availability: LimitAvailability::Available {
                    measure: LimitMeasure::TokenFraction(0.92),
                    resets_at: ts(55_500 + 41 * 60),
                    reset_in_seconds: 41 * 60,
                },
            },
            fraction: 0.92,
        };
        assert_eq!(
            tooltip(Some(&constraint)),
            "claude code 5h — 92% used · resets in 41m · as of 15:25"
        );
    }
}
