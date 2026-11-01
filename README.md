# Costroid

Costroid is the cost check for pull requests. It runs a coding agent under a budget, records what the run cost per token class as FOCUS rows, and posts the figure on the pull request.

## Why it exists

An agent's own cost field is an estimate, and a budget flag can be weaker than it sounds. Costroid prices the agent's token counts against a pinned, dated catalog, and it writes down how the budget was enforced. A grade is never shown as stronger than it is.

## Install

The first release will be `1.0.0-alpha.1`. Until it ships, build from a checkout, with Go 1.27 or later:

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

That row is shortened. A full row has every column of the [row contract](https://github.com/Costroid/costroid-schemas/tree/main/focus-row/v0.2), and every amount is a decimal string.

## Next

| Task                                      | Page                                             |
| ----------------------------------------- | ------------------------------------------------ |
| Run an agent on a pull request            | [`docs/recipes.md`](docs/recipes.md)             |
| Read the comment                          | [`docs/comment-example.md`](docs/comment-example.md) |
| Configure a repository                    | [`docs/configuration.md`](docs/configuration.md) |
| Host the ledger                           | [`docs/install.md`](docs/install.md), [`docs/serve.md`](docs/serve.md) |
| Compare a ledger with a provider's report | [`docs/reconcile.md`](docs/reconcile.md)         |
| See what a budget grade means             | [`docs/adapters.md`](docs/adapters.md)           |

## Licence

Apache-2.0. The CLI, the gate, the ledger schema, the plan schema, the FOCUS tag convention, the price catalog and the policy packs stay Apache-2.0 and are never relicensed. Contributions are under the [Developer Certificate of Origin](https://developercertificate.org/).
