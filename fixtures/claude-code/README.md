# Claude Code fixtures

Committed sample data for the Claude Code adapter's tests — **synthetic, no real
user data and no secrets.**

- **`rate-limits-*.json`** — the sanctioned `statusLine` `rate_limits` cache (T4).
  Each is a `claude-rate-limits.json`-shaped file exercising one parse/sanitize path:
  `happy` (in-range → Verified), `impossible-900` (>100 → sanitized out),
  `poisoned-epoch` (`used_percentage == resets_at` → sanitized out), `false-100`
  (flat 100%, demoted to Unverified by the core cross-check against trivial volume),
  `absent` (window key missing → Unavailable), `stale` (`resets_at` in the past →
  aged out at the core layer), and `iso-resets` (`resets_at` as an RFC3339 string).
- **`project-transcript*.jsonl`** — session transcripts for the usage parser (cost +
  model mix): the base case, a dated-snapshot model id, and a priced model.
- **`dedup-*.jsonl`** — transcript de-duplication cases (golden a/b, keyless entries).
- **`installed-no-usage.json`** — the "installed, usage unavailable" discovery case.
- **`statusline-stdin.json`** — a raw Claude Code `statusLine` stdin session object (T5
  capture-writer tests): the full hook payload (`session_id`, `model`, `workspace`,
  `rate_limits`) plus `leftover` and `secret_should_never_be_written` fields, proving the
  writer keeps only `used_percentage` + `resets_at` per window (plus a top-level
  `captured_at`) and never leaks the secret or any extra field.
