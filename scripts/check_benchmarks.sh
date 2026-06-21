#!/usr/bin/env bash
# check_benchmarks.sh — R8/R10 integrity gate for the versioned benchmark datasets.
#
# Verify every committed benchmark artifact under benchmarks/ (each `manifest.v1.json` + every
# raw `*.bench.json` output) is byte-for-byte its committed `.sha256` sidecar — i.e. nobody
# hand-edited a published figure after generation. Offline: a pure `sha256sum -c`. A sibling of
# scripts/check_power_profiles.sh, rooted at benchmarks/ instead of crates/costroid-power.
#
# Fail-closed by construction:
#   * every JSON artifact MUST have a `.sha256` sidecar (a missing sidecar fails loudly);
#   * every `.sha256` sidecar MUST have its JSON artifact (an orphan sidecar fails loudly);
#   * each is checked from its OWN directory (the sidecar records the bare filename);
#   * if benchmarks/ exists but no artifact is found, the gate FAILS (it can't pass vacuously).
#
# The Rust drift-guard test (apps/cli/tests/post_m3b_refresh.rs) pins the set of these artifacts
# to docs/POST-M3B-REFRESH.md from the other side; this script is the byte-level guard.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
bench_root="$repo_root/benchmarks"

echo "==> Verifying versioned benchmark dataset integrity (sha256sum -c)"

if [ ! -d "$bench_root" ]; then
  echo "FAIL: benchmarks/ directory is missing" >&2
  exit 1
fi

# Enumerate every committed JSON artifact (manifests + raw outputs), excluding the sidecars.
# `-print0` + a NUL-delimited read so paths with spaces never split.
mapfile -d '' artifacts < <(
  find "$bench_root" -type f -name '*.json' ! -name '*.sha256' -print0 | sort -z
)

if [ "${#artifacts[@]}" -eq 0 ]; then
  echo "FAIL: no benchmark artifacts found under benchmarks/ (gate must not pass vacuously)" >&2
  exit 1
fi

checked=0
for artifact in "${artifacts[@]}"; do
  sidecar="$artifact.sha256"
  if [ ! -f "$sidecar" ]; then
    echo "FAIL: benchmark artifact ${artifact#"$repo_root/"} has no .sha256 sidecar (re-generate it)" >&2
    exit 1
  fi
  # The sidecar records the bare filename (generated with `cd <dir> && sha256sum <file>`), so
  # run the check from the artifact's own directory.
  ( cd "$(dirname "$artifact")" && sha256sum -c "$(basename "$sidecar")" )
  checked=$((checked + 1))
done

# Catch an orphan sidecar (a `.sha256` whose JSON artifact was removed but the sidecar left behind).
mapfile -d '' sidecars < <(
  find "$bench_root" -type f -name '*.json.sha256' -print0 | sort -z
)
for sidecar in "${sidecars[@]}"; do
  artifact="${sidecar%.sha256}"
  if [ ! -f "$artifact" ]; then
    echo "FAIL: orphan sidecar ${sidecar#"$repo_root/"} has no JSON artifact" >&2
    exit 1
  fi
done

echo "==> All $checked benchmark artifact(s) intact."
