#!/usr/bin/env bash
# Sends the run's rows to the ledger under this job's OIDC token. A failure warns
# and never fails the job, because a ledger outage must not block a merge.
set -euo pipefail

warn() {
  echo "::warning::costroid: $1"
  exit 0
}

[[ -n ${COSTROID_INGEST_URL:-} ]] || warn "ingest-url is empty, so no rows were sent"
[[ -s $COSTROID_ROWS ]] || warn "the run left no rows"
[[ -n ${ACTIONS_ID_TOKEN_REQUEST_URL:-} ]] || warn "this job has no id-token permission"

answer=$(printf 'Authorization: Bearer %s\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" |
  curl --fail --silent --show-error --get --header @- \
    --data-urlencode "audience=${COSTROID_INGEST_AUDIENCE:-}" \
    "$ACTIONS_ID_TOKEN_REQUEST_URL") || warn "GitHub issued no OIDC token"
token=$(jq --raw-output .value <<<"$answer")
[[ -n $token && $token != null ]] || warn "GitHub issued no OIDC token"
echo "::add-mask::$token"

printf 'Authorization: Bearer %s\n' "$token" |
  curl --fail --silent --show-error --proto '=https' --header @- \
    --header 'Content-Type: application/x-ndjson' \
    --data-binary "@$COSTROID_ROWS" "$COSTROID_INGEST_URL" || warn "the ledger refused the rows"
