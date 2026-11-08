#!/bin/sh
# Installs one release of costroid on Linux or macOS, amd64 or arm64.
#
# Read it before you run it. It downloads three files from the release's page
# on github.com into a temporary directory: SHA256SUMS, its signature
# SHA256SUMS.sigstore.json, and the binary for this machine. cosign checks
# that Costroid's release workflow signed SHA256SUMS at this release's tag and
# commit. The binary must then match its line in SHA256SUMS. Only then is it
# copied to DIRECTORY as costroid. It asks for no privilege and changes
# nothing else.
#
# Usage: sh costroid-install.sh [DIRECTORY]
#
# DIRECTORY defaults to $HOME/.local/bin. It needs curl, and cosign 3 or later
# on PATH.
set -eu

# The release workflow writes the release's version and commit into the copy
# it attaches to that release.
version=
commit=

repository=https://github.com/Costroid/costroid
issuer=https://token.actions.githubusercontent.com

fail() {
  echo "costroid: $1" >&2
  exit 1
}

[ -n "$version" ] && [ -n "$commit" ] ||
  fail "this copy names no release: download costroid-install.sh from the release you want to install"
directory=${1:-$HOME/.local/bin}

case $(uname -s) in
  Linux) os=linux ;;
  Darwin) os=darwin ;;
  *) fail "no release for $(uname -s)" ;;
esac
case $(uname -m) in
  x86_64 | amd64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no release for $(uname -m)" ;;
esac
binary=costroid-$os-$arch

command -v curl >/dev/null 2>&1 || fail "curl is not on PATH"
command -v cosign >/dev/null 2>&1 ||
  fail "cosign is not on PATH, and nothing is installed without its check: install cosign 3 or later, then run this again"
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1"; }
else
  sha256() { shasum -a 256 "$1"; }
fi

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT TERM

release=$repository/releases/download/v$version
fetch() {
  curl --fail --silent --show-error --location --proto '=https' --output "$scratch/$1" "$release/$1" ||
    fail "could not download $1 of v$version"
}

fetch SHA256SUMS
fetch SHA256SUMS.sigstore.json
cosign verify-blob --bundle "$scratch/SHA256SUMS.sigstore.json" \
  --certificate-identity "$repository/.github/workflows/release.yml@refs/tags/v$version" \
  --certificate-oidc-issuer "$issuer" \
  --certificate-github-workflow-sha "$commit" "$scratch/SHA256SUMS" >&2 ||
  fail "the signature on SHA256SUMS of v$version does not verify, so nothing was installed"

expected=$(awk -v name="$binary" '$2 == name { print $1 }' "$scratch/SHA256SUMS")
[ -n "$expected" ] || fail "SHA256SUMS of v$version holds no line for $binary"
fetch "$binary"
actual=$(sha256 "$scratch/$binary" | cut -d ' ' -f 1)
[ "$actual" = "$expected" ] ||
  fail "$binary does not match its line in the signed SHA256SUMS of v$version, so nothing was installed"

mkdir -p "$directory"
cp "$scratch/$binary" "$directory/.costroid.new"
chmod 0755 "$directory/.costroid.new"
mv -f "$directory/.costroid.new" "$directory/costroid"
echo "costroid: installed v$version at $directory/costroid, checked by its signature" >&2
case :$PATH: in
  *:"$directory":*) ;;
  *) echo "costroid: $directory is not on your PATH: add it, or run $directory/costroid" >&2 ;;
esac
