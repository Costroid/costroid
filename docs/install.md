# Install

`costroid` is one static binary for Linux and macOS. Install it from the release, check it, then let `costroid init` write the workflow for your repository. Hosting the ledger is optional, and [`docs/ledger.md`](ledger.md) sets it up.

## In one step

The release carries `costroid-install.sh`. It checks the release's signature with cosign and the binary against `SHA256SUMS` before it installs anything, to `$HOME/.local/bin`. Read it first:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.19/costroid-install.sh
less costroid-install.sh
sh costroid-install.sh
```

## The binary

The commands below install `1.0.0-alpha.19`, the release the recipes pin. It is a pre-release. Download the binary for your platform from [the release](https://github.com/Costroid/costroid/releases/tag/v1.0.0-alpha.19), check it against `SHA256SUMS`, and put it on your `PATH`:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.19/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.19/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
chmod +x costroid-linux-amd64
sudo mv costroid-linux-amd64 /usr/local/bin/costroid
costroid version
```

The last line prints `costroid 1.0.0-alpha.19, catalog 0.3.0`. The release holds four binaries: `costroid-linux-amd64`, `costroid-linux-arm64`, `costroid-darwin-amd64` and `costroid-darwin-arm64`. For another platform, replace `linux-amd64` in every line. On macOS, `shasum -a 256 --check` takes the place of `sha256sum --check`. Any directory on your `PATH` works in place of `/usr/local/bin`.

## The signature

Each file is signed with cosign, keyless, by this repository's release workflow. To check the binary's signature, with cosign 3 or later:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.19/costroid-linux-amd64.sigstore.json
cosign verify-blob --bundle costroid-linux-amd64.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.19 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com /usr/local/bin/costroid
```

The identity is the release workflow at the release's tag, so a file signed by any other workflow, branch or repository fails. [`docs/verify.md`](verify.md) checks `SHA256SUMS`, the image and the build provenance as well.

## With Go

With Go 1.27 or later:

```sh
go install github.com/Costroid/costroid/cmd/costroid@v1.0.0-alpha.19
```

`go install` records the release's version, so `costroid version` prints it. The build records no commit, so `init` reads the commit the release's tag names from github.com with `git ls-remote`, pins it and says so. Where github.com is unreachable, pass the commit of `v1.0.0-alpha.19` with `--action-ref c14ca2dd10b7ca5373af4453a7d4bdde3a4dafc7`.

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
