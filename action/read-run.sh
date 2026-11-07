#!/usr/bin/env bash
# Reads an agent's run that already ended: a claude-code-action run from its
# execution file, or an openai/codex-action run from its codex-home. Costroid
# did not start it, so the grade reads none unless budget-usd states the cap
# the step gave Claude Code, and the run shows that cap held it.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 125
}

execution=${COSTROID_EXECUTION_FILE:-}
home=${COSTROID_CODEX_HOME:-}
[[ -z $execution || -z $home ]] || fail "execution-file and codex-home are both set: set the one for the agent's action"

if [[ -n $home ]]; then
  [[ -d $home ]] || fail "codex-home $home is not a directory: pass the codex-home the codex-action step was given"
  agent=codex
  args=(--codex-home "$home")
  # The runner writes the event file as the job starts, so a rollout file last
  # written before it belongs to an earlier job.
  if [[ -n ${GITHUB_EVENT_PATH:-} && -f $GITHUB_EVENT_PATH ]]; then
    args+=(--since "$(date -u -r "$GITHUB_EVENT_PATH" +%Y-%m-%dT%H:%M:%SZ)")
  fi
else
  [[ -n $execution ]] || fail "execution-file is empty: pass the claude-code-action step's execution_file output"
  [[ -f $execution ]] || fail "execution-file $execution does not exist"
  agent=claude
  args=(--execution-file "$execution")
fi

floor=${COSTROID_MINIMUM_AGENT_VERSION:-}
if [[ -n $floor ]]; then
  found=$("$agent" --version | grep --only-matching --extended-regexp '[0-9]+\.[0-9]+\.[0-9]+[^ ]*' | head -n 1) ||
    fail "could not read the version of $agent"
  lowest=$(printf '%s\n%s\n' "$floor" "$found" | sort --version-sort | head -n 1)
  [[ $lowest == "$floor" ]] || fail "$agent $found is older than the minimum $floor"
fi

args+=(--out "$COSTROID_ROWS")
[[ -n ${COSTROID_BUDGET_USD:-} ]] && args+=(--cap-usd "$COSTROID_BUDGET_USD")
[[ -n ${COSTROID_CLAUDE_ARGS:-} ]] && args+=(--claude-args "$COSTROID_CLAUDE_ARGS")
[[ -n ${COSTROID_BILLING_ACCOUNT:-} ]] && args+=(--billing-account "$COSTROID_BILLING_ACCOUNT")
[[ -n ${COSTROID_CREDENTIAL:-} ]] && args+=(--credential "$COSTROID_CREDENTIAL")
exec costroid ingest-run "${args[@]}"
