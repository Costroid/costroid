# Costroid

Costroid is the cost check for pull requests. An agent's own cost figure is an estimate, so Costroid prices the run's tokens from a pinned, [dated catalog](docs/catalog.md) and posts the figure, with its basis, on the pull request. A recorded run on 2026-11-07:

> This run cost USD 0.0391529 at list (Measured). No cap held it.

## First commands

They need no key and spend nothing:

```sh
costroid demo                  # the whole comment a pull request gets, from a recorded run
costroid demo --state capped   # a run stopped at its cap; also failure and skipped
costroid demo --markdown       # the same comment as the Markdown the report posts
costroid lint                  # judges the agent workflows in .github/workflows
```

## Without installing anything

Copy a recipe to `.github/workflows/costroid.yml`, set its one secret and open a pull request: [`github-claude-code-action.yml`](recipes/github-claude-code-action.yml) for Claude Code through `anthropics/claude-code-action`, on `ANTHROPIC_API_KEY` and capped at USD 5 a run, or [`github-codex-action.yml`](recipes/github-codex-action.yml) for `openai/codex-action`, on `OPENAI_API_KEY` and uncapped. Costroid's action installs its own binary. On a private repository, [`harden-runner`](docs/recipes.md#egress-in-the-agents-job) limits egress only with StepSecurity's subscription.

## Install

The recipes pin `1.0.0-alpha.23`: [install it](docs/install.md).

## Add it to the workflow you have

Already on `claude-code-action`, or on `openai/codex-action` with a `codex-home`:

```sh
costroid init github --existing .github/workflows/claude.yml > costroid.patch
git apply costroid.patch
```

[The patch](docs/existing-workflow.md) adds a `read` step and a `report` job.

## Try it on a pull request

`init` prints the commands left, and `--pr` runs them:

```sh
costroid init github --agent claude
gh secret set ANTHROPIC_API_KEY
git switch -c try-costroid
git add .github/workflows/costroid.yml costroid.yml
git commit -m "Add the costroid check"
gh pr create --fill
```

## A run on your machine

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

Each row is one request's token class, such as `"SkuMeter":"input_uncached"`.

## More

[`docs/`](docs/README.md) lists every task. In Claude Code, install the plugin:

```text
/plugin marketplace add Costroid/costroid
/plugin install costroid@costroid
```

## Licence

Apache-2.0. The CLI, the gate, the ledger schema, the plan schema, the FOCUS tag convention, the price catalog and the policy packs stay Apache-2.0 and are never relicensed. Contributions are under the [Developer Certificate of Origin](https://developercertificate.org/).
