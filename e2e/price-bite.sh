#!/usr/bin/env bash
# Moves one price in a copy of the tree and proves make e2e goes red at every
# step that states the figure. The price of cache_read on claude-haiku-5-5 goes
# from 0.01 to 0.02 per million tokens. The recorded run read 15605 tokens from
# cache, so its total moves by 0.00015605, to 0.001775775.
set -euo pipefail

moved=0.001775775
copy=$(mktemp -d)
trap 'rm -rf "$copy"' EXIT
tar --exclude=./bin --create --file - . | tar --extract --file - --directory "$copy"

sheet=$copy/catalog/0.1.0/anthropic-2026-11-01.json
sed -i '/"anthropic:claude-haiku-5-5:global:standard:to-100000:cache_read"/s/"ListUnitPrice":"0.01"/"ListUnitPrice":"0.02"/' "$sheet"
if cmp --silent "$sheet" catalog/0.1.0/anthropic-2026-11-01.json; then
  echo "price-bite: the sheet did not change" >&2
  exit 1
fi

log=$copy/e2e.log
if (cd "$copy" && make e2e) >"$log" 2>&1; then
  cat "$log"
  echo "price-bite: make e2e passed with a moved price" >&2
  exit 1
fi

for step in 'costroid run: want' 'per-PR view: want' 'comment: want'; do
  if ! grep --quiet "$step" "$log"; then
    cat "$log"
    echo "price-bite: no failure at '$step'" >&2
    exit 1
  fi
done
grep --quiet "$moved" "$log" || {
  cat "$log"
  echo "price-bite: the log does not show $moved" >&2
  exit 1
}
grep --extended-regexp "costroid run: want|per-PR view: want|comment: want|$moved at list" "$log"
echo "price-bite: make e2e failed at all three steps with the moved price"
