# Host the ledger

The CLI needs no server: `costroid run` and `costroid report` work from a job alone. Host the ledger to keep every run's rows, read the per-PR view and hold budgets to verified rows.

The image is one static binary on a non-root user, with no shell. It runs the service, and every other `costroid` command, from one tag. `docs/serve.md` has what the service does.

The Compose file and the chart still default to the image of the first release. The commands below run `1.0.0-alpha.20`, the release the recipes pin.

## Docker Compose

```sh
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_OWNER_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_APP_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_PSEUDONYM_KEY="$(openssl rand -base64 32)"
export COSTROID_OIDC_AUDIENCE=https://costroid.example
export COSTROID_BIND=github.com:example
export COSTROID_IMAGE=ghcr.io/costroid/costroid:1.0.0-alpha.20
docker compose -f deploy/compose.yaml up -d
```

The service listens on `127.0.0.1:8080`. Put your own TLS in front of it, and set `COSTROID_LISTEN` to publish on another address. An empty data directory gets the two roles from `deploy/postgres-init/`, and the service runs its migrations on start.

## Helm

```sh
helm install costroid deploy/helm/costroid --set image.tag=1.0.0-alpha.20 \
  --set oidcAudience=https://costroid.example --set 'bindings={github.com:example}'
```

The chart starts its own Postgres and makes one Secret, which it keeps across upgrades.

To use your own Postgres, create the two roles [`docs/serve.md`](serve.md) lists, put three keys in a Secret and name it:

| Key                      | Value                             |
| ------------------------ | --------------------------------- |
| `database-url`           | The application role's connection |
| `migration-database-url` | The owner role's connection       |
| `pseudonym-key`          | At least 32 bytes in base64       |

```sh
helm install costroid deploy/helm/costroid --set image.tag=1.0.0-alpha.20 --set postgres.enabled=false --set existingSecret=costroid-connections \
  --set oidcAudience=https://costroid.example --set 'bindings={github.com:example}'
```

The chart renders and lints in both shapes. It has not been installed on a cluster yet.

## Issue a key

```sh
docker compose -f deploy/compose.yaml exec costroid /usr/local/bin/costroid keys create --owner github.com:example
```

Under Helm, `kubectl exec` the same command in the service's pod. The key is shown once. Rows sent under it stay unverified and never reach a budget. CI jobs send rows under their OIDC token instead.

## Egress

The service keeps an egress allowlist of exact hosts. It is empty by default, and an empty list refuses every outbound call. It never calls costroid.com.

The service makes one kind of outbound call: it reads the signing keys of the CI issuers it trusts. A deployment that takes only ingest keys needs nothing on the list. For OIDC ingest, list the host of each issuer you use. No issuer is exempt, the default ones included.

| Issuer               | Host to list                          |
| -------------------- | ------------------------------------- |
| GitHub Actions       | `token.actions.githubusercontent.com` |
| gitlab.com           | `gitlab.com`                          |
| Each `--gitlab-host` | That host                             |

```sh
export COSTROID_EGRESS_ALLOW=token.actions.githubusercontent.com,gitlab.com
```

Under Helm, `--set 'egressAllow={token.actions.githubusercontent.com,gitlab.com}'`. On the command line, `--egress-allow` takes one host, repeats, and adds to the variable.

Hosts compare after lower-casing and dropping one trailing dot. A wildcard, a scheme, a port or a path stops the service at start. The list is checked for each request and again at each dial, so a redirect cannot leave it. The service does not read `HTTP_PROXY` or `HTTPS_PROXY`.

A token from an issuer off the list answers `503` with `EGRESS_REFUSED`, which names the host and the flag, and stores nothing. At start the service logs the list once and warns about each trusted issuer the list does not cover.

### The report

```sh
docker compose -f deploy/compose.yaml exec costroid /usr/local/bin/costroid egress report
```

```
DAY         HOST                                 OUTCOME    COUNT
2026-11-02  gitlab.com                           refused    3
2026-11-01  token.actions.githubusercontent.com  completed  12
```

`--json` prints the same lines as `{"counters":[{"day","host","outcome","count"}]}`. Each line is one UTC day, one host and one outcome: `completed` for any answer, `failed` for no answer, and `refused` for a call the list stopped before a socket opened. Each hop of a redirect counts once. The report keeps no path, query, header or body.

The service writes its counters every 5 seconds, so a process that is killed loses at most the last 5 seconds. A failed write is logged and retried, and it never fails the call it counts.

### What the list does not cover

Postgres is the service's own store, and its connection is not on the list. `costroid reconcile` and `costroid report` run in your CI, not in the container, and the list does not bind them.

An allowlist inside the process does not hold against code running in the process. A firewall or a network policy is the stronger control, so set one as well. Compose keeps Postgres on an internal network with no route out.
