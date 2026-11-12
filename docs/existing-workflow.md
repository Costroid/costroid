# Report from the workflow you have

A team that already runs `claude-code-action` on its pull requests can add Costroid to that workflow without replacing it. Two blocks do it: a `read` step after your Claude step, and a `report` job that waits for your job. Your step keeps its prompt, its arguments and its review, and no new secret is needed. A workflow that runs `openai/codex-action` takes the same two blocks, with one change [below](#on-openaicodex-action).

## The two blocks

`costroid init github --existing` prints a patch that adds them. The workflow below is a review workflow as a team might already have it, after that patch. Every line above `id: claude` is the team's, and stays as it is.

1. Your Claude step gets an `id`, here `claude`, if it has none. The `read` step names it.
2. The `read` step follows your Claude step, in the same job. It reads the run's `execution_file` and keeps the rows as an artifact.
3. The `report` job's `needs:` names your job, here `review`. It posts the check and the comment.

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
        uses: anthropics/claude-code-action@1d6de8cb0c237e7c15e9e1bdf973826ebae490cc # v1.0.248
        env:
          OTEL_RESOURCE_ATTRIBUTES: cicd.pipeline.run.id=${{ github.run_id }},cicd.pipeline.task.run.id=${{ github.job }},vcs.change.id=${{ github.event.pull_request.number }},vcs.ref.base.revision=${{ github.event.pull_request.base.sha }},vcs.ref.head.revision=${{ github.event.pull_request.head.sha }},vcs.repository.url.full=${{ github.server_url }}/${{ github.repository }}
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          github_token: ${{ github.token }}
          claude_args: --max-budget-usd 5
          prompt: Review this pull request.
        id: claude
      - name: Read what the run cost
        if: always() && steps.claude.outputs.execution_file != ''
        uses: Costroid/costroid/action@61a5b2e3836a940838e127311ed15f3087f3d6d5 # v1.0.0-alpha.23
        with:
          stage: read
          execution-file: ${{ steps.claude.outputs.execution_file }}
          budget-usd: "5"
          claude-args: --max-budget-usd 5
          billing-account: ${{ github.repository_owner }}

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
      - uses: Costroid/costroid/action@61a5b2e3836a940838e127311ed15f3087f3d6d5 # v1.0.0-alpha.23
        with:
          stage: report
          budget-usd: "5"
          agent-result: ${{ needs.review.result }}
```

`costroid lint` finds nothing in this workflow. A test runs `init --existing` on the lines above `id: claude`, applies its patch and gets this workflow back, byte for byte.

## What changes and what does not

Your job keeps the permissions you gave it. The `read` step runs inside it and asks for nothing more. If your job can write to the pull request, so can an injected agent, as before.

The `report` job holds `checks: write` and `pull-requests: write`, and runs nothing your pull request wrote. It reads `costroid.yml` from the base branch.

Your job wrote the rows, so the comment says the figures are unverified.

## Hold each run to a cap

`--max-budget-usd` in `claude_args` makes Claude Code stop a run once its own estimate of the run's cost passes that figure in USD. It checks between requests, so the request that crosses the cap still runs and is still paid. [`docs/adapters.md`](adapters.md#what-the-budget-means) says what each grade holds.

The `read` step cannot see your step's inputs, so the patch tells it the cap: `budget-usd` is the cap in USD, and `claude-args` is your step's `claude_args`, word for word. The step runs `costroid ingest-run --from execution-file=F --cap-usd N --claude-args TEXT`, which checks the run against that cap and writes it into the run's result object. The `report` job reads the cap from that object. Its own `budget-usd` is read only when the `read` step left no result object.

`init` writes the same cap in four places: `claude_args`, `claude-args` and both `budget-usd` inputs. To change the cap, change all four. It adds `--max-budget-usd 5` to `claude_args` that hold no cap, and `--budget-usd` names another amount in USD, such as `--budget-usd 2.50`. When it cannot read `claude_args`, it leaves them as they are and the patch states no cap: add the four lines by hand, as the example above shows them.

The comment's grade then reads `agent-flag` when the run stopped at that cap, or ended under it by Claude Code's own total. It reads `none`, with the reason, when `budget-usd` and the flag differ, when `claude-args` holds no `--max-budget-usd`, or when the run passed the cap without a stop. Without `budget-usd` it reads `none`, because Costroid did not start the run.

`costroid lint` prints a note for a `claude-code-action` step whose `claude_args` hold no `--max-budget-usd`.

## On `openai/codex-action`

`costroid init github --existing` writes the same two blocks into a workflow that runs `openai/codex-action`. The `read` step reads the run from the step's `codex-home` in place of an execution file, so your step needs a `codex-home` made for the job under `runner.temp`. Without one, `init` stops and says so. It gives the step the id `codex` if it has none, and the `read` step reads:

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
+        uses: Costroid/costroid/action@61a5b2e3836a940838e127311ed15f3087f3d6d5 # v1.0.0-alpha.23
+        with:
+          stage: read
+          codex-home: ${{ runner.temp }}/codex-home
+          billing-account: ${{ github.repository_owner }}
```

The step runs `costroid ingest-run --from codex-home=D --since T`, where `T` is when the job started, so a file an earlier job left in that directory is not read. The `report` job is the one above. `openai/codex-action` holds no cap Costroid can read, so the comment's grade reads `none`. The action takes only an API key, so the rows read `measured`.

The optional `OTEL_RESOURCE_ATTRIBUTES` line goes on your Codex step in the same way, under its `env`.

## Which events report

`costroid report` posts on the `pull_request` event. On any other event it posts nothing and prints one line that says why. A workflow that a `@claude` mention starts runs on `issue_comment` or another comment event, so its runs are not reported yet. `costroid lint` still judges that workflow.

A fork's pull request receives no secrets on `pull_request`, so your Claude step has no key there, and the fork's token cannot post the check. [`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) posts it from your repository. It watches the workflow named `costroid`, so put your workflow's `name` in its `workflows:` list. [`docs/recipes.md`](recipes.md#a-forks-pull-request) says more.

## On a Claude subscription

A workflow that signs Claude in with `claude_code_oauth_token` in place of `anthropic_api_key` reports the same way. The run's execution file states how it was billed in `apiKeySource`. A value that names a key reads `measured`. Any other value reads `list_equivalent`: the tokens priced at the API's list rate, which says what the run would cost on a key, and the comment prints "not billed" beside the amount. A recorded run on a subscription's token, `claude-code-action` v1.0.248 on 2026-11-11, reads `list_equivalent`, and the comment says so beside its figure ([`docs/adapters.md`](adapters.md)). [`docs/bases.md`](bases.md) defines both bases.

`costroid lint` prints a note for a subscription's token. A note fails nothing.

## Check your workflows

```sh
costroid lint
```

[`docs/lint.md`](lint.md) lists what it checks.
