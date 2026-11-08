# Costroid

Costroid is the cost check for pull requests. An agent's own cost figure is an estimate, so Costroid prices the run's tokens from a pinned, dated catalog and posts the figure, with its basis, on the pull request. A recorded run on 2026-11-07:

> This run cost USD 0.0391529 at list (Measured). Nothing held it to a budget.

## Install

The recipes pin `1.0.0-alpha.12`: [install it](docs/install.md).

## Add it to the workflow you have

Already on `claude-code-action`, with an API key or a Claude subscription, or on `openai/codex-action`:

```sh
costroid init github --existing .github/workflows/claude.yml > costroid.patch
git apply costroid.patch
```

The patch adds a `read` step and a `report` job, [shown here](docs/existing-workflow.md).

## Try it on a pull request

`--pr` also sets the key and opens the pull request. Without it, `init` prints the rest:

```sh
costroid init github --agent claude
gh secret set ANTHROPIC_API_KEY
git switch -c try-costroid
git add .github/workflows/costroid.yml costroid.yml
git commit -m "Add the costroid check"
gh pr create --fill
```

The agent's job runs the third-party [`harden-runner`](docs/recipes.md#egress-in-the-agents-job) to limit its egress.

## First command

With Claude Code installed and signed in:

```sh
costroid run --budget-usd 5 --out rows.jsonl -- claude -p "Fix the failing test."
costroid report --preview --rows rows.jsonl
```

`run` prints, for a recorded subscription run:

```text
costroid: claude-code, 4 requests
costroid: 19 rows in rows.jsonl, catalog 0.3.0
costroid: USD 0.001619725 at list, List-equivalent, not billed
costroid: budget USD 5, agent-flag: client-side estimate, can pass the cap
costroid: warning: BillingAccountId reads unknown: pass --billing-account or set COSTROID_BILLING_ACCOUNT
```

Each row is one request's token class, such as `"SkuMeter":"input_uncached"`. `report --preview` prints the comment the rows would post.

## More

In Claude Code, install the plugin:

```text
/plugin marketplace add Costroid/costroid
/plugin install costroid@costroid
```

[`costroid connect`](docs/connect.md) binds an owner to Costroid Platform. [`export --format`](docs/export.md#files-for-another-tool) writes rows for another cost tool. [The SDK recipes](docs/sdk.md) record Claude Agent SDK and Codex SDK programs. [`docs/`](docs) has the rest.

## Licence

Apache-2.0. The CLI, the gate, the ledger schema, the plan schema, the FOCUS tag convention, the price catalog and the policy packs stay Apache-2.0 and are never relicensed. Contributions are under the [Developer Certificate of Origin](https://developercertificate.org/).
