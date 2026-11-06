# Require the check

The report job posts one check run named `costroid` on the pull request's head commit. It concludes `success`, `neutral` or `failure` from the policy that `costroid.yml` sets at the base commit. [`docs/policy.md`](policy.md) says how each is reached. With no policy set, it concludes `neutral`.

GitHub counts `neutral` as passing. "Required status checks must have a `successful`, `skipped`, or `neutral` status" ([About protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches), read 2026-11-04). A required check blocks a merge only when it concludes `failure`: a run over its budget or threshold after the 7-day warn-only period. Until then, requiring it makes a merge wait until the report job has posted.

## Before you require it

A required check that never posts blocks the merge. GitHub shows "Waiting for status to be reported" ([Troubleshooting required status checks](https://docs.github.com/en/pull-requests/collaborating-with-pull-requests/collaborating-on-repositories-with-code-quality-features/troubleshooting-required-status-checks), read 2026-11-04). Two cases reach that state with the recipes:

- A fork's pull request. "The `GITHUB_TOKEN` has read-only permissions in pull requests from forked repositories" ([Events that trigger workflows](https://docs.github.com/en/actions/reference/workflows-and-actions/events-that-trigger-workflows), read 2026-11-04). The report job runs, and its post is refused with `costroid: check not posted`. No check appears, so a required `costroid` blocks every fork's pull request. A repository that takes pull requests from forks should not require it yet.
- A workflow that did not run, for example because a `paths` filter you added skipped it. Keep the recipe's triggers as they ship.

Costroid has not run on a fork's pull request yet. The first case is read from GitHub's documentation, not observed.

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
