#!/bin/sh
# SPDX-License-Identifier: Apache-2.0
# Copyright 2026 The Costroid Authors
#
# Costroid installer.
#
# Detects your OS/architecture, downloads the matching release archive from
# GitHub Releases, VERIFIES it against the published SHA-256 checksums (and the
# keyless cosign signature when cosign is installed) BEFORE extracting, and
# installs the `costroid` binary to a user directory. It never uses sudo and
# never writes to a root-owned path. It talks only to github.com and
# api.github.com to fetch the release; it phones nothing home.
#
# Environment knobs:
#   COSTROID_VERSION      release to install, e.g. v0.1.0 or 0.1.0
#                         (default: the latest published release).
#   COSTROID_INSTALL_DIR  install directory (default: $HOME/.local/bin).
#
# Usage:
#   curl -fsSL https://raw.githubusercontent.com/Costroid/costroid/main/scripts/install.sh | sh
#   COSTROID_VERSION=v0.1.0 sh install.sh

set -eu

REPO="Costroid/costroid"
INSTALL_DIR="${COSTROID_INSTALL_DIR:-$HOME/.local/bin}"

usage() {
	cat <<'EOF'
Install the costroid binary from GitHub Releases.

The script detects your OS and architecture, downloads the matching release
archive, verifies its SHA-256 checksum (and the cosign signature when cosign is
installed) before extracting, and installs `costroid` to a user directory.

Environment variables:
  COSTROID_VERSION      version to install, e.g. v0.1.0 (default: latest release)
  COSTROID_INSTALL_DIR  install directory (default: $HOME/.local/bin)

Usage:
  curl -fsSL https://raw.githubusercontent.com/Costroid/costroid/main/scripts/install.sh | sh
EOF
}

case "${1:-}" in
	-h | --help)
		usage
		exit 0
		;;
esac

# curl with a locked-down transport: HTTPS only, no scheme downgrade across the
# 302 redirect to the release CDN, TLS 1.2 or better. Used for every fetch.
fetch() {
	# fetch URL OUTFILE
	curl -fsSL --proto '=https' --proto-redir '=https' --tlsv1.2 -o "$2" "$1"
}

# --- Platform detection ------------------------------------------------------

os="$(uname -s)"
case "$os" in
	Linux) os="linux" ;;
	Darwin) os="darwin" ;;
	MINGW* | MSYS* | CYGWIN*)
		echo "error: this installer supports only Linux and macOS." >&2
		echo "Windows: download the archive from https://github.com/$REPO/releases and verify it manually." >&2
		exit 1
		;;
	*)
		echo "error: unsupported operating system: $os (only Linux and macOS are supported)." >&2
		echo "See https://github.com/$REPO/releases for all release archives." >&2
		exit 1
		;;
esac

arch="$(uname -m)"
case "$arch" in
	x86_64 | amd64) arch="amd64" ;;
	aarch64 | arm64) arch="arm64" ;;
	*)
		echo "error: unsupported architecture: $arch (supported: x86_64/amd64, aarch64/arm64)." >&2
		exit 1
		;;
esac

# --- Version resolution ------------------------------------------------------

if [ -n "${COSTROID_VERSION:-}" ]; then
	ver="$COSTROID_VERSION"
else
	echo "Resolving the latest release ..."
	# No -f here: on a rate-limit / not-found response the API still returns a
	# JSON body from which the parse yields an empty version, and the guard
	# below turns that into a clear message instead of an opaque curl failure.
	resp="$(curl -sSL --proto '=https' --proto-redir '=https' --tlsv1.2 "https://api.github.com/repos/$REPO/releases/latest" || true)"
	# POSIX grep/BRE sed, no jq: pull the tag_name value.
	ver="$(printf '%s\n' "$resp" | grep -m1 '"tag_name"' | sed 's/.*"tag_name"[^"]*"\([^"]*\)".*/\1/')"
	[ -n "$ver" ] || {
		echo "error: could not resolve the latest release (the GitHub API may be rate-limited); set COSTROID_VERSION to a specific version" >&2
		exit 1
	}
fi

# Reject a stray slash (a user-set COSTROID_VERSION like v1/../x could otherwise
# rewrite the download URL path), then require a bare or v-prefixed number so a
# rate-limit body or typo cannot slip through as a version.
case "$ver" in
	*/*)
		echo "error: unexpected version $ver" >&2
		exit 1
		;;
esac
case "$ver" in
	v[0-9]* | [0-9]*) ;;
	*)
		echo "error: unexpected version $ver" >&2
		exit 1
		;;
esac

ver="${ver#v}"
tag="v$ver"

# Resolve the install dir to an absolute path before we cd into the scratch
# directory, so a relative COSTROID_INSTALL_DIR still lands in the caller's tree
# rather than the scratch dir that cleanup removes.
case "$INSTALL_DIR" in
	/*) ;;
	*) INSTALL_DIR="$PWD/$INSTALL_DIR" ;;
esac

# --- Download (into a self-cleaning scratch dir) -----------------------------

# Template form: bare `mktemp -d` is non-portable on old BSD.
tmp="$(mktemp -d "${TMPDIR:-/tmp}/costroid.XXXXXXXX")"
trap 'rm -rf "$tmp"' EXIT INT TERM
cd "$tmp"

asset="costroid_${ver}_${os}-${arch}.tar.gz"
base="https://github.com/$REPO/releases/download/$tag"

echo "Downloading $asset ($tag) ..."
if ! fetch "$base/$asset" "$asset"; then
	echo "error: no release asset for $os-$arch at $tag ($base/$asset)" >&2
	exit 1
fi
if ! fetch "$base/checksums.txt" "checksums.txt"; then
	echo "error: could not download checksums.txt for $tag" >&2
	exit 1
fi

# The signature bundle is best-effort: a release without one must still install
# via the mandatory checksum, so the fetch is guarded (an if condition is exempt
# from set -e) rather than a bare statement that would abort the whole script.
have_bundle=0
if fetch "$base/checksums.txt.sigstore.json" "checksums.txt.sigstore.json"; then
	have_bundle=1
fi

# --- Signature verification (optional) ---------------------------------------

if [ "$have_bundle" -eq 1 ] && command -v cosign >/dev/null 2>&1; then
	echo "Verifying the checksums signature with cosign ..."
	# The identity regexp is anchored (^...$) with dots escaped because cosign
	# matches unanchored: a crafted identity that merely contains the substring
	# must not match. A verification failure aborts here (set -e), installing
	# nothing.
	cosign verify-blob \
		--bundle checksums.txt.sigstore.json \
		--certificate-identity-regexp '^https://github\.com/Costroid/costroid/\.github/workflows/release\.yml@refs/tags/v.*$' \
		--certificate-oidc-issuer https://token.actions.githubusercontent.com \
		checksums.txt
else
	echo "note: cosign not available (or no signature bundle for this release); skipping signature verification. Install cosign to enable it."
fi

# --- Checksum verification (mandatory) ---------------------------------------

# checksums.txt is sha256sum output: <64 hex><two spaces><bare filename>. Match
# our asset's line as a fixed string (so the filename dots are literal) and fail
# closed with a clear message if it is absent, rather than feeding the sha256
# tool empty input and surfacing its cryptic error.
line="$(grep -F -- "  ${asset}" checksums.txt || true)"
[ -n "$line" ] || {
	echo "error: no checksum for $asset in checksums.txt" >&2
	exit 1
}

if command -v sha256sum >/dev/null 2>&1; then
	printf '%s\n' "$line" | sha256sum -c -
elif command -v shasum >/dev/null 2>&1; then
	printf '%s\n' "$line" | shasum -a 256 -c -
else
	echo "error: no SHA-256 tool found (need sha256sum or shasum)" >&2
	exit 1
fi

# --- Install -----------------------------------------------------------------

tar -xzf "$asset"
[ -f costroid ] || {
	echo "error: the release archive did not contain a costroid binary" >&2
	exit 1
}
mkdir -p "$INSTALL_DIR"
mv costroid "$INSTALL_DIR/costroid"
chmod +x "$INSTALL_DIR/costroid"
[ -x "$INSTALL_DIR/costroid" ] || {
	printf 'error: the installed file is not executable: %s/costroid\n' "$INSTALL_DIR" >&2
	exit 1
}

# printf, not echo, for any message carrying $INSTALL_DIR: a dash/busybox builtin
# echo would interpret backslash escapes in a user-set install path, garbling it.
printf 'Installed costroid %s to %s/costroid\n' "$ver" "$INSTALL_DIR"

# --- PATH check --------------------------------------------------------------

case ":$PATH:" in
	*":$INSTALL_DIR:"*) ;;
	*)
		printf 'warning: %s is not on your PATH.\n' "$INSTALL_DIR"
		# The $PATH below is a literal for the user to copy, not for us to expand.
		# shellcheck disable=SC2016
		printf '  add it, e.g.:  export PATH="%s:$PATH"\n' "$INSTALL_DIR"
		;;
esac

echo "Run 'costroid demo' to start an isolated dashboard with synthetic data."
