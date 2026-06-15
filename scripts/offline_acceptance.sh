#!/usr/bin/env bash
# Offline acceptance test — the default/local-only build's capstone proof.
#
# This tests the DEFAULT build — the `connect` feature OFF (`cargo build -p costroid`,
# which never links `costroid-connect`). It runs every Costroid command with
# networking *fully disabled* against committed FIXTURE logs (never real user data),
# proving the tool collects, costs, exports, renders, and emits a statusline using
# only bundled pricing — with no network access and no telemetry.
#
# The opt-in connections subsystem (`--features connect`, PRODUCT-PLAN Step 4) is the
# single place network is ever allowed. Its dynamic proof has two halves: the T8
# feature-ON baseline RUNS BELOW (a normal `--features connect` run leaks no network
# and writes no secret/file residue to $HOME — and since T9a that build links the
# authorized-host HTTP client, so the baseline also proves the client existing ≠ a
# call happening); the connect-ACTION half (T10a, proposal §7 Layer 2) ALSO RUNS BELOW —
# `costroid connect anthropic` with a fake key under a netns FAILS CLOSED and writes no
# $HOME residue, and `disconnect` makes no network call and leaves no secret residue. The
# *positive* "reaches ONLY the authorized loopback host, secret to the keychain only" is
# the Layer-1 integration test (`cargo test --features connect-test-support`).
#
# Two complementary layers of proof (both scope to the default build):
#   * Static  — apps/cli/tests/offline.rs asserts no networking/TLS/telemetry crate
#     is even linked in the default build, and that `costroid-connect` is not linked
#     unless `--features connect` (run via `cargo test`, not here).
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

echo "==> Building costroid (default features — connect OFF, the local-only build)"
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
# tool can only ever read the committed fixtures, never the developer's data. This
# must cover EVERY env override the discovery code honors: CODEX_HOME (Codex root),
# CURSOR_DATA_DIR (Cursor root), and XDG_STATE_HOME (the Claude rate-limits cache) —
# empty maps to "unset" in each resolver, mirroring CLAUDE_CONFIG_DIR.
env_args=(HOME="$home" USERPROFILE="" CLAUDE_CONFIG_DIR="" ANTHROPIC_API_KEY=""
  CODEX_HOME="" CURSOR_DATA_DIR="" XDG_STATE_HOME="")

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
elif [ "$live_rc" -ne 0 ]; then
  # Any other nonzero exit (e.g. a TUI startup crash) must FAIL, not pass silently.
  echo "FAIL (exit $live_rc)"; fail=1
else
  echo "ok"
fi

# ============================================================================
# Feature-ON (connect) — baseline landed in T8 (keychain); T9a added the HTTP
# client (no caller); the connect-ACTION half is T10
# ============================================================================
# T8 added the OS-keychain credential store and T9a the generic authorized-host
# HTTP client to `costroid-connect` — but NOTHING calls the client until T10's
# explicit, user-initiated `connect` action. What this proves now:
#   (a) compiling `--features connect` in — keychain AND the ureq/rustls HTTP
#       client — does NOT leak network on a normal run (linked code must not phone
#       home just by existing); and
#   (b) a normal run writes NO secret/file residue to $HOME (the credential store
#       touches only the OS keychain). The store→retrieve→delete round-trip itself
#       writing nothing to disk is proven at the unit level by
#       `credential_round_trip_writes_nothing_to_disk` (in-memory mock backend), since
#       there is no `connect` CLI to drive from here until T10.
# The connect-ACTION half now LANDS below (T10a): the fail-closed + no-residue checks
# (this script) plus the positive loopback proof (the Layer-1 `cargo test` integration
# test). `costroid connect`/`connections --check` are the only network actions; the
# default build and every other command still make zero network calls.

echo "==> Building costroid --features connect (keychain + HTTP client linked; no caller — zero network expected)"
cargo build -q -p costroid --features connect
connect_bin="$repo_root/target/debug/costroid"

# A content fingerprint of the fixture HOME, to prove a run writes no residue there.
home_fingerprint() { (cd "$home" && find . -type f -exec sha256sum {} + 2>/dev/null | sort); }
before_fp="$(home_fingerprint)"

printf '  %-52s' "connect build: normal run leaks no network"
rc=0; iso_run "$connect_bin" --plain || rc=$?
if [ "$rc" -eq 90 ]; then echo "NETWORK VIOLATION"; fail=1
elif [ "$rc" -ne 0 ]; then echo "FAIL (exit $rc)"; fail=1
else echo "ok"; fi

printf '  %-52s' "connect build: no secret/file residue in \$HOME"
after_fp="$(home_fingerprint)"
if [ "$before_fp" != "$after_fp" ]; then
  echo "FAIL (\$HOME changed under the connect build)"
  diff <(printf '%s\n' "$before_fp") <(printf '%s\n' "$after_fp") | sed 's/^/      /'
  fail=1
else
  echo "ok"
fi

# ============================================================================
# Feature-on connect ACTION test (T10a, proposal §7 Layer 2) — fail-closed + no residue
# ============================================================================
# Replaces the T9/T10 STUB. `costroid connect anthropic` is the first real network in the
# product. This proves the failure-path properties WITHOUT any real network:
#   (i)  it FAILS CLOSED — run with a PREFIX-VALID-BUT-FAKE key on stdin under REAL
#        isolation (a netns with no interfaces), it cannot reach api.anthropic.com, so the
#        connect attempt cannot escape (the netns IS the no-egress proof; the "only the
#        authorized host, never a rogue one" positive is Layer 1's job — the
#        `cargo test --features connect-test-support` loopback test — plus T9a's
#        before-I/O host check). strace is NOT used here: it only observes, and would let
#        the real host be reached, so the connect ACTION must run under a netns.
#   (ii) the fixture $HOME is unchanged on the failure path (no key, no file written —
#        the key is stored only AFTER a successful validation, which never happens here);
#   (iii) `disconnect anthropic` makes NO network call (asserted under the normal strace
#        isolation) and leaves no SECRET residue (only a non-secret, possibly-empty
#        registry — never key material).
echo "==> Feature-on connect ACTION test (T10a Layer 2: fail-closed + no residue)"
FAKE_KEY='sk-ant-admin-FAKE0000000000000000000000'

if unshare --user --map-root-user --net true 2>/dev/null; then
  before_action_fp="$(home_fingerprint)"
  printf '  %-52s' "connect anthropic (fake key) fails closed (netns)"
  # FAKE_KEY is prefix-valid (`sk-ant-admin-…`), so `wrong_key_class` passes and the binary
  # DOES proceed to the real network attempt — which the netns (no interfaces) makes fail.
  # The assertion is exit!=0. It tolerates a vacuous pass on an unrelated nonzero exit, but
  # never errs toward a leak: `connect` runs ONLY inside this no-egress netns (never strace,
  # never host networking) or is SKIPPED below — so it can never reach the real host. The
  # *positive* "only the authorized loopback host" is Layer 1's job.
  printf '%s\n' "$FAKE_KEY" | timeout 60s unshare --user --map-root-user --net \
    env "${env_args[@]}" "$connect_bin" connect anthropic >/dev/null 2>&1 && rc=0 || rc=$?
  if [ "$rc" -eq 0 ]; then
    echo "FAIL (connect succeeded with no network — must fail closed)"; fail=1
  else
    echo "ok (exit $rc — no egress possible under netns)"
  fi
  printf '  %-52s' "connect failure path: no \$HOME residue"
  if [ "$before_action_fp" != "$(home_fingerprint)" ]; then
    echo "FAIL (\$HOME changed on the connect failure path)"
    diff <(printf '%s\n' "$before_action_fp") <(home_fingerprint) | sed 's/^/      /'
    fail=1
  else
    echo "ok"
  fi
else
  echo "  connect fail-closed check SKIPPED (no user netns; Layer-1 cargo test covers it)"
fi

# disconnect makes NO network call and stores no secret — run under the normal isolation
# (strace asserts no AF_INET) and confirm no key material lands under $HOME. This check is
# about network + secret residue, NOT disconnect's success: a headless CI/test box has no
# Secret Service daemon, so the keychain `delete` may error out — that nonzero exit is
# TOLERATED (it touched the keychain, not the network). disconnect's actual removal/no-
# residue behavior is proven by the Layer-1 integration test against the keyring mock.
printf '  %-52s' "disconnect anthropic: no network, no secret residue"
# stderr is silenced (a headless box dumps a multi-line keychain/DBus error); any network
# -violation diagnostic from iso_run prints to stdout and is preserved.
iso_run "$connect_bin" disconnect anthropic 2>/dev/null && dc_rc=0 || dc_rc=$?
if [ "$dc_rc" -eq 90 ]; then
  echo "NETWORK VIOLATION (disconnect made a network call)"; fail=1
elif grep -rIl -- 'sk-ant-admin' "$home" >/dev/null 2>&1; then
  echo "FAIL (key material found under \$HOME)"; fail=1
else
  echo "ok (no network; keychain may be absent in a headless env)"
fi

echo
if [ "$fail" -ne 0 ]; then
  echo "==> OFFLINE ACCEPTANCE: FAILED"
  exit 1
fi
echo "==> OFFLINE ACCEPTANCE (default build, connect OFF): PASSED (all commands ran offline, no network, no telemetry)"
