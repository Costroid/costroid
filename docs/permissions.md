# What Costroid reads and changes

This page lists every call Costroid makes to GitHub or GitLab under a token: a recipe job's, the GitHub App's, or the `GITHUB_TOKEN` you give a command. It also lists each `gh` and `git` command Costroid runs on your machine or a runner, most of them under your own sign-in, which no recipe or App grants. [`permissions.json`](../permissions.json) holds the same calls, commands and permissions for a program to read, and [`permissions.schema.json`](../permissions.schema.json) is its JSON Schema. A call that carries no token and runs no `gh` or `git`, such as a release download, is not listed.

A test holds the file to the code. It runs every method that reaches a host through a recording transport, and fails on a call the file does not list, on a call marked read that is not a `GET`, on a listed call no method reaches, and on a recipe or the App that asks for a permission none of its calls needs at that level, unless the file states why it is asked all the same. It reads every `gh` and remote `git` command in the code and in the scripts a release ships, and fails on one the file does not list. Which command makes each call is written by hand, and the test checks it only against the writes recorded for `costroid report`, `costroid follow-up` and `costroid approve`.

Each call's basis is the publisher's own page, read as text on the day it names and quoted in `permissions.json`, or a recorded GitHub Actions run. Unverified marks an entry that neither states. A GitHub recipe's job holds the workflow's `GITHUB_TOKEN`, an installation access token of a GitHub App, with the permissions its `permissions:` block asks for and nothing more. A job that a table does not name asks for no permission and makes no call to the host.

## Recipes

Every workflow token reads the repository's metadata without asking, so a recipe never asks for `metadata: read`. Basis: a recorded GitHub Actions run 45332227002, read 2026-11-24.

### `github-approval.yml`

| Job       | Permission                     | Can read                                                              | Can change               |
| --------- | ------------------------------ | --------------------------------------------------------------------- | ------------------------ |
| `approve` | `checks: write`                | a commit's `costroid` check runs                                      | the `costroid` check run |
| `approve` | `contents: read`               | the base branch's head commit; `costroid.yml` and the policy it names | nothing                  |
| `approve` | `metadata: read`, held unasked | whether the repository is public; an approver's role                  | nothing                  |
| `approve` | `pull-requests: read`          | a pull request's head, base and author; a pull request's reviews      | nothing                  |

### `github-claude-agent-sdk.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-claude-code-action-infrastructure.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-claude-code-action.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-claude-code-litellm.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-claude-code.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-codex-action.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-codex-sdk.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-codex.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-connect.yml`

| Job       | Permission        | Can read             | Can change |
| --------- | ----------------- | -------------------- | ---------- |
| `connect` | `id-token: write` | the job's OIDC token | nothing    |

### `github-datadog-upload.yml`

| Job      | Permission      | Can read                                        | Can change |
| -------- | --------------- | ----------------------------------------------- | ---------- |
| `upload` | `actions: read` | the repository's artifacts; an artifact's files | nothing    |

### `github-follow-up.yml`

| Job      | Permission                     | Can read                                                                                                      | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `report` | `actions: read`                | a finished run's artifacts                                                                                    | nothing                  |
| `report` | `checks: write`                | nothing                                                                                                       | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the branches a commit heads; `costroid.yml` and the policy it names | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public                                                                              | nothing                  |
| `report` | `pull-requests: read`          | the repository's pull requests                                                                                | nothing                  |

### `github-gateway.yml`

| Job     | Permission            | Can read                                                        | Can change |
| ------- | --------------------- | --------------------------------------------------------------- | ---------- |
| `mint`  | `actions: read`       | a run's artifacts; whether a run has ended; an artifact's files | nothing    |
| `mint`  | `contents: read`      | `costroid.yml` and the policy it names                          | nothing    |
| `mint`  | `id-token: write`     | the job's OIDC token                                            | nothing    |
| `mint`  | `pull-requests: read` | a pull request's head, base and author                          | nothing    |
| `close` | `id-token: write`     | the job's OIDC token                                            | nothing    |

### `github-gh-aw-follow-up.yml`

| Job      | Permission                     | Can read                                                                                                      | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `read`   | `actions: read`                | a finished run's artifacts                                                                                    | nothing                  |
| `read`   | `checks: write`                | nothing                                                                                                       | the `costroid` check run |
| `read`   | `contents: read`               | the commits that touched a workflow file; the branches a commit heads; `costroid.yml` and the policy it names | nothing                  |
| `read`   | `metadata: read`, held unasked | whether the repository is public                                                                              | nothing                  |
| `read`   | `pull-requests: read`          | the repository's pull requests                                                                                | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                          | nothing                  |

### `github-merged.yml`

| Job      | Permission            | Can read                          | Can change |
| -------- | --------------------- | --------------------------------- | ---------- |
| `merged` | `id-token: write`     | the job's OIDC token              | nothing    |
| `merged` | `pull-requests: read` | the pull requests a commit merged | nothing    |

### `github-opencode.yml`

| Job      | Permission                     | Can read                                                                                                                                                | Can change               |
| -------- | ------------------------------ | ------------------------------------------------------------------------------------------------------------------------------------------------------- | ------------------------ |
| `agent`  | `contents: read`               | the repository's code                                                                                                                                   | nothing                  |
| `ingest` | `id-token: write`              | the job's OIDC token                                                                                                                                    | nothing                  |
| `report` | `checks: write`                | a commit's `costroid` check runs                                                                                                                        | the `costroid` check run |
| `report` | `contents: read`               | the commits that touched a workflow file; the base branch's head commit; `costroid.yml` and the policy it names                                         | nothing                  |
| `report` | `metadata: read`, held unasked | whether the repository is public; an approver's role                                                                                                    | nothing                  |
| `report` | `pull-requests: write`         | the pull request's comments; a pull request's head, base and author; the repository's pull requests; a pull request's commits; a pull request's reviews | Costroid's comment       |

### `github-vantage-upload.yml`

| Job      | Permission      | Can read                                        | Can change |
| -------- | --------------- | ----------------------------------------------- | ---------- |
| `upload` | `actions: read` | the repository's artifacts; an artifact's files | nothing    |

### `gitlab-ci.yml`

| Job               | Permission  | Can read                                                                                                                                              | Can change      |
| ----------------- | ----------- | ----------------------------------------------------------------------------------------------------------------------------------------------------- | --------------- |
| `costroid:report` | `api` scope | the token's own user; the merge request's notes; the base branch's head commit; `costroid.yml` and the policy it names; whether the project is public | Costroid's note |

The `costroid:report` job's token is `COSTROID_GITLAB_TOKEN`, and its user holds the Reporter role.

## The GitHub App

| Permission            | Can read                                                              | Can change               |
| --------------------- | --------------------------------------------------------------------- | ------------------------ |
| `checks: write`       | a commit's `costroid` check runs                                      | the `costroid` check run |
| `contents: read`      | the base branch's head commit; `costroid.yml` and the policy it names | nothing                  |
| `metadata: read`      | whether the repository is public; an approver's role                  | nothing                  |
| `pull-requests: read` | a pull request's head, base and author                                | nothing                  |

## Every call

| Call                                                                    | Host   | Needs                      | Made by                                                                                                                                        | Why                                                                                                                                | Basis                                                                                                                        |
| ----------------------------------------------------------------------- | ------ | -------------------------- | ---------------------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------------- | ---------------------------------------------------------------------------------------------------------------------------- |
| `POST /repos/{owner}/{repo}/check-runs`                                 | GitHub | `checks: write`            | `costroid report`, `costroid follow-up`, `costroid approve`, `costroid serve`                                                                  | Posts the `costroid` check on the commit the report is about.                                                                      | [GitHub](https://docs.github.com/en/rest/checks/runs#create-a-check-run), read 2026-11-24                                    |
| `PATCH /repos/{owner}/{repo}/check-runs/{check_run_id}`                 | GitHub | `checks: write`            | `costroid report`, `costroid follow-up`, `costroid approve`, `costroid serve`                                                                  | Rewrites the output of a check run the same token posted, to name the check run's own id in it.                                    | [GitHub](https://docs.github.com/en/rest/checks/runs#update-a-check-run), read 2026-11-24                                    |
| `GET /repos/{owner}/{repo}/commits/{ref}/check-runs`                    | GitHub | `checks: read`             | `costroid report`, `costroid approve`, `costroid serve`, `costroid result`                                                                     | Reads the results earlier reports posted, for the pull request's total, a baseline and an approval.                                | [GitHub](https://docs.github.com/en/rest/checks/runs#list-check-runs-for-a-git-reference), read 2026-11-24                   |
| `GET /repos/{owner}/{repo}/issues/{issue_number}/comments`              | GitHub | `pull-requests: read`      | `costroid report`, `costroid review --post`                                                                                                    | Finds the comment Costroid posted earlier, so it is edited and never posted twice, and the author's receipt where receipts are on. | [GitHub](https://docs.github.com/en/rest/issues/comments#list-issue-comments), read 2026-11-24                               |
| `POST /repos/{owner}/{repo}/issues/{issue_number}/comments`             | GitHub | `pull-requests: write`     | `costroid report`, `costroid review --post`                                                                                                    | Posts Costroid's comment on the pull request when it holds none.                                                                   | [GitHub](https://docs.github.com/en/rest/issues/comments#create-an-issue-comment), read 2026-11-24                           |
| `PATCH /repos/{owner}/{repo}/issues/comments/{comment_id}`              | GitHub | `pull-requests: write`     | `costroid report`, `costroid review --post`                                                                                                    | Edits the comment the same account posted earlier, and no other.                                                                   | [GitHub](https://docs.github.com/en/rest/issues/comments#update-an-issue-comment), read 2026-11-24                           |
| `GET /repos/{owner}/{repo}/pulls/{pull_number}`                         | GitHub | `pull-requests: read`      | `costroid report`, `costroid approve`, `costroid serve`, `costroid result`, `costroid gateway mint`                                            | Reads the pull request's head commit, base branch and author as GitHub states them, for an approval, a receipt and a key's budget. | [GitHub](https://docs.github.com/en/rest/pulls/pulls#get-a-pull-request), read 2026-11-24                                    |
| `GET /repos/{owner}/{repo}/pulls`                                       | GitHub | `pull-requests: read`      | `costroid report`, `costroid follow-up`                                                                                                        | Finds the open pull request a finished run's branch heads, and the pull requests merged into the base branch for a baseline.       | [GitHub](https://docs.github.com/en/rest/pulls/pulls#list-pull-requests), read 2026-11-24                                    |
| `GET /repos/{owner}/{repo}/pulls/{pull_number}/commits`                 | GitHub | `pull-requests: read`      | `costroid report`                                                                                                                              | Lists the pull request's commits, to add up the runs reported on each.                                                             | [GitHub](https://docs.github.com/en/rest/pulls/pulls#list-commits-on-a-pull-request), read 2026-11-24                        |
| `GET /repos/{owner}/{repo}/pulls/{pull_number}/reviews`                 | GitHub | `pull-requests: read`      | `costroid report`, `costroid approve`                                                                                                          | Reads the reviews, to find an approval that turns a failed check neutral.                                                          | [GitHub](https://docs.github.com/en/rest/pulls/reviews#list-reviews-for-a-pull-request), read 2026-11-24                     |
| `GET /repos/{owner}/{repo}/commits/{commit_sha}/pulls`                  | GitHub | `pull-requests: read`      | `costroid merged`                                                                                                                              | Reads which merged pull requests each pushed commit closed.                                                                        | a recorded GitHub Actions run 45330801950, read 2026-11-24                                                                   |
| `GET /repos/{owner}/{repo}/commits`                                     | GitHub | `contents: read`           | `costroid report`, `costroid follow-up`                                                                                                        | Reads when the workflow file reached the base branch, which starts the warn-only period.                                           | [GitHub](https://docs.github.com/en/rest/commits/commits#list-commits), read 2026-11-24                                      |
| `GET /repos/{owner}/{repo}/git/ref/heads/{+branch}`                     | GitHub | `contents: read`           | `costroid report`, `costroid approve`, `costroid serve`                                                                                        | Reads the commit at the head of the base branch, where `costroid.yml` is read.                                                     | [GitHub](https://docs.github.com/en/rest/git/refs#get-a-reference), read 2026-11-24                                          |
| `GET /repos/{owner}/{repo}/commits/{commit_sha}/branches-where-head`    | GitHub | `contents: read`           | `costroid follow-up`                                                                                                                           | Refuses a fork's check on a commit that heads a branch of this repository.                                                         | [GitHub](https://docs.github.com/en/rest/commits/commits#list-branches-for-head-commit), read 2026-11-24                     |
| `GET /repos/{owner}/{repo}/contents/{+path}`                            | GitHub | `contents: read`           | `costroid report`, `costroid follow-up`, `costroid approve`, `costroid serve`, `costroid gateway mint`                                         | Reads `costroid.yml`, and the policy pack it names, at the base branch's commit, which the pull request cannot change.             | [GitHub](https://docs.github.com/en/rest/repos/contents#get-repository-content), read 2026-11-24                             |
| `GET /repos/{owner}/{repo}`                                             | GitHub | `metadata: read`           | `costroid report`, `costroid follow-up`, `costroid approve`, `costroid serve`                                                                  | Reads whether the repository is public, which decides whether the comment shows amounts.                                           | [GitHub](https://docs.github.com/en/rest/repos/repos#get-a-repository), read 2026-11-24                                      |
| `GET /repos/{owner}/{repo}/collaborators/{username}/permission`         | GitHub | `metadata: read`           | `costroid report`, `costroid approve`, `costroid serve`                                                                                        | Reads whether the person who approved holds a write role, the only role whose approval counts.                                     | [GitHub](https://docs.github.com/en/rest/collaborators/collaborators#get-repository-permissions-for-a-user), read 2026-11-24 |
| `POST /app/installations/{installation_id}/access_tokens`               | GitHub | the App's own signed token | `costroid serve`                                                                                                                               | Mints a token for one installation under the App's own signed token, with the permissions listed for the App and no others.        | [GitHub](https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app), read 2026-11-24          |
| `GET /repos/{owner}/{repo}/actions/runs/{run_id}/artifacts`             | GitHub | `actions: read`            | `costroid gateway mint`                                                                                                                        | Finds the sealed request the agent's run uploaded.                                                                                 | [GitHub](https://docs.github.com/en/rest/actions/artifacts#list-workflow-run-artifacts), read 2026-11-24                     |
| `GET /repos/{owner}/{repo}/actions/runs/{run_id}`                       | GitHub | `actions: read`            | `costroid gateway mint`                                                                                                                        | Reads whether the agent's run has ended, so mint stops waiting for a request that will not come.                                   | [GitHub](https://docs.github.com/en/rest/actions/workflow-runs#get-a-workflow-run), read 2026-11-24                          |
| `GET /repos/{owner}/{repo}/actions/artifacts`                           | GitHub | `actions: read`            | `gh api`                                                                                                                                       | Lists the rows artifacts the day's runs kept.                                                                                      | [GitHub](https://docs.github.com/en/rest/actions/artifacts#list-artifacts-for-a-repository), read 2026-11-24                 |
| `GET /repos/{owner}/{repo}/actions/artifacts/{artifact_id}/zip`         | GitHub | `actions: read`            | `costroid gateway mint`, `gh api`                                                                                                              | Downloads an artifact: the agent's sealed request, or a day's rows for an upload.                                                  | [GitHub](https://docs.github.com/en/rest/actions/artifacts#download-an-artifact), read 2026-11-24                            |
| `GET $ACTIONS_ID_TOKEN_REQUEST_URL`                                     | GitHub | `id-token: write`          | `costroid merged`, `costroid connect`, `costroid gateway mint`, `costroid gateway close`, `costroid export --platform-url`, `action/ingest.sh` | Asks GitHub Actions for the job's OIDC token, which names the run to a ledger or to Costroid Platform.                             | [GitHub](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax#permissions), read 2026-11-24    |
| `GET /user`                                                             | GitLab | `read_api` scope           | `costroid report`, `costroid review --post`                                                                                                    | Reads the token's own user, so only a note that user wrote is ever edited.                                                         | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `GET /projects/{id}/merge_requests/{merge_request_iid}/notes`           | GitLab | `read_api` scope           | `costroid report`, `costroid review --post`                                                                                                    | Finds the note Costroid posted earlier, so it is edited and never posted twice.                                                    | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `POST /projects/{id}/merge_requests/{merge_request_iid}/notes`          | GitLab | `api` scope                | `costroid report`, `costroid review --post`                                                                                                    | Posts Costroid's note on the merge request when it holds none.                                                                     | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `PUT /projects/{id}/merge_requests/{merge_request_iid}/notes/{note_id}` | GitLab | `api` scope                | `costroid report`, `costroid review --post`                                                                                                    | Edits the note the same user posted earlier, and no other.                                                                         | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `GET /projects/{id}/repository/branches/{branch}`                       | GitLab | `read_api` scope           | `costroid report`                                                                                                                              | Reads the commit at the head of the base branch, where `costroid.yml` is read.                                                     | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `GET /projects/{id}/repository/files/{file_path}`                       | GitLab | `read_api` scope           | `costroid report`                                                                                                                              | Reads `costroid.yml`, and the policy pack it names, at the merge request's base commit.                                            | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |
| `GET /projects/{id}`                                                    | GitLab | `read_api` scope           | `costroid report`                                                                                                                              | Reads whether the project is public, which decides whether the note shows amounts.                                                 | [GitLab](https://docs.gitlab.com/security/tokens/access_token_scopes/), read 2026-11-24                                      |

## Steps of other publishers

| Step                                            | Needs            | Why                                                                                   | Basis                                                                                                                                                     |
| ----------------------------------------------- | ---------------- | ------------------------------------------------------------------------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `actions/checkout`                              | `contents: read` | Checks out the code the agent works on.                                               | unverified. Another publisher's step, whose needs no page this file cites states.                                                                         |
| `actions/download-artifact` with `github-token` | `actions: read`  | Downloads what another run kept, which the job's token reads only with actions: read. | unverified. Another publisher's step, whose needs no page this file cites states. The artifact calls it makes need actions: read, as the calls above say. |

## Through gh and git

| Command                       | Signed in | Can read                                                      | Can change                                      | Made by                                 | Why                                                                                                                       |
| ----------------------------- | --------- | ------------------------------------------------------------- | ----------------------------------------------- | --------------------------------------- | ------------------------------------------------------------------------------------------------------------------------- |
| `gh secret list`              | as you    | the names of the repository's secrets and of an environment's | nothing                                         | `costroid doctor`, `costroid init --pr` | Reads names alone: doctor checks the secrets a workflow needs, and init --pr whether the key's secret is set already.     |
| `gh variable list`            | as you    | the names of the repository's variables                       | nothing                                         | `costroid doctor`                       | Reads names alone, to check the variables the ingest job needs.                                                           |
| `gh repo view`                | as you    | whether the repository is private                             | nothing                                         | `costroid doctor`                       | Says whether the comment will show amounts.                                                                               |
| `gh api repos/{owner}/{repo}` | as you    | the repository owner's account id                             | nothing                                         | `costroid doctor`                       | Names the installation a CI job of this repository proves.                                                                |
| `gh secret set`               | as you    | nothing                                                       | a repository secret                             | `costroid init --pr`                    | Stores the provider key the workflow reads, when the repository holds none.                                               |
| `gh variable set`             | as you    | nothing                                                       | the repository variable `COSTROID_PLATFORM_URL` | `costroid init --pr`                    | Points the workflow at Costroid Platform, when `--platform-url` is given.                                                 |
| `git push`                    | as you    | nothing                                                       | a new branch                                    | `costroid init --pr`                    | Pushes the branch that holds the workflow init wrote.                                                                     |
| `gh pr create`                | as you    | nothing                                                       | a pull request                                  | `costroid init --pr`                    | Opens the pull request that adds the workflow.                                                                            |
| `gh run list`                 | as you    | the pull request's workflow runs                              | nothing                                         | `costroid init --pr`                    | Finds the run the new pull request started.                                                                               |
| `gh run watch`                | as you    | one workflow run                                              | nothing                                         | `costroid init --pr`                    | Waits for that run to end.                                                                                                |
| `git ls-remote`               | no        | the release tags of Costroid's own public repository          | nothing                                         | `costroid init`, `action/install.sh`    | Reads which commit a release tag names: init pins the workflow to it, and the action finds the release its own commit is. |
| `gh pr view`                  | as you    | the pull request's comments                                   | nothing                                         | `costroid init --pr`                    | Finds the comment the run posted.                                                                                         |

## Sources

- GitHub, [GITHUB_TOKEN](https://docs.github.com/en/actions/concepts/security/github_token), read on 2026-11-24: "The `GITHUB_TOKEN` secret is a GitHub App installation access token."
- GitHub's REST reference, read on 2026-11-24: each GitHub call's basis links the endpoint's own page, and `permissions.json` quotes what its "Fine-grained access tokens" section says. Where it reads "at least one of the following permission sets", the call states the set the recipes grant.
- A recorded GitHub Actions run 45330801950, on 2026-11-24, listed a commit's pull requests (`GET /repos/{owner}/{repo}/commits/{commit_sha}/pulls`) under three job tokens. With `pull-requests: read` alone it answered. With `contents: read` alone, and with neither, GitHub answered 403 "Resource not accessible by integration".
- A recorded GitHub Actions run 45332227002, on 2026-11-24, read `GET /repos/{owner}/{repo}` and its visibility with the job's token under four permission sets, and each answered `private`: `contents: read` alone, `pull-requests: read` alone, `id-token: write` alone, and no permission at all, the workflow's `permissions: {}` with none on the job.
- GitHub, [Apps](https://docs.github.com/en/rest/apps/apps), read on 2026-11-24: of an installation access token, "You must use a JWT to access this endpoint."
- GitHub, [Workflow syntax](https://docs.github.com/en/actions/reference/workflows-and-actions/workflow-syntax), read on 2026-11-24: `id-token` is to "Fetch an OpenID Connect (OIDC) token. This requires `id-token: write`."
- GitLab, [Access token scopes](https://docs.gitlab.com/security/tokens/access_token_scopes/), read on 2026-11-24: `read_api` "Grants read access to the API for the token’s scope", and `api` "Grants complete read and write access to the API for the token’s scope."
- GitLab, [Roles and permissions](https://docs.gitlab.com/user/permissions/), read November 1, 2026: the Reporter role is enough to comment on a merge request and read the code.
