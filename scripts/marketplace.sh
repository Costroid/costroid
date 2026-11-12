#!/usr/bin/env bash
# Writes the Costroid/costroid-action repository's files for one release into a
# directory: marketplace/ with the release's tag and commit filled in, and
# SECURITY.md. It never commits or pushes.
#
# Given notes first, it prints the listing's release notes for a tag that the
# listing's checkout already holds. A commit cannot name itself, so the
# listing's README names its tag, and the notes, written once the commit
# exists, name the commit.
#
#   scripts/marketplace.sh <tag> <out-dir>
#   scripts/marketplace.sh notes <tag> <listing-checkout>
set -euo pipefail

listing=Costroid/costroid-action
semver='^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?(\+[0-9A-Za-z-]+(\.[0-9A-Za-z-]+)*)?$'
origin="^(https://github\.com/|git@github\.com:|ssh://git@github\.com/)$listing(\.git)?$"

fail() {
  echo "marketplace: $1" >&2
  exit 1
}

mode="write"
if [[ ${1:-} == notes ]]; then
  mode=notes
  shift
fi
[[ $# -eq 2 ]] || fail "usage: scripts/marketplace.sh <tag> <out-dir>, or scripts/marketplace.sh notes <tag> <listing-checkout>"
tag=$1
out=$2
[[ $tag =~ $semver ]] || fail "$tag is not a release tag of the form v<semver>"

root=$(git -C "$(dirname "$0")" rev-parse --show-toplevel) || fail "scripts/marketplace.sh is not inside a checkout"
commit=$(git -C "$root" rev-parse --verify --quiet "refs/tags/$tag^{commit}") || fail "$tag is not a tag in $root"

# require_listing holds a directory to a checkout whose origin is the listing.
require_listing() {
  [[ $(git -C "$1" rev-parse --show-toplevel 2>/dev/null) == "$(cd "$1" && pwd -P)" ]] ||
    fail "$1 is not empty and not a git repository"
  local remote
  remote=$(git -C "$1" remote get-url origin 2>/dev/null) || fail "$1 has no origin"
  [[ $remote =~ $origin ]] || fail "$1 has origin $remote, not $listing"
}

write_security() {
  cat >"$out/SECURITY.md" <<SECURITY
# Security policy

This repository holds the GitHub Marketplace listing of Costroid's action. Its one step calls \`Costroid/costroid/action\` at the commit of Costroid's \`$tag\`, so a vulnerability here is a vulnerability in Costroid.

Report it through GitHub private vulnerability reporting on Costroid/costroid: open the Security tab at https://github.com/Costroid/costroid/security and choose "Report a vulnerability". Do not open a public issue.

[Costroid's security policy](https://github.com/Costroid/costroid/blob/$tag/SECURITY.md) states the response targets and the scope.
SECURITY
}

# print_notes prints the release notes of the listing's tag: the listing's own
# commit, which a workflow pins, and Costroid's commit, which the action calls.
# The example is the README's last YAML block, the report job, calling this
# listing at its own commit.
print_notes() {
  [[ $(git -C "$out" cat-file -t "refs/tags/$tag" 2>/dev/null) == tag ]] ||
    fail "$out holds no annotated tag $tag: tag the listing's commit with git tag --annotate"
  local own example
  own=$(git -C "$out" rev-parse --verify --quiet "refs/tags/$tag^{commit}") || fail "$tag names no commit in $out"
  git -C "$out" show "$own:action.yml" | grep -qF "uses: Costroid/costroid/action@$commit # $tag" ||
    fail "action.yml at $tag in $out does not call Costroid/costroid/action at $commit, the commit of Costroid's $tag"
  example=$(git -C "$out" show "$own:README.md" |
    awk '/^```yaml$/ { inside = 1; block = ""; next } inside && /^```$/ { inside = 0; last = block; next } inside { block = block $0 "\n" } END { printf "%s", last }')
  [[ $example == *"Costroid/costroid/action@$commit # $tag"* ]] ||
    fail "README.md at $tag in $out ends with no YAML block that calls Costroid's $tag"
  cat <<NOTES
Pin this action to \`$own\`, the commit of this tag. It calls \`Costroid/costroid/action\` at commit \`$commit\`, Costroid's [\`$tag\`](https://github.com/Costroid/costroid/releases/tag/$tag), whose release notes list what changed.

The README's report job, calling this action at that commit:

\`\`\`yaml
${example//Costroid\/costroid\/action@$commit/$listing@$own}
\`\`\`
NOTES
}

if [[ $mode == notes ]]; then
  [[ -d $out ]] || fail "$out is not a directory"
  require_listing "$out"
  print_notes
  exit 0
fi

if [[ -d $out && -n $(ls -A "$out") ]]; then
  require_listing "$out"
fi
mkdir -p "$out"

for file in "$root"/marketplace/*; do
  sed -e "s/__RELEASE_COMMIT__/$commit/g" -e "s/__RELEASE_TAG__/$tag/g" "$file" >"$out/$(basename "$file")"
done
write_security
if grep --recursive --files-with-matches '__RELEASE_[A-Z]*__' "$out" --exclude-dir=.git >&2; then
  fail "a placeholder is left in the files above"
fi
echo "marketplace: wrote $listing for $tag at $commit into $out" >&2
