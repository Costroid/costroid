# Install

`costroid` is one static binary for Linux and macOS. Install it from the release, check it, then let `costroid init` write the workflow for your repository. Hosting the ledger is optional.

## The binary

The commands below install `1.0.0-alpha.12`, the release the recipes pin. It is a pre-release. Download the binary for your platform from [the release](https://github.com/Costroid/costroid/releases/tag/v1.0.0-alpha.12), check it against `SHA256SUMS`, and put it on your `PATH`:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.12/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.12/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
chmod +x costroid-linux-amd64
sudo mv costroid-linux-amd64 /usr/local/bin/costroid
costroid version
```

The last line prints `costroid 1.0.0-alpha.12, catalog 0.1.0`. The release holds four binaries: `costroid-linux-amd64`, `costroid-linux-arm64`, `costroid-darwin-amd64` and `costroid-darwin-arm64`. For another platform, replace `linux-amd64` in every line. On macOS, `shasum -a 256 --check` takes the place of `sha256sum --check`. Any directory on your `PATH` works in place of `/usr/local/bin`.

## The signature

Each file is signed with cosign, keyless, by this repository's release workflow. To check the binary's signature, with cosign 3 or later:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.12/costroid-linux-amd64.sigstore.json
cosign verify-blob --bundle costroid-linux-amd64.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.12 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com /usr/local/bin/costroid
```

The identity is the release workflow at the release's tag, so a file signed by any other workflow, branch or repository fails. [`docs/verify.md`](verify.md) checks `SHA256SUMS`, the image and the build provenance as well.

## With Go

With Go 1.27 or later:

```sh
go install github.com/Costroid/costroid/cmd/costroid@v1.0.0-alpha.12
```

`go install` builds from the module's source, so the build carries no release number: `costroid version` prints `costroid dev, catalog 0.1.0`. `init` refuses such a build, because it cannot tell which release to pin. Pass it the commit of `v1.0.0-alpha.12` with `--action-ref 8defbe00a23afb0f3a07130e24087cb8b9b68ed0`, or install the binary above.

To build from a checkout instead, run `make build`. That build belongs to no release either.

## In GitHub Actions

The workflow `init` writes needs no install step of yours. Costroid's action installs the release its own commit belongs to and checks it by its signature, on `amd64` and `arm64` runners alike. The Claude Code and Codex recipes then need one secret, the provider's API key. [`docs/bases.md`](bases.md#which-key-each-recipe-takes) lists what each recipe takes, and what its figures read as a result. The same action is listed on the [GitHub Marketplace](https://github.com/marketplace/actions/costroid-cost-check) as `Costroid/costroid-action`.

## Write the workflow

From the root of your repository:

```sh
costroid init github --agent claude
```

`init` writes `.github/workflows/costroid.yml` from the recipe this release ships, with Costroid's action pinned to the commit of the release the binary belongs to. `--agent` takes `claude`, `codex`, `claude-code-action` or `codex-action`. [`docs/recipes.md`](recipes.md) describes each recipe.

On GitLab, `init` writes `.gitlab/costroid.yml`, with `COSTROID_VERSION` and `COSTROID_COMMIT` set to that release and `COSTROID_AGENT` to the agent, `claude` or `codex`:

```sh
costroid init gitlab --agent codex
```

`init` also writes `costroid.yml` at the root when the repository has none. Every key of the schema is in it, commented out under its description, so the file sets nothing until you remove a `#`. [`docs/configuration.md`](configuration.md) describes the keys.

It holds the recipe to the rules of `costroid lint-recipes` before it writes it, and it refuses to replace a file unless you pass `--force`. Then it prints what each push to a pull request can spend, and the commands that are left: the secret, a branch, a commit and a pull request, then the label a first contributor's pull request needs. It runs none of them.

On GitHub, `--pr` runs them up to the pull request, and prints each command before it runs it. It sets the key as a repository secret, read from the environment variable `--key-env` names or from a prompt that does not echo it, and never prints it. Then it commits on the branch `try-costroid`, pushes, opens the pull request, watches its run and prints the URL of Costroid's comment. It stops at the first command that fails. `--pr` is on `main` and in no release yet.

A binary built from a checkout with `make build` belongs to no release, so `init` refuses rather than pin a commit that was never released. On GitHub, `--action-ref` names the commit to pin instead.

### A workflow that already runs claude-code-action

```sh
costroid init github --agent claude-code-action --existing .github/workflows/claude.yml > costroid.patch
git apply costroid.patch
```

`--existing` writes nothing. It prints a patch that gives your Claude step an `id` if it has none, adds a `read` step after that step and adds a `report` job. [`docs/existing-workflow.md`](existing-workflow.md) shows the result and what each block holds.

## Host the ledger

The CLI needs no server: `costroid run` and `costroid report` work from a job alone. Host the ledger to keep every run's rows, read the per-PR view and hold budgets to verified rows.

The image is one static binary on a non-root user, with no shell. It runs the service, and every other `costroid` command, from one tag. `docs/serve.md` has what the service does.

The Compose file and the chart still default to the image of the first release. The commands below run `1.0.0-alpha.12`, the release the recipes pin.

## Docker Compose

```sh
export POSTGRES_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_OWNER_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_APP_PASSWORD="$(openssl rand -hex 32)"
export COSTROID_PSEUDONYM_KEY="$(openssl rand -base64 32)"
export COSTROID_OIDC_AUDIENCE=https://costroid.example
export COSTROID_BIND=github.com:example
export COSTROID_IMAGE=ghcr.io/costroid/costroid:1.0.0-alpha.12
docker compose -f deploy/compose.yaml up -d
```

The service listens on `127.0.0.1:8080`. Put your own TLS in front of it, and set `COSTROID_LISTEN` to publish on another address. An empty data directory gets the two roles from `deploy/postgres-init/`, and the service runs its migrations on start.

## Helm

```sh
helm install costroid deploy/helm/costroid --set image.tag=1.0.0-alpha.12 \
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
helm install costroid deploy/helm/costroid --set image.tag=1.0.0-alpha.12 --set postgres.enabled=false --set existingSecret=costroid-connections \
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
