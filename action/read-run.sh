#!/usr/bin/env bash
# Reads a claude-code-action run that already ended. Costroid did not start it,
# so no budget held it and the grade reads none.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 125
}

[[ -n ${COSTROID_EXECUTION_FILE:-} ]] || fail "execution-file is empty: pass the claude-code-action step's execution_file output"
[[ -f $COSTROID_EXECUTION_FILE ]] || fail "execution-file $COSTROID_EXECUTION_FILE does not exist"

floor=${COSTROID_MINIMUM_AGENT_VERSION:-}
if [[ -n $floor ]]; then
  found=$(claude --version | grep --only-matching --extended-regexp '[0-9]+\.[0-9]+\.[0-9]+[^ ]*' | head -n 1) ||
    fail "could not read the version of claude"
  lowest=$(printf '%s\n%s\n' "$floor" "$found" | sort --version-sort | head -n 1)
  [[ $lowest == "$floor" ]] || fail "claude $found is older than the minimum $floor"
fi

args=(--execution-file "$COSTROID_EXECUTION_FILE" --out "$COSTROID_ROWS")
[[ -n ${COSTROID_BILLING_ACCOUNT:-} ]] && args+=(--billing-account "$COSTROID_BILLING_ACCOUNT")
[[ -n ${COSTROID_CREDENTIAL:-} ]] && args+=(--credential "$COSTROID_CREDENTIAL")
exec costroid ingest-run "${args[@]}"
