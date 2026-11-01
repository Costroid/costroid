#!/usr/bin/env bash
# Runs one agent under costroid run, with the provider key on this process only.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 125
}

case ${COSTROID_AGENT:-} in
  claude)
    agent=(claude -p)
    key_name=ANTHROPIC_API_KEY
    ;;
  codex)
    agent=(codex exec)
    key_name=CODEX_API_KEY
    ;;
  *) fail "agent must be claude or codex, got '${COSTROID_AGENT:-}'" ;;
esac

[[ -n ${COSTROID_PROMPT:-} ]] || fail "prompt is empty"
[[ -n ${COSTROID_BUDGET_USD:-} ]] || fail "budget-usd is empty"
[[ -n ${COSTROID_AGENT_KEY:-} ]] || fail "api-key is empty"
floor=${COSTROID_MINIMUM_AGENT_VERSION:-}
[[ -n $floor ]] || fail "minimum-agent-version is empty"

found=$("${agent[0]}" --version | grep --only-matching --extended-regexp '[0-9]+\.[0-9]+\.[0-9]+[^ ]*' | head -n 1) ||
  fail "could not read the version of ${agent[0]}"
lowest=$(printf '%s\n%s\n' "$floor" "$found" | sort --version-sort | head -n 1)
[[ $lowest == "$floor" ]] || fail "${agent[0]} $found is older than the minimum $floor"

export "$key_name=$COSTROID_AGENT_KEY"
unset COSTROID_AGENT_KEY

extra=()
while IFS= read -r line; do
  if [[ -n $line ]]; then
    extra+=("$line")
  fi
done <<<"${COSTROID_AGENT_ARGS:-}"

exec costroid run --budget-usd "$COSTROID_BUDGET_USD" --out "$COSTROID_ROWS" -- \
  "${agent[@]}" "${extra[@]}" "$COSTROID_PROMPT" </dev/null
