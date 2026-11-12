#!/usr/bin/env bash
# Adds the rows of a FOCUS cost file an earlier step wrote, the workflow's own
# estimate of the change's infrastructure, to the agent's rows before they are
# kept. With infrastructure-optional true, a file that is not there adds no
# rows and leaves a note the report reads to say so.
set -euo pipefail

fail() {
  echo "costroid: $1" >&2
  exit 125
}

case ${COSTROID_INFRASTRUCTURE_OPTIONAL:-false} in
  true) optional=(--optional) ;;
  false) optional=() ;;
  *) fail "infrastructure-optional must be true or false, got '$COSTROID_INFRASTRUCTURE_OPTIONAL'" ;;
esac

exec costroid ingest-run --from "focus=$COSTROID_INFRASTRUCTURE_FILE" \
  --source "${COSTROID_INFRASTRUCTURE_SOURCE:-infrastructure}" "${optional[@]}" --out "$COSTROID_ROWS"
