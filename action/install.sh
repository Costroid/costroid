#!/usr/bin/env bash
# Installs one released costroid binary and refuses it unless its SHA-256 matches.
# Given no version and no sha256, it installs the release whose tag points at
# this action's commit, and takes the SHA-256 from that release's SHA256SUMS
# once cosign shows the release workflow signed it at that tag and commit.
# The commit comes from the action's checkout path, because an action called
# from another composite action gets an empty github.action_ref.
set -euo pipefail

repository=Costroid/costroid
issuer=https://token.actions.githubusercontent.com
release_tag='^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'
pin_hint="pin Costroid/costroid/action to the full commit SHA of a release tag, as the recipes do, or set both version and sha256"

fail() {
  echo "costroid: $1" >&2
  exit 1
}

case $(uname -m) in
  x86_64) arch=amd64 ;;
  aarch64 | arm64) arch=arm64 ;;
  *) fail "no release for $(uname -m)" ;;
esac
asset=costroid-linux-$arch

scratch=$(mktemp -d)
trap 'rm -rf "$scratch"' EXIT

download() {
  curl --fail --silent --show-error --location --proto '=https' --output "$scratch/$1" \
    "https://github.com/Costroid/costroid/releases/download/v$version/$1"
}

# action_commit prints this action's commit. The runner checks a remote action
# out at _actions/<owner>/<repo>/<ref>/<path>, and that path holds the commit
# even where github.action_ref is empty or names the calling action, so it wins
# over the ref. Any other path, such as one named by a tag, leaves the ref.
action_commit() {
  local path=${GITHUB_ACTION_PATH:-}
  if [[ $path =~ /_actions/$repository/([0-9a-f]{40})/action$ ]]; then
    echo "${BASH_REMATCH[1]}"
  else
    echo "${COSTROID_ACTION_REF:-}"
  fi
}

# tags_at prints the tags that point at a commit. An annotated tag's own object
# is not a commit, so its peeled line, which ends in ^{}, names the commit.
tags_at() {
  awk -v commit="$1" '
    { name = $2; sub(/^refs\/tags\//, "", name) }
    name ~ /\^\{\}$/ { sub(/\^\{\}$/, "", name); peeled[name] = $1; next }
    { plain[name] = $1 }
    END {
      for (name in plain) {
        target = plain[name]
        if (name in peeled) target = peeled[name]
        if (target == commit) print name
      }
    }'
}

# find_release sets version to the release whose tag points at the commit. Two
# release tags on one commit are one build: a release wins over a pre-release,
# and the higher of two wins.
find_release() {
  local commit=$1 tags candidate tag=""
  [[ $commit =~ ^[0-9a-f]{40}$ ]] || fail "the action's ref '$commit' is not a commit SHA: $pin_hint"
  tags=$(GIT_TERMINAL_PROMPT=0 git ls-remote --tags "https://github.com/$repository") ||
    fail "could not list the tags of $repository"
  while IFS= read -r candidate; do
    if [[ -z $tag || $candidate != *-* || $tag == *-* ]]; then
      tag=$candidate
    fi
  done < <(tags_at "$commit" <<<"$tags" | { grep --extended-regexp "$release_tag" || true; } | sort --version-sort)
  [[ -n $tag ]] || fail "$commit is not the commit of a release of $repository: $pin_hint"
  version=${tag#v}
}

# read_signed_sum sets sum to the binary's line of SHA256SUMS, once cosign
# shows that the release workflow signed SHA256SUMS at the tag, from the commit.
read_signed_sum() {
  local commit=$1 tag=v$version
  download SHA256SUMS || fail "$tag has no SHA256SUMS"
  download SHA256SUMS.sigstore.json || fail "$tag has no signature for SHA256SUMS"
  cosign verify-blob --bundle "$scratch/SHA256SUMS.sigstore.json" \
    --certificate-identity "https://github.com/$repository/.github/workflows/release.yml@refs/tags/$tag" \
    --certificate-oidc-issuer "$issuer" \
    --certificate-github-workflow-sha "$commit" \
    "$scratch/SHA256SUMS" >&2 ||
    fail "the signature on SHA256SUMS of $tag does not verify, so nothing was installed"
  sum=$(awk -v name="$asset" '$2 == name { print $1 }' "$scratch/SHA256SUMS")
  [[ $sum =~ ^[0-9a-f]{64}$ ]] || fail "SHA256SUMS of $tag holds no line for $asset"
}

version=${COSTROID_VERSION:-}
sum=${COSTROID_SHA256:-}
if [[ -z $version && -z $sum ]]; then
  commit=$(action_commit)
  find_release "$commit"
  read_signed_sum "$commit"
  echo "costroid: installing $version, the release at this action's commit, checked by its signature" >&2
elif [[ -z $version || -z $sum ]]; then
  fail "set both version and sha256, or neither to install the release this action is pinned to"
fi
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] || fail "version must be a release number, got '$version'"
[[ $sum =~ ^[0-9a-f]{64}$ ]] || fail "sha256 must be 64 lowercase hex characters"

download "$asset"
echo "$sum  $scratch/$asset" | sha256sum --check --status ||
  fail "the binary for $version does not match the pinned SHA-256"

bin=$RUNNER_TEMP/costroid-bin
mkdir -p "$bin"
install -m 0755 "$scratch/$asset" "$bin/costroid"
echo "$bin" >>"$GITHUB_PATH"
