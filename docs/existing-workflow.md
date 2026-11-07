# Report from the workflow you have

A team that already runs `claude-code-action` on its pull requests can add Costroid to that workflow without replacing it. Two blocks do it: a `read` step after your Claude step, and a `report` job that waits for your job. Your step keeps its prompt, its arguments and its review, and no new secret is needed.

## The two blocks

The workflow below is a review workflow as a team might already have it, with the two blocks added. Everything else is yours and stays as it is.

1. Give your Claude step an `id`, here `claude`, if it has none. The `read` step names it.
2. Add the `read` step after your Claude step, in the same job. It reads the run's `execution_file` and keeps the rows as an artifact.
3. Add the `report` job. Its `needs:` names your job, here `review`. It posts the check and the comment.

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
        with:
          anthropic_api_key: ${{ secrets.ANTHROPIC_API_KEY }}
          github_token: ${{ github.token }}
          prompt: Review this pull request.

      # Block 1: read what the run cost.
      - name: Read what the run cost
        if: always() && steps.claude.outputs.execution_file != ''
        uses: Costroid/costroid/action@a4db648a1ad602760e88277f4875b55d08ea73ef # v1.0.0-alpha.9
        with:
          stage: read
          execution-file: ${{ steps.claude.outputs.execution_file }}
          billing-account: ${{ github.repository_owner }}

  # Block 2: report it on the pull request.
  report:
    needs: review
    if: always()
    runs-on: ubuntu-24.04
    timeout-minutes: 5
    permissions:
      checks: write
      contents: read
      pull-requests: write
    steps:
      - uses: Costroid/costroid/action@a4db648a1ad602760e88277f4875b55d08ea73ef # v1.0.0-alpha.9
        with:
          stage: report
          agent-result: ${{ needs.review.result }}
```

`costroid lint` finds nothing in this workflow, and a test holds it to that.

## What changes and what does not

Your job keeps the permissions you gave it. The `read` step runs inside it and asks for nothing more. If your job can write to the pull request, so can an injected agent, as before.

The `report` job holds `checks: write` and `pull-requests: write`, and runs nothing your pull request wrote. It reads `costroid.yml` from the base branch.

Costroid did not start the run, so no budget held it, and the comment's grade reads `none`. Your job wrote the rows, so the comment says the figures are unverified.

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
