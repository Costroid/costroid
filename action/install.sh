#!/usr/bin/env bash
# Installs one released costroid binary and refuses it unless its SHA-256 matches.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 1
}

version=${COSTROID_VERSION:-}
sum=${COSTROID_SHA256:-}
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || fail "version must be a release number, got '$version'"
[[ $sum =~ ^[0-9a-f]{64}$ ]] || fail "sha256 must be 64 lowercase hex characters"

case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no release for $(uname -m)" ;;
esac

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT
curl --fail --silent --show-error --location --proto '=https' --output "$scratch/costroid" \
  "https://github.com/Costroid/costroid/releases/download/v$version/costroid-linux-$arch"

echo "$sum  $scratch/costroid" | sha256sum --check --status ||
  fail "the binary for $version does not match the pinned SHA-256"

bin=$RUNNER_TEMP/costroid-bin
mkdir -p "$bin"
install -m 0755 "$scratch/costroid" "$bin/costroid"
echo "$bin" >>"$GITHUB_PATH"
