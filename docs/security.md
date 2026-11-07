# Security of the recipes

## A fork's pull request

A fork's pull request runs the agent's workflow from the pull request's own branch, so its author can edit that workflow. GitHub gives that run a read-only token, so its report job cannot post the `costroid` check. The follow-up, [`recipes/github-follow-up.yml`](../recipes/github-follow-up.yml), posts it instead. It holds your repository's token, and it never meets the pull request's code:

- It starts on `workflow_run` from the file on your default branch. Nothing in the pull request changes what it runs.
- It checks out nothing and fetches no code.
- It reads the head commit from the event file GitHub writes, never from the fork's run.
- It downloads the rows the fork's run kept into a directory of its own. `costroid follow-up` reads one file there as data: at most 8 MiB, each string through the allowlist and length cap the comment applies, and every one rendered as code, never as Markdown or HTML. No step runs a file from that directory.
- It reads `costroid.yml` at your default branch's commit, never at the head.
- Its job holds `actions: read`, `checks: write`, `contents: read` and `pull-requests: read`. It holds no provider key and no id token, and it runs no agent.
- It finds the fork's pull request by the branch the finished run names, and posts only when exactly one open pull request from that branch has the head commit as its head. A head commit that is also the head of a branch of your repository gets nothing. So does a run whose pull requests or branches it cannot read.
- It posts the check on the head commit and nothing else. It posts no comment. The check concludes `neutral` at most.

Three of these rest on GitHub's behaviour and are unverified on a real fork's pull request: that `workflow_run` runs the file on the default branch, that its token can post a check on a fork's head commit, and that GitHub's list of open pull requests, filtered by the fork's owner and branch, names the fork's pull request.

`costroid lint-recipes` refuses a `workflow_run` workflow that checks out code, names the pull request's head in a step, fetches code in a script, runs an agent, uses an action from disk, downloads an artifact without a directory of its own, or names that directory anywhere but `costroid follow-up --rows`. It also refuses a follow-up job that writes anything but checks, and one that runs `costroid follow-up` without `pull-requests: read`.

A fork can still write any rows it likes into its own run. Those rows are unverified, and the check concludes `neutral` at most, so they never block a merge and never pass a budget rule. It can name its own workflow `costroid` to start the follow-up, which then posts the same kind of check on its own head commit. It can push a commit that heads a branch of your repository to a branch of its own, and the follow-up then posts nothing. It can push the head commit of another fork's pull request to a branch of its own, and the follow-up then posts a `neutral` check on that commit. It can upload a large artifact. The follow-up downloads it, refuses a rows file past 8 MiB, and stops at the job's 5-minute timeout.
