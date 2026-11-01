# Host the ledger

The CLI needs no server: `costroid run` and `costroid report` work from a job alone. Host the ledger to keep every run's rows, read the per-PR view and hold budgets to verified rows.

The image is one static binary on a non-root user, with no shell. It runs the service, and every other `costroid` command, from one tag. `docs/serve.md` has what the service does.

## Docker Compose

```sh
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_OWNER_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_APP_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_PSEUDONYM_KEY="$(openssl rand -base64 32)"
export COSTROID_OIDC_AUDIENCE=https://costroid.example
export COSTROID_BIND=github.com:example
docker compose -f deploy/compose.yaml up -d
```

The service listens on `127.0.0.1:8080`. Put your own TLS in front of it, and set `COSTROID_LISTEN` to publish on another address. An empty data directory gets the two roles from `deploy/postgres-init/`, and the service runs its migrations on start.

## Helm

```sh
helm install costroid deploy/helm/costroid --set oidcAudience=https://costroid.example --set 'bindings={github.com:example}'
```

The chart starts its own Postgres and makes one Secret, which it keeps across upgrades.

To use your own Postgres, create the two roles [`docs/serve.md`](serve.md) lists, put three keys in a Secret and name it:

| Key                      | Value                             |
| ------------------------ | --------------------------------- |
| `database-url`           | The application role's connection |
| `migration-database-url` | The owner role's connection       |
| `pseudonym-key`          | At least 32 bytes in base64       |

```sh
helm install costroid deploy/helm/costroid --set postgres.enabled=false --set existingSecret=costroid-connections \
  --set oidcAudience=https://costroid.example --set 'bindings={github.com:example}'
```

The chart renders and lints in both shapes. It has not been installed on a cluster yet.

## Issue a key

```sh
docker compose -f deploy/compose.yaml exec costroid /usr/local/bin/costroid keys create --owner github.com:example
```

Under Helm, `kubectl exec` the same command in the service's pod. The key is shown once. Rows sent under it stay unverified and never reach a budget. CI jobs send rows under their OIDC token instead.

## Egress

The service makes one kind of outbound call: it reads the signing keys of the CI issuers it trusts, `token.actions.githubusercontent.com` and `gitlab.com` by default, and each `--gitlab-host`. A deployment that takes only ingest keys makes none. It never calls costroid.com.

Compose keeps Postgres on an internal network with no route out. The container does not enforce an egress allowlist yet, and it has no egress report, so limit outbound traffic at your firewall or with a network policy.
