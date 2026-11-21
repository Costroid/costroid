#!/bin/sh
# Installs one release of costroid on Linux or macOS, amd64 or arm64.
#
# Read it before you run it. It downloads SHA256SUMS and its signature,
# SHA256SUMS.sigstore.json, into a temporary directory from the release's page,
# https://github.com/Costroid/costroid/releases/download/v<version>/. cosign
# checks that Costroid's release workflow signed SHA256SUMS at this release's
# tag and commit. Only then does it download costroid-<os>-<arch>, the binary
# for this machine, from the same page. The binary must match its line in
# SHA256SUMS, and only then is it copied to DIRECTORY as costroid.
#
# The check runs the cosign on PATH when it is 3 or later. Otherwise the script
# first downloads cosign-<os>-<arch> into the same temporary directory from
# https://github.com/sigstore/cosign/releases/download/v3.0.6/, and runs it
# only when its SHA-256 is the one this script holds for that machine. Nothing
# is installed on SHA256SUMS alone.
#
# It asks for no privilege and changes nothing outside DIRECTORY. The temporary
# directory is removed when it ends. Its last line is the command to run next.
#
# Usage: sh costroid-install.sh [DIRECTORY]
#
# DIRECTORY defaults to $HOME/.local/bin. It needs curl.
set -eu

# The release workflow writes the release's version and commit into the copy
# it attaches to that release.
version=
commit=

# The cosign the GitLab template pins.
cosign_version=v3.0.6

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
# The SHA-256 of cosign-<os>-<arch> at cosign_version, as the cosign release's
# cosign_checksums.txt lists it.
case $os-$arch in
  linux-amd64) cosign_sha256=c956e5dfcac53d52bcf058360d579472f0c1d2d9b69f55209e256fe7783f4c74 ;;
  linux-arm64) cosign_sha256=bedac92e8c3729864e13d4a17048007cfafa79d5deca993a43a90ffe018ef2b8 ;;
  darwin-amd64) cosign_sha256=4c3e7af8372d3ca3296e62fa56f23fcbb5721cc6ac1827900d398f110d7cd280 ;;
  darwin-arm64) cosign_sha256=5fadd012ae6381a6a29ff86a7d39aa873878852f1073fc90b15995961ecfb084 ;;
esac

command -v curl >/dev/null 2>&1 || fail "curl is not on PATH"
if command -v sha256sum >/dev/null 2>&1; then
  sha256() { sha256sum "$1" | cut -d ' ' -f 1; }
elif command -v shasum >/dev/null 2>&1; then
  sha256() { shasum -a 256 "$1" | cut -d ' ' -f 1; }
else
  fail "neither sha256sum nor shasum is on PATH"
fi

scratch=$(mktemp -d)
trap 'rm -rf -- "$scratch"' EXIT
trap 'exit 130' INT TERM

# A terminal sees curl's progress bar on the two downloads that take seconds,
# cosign and the binary.
if [ -t 2 ]; then long=--progress-bar; else long=--silent; fi
download() {
  curl --fail "${3:---silent}" --show-error --location --proto '=https' --output "$scratch/$1" "$2/$1"
}

# cosign starts empty, so nothing in the environment can name it. It is set
# only to a cosign on PATH that states 3 or later, or to the one this script
# downloads once it matches its pin, and every install runs it.
cosign=
on_path=$(command -v cosign 2>/dev/null) || on_path=
if [ -n "$on_path" ]; then
  stated=$("$on_path" version 2>&1 | awk '$1 == "GitVersion:" { print $2; exit }')
  major=${stated#v}
  major=${major%%.*}
  case $major in
    '' | *[!0-9]*) ;;
    *) if [ "$major" -ge 3 ]; then cosign=$on_path; fi ;;
  esac
  [ -n "$cosign" ] ||
    echo "costroid: cosign ${stated:-of an unknown version} is on PATH and this check needs 3 or later, so this script downloads cosign $cosign_version to its temporary directory, checks it against the SHA-256 it holds and removes it afterwards" >&2
else
  echo "costroid: cosign 3 or later is not on PATH, so this script downloads cosign $cosign_version to its temporary directory, checks it against the SHA-256 it holds and removes it afterwards" >&2
fi
if [ -z "$cosign" ]; then
  download "cosign-$os-$arch" "https://github.com/sigstore/cosign/releases/download/$cosign_version" "$long" ||
    fail "could not download cosign $cosign_version, so nothing was installed: install cosign 3 or later and run this again, or install costroid by hand as $repository/blob/main/docs/install.md#the-binary shows"
  [ "$(sha256 "$scratch/cosign-$os-$arch")" = "$cosign_sha256" ] ||
    fail "cosign $cosign_version does not match its pinned SHA-256, so nothing was installed"
  chmod 0755 "$scratch/cosign-$os-$arch"
  cosign=$scratch/cosign-$os-$arch
fi

release=$repository/releases/download/v$version
fetch() {
  download "$1" "$release" "${2:-}" || fail "could not download $1 of v$version"
}

echo "costroid: checking the signature on SHA256SUMS of v$version" >&2
fetch SHA256SUMS
fetch SHA256SUMS.sigstore.json
"$cosign" verify-blob --bundle "$scratch/SHA256SUMS.sigstore.json" \
  --certificate-identity "$repository/.github/workflows/release.yml@refs/tags/v$version" \
  --certificate-oidc-issuer "$issuer" \
  --certificate-github-workflow-sha "$commit" "$scratch/SHA256SUMS" >&2 ||
  fail "the signature on SHA256SUMS of v$version does not verify, so nothing was installed"

expected=$(awk -v name="$binary" '$2 == name { print $1 }' "$scratch/SHA256SUMS")
[ -n "$expected" ] || fail "SHA256SUMS of v$version holds no line for $binary"
echo "costroid: downloading $binary" >&2
fetch "$binary" "$long"
[ "$(sha256 "$scratch/$binary")" = "$expected" ] ||
  fail "$binary does not match its line in the signed SHA256SUMS of v$version, so nothing was installed"

mkdir -p "$directory"
cp "$scratch/$binary" "$directory/.costroid.new"
chmod 0755 "$directory/.costroid.new"
mv -f "$directory/.costroid.new" "$directory/costroid"
echo "costroid: installed v$version at $directory/costroid, checked by its signature" >&2

next=$directory/costroid
case :$PATH: in
  *:"$directory":*)
    if [ "$(command -v costroid)" = "$directory/costroid" ]; then next=costroid; fi
    ;;
  *)
    echo "costroid: $directory is not on your PATH. To add it in this shell, run:" >&2
    printf '  export PATH="%s:%s"\n' "$directory" "\$PATH" >&2
    ;;
esac
echo "Next: $next demo prints the comment a pull request gets." >&2
