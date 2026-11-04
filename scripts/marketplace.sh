#!/usr/bin/env bash
# Writes the Costroid/costroid-action repository's files for one release into a
# directory: marketplace/ with the release's tag and commit filled in. It never
# commits or pushes.
set -euo pipefail

listing=Costroid/costroid-action
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
origin="^(https://github\.com/|git@github\.com:|ssh://git@github\.com/)$listing(\.git)?$"

fail() {
  echo "marketplace: $1" >&2
  exit 1
}

[[ $# -eq 2 ]] || fail "usage: scripts/marketplace.sh <tag> <out-dir>"
tag=$1
out=$2
[[ $tag =~ $semver ]] || fail "$tag is not a release tag of the form v<semver>"

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel) || fail "scripts/marketplace.sh is not inside a checkout"
commit=$(git -C "$root" rev-parse --verify --quiet "refs/tags/$tag^{commit}") || fail "$tag is not a tag in $root"

if [[ -d $out && -n $(ls -A "$out") ]]; then
  [[ $(git -C "$out" rev-parse --show-toplevel 2>/dev/null) == "$(cd "$out" && pwd -P)" ]] ||
    fail "$out is not empty and not a git repository"
  remote=$(git -C "$out" remote get-url origin 2>/dev/null) || fail "$out has no origin"
  [[ $remote =~ $origin ]] || fail "$out has origin $remote, not $listing"
fi
mkdir -p "$out"

for file in "$root"/marketplace/*; do
  sed -e "s/__RELEASE_COMMIT__/$commit/g" -e "s/__RELEASE_TAG__/$tag/g" "$file" >"$out/$(basename "$file")"
done
if grep --recursive --files-with-matches '__RELEASE_[A-Z]*__' "$out" --exclude-dir=.git >&2; then
  fail "a placeholder is left in the files above"
fi
echo "marketplace: wrote $listing for $tag at $commit into $out" >&2
