# Costroid documentation

One page per task, in the order a team meets them.

## Try it

- [`comment-example.md`](comment-example.md): the comment a pull request gets, as `costroid demo --markdown` prints it.
- [`install.md`](install.md): install the binary, check it and write the workflow.
- [`lint.md`](lint.md): judge the agent workflows a repository already has.

## Add it to a workflow

- [`recipes.md`](recipes.md): the workflow for each agent and CI system, and what each needs.
- [`existing-workflow.md`](existing-workflow.md): report from a `claude-code-action` or `openai/codex-action` workflow you already run.
- [`sdk.md`](sdk.md): record a program that runs an agent through its SDK.
- [`gh-aw.md`](gh-aw.md): read what a GitHub Agentic Workflows run cost.
- [`review.md`](review.md): post the agent's own answer beside the report.
- [`configuration.md`](configuration.md): the keys of `costroid.yml`.
- [`security.md`](security.md): what a fork's pull request and a branch can reach.

## Require it

- [`policy.md`](policy.md): hold a pull request to budgets, a ceiling and a model list.
- [`required-check.md`](required-check.md): make the `costroid` check required on a branch.
- [`approval.md`](approval.md): let a reviewer approve a failed check.
- [`gitlab.md`](gitlab.md): fail a merge request's pipeline on its cost.

## Cap a run

- [`adapters.md`](adapters.md#what-the-budget-means): how each agent is read, and what each budget grade holds.
- [`gateway.md`](gateway.md): stop a request before it is paid, with a LiteLLM gateway.

## Read a figure

- [`bases.md`](bases.md): what Measured, List-equivalent and Billed promise.
- [`catalog.md`](catalog.md): where each price comes from, and the day it was read.
- [`accuracy.md`](accuracy.md): Costroid's figures beside the provider's reports, one UTC day at a time.
- [`agents.md`](agents.md): read a run's result object from an agent or a script.

## Export

- [`export.md`](export.md): every way the rows leave Costroid, and the row contract they follow.
- [`tags.md`](tags.md): tag a run with its work item, arm or team.
- [`focus-profile.md`](focus-profile.md): write rows another FOCUS reader can merge.
- [`otlp.md`](otlp.md): send the rows as OpenTelemetry spans.
- [`connect.md`](connect.md): bind an owner to Costroid Platform.
- [`account.md`](account.md): the account page the comment links to.

## Run a ledger

- [`ledger.md`](ledger.md): host the ledger with Docker Compose or Helm.
- [`serve.md`](serve.md): what the ingest service checks, stores and answers.
- [`reconcile.md`](reconcile.md): compare a ledger with a provider's usage report.
- [`github-app.md`](github-app.md): register the GitHub App the approve button posts as.

## Verify a release

- [`verify.md`](verify.md): check a release's checksum, signature, image and provenance.

## When something fails

- [`troubleshooting.md`](troubleshooting.md): each line Costroid prints, what it means and what to do.
