# Require the check

The report job posts one check run named `costroid` on the pull request's head commit. It concludes `success`, `neutral` or `failure` from the policy that `costroid.yml` sets on the base branch, read at its head when the report runs. [`docs/policy.md`](policy.md) says how each is reached. With no policy set, it concludes `neutral`.

GitHub counts `neutral` as passing. "Required status checks must have a `successful`, `skipped`, or `neutral` status" ([About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches), read 2026-11-04). A required check blocks a merge only when it concludes `failure`: a run over its budget or threshold after the 7-day warn-only period. Until then, requiring it makes a merge wait until the report job has posted.

## Before you require it

A required check that never posts blocks the merge. GitHub shows "Waiting for status to be reported" ([Troubleshooting required status checks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks), read 2026-11-04). Two cases reach that state with the recipes:

- A fork's pull request without the follow-up. "The `GITHUB_TOKEN` has read-only permissions in pull requests from forked repositories" ([Events that trigger workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows), read 2026-11-04). The report job runs, and its post is refused with `costroid: check not posted`. No check appears, so a required `costroid` blocks every fork's pull request. Add the follow-up below before you require the check on a repository that takes pull requests from forks.
- A workflow that did not run, for example because a `paths` filter you added skipped it. Keep the recipe's triggers as they ship.

Costroid has not run on a fork's pull request yet. The first case is read from GitHub's documentation, not observed.

## A fork's pull request

[`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml) posts the check for a fork's pull request. Copy it to `.github/workflows/costroid-follow-up.yml` on the default branch, beside the agent's recipe. It starts on `workflow_run` when the workflow named `costroid` completes, and its one job runs in your repository with your repository's token.

The follow-up finds the fork's pull request by the branch the finished run names. It posts only when exactly one open pull request from that branch has the run's head commit as its head, and no branch of your repository has that commit as its head. Otherwise it posts nothing, and its log says why in a line that starts `costroid: not posted:`. A required `costroid` check then waits.

When it posts, a fork's pull request gets one `costroid` check on its head commit, and no comment:

| What the fork's run left | The check | Why |
| --- | --- | --- |
| No rows, as with every recipe as it ships | `neutral`, with the reason `fork_no_rows` at the end of its summary | The recipes run no agent on a fork's pull request, so no figure reached the check |
| Rows, because you let an agent run on forks | `neutral`, never `failure` or `success` | The fork's own run wrote the rows. A rule they fail warns, and the summary ends with the reason. An unverified row never satisfies a budget rule. [`docs/policy.md`](policy.md) has the table |
| A rows file over 8 MiB | `neutral`, with the reason `rows_file_unread` | The follow-up does not read a file past its cap |

A `neutral` check passes a required check, so a fork's pull request can merge. The check's machine block reads `no_rows_file` when no rows arrived, because the result object's list of reasons has no fork entry yet.

The follow-up reads `costroid.yml` at your default branch's latest commit, the commit it runs from. The warn-only period counts from the commit that added the agent's workflow file to that branch. A branch of your own repository gets nothing from the follow-up, because the report job in the agent's workflow posts its check.

The follow-up checks out no code and runs nothing the fork's run left behind. [`docs/security.md`](security.md#a-forks-pull-request) says how, and what a fork can still do.

The recipe pins Costroid's action to the zero SHA, because no release holds `costroid follow-up` yet, so it fails at start until a release is pinned. It has run against a stub of GitHub's API, not on a real fork's pull request. Unverified: whether GitHub runs the follow-up while a first-time contributor's workflow waits for a maintainer's approval.

## Require it

1. Copy a recipe, merge it to the default branch, then open a test pull request and wait for the `costroid` check to post once.
2. On the repository, open **Settings**. In the left sidebar, under "Code, planning, and automation", click **Rulesets**, then **Rulesets** again. Click **New ruleset**, then **New branch ruleset**.
3. Name the ruleset and set its enforcement status to **Active**.
4. Under "Target branches", click **Add a target** and include the default branch.
5. Select **Require status checks before merging**, enter `costroid`, and click the plus button to add it.
6. Click **Create**.

The steps follow [Creating rulesets for a repository](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-rulesets/creating-rulesets-for-a-repository), read 2026-11-04. A branch protection rule that requires `costroid` does the same.

The check that a job's name creates, such as `report`, is a different check. Require `costroid`, the check the job posts.

## GitLab

GitLab has no check run, and the report posts none. The `costroid:report` job is allowed to fail, so a merge request's pipeline never waits on it. There is nothing to require there.
