#!/usr/bin/env bash
# Sends the run's rows to the ledger under this job's OIDC token, to Costroid
# Platform under an integration token, or to both. A failure warns and never
# fails the job, because an outage must not block a merge.
set -euo pipefail

# On a runner, curl and jq are the system's own and costroid is the binary
# install.sh checked, each by absolute path, so nothing a step put on PATH
# answers for them while this job holds id-token: write. Off a runner, as in
# this script's tests, PATH finds them.
curl=curl
jq=jq
costroid=costroid
if [[ ${GITHUB_ACTIONS:-} == true ]]; then
  curl=/usr/bin/curl
  jq=/usr/bin/jq
  costroid=${RUNNER_TEMP:-}/costroid-bin/costroid
fi

# warn ends the destination it is called for. Each destination runs in a
# subshell, so a warning for one never skips the other.
warn() {
  echo "::warning::costroid: $1"
  exit 0
}

send_to_ledger() {
  [[ -n ${ACTIONS_ID_TOKEN_REQUEST_URL:-} ]] || warn "this job has no id-token permission"
  [[ $curl != /* || (-x $curl && -x $jq) ]] || warn "this runner has no /usr/bin/curl or no /usr/bin/jq, so no rows were sent"

  answer=$(printf 'Authorization: Bearer %s\n' "$ACTIONS_ID_TOKEN_REQUEST_TOKEN" |
    "$curl" --fail --silent --show-error --get --header @- \
      --data-urlencode "audience=${COSTROID_INGEST_AUDIENCE:-}" \
      "$ACTIONS_ID_TOKEN_REQUEST_URL") || warn "GitHub issued no OIDC token"
  token=$("$jq" --raw-output .value <<<"$answer")
  [[ -n $token && $token != null ]] || warn "GitHub issued no OIDC token"
  echo "::add-mask::$token"

  printf 'Authorization: Bearer %s\n' "$token" |
    "$curl" --fail --silent --show-error --proto '=https' --header @- \
      --header 'Content-Type: application/x-ndjson' \
      --data-binary "@$COSTROID_ROWS" "$COSTROID_INGEST_URL" || warn "the ledger refused the rows"
}

# costroid export reads the token from the environment, never from its
# arguments. Its warnings become annotations, as this script's own are.
send_to_platform() {
  [[ -n ${COSTROID_PLATFORM_TOKEN:-} ]] || warn "platform-token is empty, so no rows were sent to Costroid Platform"
  [[ -n ${COSTROID_PLATFORM_INSTALLATION_ID:-} ]] || warn "installation-id is empty, so no rows were sent to Costroid Platform"
  status=0
  output=$("$costroid" export --rows "$COSTROID_ROWS" --platform-url "$COSTROID_PLATFORM_URL" \
    --installation-id "$COSTROID_PLATFORM_INSTALLATION_ID" 2>&1) || status=$?
  printf '%s\n' "$output" | sed 's/^costroid: warning: /::warning::costroid: /'
  ((status == 0)) || warn "Costroid Platform did not take the rows"
}

[[ -n ${COSTROID_INGEST_URL:-}${COSTROID_PLATFORM_URL:-} ]] || warn "ingest-url and platform-url are empty, so no rows were sent"
[[ -s $COSTROID_ROWS ]] || warn "the run left no rows"
if [[ -n ${COSTROID_INGEST_URL:-} ]]; then
  (send_to_ledger)
fi
if [[ -n ${COSTROID_PLATFORM_URL:-} ]]; then
  (send_to_platform)
fi
