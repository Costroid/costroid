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

args=(--execution-file "$COSTROID_EXECUTION_FILE" --out "$COSTROID_ROWS")
[[ -n ${COSTROID_BILLING_ACCOUNT:-} ]] && args+=(--billing-account "$COSTROID_BILLING_ACCOUNT")
[[ -n ${COSTROID_CREDENTIAL:-} ]] && args+=(--credential "$COSTROID_CREDENTIAL")
exec costroid ingest-run "${args[@]}"
