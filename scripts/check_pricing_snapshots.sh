#!/usr/bin/env bash
# Verify the vendored pricing snapshots are byte-for-byte the deterministic output of
# their pinned transform — i.e. nobody hand-edited a price after generation (R8 integrity).
# Offline: a pure `sha256sum -c` of the committed `.sha256` sidecars. Wired into CI.
#
# This does NOT re-fetch upstream (CI is offline); the upstream pin is asserted by the
# refresh script itself (scripts/refresh_litellm_pricing.py) at vendor time and by the
# Rust loader test's embedded source/as_of/content_hash constants.
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
pricing_dir="$repo_root/crates/costroid-core/pricing"

echo "==> Verifying vendored pricing-snapshot integrity (sha256sum -c)"
# Fail-closed: every snapshot Costroid ships MUST have a sidecar and pass. A glob alone
# would silently "pass" if a snapshot's sidecar were missing — so the required set is
# enumerated here and each is asserted present before checking.
REQUIRED_SNAPSHOTS=(pricing.v1.json litellm-prices.v1.json)
cd "$pricing_dir"
for snap in "${REQUIRED_SNAPSHOTS[@]}"; do
  if [ ! -f "$snap" ]; then
    echo "FAIL: required snapshot $snap is missing" >&2
    exit 1
  fi
  if [ ! -f "$snap.sha256" ]; then
    echo "FAIL: snapshot $snap has no .sha256 sidecar (re-generate it)" >&2
    exit 1
  fi
  sha256sum -c "$snap.sha256"
done
echo "==> All pricing snapshots intact."
