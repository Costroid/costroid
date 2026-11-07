# Costroid cost check

Costroid is the cost check for pull requests. It reads what a coding agent's run cost, or runs the agent under a budget, records the cost per token class as FOCUS rows and posts the figure on the pull request. This action is `Costroid/costroid/action` at the commit of `__RELEASE_TAG__`, with the same inputs.

## Use it with claude-code-action

Save [the claude-code-action recipe at `__RELEASE_TAG__`](https://github.com/Costroid/costroid/blob/__RELEASE_TAG__/recipes/github-claude-code-action.yml) as `.github/workflows/costroid.yml`. It keeps your `claude-code-action` step and calls Costroid's action in three jobs. Point those three steps at this repository, pinned to the full commit SHA of its `__RELEASE_TAG__` tag:

```yaml
jobs:
  agent:
    steps:
      # The recipe's checkout, Claude Code install and claude-code-action step come first.
      - name: Read what the run cost
        if: always() && steps.claude.outputs.execution_file != ''
        uses: Costroid/costroid-action@<commit> # __RELEASE_TAG__
        with:
          stage: read
          minimum-agent-version: 2.1.316
          execution-file: ${{ steps.claude.outputs.execution_file }}
          billing-account: ${{ github.repository_owner }}
          credential: ${{ vars.COSTROID_CREDENTIAL }}

  ingest:
    steps:
      - uses: Costroid/costroid-action@<commit> # __RELEASE_TAG__
        with:
          stage: ingest
          ingest-url: ${{ vars.COSTROID_INGEST_URL }}
          ingest-audience: ${{ vars.COSTROID_INGEST_AUDIENCE }}

  report:
    steps:
      - uses: Costroid/costroid-action@<commit> # __RELEASE_TAG__
        with:
          stage: report
          agent-result: ${{ needs.agent.result }}
```

Set one secret on the repository, `ANTHROPIC_API_KEY`, the provider key. The action installs the Costroid release at its own commit and checks it by its signature, so it needs no version, no checksum and no account. Then open a test pull request: the `report` job posts the `costroid` check and the comment.

## Everything else

The other recipes, the configuration, each line of the job log and the source live in [Costroid/costroid](https://github.com/Costroid/costroid). Report problems there.
