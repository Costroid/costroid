#!/usr/bin/env bash
# Verify the vendored local-inference data artifacts (the dated hardware/electricity power
# profile + the Gemma 4 model manifest) are byte-for-byte their committed `.sha256` sidecars —
# i.e. nobody hand-edited an assumption after generation (R8 integrity). Offline: a pure
# `sha256sum -c`. Wired into CI alongside scripts/check_pricing_snapshots.sh.
#
# The Rust loader tests pin each artifact's `as_of` (POWER_PROFILE_AS_OF / GEMMA4_MANIFEST_AS_OF);
# this script is the byte-level guard that catches an edit that did NOT bump `as_of` (the
# "swapped file passes silently" case the as_of pin alone cannot catch).
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
power_dir="$repo_root/crates/costroid-power"

echo "==> Verifying vendored power-profile + model-manifest integrity (sha256sum -c)"
# Fail-closed: every artifact Costroid ships MUST have a sidecar and pass. Enumerated (not a
# glob) so a missing sidecar fails loudly rather than vacuously passing. Paths are relative to
# costroid-power so the sidecar's recorded filename matches.
REQUIRED=(
  "profiles/hardware.v1.json"
  "models/gemma4.v1.json"
)
cd "$power_dir"
for artifact in "${REQUIRED[@]}"; do
  if [ ! -f "$artifact" ]; then
    echo "FAIL: required artifact $artifact is missing" >&2
    exit 1
  fi
  if [ ! -f "$artifact.sha256" ]; then
    echo "FAIL: artifact $artifact has no .sha256 sidecar (re-generate it)" >&2
    exit 1
  fi
  # The sidecar records the bare filename (generated with `cd <dir> && sha256sum <file>`), so
  # run the check from the artifact's own directory.
  ( cd "$(dirname "$artifact")" && sha256sum -c "$(basename "$artifact").sha256" )
done
echo "==> All power-profile + model-manifest artifacts intact."
