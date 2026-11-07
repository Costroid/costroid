#!/usr/bin/env bash
# Runs one agent under costroid run, with the provider key on this process only.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 125
}

# The release each agent is installed at when it is not on the runner and
# agent-version is empty: the one the recipes install.
claude_release=@anthropic-ai/claude-code@2.1.316
codex_release=@openai/codex@0.164.0-alpha.11.1
release_number='^[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$'

# Every input is checked before any is acted on, so one run names every
# problem at once.
refused=false
case ${COSTROID_AGENT:-} in
  claude)
    agent=(claude -p)
    key_name=ANTHROPIC_API_KEY
    release=$claude_release
    ;;
  codex)
    agent=(codex exec)
    key_name=CODEX_API_KEY
    release=$codex_release
    ;;
  "") ;;
  *)
    echo "costroid: agent must be claude or codex, got '$COSTROID_AGENT'" >&2
    refused=true
    ;;
esac

empty=""
for input in agent=COSTROID_AGENT prompt=COSTROID_PROMPT budget-usd=COSTROID_BUDGET_USD \
  api-key=COSTROID_AGENT_KEY minimum-agent-version=COSTROID_MINIMUM_AGENT_VERSION; do
  variable=${input#*=}
  [[ -n ${!variable:-} ]] || empty+="${empty:+, }${input%%=*}"
done
if [[ -n $empty ]]; then
  echo "costroid: the run stage needs these inputs, which are empty: $empty" >&2
  refused=true
fi

if [[ -n ${COSTROID_AGENT_VERSION:-} && ! $COSTROID_AGENT_VERSION =~ $release_number ]]; then
  echo "costroid: agent-version must be a release number, got '$COSTROID_AGENT_VERSION'" >&2
  refused=true
fi
[[ $refused == false ]] || exit 125
floor=$COSTROID_MINIMUM_AGENT_VERSION

if ! command -v "${agent[0]}" >/dev/null; then
  package=${release%@*}@${COSTROID_AGENT_VERSION:-${release##*@}}
  echo "costroid: ${agent[0]} is not on this runner, so the action installs $package" >&2
  npm install --global "$package" >&2 || fail "could not install $package"
fi

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
