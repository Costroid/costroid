# Costroid

Costroid is the cost check for pull requests. It runs a coding agent under a budget, records what the run cost per token class as FOCUS rows, and posts the figure on the pull request.

## Why it exists

An agent's own cost field is an estimate, and a budget flag can be weaker than it sounds. Costroid prices the agent's token counts against a pinned, dated catalog, and it writes down how the budget was enforced. A grade is never shown as stronger than it is.

## Install

`1.0.0-alpha.3` is the latest release, a pre-release. Download the binary for your platform from [the release](https://github.com/Costroid/costroid/releases/tag/v1.0.0-alpha.3) and check it:

```sh
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.3/costroid-linux-amd64
curl -fsSLO https://github.com/Costroid/costroid/releases/download/v1.0.0-alpha.3/SHA256SUMS
grep ' costroid-linux-amd64$' SHA256SUMS | sha256sum --check
```

Each file is signed with cosign, keyless, by this repository's release workflow. To check a signature, with cosign 3 or later:

```sh
cosign verify-blob --bundle costroid-linux-amd64.sigstore.json \
  --certificate-identity https://github.com/Costroid/costroid/.github/workflows/release.yml@refs/tags/v1.0.0-alpha.3 \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com costroid-linux-amd64
```

[`docs/verify.md`](docs/verify.md) checks the image as well.

From the next release, the action installs the release its own commit belongs to and checks it by its signature, so a repository sets one secret, the provider key. Until then, [`docs/recipes.md`](docs/recipes.md) says how to run the `1.0.0-alpha.3` recipes. To build from a checkout instead, with Go 1.27 or later:

```sh
make build
```

To keep rows in your own ledger, [`docs/install.md`](docs/install.md) has the container, Compose and Helm shapes.

## First command

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
| Configure a repository                    | [`docs/configuration.md`](docs/configuration.md) |
| Host the ledger                           | [`docs/install.md`](docs/install.md), [`docs/serve.md`](docs/serve.md) |
| Compare a ledger with a provider's report | [`docs/reconcile.md`](docs/reconcile.md)         |
| Take the rows to a warehouse              | [`docs/export.md`](docs/export.md)               |
| Verify a release                          | [`docs/verify.md`](docs/verify.md)               |
| See what a budget grade means             | [`docs/adapters.md`](docs/adapters.md)           |

## Licence

Apache-2.0. The CLI, the gate, the ledger schema, the plan schema, the FOCUS tag convention, the price catalog and the policy packs stay Apache-2.0 and are never relicensed. Contributions are under the [Developer Certificate of Origin](https://developercertificate.org/).
