#!/usr/bin/env bash
# Installs deploy/helm/costroid on a kind cluster in each of its shapes, sends
# one recorded run's rows to the service and reads the pull request's total
# back, exactly. Then it upgrades to this tree's chart from the release before
# it, in each shape.
#
#   scripts/chart-install.sh [bundled|external]...
#
# With no argument it walks both shapes, one after the other, in one cluster:
#
#   bundled   The chart's own Postgres and the Secret the chart makes.
#   external  A Postgres the script starts as a plain Deployment, with the roles
#             of deploy/postgres-init, and a Secret the script makes, named by
#             ci/external-postgres-values.yaml.
#
# The image is built from this tree and loaded into the cluster, and the chart
# runs it with pullPolicy Never, so no published image stands in for it. The
# service runs its migrations on start, as the chart deploys it. Each rollout
# is awaited for at most ROLLOUT, 180s by default, and the pods, the events and
# the logs are printed when the bound passes. The rows are the recorded run
# make e2e walks, priced by this tree's costroid run, sent through a
# port-forward under a key the service issues. Every password and key is
# generated here and lives only in the cluster and in a private temporary
# directory. On a first install the service may restart while Postgres comes
# up; a restart for any other reason fails the walk.
#
# Each shape then walks an upgrade in a namespace of its own. The chart as the
# release that this tree's Chart.yaml names shipped it, read with git from the
# release's tag, is installed with the image that release published, which the
# cluster pulls without a sign-in. One run's rows go in and the figure is read.
# helm upgrade then moves the release to this tree's chart and image, and the
# service runs the migrations as it starts. The first figure must read the
# same, and after a second run's rows the total must be the exact sum. In the
# bundled shape the release is then uninstalled and installed again in the
# same namespace: Postgres keeps its volume and the chart keeps its Secret, so
# the sum must read the same from the rows already stored.
#
# The cluster is deleted on every exit, and the last line names the step that
# failed. It needs Docker, kind, kubectl, helm, Go, git, curl, jq and openssl,
# and the release's tag in this clone. The kubeconfig and the caches of
# kubectl and helm live in the temporary directory, so no file of yours
# changes.
set -euo pipefail

cd "$(dirname "$0")/.."

# kind v0.33.0 publishes this image for its nodes, Kubernetes v1.37.0.
node_image=kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5
chart=deploy/helm/costroid
image_repository=costroid-chart
image_tag=install
image=$image_repository:$image_tag
release=costroid
service=$release-costroid
rollout=${ROLLOUT:-180s}

# The recorded run make e2e walks, and its list cost: 8 × 0.10 + 15605 × 0.01
# + 3495 × 0.125 + 4450 × 0.20 + 272 × 0.50, per million tokens. The upgrade
# walk sends it as two runs of one pull request, so the total doubles.
fixtures=internal/adapter/claudecode/testdata/2.1.316/subagent
session=00000000-0000-4000-8000-000000000002
recorded_total=0.001619725
two_runs_total=0.00323945
recorded_rows=19
owner=github.com:example
repository=github.com/example/widgets
pull=7

if (($# == 0)); then
  set -- bundled external
fi
for shape in "$@"; do
  case $shape in
    bundled | external) ;;
    *)
      echo "chart-install: unknown shape '$shape': name bundled or external" >&2
      exit 2
      ;;
  esac
done

for tool in docker kind kubectl helm go git curl jq openssl; do
  if ! command -v "$tool" >/dev/null; then
    echo "chart-install: $tool is not on PATH" >&2
    exit 2
  fi
done

cluster=costroid-chart-$$
work=$(mktemp -d)
chmod 700 "$work"
export KUBECONFIG=$work/kubeconfig KUBECACHEDIR=$work/kube-cache HELM_CACHE_HOME=$work/helm-cache \
  HELM_CONFIG_HOME=$work/helm-config HELM_DATA_HOME=$work/helm-data
released=$(sed -n 's/^appVersion: //p' "$chart/Chart.yaml")
released_chart=$work/released/$chart
forward=
created=
shape=setup
phase=
step=start
reason=

finish() {
  local status=$?
  stop_forward
  if [[ $created ]] && ! kind delete cluster --name "$cluster" >"$work/delete.log" 2>&1; then
    cat "$work/delete.log" >&2
    echo "chart-install: could not delete kind cluster $cluster; delete it with: kind delete cluster --name $cluster" >&2
    status=1
  fi
  rm -rf "$work"
  if ((status != 0)); then
    echo "chart-install: $shape${phase:+, $phase}: ${reason:-$step failed with exit $status}" >&2
  fi
  exit "$status"
}
trap finish EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

fail() {
  reason=$1
  exit 1
}

stop_forward() {
  if [[ $forward ]]; then
    kill "$forward" 2>/dev/null || true
    wait "$forward" 2>/dev/null || true
    forward=
  fi
}

# diagnose prints what a team reads first when a rollout does not finish.
diagnose() {
  local namespace=$1 pod
  echo "--- pods in $namespace" >&2
  kubectl get pods --namespace "$namespace" --output wide >&2 || true
  echo "--- events in $namespace" >&2
  kubectl get events --namespace "$namespace" --sort-by=.lastTimestamp >&2 || true
  for pod in $(kubectl get pods --namespace "$namespace" --output name 2>/dev/null); do
    echo "--- logs of $pod" >&2
    kubectl logs --namespace "$namespace" "$pod" --all-containers --tail=40 >&2 || true
    if kubectl logs --namespace "$namespace" "$pod" --all-containers --tail=40 --previous >"$work/previous.log" 2>/dev/null; then
      echo "--- logs of $pod before its last restart" >&2
      cat "$work/previous.log" >&2
    fi
  done
}

# last_error prints, on one line, the last error costroid printed in a pod's
# container before it last ended, without the pointer to the troubleshooting
# page.
last_error() {
  kubectl logs --namespace "$1" "$2" --all-containers --previous --tail=20 2>/dev/null |
    awk '/^costroid: / { error = "" } !/^See https:/ { gsub(/[ \t]+/, " "); sub(/^ /, ""); error = error (error == "" ? "" : " ") $0 } END { print error }'
}

# waiting names each pod whose container waits, with the reason, and for a
# container that keeps crashing the last error it printed.
waiting() {
  local namespace=$1 pod reason said=
  while read -r pod reason; do
    [[ $reason ]] || continue
    if [[ $reason == *CrashLoopBackOff* ]]; then
      reason+=" after: $(last_error "$namespace" "$pod")"
    fi
    said+="${said:+; }$pod is $reason"
  done < <(kubectl get pods --namespace "$namespace" \
    --output jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[*].state.waiting.reason}{"\n"}{end}')
  echo "${said:-no container is waiting}"
}

await_rollout() {
  local namespace=$1 kind=$2 name=$3
  step="waiting $rollout for $kind/$name"
  if ! kubectl rollout status --namespace "$namespace" "$kind/$name" --timeout "$rollout"; then
    diagnose "$namespace"
    fail "$kind/$name did not roll out within $rollout: $(waiting "$namespace")"
  fi
}

# await_service waits for the service's rollout after a first install. The
# service exits while Postgres does not answer yet, so on a first install it
# may restart a few times, as long as the image is pulled and the data
# directory initialised. Such a restart is allowed only when the service last
# ended on Postgres not answering: a name that does not resolve while the
# headless Service has no ready pod, which a resolver answers as no such host
# or as a failure of its own, a refused connection, or a database still
# starting.
await_service() {
  local namespace=$1 pod restarts error
  await_rollout "$namespace" deployment "$service"
  step="reading why the service restarted"
  while read -r pod restarts; do
    ((restarts > 0)) || continue
    error=$(last_error "$namespace" "$pod")
    if [[ $error != *"failed to connect to"* ]] ||
      ! grep --quiet --extended-regexp 'no such host|hostname resolving error|connection refused|database system is (starting up|not yet accepting connections)' <<<"$error"; then
      fail "$pod took $restarts restarts on a first install, the last not on Postgres starting: $error"
    fi
    echo "chart-install: $shape: $pod took $restarts restarts while Postgres came up, the last after: $error"
  done < <(kubectl get pods --namespace "$namespace" --selector "app.kubernetes.io/name=costroid,app.kubernetes.io/instance=$release" \
    --output jsonpath='{range .items[*]}{.metadata.name} {.status.containerStatuses[0].restartCount}{"\n"}{end}')
}

build_image() {
  step="building $image from this tree"
  docker build --quiet --tag "$image" --build-arg VERSION=chart-install \
    --build-arg COMMIT="$(git rev-parse HEAD 2>/dev/null || true)" . >/dev/null
}

# read_released puts the chart as release $released shipped it under
# $released_chart, from the release's tag in this clone, and checks that the
# release's image can be pulled without a sign-in, as the cluster will.
read_released() {
  local published
  step="reading the chart at tag v$released"
  git rev-parse --quiet --verify "refs/tags/v$released" >/dev/null ||
    fail "tag v$released is not in this clone; fetch it with: git fetch --no-tags --depth=1 origin refs/tags/v$released:refs/tags/v$released"
  mkdir -p "$work/released"
  git archive "v$released" "$chart" | tar --extract --directory "$work/released"
  published=$(sed -n 's/^  repository: \(.*costroid.*\)$/\1/p' "$released_chart/values.yaml"):$released
  step="checking that $published pulls without a sign-in"
  mkdir -p "$work/docker-anonymous"
  DOCKER_CONFIG=$work/docker-anonymous docker manifest inspect "$published" >/dev/null ||
    fail "$published cannot be read without a sign-in"
}

# make_rows prices the recorded run with this tree's costroid run, as a pull
# request's job would, as run $1, and leaves its rows in $3. The provider's
# request ids, which a row's id is made from, are rewritten to start with $2:
# a second run of the recording is a second set of rows at the same price.
make_rows() {
  local run=$1 requests=$2 rows=$3 dir=$work/run-$1
  local project=$dir/config/projects/-work
  step="pricing the recorded run as run $run"
  if [[ ! -x $work/costroid ]]; then
    CGO_ENABLED=0 go build -o "$work/costroid" ./cmd/costroid
    printf '{"pull_request":{"number":%s,"head":{"sha":"9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d"},"base":{"sha":"1111111111111111111111111111111111111111","ref":"main"}},"repository":{"visibility":"private","private":true}}' "$pull" >"$work/event.json"
  fi
  mkdir -p "$project/$session/subagents"
  sed "s/req_fixture_/$requests/g" "$fixtures/session.jsonl" >"$project/$session.jsonl"
  sed "s/req_fixture_/$requests/g" "$fixtures/subagents/agent-1.jsonl" >"$project/$session/subagents/agent-1.jsonl"
  sed "s/req_fixture_/$requests/g" "$fixtures/stream.jsonl" >"$dir/stream.jsonl"
  printf '#!/bin/sh\nexec cat %s\n' "$dir/stream.jsonl" >"$dir/claude"
  chmod +x "$dir/claude"
  CLAUDE_CONFIG_DIR=$dir/config GITHUB_ACTIONS=true GITHUB_SERVER_URL=https://github.com \
    GITHUB_REPOSITORY=${repository#github.com/} GITHUB_RUN_ID=$run GITHUB_JOB=agent \
    GITHUB_SHA=2222222222222222222222222222222222222222 GITHUB_EVENT_NAME=pull_request \
    GITHUB_EVENT_PATH=$work/event.json COSTROID_INSTALLATION_ID='' COSTROID_CREDENTIAL=apikey_01ExampleChart \
    "$work/costroid" run --budget-usd 5 --out "$rows" -- "$dir/claude" -p 'Fix the failing test.' >/dev/null 2>"$dir/run.log" ||
    { cat "$dir/run.log" >&2; fail "costroid run could not price the recorded run"; }
  grep --quiet "USD $recorded_total at list" "$dir/run.log" ||
    { cat "$dir/run.log" >&2; fail "costroid run did not state USD $recorded_total at list"; }
}

create_cluster() {
  step="creating kind cluster $cluster"
  created=1
  kind create cluster --name "$cluster" --image "$node_image" --wait 120s
  step="loading $image into $cluster"
  kind load docker-image "$image" --name "$cluster"
}

secret_file() {
  local file=$work/secret/$1
  mkdir -p "$work/secret"
  printf '%s' "$2" >"$file"
  echo "$file"
}

# start_external_postgres runs Postgres as a team's own would stand: a plain
# Deployment, the roles from deploy/postgres-init, and the three keys the
# chart reads in a Secret the chart does not manage.
start_external_postgres() {
  local namespace=$1 superuser owner_password app_password postgres_image
  step="starting the external Postgres"
  superuser=$(openssl rand -hex 32)
  owner_password=$(openssl rand -hex 32)
  app_password=$(openssl rand -hex 32)
  kubectl create namespace "$namespace"
  kubectl create secret generic postgres --namespace "$namespace" \
    --from-file=postgres-password="$(secret_file superuser "$superuser")" \
    --from-file=owner-password="$(secret_file owner "$owner_password")" \
    --from-file=app-password="$(secret_file app "$app_password")"
  kubectl create configmap postgres-init --namespace "$namespace" --from-file=deploy/postgres-init/10-roles.sh
  kubectl create secret generic costroid-connections --namespace "$namespace" \
    --from-file=database-url="$(secret_file database-url "postgres://costroid_app:$app_password@postgres/costroid")" \
    --from-file=migration-database-url="$(secret_file migration-database-url "postgres://costroid_owner:$owner_password@postgres/costroid")" \
    --from-file=pseudonym-key="$(secret_file pseudonym-key "$(openssl rand -base64 32)")"
  rm -rf "$work/secret"
  postgres_image=$(sed -n 's/^    image: \(postgres:.*\)$/\1/p' deploy/compose.yaml)
  kubectl apply --namespace "$namespace" -f - <<EOF
apiVersion: v1
kind: Service
metadata:
  name: postgres
spec:
  selector:
    app: postgres
  ports:
    - port: 5432
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app: postgres
  template:
    metadata:
      labels:
        app: postgres
    spec:
      containers:
        - name: postgres
          image: $postgres_image
          env:
            - name: POSTGRES_DB
              value: costroid
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef: {name: postgres, key: postgres-password}
            - name: COSTROID_OWNER_PASSWORD
              valueFrom:
                secretKeyRef: {name: postgres, key: owner-password}
            - name: COSTROID_APP_PASSWORD
              valueFrom:
                secretKeyRef: {name: postgres, key: app-password}
          readinessProbe:
            exec:
              command: [pg_isready, --host=127.0.0.1, --username=postgres, --dbname=costroid]
          volumeMounts:
            - {name: init, mountPath: /docker-entrypoint-initdb.d, readOnly: true}
      volumes:
        - name: init
          configMap: {name: postgres-init, defaultMode: 0555}
EOF
  await_rollout "$namespace" deployment postgres
}

# helm_tree installs or upgrades the release to this tree's chart and image.
helm_tree() {
  local verb=$1 namespace=$2
  shift 2
  step="helm $verb with the chart of this tree"
  helm "$verb" "$release" "$chart" --namespace "$namespace" \
    --set image.repository="$image_repository" --set image.tag="$image_tag" --set image.pullPolicy=Never "$@"
}

# helm_released installs the release with the chart and the published image of
# release $released, as a team on that release runs it.
helm_released() {
  local namespace=$1
  shift
  step="installing the chart of release $released"
  helm install "$release" "$released_chart" --namespace "$namespace" --create-namespace \
    --set image.tag="$released" "$@"
}

uninstall() {
  step="uninstalling the release"
  helm uninstall "$release" --namespace "$1" --wait --timeout 120s
}

open_forward() {
  local namespace=$1 port _
  step="forwarding a local port to service/$service"
  kubectl port-forward --namespace "$namespace" "service/$service" :8080 >"$work/forward.log" 2>&1 &
  forward=$!
  for _ in $(seq 60); do
    port=$(sed -n 's/^Forwarding from 127\.0\.0\.1:\([0-9]*\) -> 8080$/\1/p' "$work/forward.log")
    if [[ $port ]]; then
      url=http://127.0.0.1:$port
      return
    fi
    if ! kill -0 "$forward" 2>/dev/null; then
      cat "$work/forward.log" >&2
      fail "kubectl port-forward ended before it forwarded"
    fi
    sleep 0.5
  done
  cat "$work/forward.log" >&2
  fail "kubectl port-forward did not forward within 30 s"
}

# issue_key asks the service for an ingest key, as docs/ledger.md tells a team
# to. The key goes to a header file only this user reads, never to a command
# line or the log.
issue_key() {
  local namespace=$1 key
  step="issuing an ingest key"
  key=$(kubectl exec --namespace "$namespace" "deployment/$service" -- /usr/local/bin/costroid keys create --owner "$owner")
  [[ $key ]] || fail "costroid keys create printed no key"
  printf 'Authorization: Bearer %s\n' "$key" >"$work/authorization"
}

send_rows() {
  local rows=$1 answer accepted
  step="sending $(basename "$rows") to POST /v1/rows"
  answer=$(curl --silent --show-error --fail-with-body --header @"$work/authorization" \
    --header 'Content-Type: application/x-ndjson' --data-binary @"$rows" "$url/v1/rows") ||
    fail "POST /v1/rows failed: $answer"
  accepted=$(jq --raw-output .accepted <<<"$answer")
  [[ $accepted == "$recorded_rows" ]] || fail "POST /v1/rows accepted $accepted rows, want $recorded_rows: $answer"
}

# read_total reads the pull request's total and holds it to USD $1 exactly.
read_total() {
  local want="USD $1 list_equivalent" answer got
  step="reading the per-PR view"
  answer=$(curl --silent --show-error --fail-with-body --header @"$work/authorization" \
    "$url/v1/pulls/$pull?repo=$repository") || fail "GET /v1/pulls/$pull failed: $answer"
  got=$(jq --raw-output '[.totals[] | "\(.currency) \(.list_cost) \(.cost_basis)"] | join("; ")' <<<"$answer")
  [[ $got == "$want" ]] || fail "$2: the per-PR view reads '$got', want '$want'"
  echo "chart-install: $shape: $2: pull request $pull reads $want"
}

# ingest_and_read issues a key, sends the first run's rows and reads them.
ingest_and_read() {
  open_forward "$1"
  issue_key "$1"
  send_rows "$work/run-1.jsonl"
  read_total "$recorded_total" "$2"
  stop_forward
}

# upgrade_and_read moves the release to this tree, reads the first run's
# figure, sends the second run's rows and reads the sum.
upgrade_and_read() {
  local namespace=$1
  shift
  phase="the upgrade from $released"
  helm_tree upgrade "$namespace" "$@"
  if kubectl get statefulset --namespace "$namespace" "$service-postgres" >/dev/null 2>&1; then
    await_rollout "$namespace" statefulset "$service-postgres"
  fi
  await_rollout "$namespace" deployment "$service"
  open_forward "$namespace"
  read_total "$recorded_total" "after the upgrade from $released"
  send_rows "$work/run-2.jsonl"
  read_total "$two_runs_total" "after a second run"
  stop_forward
}

walk_bundled() {
  local namespace=costroid-bundled
  phase="a fresh install"
  helm_tree install "$namespace" --create-namespace --set "bindings={$owner}"
  await_rollout "$namespace" statefulset "$service-postgres"
  await_service "$namespace"
  ingest_and_read "$namespace" "a fresh install"
  uninstall "$namespace"

  namespace=costroid-bundled-upgrade
  phase="release $released"
  helm_released "$namespace" --set "bindings={$owner}"
  await_rollout "$namespace" statefulset "$service-postgres"
  await_service "$namespace"
  ingest_and_read "$namespace" "release $released"
  upgrade_and_read "$namespace" --set "bindings={$owner}"
  uninstall "$namespace"
  phase="an install after the uninstall"
  helm_tree install "$namespace" --set "bindings={$owner}"
  await_rollout "$namespace" statefulset "$service-postgres"
  await_rollout "$namespace" deployment "$service"
  open_forward "$namespace"
  read_total "$two_runs_total" "after an uninstall and an install"
  stop_forward
  uninstall "$namespace"
}

walk_external() {
  local namespace=costroid-external
  phase="a fresh install"
  start_external_postgres "$namespace"
  helm_tree install "$namespace" --values "$chart/ci/external-postgres-values.yaml"
  await_service "$namespace"
  ingest_and_read "$namespace" "a fresh install"
  uninstall "$namespace"

  namespace=costroid-external-upgrade
  phase="release $released"
  start_external_postgres "$namespace"
  helm_released "$namespace" --values "$released_chart/ci/external-postgres-values.yaml"
  await_service "$namespace"
  ingest_and_read "$namespace" "release $released"
  upgrade_and_read "$namespace" --values "$chart/ci/external-postgres-values.yaml"
  uninstall "$namespace"
}

read_released
build_image
make_rows 16873409921 req_fixture_ "$work/run-1.jsonl"
make_rows 16873409922 req_second_run_ "$work/run-2.jsonl"
create_cluster
for shape in "$@"; do
  "walk_$shape"
done
shape=all
phase=
step=finished
echo "chart-install: the chart installed and upgraded from $released, took the rows and read them exactly in: $*"
