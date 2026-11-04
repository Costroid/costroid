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

run=(costroid run --budget-usd "$COSTROID_BUDGET_USD" --out "$COSTROID_ROWS" -- "${agent[@]}" "${extra[@]}" "$COSTROID_PROMPT")
if [[ ${COSTROID_SHOW_OUTPUT:-false} == true ]]; then
  exec "${run[@]}" </dev/null
fi

# The agent's stream holds its reasoning, its replies and what it read. A public
# repository's job log is public, so the stream goes to a file on this runner,
# which the job never uploads. Costroid's own lines stay on stderr.
output=${RUNNER_TEMP:-/tmp}/costroid-agent-output.jsonl
(umask 077 && : >"$output")
echo "costroid: the agent's output is in $output on this runner, not in this log. show-output: true prints it." >&2
exec "${run[@]}" </dev/null >"$output"
