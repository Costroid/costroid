# Costroid

Costroid is the cost check for pull requests. It runs a coding agent under a budget, records what the run cost per token class as FOCUS rows, and posts the figure on the pull request.

Bring your own key and your own agent. There is no account, and Costroid runs no service that your data goes to: the key stays in your repository's secrets, the rows stay in the job, and the ledger, if you run one, is yours.

Besides the agent's provider, the GitHub recipes that run an agent call one third-party service. They run [`step-security/harden-runner`](https://github.com/step-security/harden-runner) on the agent's job, which records the job's outbound calls and sends telemetry to its vendor. [`docs/recipes.md`](docs/recipes.md#egress-in-the-agents-job) says what it costs and how to drop it.

## Why it exists

An agent's own cost field is an estimate, and a budget flag can be weaker than it sounds. Costroid prices the agent's token counts against a pinned, dated catalog, and it writes down how the budget was enforced. A grade is never shown as stronger than it is.

## Install

The commands below install `1.0.0-alpha.9`, the release the recipes pin. It is a pre-release. Download the binary for your platform from [the release](https://github.com/Costroid/costroid/releases/tag/v1.0.0-alpha.9), check it, and put it on your `PATH`:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.9/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.9/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
chmod +x costroid-linux-amd64
sudo mv costroid-linux-amd64 /usr/local/bin/costroid
costroid version
```

The last line prints `costroid 1.0.0-alpha.9, catalog 0.1.0`. The release holds four binaries: `costroid-linux-amd64`, `costroid-linux-arm64`, `costroid-darwin-amd64` and `costroid-darwin-arm64`. For another platform, replace `linux-amd64` in every line. On macOS, `shasum -a 256 --check` takes the place of `sha256sum --check`. Any directory on your `PATH` works in place of `/usr/local/bin`.

Each file is signed with cosign, keyless, by this repository's release workflow. To check a signature, with cosign 3 or later:

```sh
cosign verify-blob --bundle costroid-linux-amd64.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.9 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com costroid-linux-amd64
```

[`docs/verify.md`](docs/verify.md) checks the image as well.

In GitHub Actions, the action installs the release its own commit belongs to and checks it by its signature. The Claude Code and Codex recipes then need one secret, the provider's API key. [`docs/bases.md`](docs/bases.md#which-key-each-recipe-takes) lists what each recipe takes, and what its figures read as a result. The same action is listed on the [GitHub Marketplace](https://github.com/marketplace/actions/costroid-cost-check) as `Costroid/costroid-action`. To build from a checkout instead, with Go 1.27 or later:

```sh
make build
```

To keep rows in your own ledger, [`docs/install.md`](docs/install.md) has the container, Compose and Helm shapes.

## First command

The first command runs Claude Code, so it needs Claude Code installed and signed in, with a Claude subscription or an `ANTHROPIC_API_KEY`.

```sh
costroid run --budget-usd 5 --out rows.jsonl -- claude -p "Fix the failing test."
```

The agent's stream passes through to stdout. Costroid prints this to stderr, here for a recorded run with one subagent on a Claude subscription:

```text
costroid: claude-code, 4 requests
costroid: 19 rows in rows.jsonl, catalog 0.1.0
costroid: USD 0.001619725 at list, List-equivalent, not billed
costroid: budget USD 5, agent-flag: client-side estimate, can pass the cap
costroid: warning: BillingAccountId reads unknown: pass --billing-account or set COSTROID_BILLING_ACCOUNT
```

`rows.jsonl` holds one FOCUS row per request per token class:

```json
{"ChargeDescription":"claude-haiku-5-5 input_uncached tokens; agent-flag: client-side estimate, can pass the cap","ConsumedQuantity":"2","ListCost":"0.0000002","ListUnitPrice":"0.1","SkuMeter":"input_uncached","SkuPriceId":"anthropic:claude-haiku-5-5:global:standard:to-100000:input_uncached"}
```

That row is shortened. A full row has every column of the [row contract](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.3), and every amount is a decimal string.

## Next

| Task                                      | Page                                             |
| ----------------------------------------- | ------------------------------------------------ |
| Run an agent on a pull request            | [`docs/recipes.md`](docs/recipes.md)             |
| Make the check required                   | [`docs/required-check.md`](docs/required-check.md) |
| Read a line of the job log                | [`docs/troubleshooting.md`](docs/troubleshooting.md) |
| Read the comment                          | [`docs/comment-example.md`](docs/comment-example.md) |
| Read a result from an agent               | [`docs/agents.md`](docs/agents.md)               |
| Configure a repository                    | [`docs/configuration.md`](docs/configuration.md) |
| Host the ledger                           | [`docs/install.md`](docs/install.md), [`docs/serve.md`](docs/serve.md) |
| Compare a ledger with a provider's report | [`docs/reconcile.md`](docs/reconcile.md)         |
| See how Costroid's own figures match the provider's | [`docs/accuracy.md`](docs/accuracy.md) |
| Take the rows to a warehouse              | [`docs/export.md`](docs/export.md)               |
| Verify a release                          | [`docs/verify.md`](docs/verify.md)               |
| See what a budget grade means             | [`docs/adapters.md`](docs/adapters.md)           |
| See what a cost basis promises            | [`docs/bases.md`](docs/bases.md)                 |
| See what changed in each release          | [`CHANGELOG.md`](CHANGELOG.md)                   |

## Licence

Apache-2.0. The CLI, the gate, the ledger schema, the plan schema, the FOCUS tag convention, the price catalog and the policy packs stay Apache-2.0 and are never relicensed. Contributions are under the [Developer Certificate of Origin](https://developercertificate.org/).
