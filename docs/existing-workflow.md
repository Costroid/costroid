# Report from the workflow you have

A team that already runs `claude-code-action` on its pull requests can add Costroid to that workflow without replacing it. Two blocks do it: a `read` step after your Claude step, and a `report` job that waits for your job. Your step keeps its prompt, its arguments and its review, and no new secret is needed. A workflow that runs `openai/codex-action` takes the same two blocks, with one change [below](#on-openaicodex-action).

## The two blocks

The workflow below is a review workflow as a team might already have it, with the two blocks added. Everything else is yours and stays as it is.

1. Give your Claude step an `id`, here `claude`, if it has none. The `read` step names it.
2. Add the `read` step after your Claude step, in the same job. It reads the run's `execution_file` and keeps the rows as an artifact.
3. Add the `report` job. Its `needs:` names your job, here `review`. It posts the check and the comment.

The `OTEL_RESOURCE_ATTRIBUTES` line on the Claude step is optional, and `init` does not write it. It gives Claude Code's own telemetry the pull request's join keys, the ones `costroid run` sets for the agents it starts. [`docs/adapters.md`](adapters.md#join-attributes) says what each value is. Without it, `costroid lint` prints a `join-attributes` note.

```yaml
name: review

on:
  pull_request:
    types: [opened, synchronize]

permissions: {}

jobs:
  review:
    runs-on: ubuntu-24.04
    permissions:
      contents: read
      pull-requests: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - name: Review with Claude
        id: claude
        uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        env:
          OTEL_RESOURCE_ATTRIBUTES: cicd.pipeline.run.id=${{ github.run_id }},cicd.pipeline.task.run.id=${{ github.job }},vcs.change.id=${{ github.event.pull_request.number }},vcs.ref.base.revision=${{ github.event.pull_request.base.sha }},vcs.ref.head.revision=${{ github.event.pull_request.head.sha }},vcs.repository.url.full=${{ github.server_url }}/${{ github.repository }}
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          github_token: ${{ github.token }}
          claude_args: --max-budget-usd 5
          prompt: Review this pull request.

      # Block 1: read what the run cost.
      - name: Read what the run cost
        if: always() && steps.claude.outputs.execution_file != ''
        uses: Costroid/costroid/action@e91e45780b83e9de2319e0062bf61f1b8321c5f2 # v1.0.0-alpha.21
        with:
          stage: read
          execution-file: ${{ steps.claude.outputs.execution_file }}
          billing-account: ${{ github.repository_owner }}

  # Block 2: report it on the pull request.
  report:
    needs: review
    if: always() && github.event_name == 'pull_request'
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    permissions:
      checks: write
      contents: read
      pull-requests: write
    steps:
      - uses: Costroid/costroid/action@e91e45780b83e9de2319e0062bf61f1b8321c5f2 # v1.0.0-alpha.21
        with:
          stage: report
          agent-result: ${{ needs.review.result }}
```

`costroid lint` finds nothing in this workflow, and a test holds it to that.

## What changes and what does not

Your job keeps the permissions you gave it. The `read` step runs inside it and asks for nothing more. If your job can write to the pull request, so can an injected agent, as before.

The `report` job holds `checks: write` and `pull-requests: write`, and runs nothing your pull request wrote. It reads `costroid.yml` from the base branch.

Your job wrote the rows, so the comment says the figures are unverified.

## Hold each run to a cap

`--max-budget-usd` in `claude_args` makes Claude Code stop a run once its own estimate of the run's cost passes that figure in USD. It checks between requests, so the request that crosses the cap still runs and is still paid. [`docs/adapters.md`](adapters.md#what-the-budget-means) says what each grade holds.

The `read` step cannot see your step's inputs, so it is told the cap. Give Block 1 the cap as `budget-usd`, and your step's `claude_args`, word for word, as `claude-args`. Give Block 2 the same `budget-usd`, so the comment states what is left of the cap or how far the run passed it:

```diff
       - name: Read what the run cost
         if: always() && steps.claude.outputs.execution_file != ''
         uses: Costroid/costroid/action@e91e45780b83e9de2319e0062bf61f1b8321c5f2 # v1.0.0-alpha.21
         with:
           stage: read
           execution-file: ${{ steps.claude.outputs.execution_file }}
+          budget-usd: "5"
+          claude-args: --max-budget-usd 5
           billing-account: ${{ github.repository_owner }}
 ...
       - uses: Costroid/costroid/action@e91e45780b83e9de2319e0062bf61f1b8321c5f2 # v1.0.0-alpha.21
         with:
           stage: report
+          budget-usd: "5"
           agent-result: ${{ needs.review.result }}
```

The comment's grade then reads `agent-flag` when the run stopped at that cap, or ended under it by Claude Code's own total. It reads `none`, with the reason, when `budget-usd` and the flag differ, when `claude-args` holds no `--max-budget-usd`, or when the run passed the cap without a stop. Without `budget-usd` it reads `none`, because Costroid did not start the run. `costroid init github --existing` writes these lines, and adds `--max-budget-usd 5` to `claude_args` that hold no cap. `--budget-usd` names another amount in USD, such as `--budget-usd 2.50`.

Block 2 reads the run's cap from the result object the `read` stage wrote, so it needs no `budget-usd` of its own. Give `budget-usd` to the `read` stage, where your step's `--max-budget-usd` is.

`costroid lint` prints a note for a `claude-code-action` step whose `claude_args` hold no `--max-budget-usd`.

## On `openai/codex-action`

`costroid init github --existing` writes the same two blocks into a workflow that runs `openai/codex-action`. Block 1 reads the run from the step's `codex-home` in place of an execution file, so your step needs a `codex-home` made for the job under `runner.temp`. Without one, `init` stops and says so. It gives the step the id `codex` if it has none, and Block 1 reads:

```diff
       - name: Review with Codex
         uses: openai/codex-action@bdf19a4a223ec2549a3e2274a0cf61556bc07675 # v1.13
         with:
           openai-api-key: ${{ secrets.OPENAI_API_KEY }}
           prompt: Review this pull request.
           codex-home: ${{ runner.temp }}/codex-home
+        id: codex
+      - name: Read what the run cost
+        if: always() && steps.codex.outcome != 'skipped'
+        uses: Costroid/costroid/action@e91e45780b83e9de2319e0062bf61f1b8321c5f2 # v1.0.0-alpha.21
+        with:
+          stage: read
+          codex-home: ${{ runner.temp }}/codex-home
+          billing-account: ${{ github.repository_owner }}
```

Block 2 is the `report` job above. `openai/codex-action` holds no cap Costroid can read, so the comment's grade reads `none`. The action takes only an API key, so the rows read `measured`.

The optional `OTEL_RESOURCE_ATTRIBUTES` line goes on your Codex step in the same way, under its `env`.

## Which events report

`costroid report` posts on the `pull_request` event. On any other event it posts nothing and prints one line that says why. A workflow that a `@claude` mention starts runs on `issue_comment` or another comment event, so its runs are not reported yet. `costroid lint` still judges that workflow.

A fork's pull request receives no secrets on `pull_request`, so your Claude step has no key there, and the fork's token cannot post the check. [`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) posts it from your repository. It watches the workflow named `costroid`, so put your workflow's `name` in its `workflows:` list. [`docs/recipes.md`](recipes.md#a-forks-pull-request) says more.

## On a Claude subscription

A workflow that signs Claude in with `claude_code_oauth_token` in place of `anthropic_api_key` reports the same way. The run's execution file states how it was billed in `apiKeySource`. A value that names a key reads `measured`. Any other value reads `list_equivalent`: the tokens priced at the API's list rate, which says what the run would cost on a key, and the comment prints "not billed" beside the amount. No run on a subscription's token has been recorded, so that reading is unverified. [`docs/bases.md`](bases.md) defines both bases.

`costroid lint` prints a note for a subscription's token. A note fails nothing.

## Check your workflows

```sh
costroid lint
```

[`docs/lint.md`](lint.md) lists what it checks.
