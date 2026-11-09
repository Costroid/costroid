# Approve a failed check

A reviewer can turn a failed `costroid` check neutral by approving the pull request. The approval holds for the commit it names. A new push ends it, and the next run is checked again.

The check stays `neutral`, never `success`. The rows behind it were written by the agent's own job, so they can show that a run went over its budget, never that it stayed under. GitHub counts `neutral` as passing a required check, so an approved pull request can merge. [`docs/required-check.md`](required-check.md) says how a required check reads each conclusion.

## Add the workflow

Copy [`recipes/github-approval.yml`](../recipes/github-approval.yml) to `.github/workflows/costroid-approval.yml` on the default branch, beside the agent's recipe. It starts when a review is submitted or dismissed. Its one job installs Costroid and runs `costroid approve`. It checks out no code and runs no agent, so a review never starts a paid run.

The job holds three permissions:

| Permission | Why |
| --- | --- |
| `checks: write` | Posts the `costroid` check again |
| `contents: read` | Reads `costroid.yml` at the head of the base branch |
| `pull-requests: read` | Reads the pull request's head, its author and its reviews |

`costroid lint-recipes` holds a workflow that runs `costroid approve` to this shape under the rule `approval-code`: the `pull_request_review` event alone, no checkout, no fetch, no action on disk, no agent and no write other than `checks`. A review may start a workflow with no agent step. A review or a comment that starts an agent is refused under `agent-prompt-input`, because a reviewer's text would reach the agent.

The recipe pins the zero SHA until the first release with `costroid approve`, so it fails at start until that release is pinned.

## Which reviews count

`costroid approve` reads the pull request's reviews from GitHub's API. A review counts only when all of these hold:

- It approves. A comment, a request for changes, a pending review and a dismissed review do not.
- It names the head commit the API answers now. A review of an earlier commit does not count, and neither does one whose commit a push has since left behind.
- Its reviewer is signed in, and the login does not end in `[bot]`, as an App's account does.
- Its reviewer is not the pull request's author, unless `approval.allow_author` is `true`.
- Its reviewer can write to the repository: the collaborator permission reads `write` or `admin`.
- It was submitted less than `approval.expires_days` days ago, 7 by default.

Each reviewer's latest approval, request for changes or dismissal is their standing. A later comment leaves it as it was. When several reviewers stand on an approval, the newest that passes every rule counts.

## Settings

The keys sit in `costroid.yml` and are read at the head of the base branch, so a pull request cannot widen who approves it.

```yaml
approval:
  allow_author: false
  expires_days: 7
```

`allow_author` is `true` or `false`. `expires_days` is a whole number from 1 to 90. A key outside these two, or a value outside its range, means no approval counts until the file is fixed, and the job's log says so.

## What the check shows

When a review counts and the newest `costroid` check on the head concludes `failure`, `costroid approve` posts the check again on the same commit. The new check concludes `neutral`. Its summary names the approver, the commit and the time, and says the policy's verdict stays `failure`. Its machine block is the earlier check's result object with `conclusion` set to `neutral` and `check_run_id` naming the new check run. `policy.conclusion` still reads `failure`. The summary names the check run that carries the report in full.

When an approval is dismissed, or no longer counts, `costroid approve` posts the policy's verdict again. When the newest check already concludes what the reviews give, it posts nothing.

`costroid approve` writes no comment. The sticky comment keeps the report's verdict until the report runs again on the same head, for example when the job is re-run. That report reads the reviews too, and its comment and check open with the approval.

The result object names no approver. The check's summary is where the approval is recorded.

## The approve button

`costroid serve` takes a press of the check's `approve` button as the GitHub App's `check_run` event with the action `requested_action`, on `POST /webhooks/github`. [`docs/serve.md`](serve.md#the-github-apps-webhook) has the route.

A press counts under the rules a review counts under in `costroid approve`, for the same account, pull request and settings:

- The check's commit is the pull request's head as the API answers it now.
- The account is signed in, and its login does not end in `[bot]`.
- The account is not the pull request's author, unless `approval.allow_author` is `true`.
- The account can write to the repository: the collaborator permission reads `write` or `admin`.
- `costroid.yml` is read at the head of the base branch the event names, and a key or value it does not take means no press counts.

The press is the approval, so `approval.expires_days` does not apply to it. The App's installation must be held in the installations table and not suspended.

When a press counts, the service posts the newest `costroid` check on the head again with the conclusion `costroid approve` gives it: an approved failure concludes `neutral`, and the summary names the presser, the commit and the time. When the newest check already concludes so, it posts nothing. A press that does not count changes nothing, and the service's log says why.

This release holds no App key, so the service posts nothing yet, and the check the report posts carries no button.

## A fork's pull request

A review on a fork's pull request runs with a read-only token, so `costroid approve` posts nothing and its log says why. A fork's pull request needs no approval to merge: the follow-up concludes `neutral` at most. [`docs/required-check.md`](required-check.md#a-forks-pull-request) has the table.

## What it trusts

On this path the check is posted with the workflow's `GITHUB_TOKEN`, as the report's is. Anyone who can push a branch of the repository can edit a workflow there and post a `costroid` check with any conclusion. An approval adds nothing against that. [`docs/required-check.md`](required-check.md#what-a-required-check-holds-against) says what a required check holds against. A check that a branch cannot post needs a GitHub App, and Costroid ships none yet.

## Not yet observed

These were read from GitHub's documentation or inferred, and have not been observed on a real pull request:

- The review list's fields: `id`, `user.login`, `state` in upper case, `commit_id` and `submitted_at`. On 2026-11-07 the four pull requests of the test repository had no reviews to record.
- That `GITHUB_TOKEN` with these three permissions can read a collaborator's permission. When it cannot, no approval counts and the log says so.
- That a required check reads the newest `costroid` check run on the head commit.

## Log lines

Each line starts with `costroid:`.

| Line | Meaning |
| --- | --- |
| `check run <id>, conclusion <conclusion>` | The check was posted again with that conclusion |
| `warning: no approval counts: <reason>` | No review passed every rule. The reason names the rule the newest approval failed, or the settings or request that could not be read |
| `not posted: check run <id> already concludes <conclusion>` | The newest check already concludes what the reviews give |
| `not posted: no costroid check is on commit <commit> yet, and the report applies an approval when it posts` | The report has not posted on this head |
| `not posted: approve runs on the pull_request_review event, and this run's event is <event>` | The workflow runs on another event |
| `not posted: a review on a fork's pull request runs with a read-only token, so it cannot post the check. docs/approval.md says what a fork's pull request gets` | The pull request comes from a fork |
