# Fail a merge request's pipeline on its cost

On GitLab, `costroid report` posts one note on the merge request and no check. With the gate on, the report's exit code carries the verdict, and a run that breaks the policy on the base branch fails the pipeline. The gate is off until you set one variable. [`docs/recipes.md`](recipes.md#gitlab-ci) sets up the template first.

## Turn it on

1. On the base branch, set the budget and end the warn-only period in `costroid.yml`:

   ```yaml
   policy:
     run_budget_usd: "5"
     enforce: true
   ```

   On GitLab the report cannot read when the template reached the base branch, so without `enforce: true` the warn-only period never ends, and a run over its budget exits 3, not 1.

2. Set the CI/CD variable `COSTROID_GATE` to `true` on the project. Any value that is not empty turns the gate on. Leave it unprotected: a protected variable reaches a merge request pipeline only when [both branches are protected](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/) (read 2026-11-01), so an ordinary branch would run without the gate.

3. Turn on **Pipelines must succeed** under **Settings > Merge requests > Merge checks**. Without it, a failed pipeline does not stop the merge. "The setting requires the existence of a successful pipeline, not the absence of failed pipelines" ([Auto-merge](https://docs.gitlab.com/user/project/merge_requests/auto_merge/), read 2026-11-07).

With `COSTROID_GATE` set, the template's `costroid:report` job runs `costroid report --exit-on-verdict`. Without it, the job runs without the flag, is allowed to fail on any exit code, and never fails the pipeline.

## Exit codes

| Verdict   | Exit code | The pipeline                           |
| --------- | --------- | -------------------------------------- |
| `success` | 0         | Passes                                 |
| `neutral` | 3         | Passes, and the job shows a warning    |
| `failure` | 1         | Fails                                  |

The report's last line names both: `costroid: the verdict is failure, so the report exits 1`.

The template sets `allow_failure: exit_codes: [3]` on the report's job. GitLab's rule: "The job is `allow_failure: true` for any of the listed exit codes, and `allow_failure` false for any other exit code" ([`allow_failure:exit_codes`](https://docs.gitlab.com/ci/yaml/#allow_failureexit_codes), read 2026-11-07). The rule without the gate sets `allow_failure: true`, and "the rule-level `rules:allow_failure` overrides the job-level `allow_failure`" ([`rules:allow_failure`](https://docs.gitlab.com/ci/yaml/#rulesallow_failure), read 2026-11-07). `costroid lint-recipes` refuses any list but `[3]` on the report's job, because a 1 in it would let a failure verdict pass as a warning.

Any other exit code fails the pipeline while the gate is on. The template's install step exits 1 when it cannot download the release or the signature does not verify, and a flag the report cannot read exits 125.

## What concludes neutral

- A rule that fails while the warn-only period runs. Step 1 ends the period.
- A run within every limit. The agent's job wrote the rows, and a figure from them can show that a run went over a limit, never that it stayed under one. So with the template a run within its budget concludes `neutral`, not `success`, and its job shows a warning. [`docs/policy.md`](policy.md#from-the-rules-to-the-conclusion) has the table.
- No rule set in `costroid.yml`.
- A report that could not reach GitLab: no token, no merge request, or a fork's pipeline. It posts nothing, has no verdict, and exits 3. A line above the last one says why.

The verdict does not wait on the note. When GitLab refuses the note, the report still exits on its verdict.

## What a merge request can change

The report reads `costroid.yml` at the head of the base branch when it runs, through GitLab's API, never at the merge request's head. A merge request that raises its own budget or removes `policy.enforce` does not change its own verdict. [`docs/policy.md`](policy.md#settings-come-from-the-base-branch) says how the head is found.

The pipeline's definition is another matter. A merge request pipeline runs "on the contents of the source branch only and ignore[s] the content of the target branch" ([Merge request pipelines](https://docs.gitlab.com/ci/pipelines/merge_request_pipelines/), read 2026-11-07). A merge request that edits `.gitlab-ci.yml` or the template it includes can drop the report's job or its exit code. Read every change to those files before you merge it.

## GitHub

GitHub's `costroid` check concludes `failure` on its own, so `--exit-on-verdict` does nothing there. The report warns and exits 0. [`docs/required-check.md`](required-check.md) says how to require the check.

## Tested so far

The exit codes and the template's rules have run against a stub of GitLab's API, not a real GitLab instance. Unverified: that a failed job in the `.post` stage fails the pipeline and blocks the merge. GitLab's page says "If any job fails, the pipeline is marked as `failed`" ([CI/CD YAML syntax](https://docs.gitlab.com/ci/yaml/), read 2026-11-07), and does not name `.post`.
