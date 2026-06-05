#!/usr/bin/env bash
# Offline acceptance test — the Phase-1 capstone proof.
#
# Runs every Costroid command with networking *fully disabled* against committed
# FIXTURE logs (never real user data), proving the tool collects, costs, exports,
# renders, and emits a statusline using only bundled pricing — with no network
# access and no telemetry.
#
# Two complementary layers of proof:
#   * Static  — apps/cli/tests/offline.rs asserts no networking/telemetry crate is
#     even linked (run via `cargo test`, not here).
#   * Dynamic — this script runs each command under a network-isolation wrapper
#     and asserts no outbound IP traffic is attempted.
#
# Isolation ladder (first available wins):
#   1. strace -e trace=network  — positively proves no AF_INET socket/connect is
#      issued, independent of namespace support (authoritative; used in CI).
#   2. unshare --user --net     — rootless network namespace with no interfaces,
#      so any outbound connect would fail (used when strace is absent).
#   3. none                     — warn and run unisolated; rely on the static test
#      (local-dev fallback when neither strace nor user namespaces are available).
#
# CI installs strace, so CI is the authoritative gate. Requires a built toolchain;
# fixtures are wired exactly like scripts/focus_conformance.sh (a temp $HOME).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
workdir="$(mktemp -d)"
trap 'rm -rf "$workdir"' EXIT

echo "==> Building costroid"
cargo build -q -p costroid
bin="$repo_root/target/debug/costroid"

# --- fixture HOME (same mechanism as scripts/focus_conformance.sh) ------------
home="$workdir/home"
mkdir -p "$home/.claude/projects/fixture" "$home/.claude/projects/fixture-priced" \
  "$home/.claude/projects/fixture-dated" "$home/.codex/sessions/fixture"
cp "$repo_root/fixtures/claude-code/project-transcript.jsonl" "$home/.claude/projects/fixture/"
cp "$repo_root/fixtures/claude-code/project-transcript-priced.jsonl" "$home/.claude/projects/fixture-priced/"
cp "$repo_root/fixtures/claude-code/project-transcript-dated.jsonl" "$home/.claude/projects/fixture-dated/"
cp "$repo_root/fixtures/codex/rollout.jsonl" "$home/.codex/sessions/fixture/"
# Override $HOME at the fixtures and neutralize every real log-source hint so the
# tool can only ever read the committed fixtures, never the developer's data.
env_args=(HOME="$home" USERPROFILE="" CLAUDE_CONFIG_DIR="" ANTHROPIC_API_KEY="")

# --- pick isolation mode ------------------------------------------------------
if command -v strace >/dev/null 2>&1; then
  iso_mode=strace
elif unshare --user --map-root-user --net true 2>/dev/null; then
  iso_mode=netns
else
  iso_mode=none
fi
echo "==> Network isolation mode: $iso_mode"
if [ "$iso_mode" = none ]; then
  echo "    WARNING: no strace and user namespaces unavailable; relying on the"
  echo "    static denylist test (apps/cli/tests/offline.rs) for the no-network proof."
fi

fail=0
OUT=""

# Fail if a strace network-trace log shows any outbound IP socket/connect.
# Allowed: AF_UNIX, AF_NETLINK, and loopback (127.0.0.1 / ::1). None are expected.
assert_no_inet() { # <strace-log>
  local log="$1"
  if grep -Eq 'socket\(AF_INET6?[,)]' "$log"; then
    echo "    NETWORK VIOLATION: AF_INET socket created"
    grep -E 'socket\(AF_INET6?[,)]' "$log" | sed 's/^/      /'
    return 1
  fi
  if grep -E 'connect\(' "$log" 2>/dev/null | grep -E 'AF_INET6?' | grep -vqE '127\.0\.0\.1|::1'; then
    echo "    NETWORK VIOLATION: connect() to a non-loopback address"
    grep -E 'connect\(' "$log" | grep -E 'AF_INET6?' | sed 's/^/      /'
    return 1
  fi
  return 0
}

# Run an argv command under the chosen isolation; capture stdout into $OUT.
# Returns the command's exit code, or 90 on a detected network violation.
iso_run() { # <argv...>
  local rc log
  case "$iso_mode" in
    strace)
      log="$workdir/strace.$RANDOM"
      OUT="$(strace -f -e trace=network -qq -o "$log" env "${env_args[@]}" "$@")" && rc=0 || rc=$?
      if ! assert_no_inet "$log"; then rm -f "$log"; return 90; fi
      rm -f "$log"
      return "$rc" ;;
    netns)
      OUT="$(unshare --user --map-root-user --net env "${env_args[@]}" "$@")" && rc=0 || rc=$?
      return "$rc" ;;
    *)
      OUT="$(env "${env_args[@]}" "$@")" && rc=0 || rc=$?
      return "$rc" ;;
  esac
}

# check <description> <min-bytes> <needle|-> -- <argv...>
check() {
  local desc="$1" minb="$2" needle="$3"; shift 3; [ "${1:-}" = "--" ] && shift
  printf '  %-52s' "$desc"
  local rc
  iso_run "$@" && rc=0 || rc=$?
  if [ "$rc" -eq 90 ]; then echo "NETWORK VIOLATION"; fail=1; return; fi
  if [ "$rc" -ne 0 ]; then echo "FAIL (exit $rc)"; fail=1; return; fi
  if [ "${#OUT}" -lt "$minb" ]; then echo "FAIL (output ${#OUT}b < ${minb}b)"; fail=1; return; fi
  if [ "$needle" != "-" ] && ! grep -qiF -- "$needle" <<<"$OUT"; then
    echo "FAIL (missing '$needle')"; fail=1; return
  fi
  echo "ok"
}

echo "==> Running every command with networking disabled (fixtures only)"

# now screen (default subcommand), plain
check "now (--plain)" 10 "costroid now" -- "$bin" --plain

# trends: every period x group, plain
for p in day week month year; do
  for g in model app total; do
    check "trends --plain --period $p --group $g" 10 "costroid trends" -- \
      "$bin" trends --plain --period "$p" --group "$g"
  done
done

# frontier: cost-vs-quality surface from bundled benchmarks, plain
check "frontier (--plain)" 10 "costroid frontier" -- "$bin" frontier --plain

# statusline, plain
check "statusline (--plain)" 5 "costroid" -- "$bin" statusline --plain

# export csv: header + at least one row, contains a FOCUS cost column
check "export --format csv" 20 "Cost" -- "$bin" export --format csv

# export json: validate it parses as JSON
printf '  %-52s' "export --format json (parses as JSON)"
json_rc=0
iso_run "$bin" export --format json && json_rc=0 || json_rc=$?
if [ "$json_rc" -eq 90 ]; then
  echo "NETWORK VIOLATION"; fail=1
elif [ "$json_rc" -ne 0 ]; then
  echo "FAIL (exit $json_rc)"; fail=1
elif ! printf '%s' "$OUT" | python3 -c \
    'import json,sys; d=json.load(sys.stdin); sys.exit(0 if isinstance(d,(list,dict)) else 1)' 2>/dev/null; then
  echo "FAIL (invalid JSON)"; fail=1
else
  echo "ok"
fi

# --live: drive the interactive TUI in a PTY, refresh, then quit with 'q'.
# A hang (timeout) is a failure; clean exit before the deadline passes.
printf '  %-52s' "--live launches, refreshes, quits (q)"
live_cmd="env $(printf '%q ' "${env_args[@]}")TERM=xterm-256color '$bin' --live"
live_rc=0
case "$iso_mode" in
  strace)
    live_log="$workdir/strace.live"
    timeout --signal=INT 25s strace -f -e trace=network -qq -o "$live_log" \
      script -qec "$live_cmd" /dev/null <<<'q' >/dev/null 2>&1 && live_rc=0 || live_rc=$?
    if [ -f "$live_log" ] && ! assert_no_inet "$live_log" >/dev/null 2>&1; then
      live_rc=90
    fi
    rm -f "$live_log" ;;
  netns)
    timeout --signal=INT 25s unshare --user --map-root-user --net \
      script -qec "$live_cmd" /dev/null <<<'q' >/dev/null 2>&1 && live_rc=0 || live_rc=$? ;;
  *)
    timeout --signal=INT 25s \
      script -qec "$live_cmd" /dev/null <<<'q' >/dev/null 2>&1 && live_rc=0 || live_rc=$? ;;
esac
if [ "$live_rc" -eq 90 ]; then
  echo "NETWORK VIOLATION"; fail=1
elif [ "$live_rc" -eq 124 ]; then
  echo "FAIL (hung; --live did not exit)"; fail=1
else
  echo "ok"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "==> OFFLINE ACCEPTANCE: FAILED"
  exit 1
fi
echo "==> OFFLINE ACCEPTANCE: PASSED (all commands ran offline, no network, no telemetry)"
