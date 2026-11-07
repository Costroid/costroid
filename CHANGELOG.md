# Changelog

Each release is dated by its tag, in UTC. Every release so far is a pre-release. [`docs/verify.md`](docs/verify.md) checks one.

## 1.0.0-alpha.9 (2026-11-07)

### Breaking changes

- The action's `stage` input has no default. A step that leaves it out fails at install and lists the six stages, one line each. It used to read `all`.
- The gateway's sealing key pair is drawn at random. Run `costroid gateway keygen`, store the private half as the secret `COSTROID_GATEWAY_SEALING_KEY` of the `costroid-gateway` environment and the public half as the variable `COSTROID_GATEWAY_PUBLIC_KEY`, then rotate the admin key. `costroid gateway mint` refuses a request sealed to a key derived from the admin key, as earlier releases derived it. [`docs/gateway.md`](docs/gateway.md) has the steps.
- The gateway's URL must be `https`, except on `localhost` and `127.0.0.1`.
- The fork follow-up's job needs `pull-requests: read`. `costroid lint-recipes` refuses a follow-up job without it, and one that writes anything but checks.
- `costroid lint-recipes` refuses three more shapes: a checkout in an agent's job without `persist-credentials: false`, a `download-artifact` step that unpacks into `runner.temp` itself or above it, and a job that holds the gateway's admin key or sealing key without naming an environment.
- The result object is `costroid.result/0.5`. It adds the reason `agent_skipped`, the run reasons `reader_refused` and `agent_error`, and the fields `reader_error`, `agent_error`, `agent_skipped` and `ci_run`. Each new field is present, and null until something states it.
- On GitHub, `costroid report` posts on the `pull_request` event alone. On any other event it posts nothing and prints one line that says why.
- The ledger grades a row `exact` only when the gateway job that minted its key attested that key for the row's run. A stated `SubAccountId` alone no longer earns `exact`. For rows to earn it, the mint job needs `id-token: write` and the two ingest variables. Rows already stored keep their grade.

### What you can now do

- See why an agent wrote no usage. A run whose agent ended in an error states `agent_error` with the error's class and its fix, in one line and in the comment. The action's new `agent-result` input lets the report say that the agent's job was skipped, instead of pointing at a job log that does not exist.
- Let the action install the agent. The new `agent-version` input installs that version when the agent is not on the runner, by default the version the recipes install. The `run` stage names every empty input in one line.
- Run any Codex version whose rollout file keeps the shape Costroid checks, with the watchdog and per-request rows. A version with no recorded fixture adds a note that names it.
- Change a budget on the base branch and have every open pull request use it at its next report. The report reads `costroid.yml` at the base branch's head, not at the merge-base, and records the commit as `config_ref`.
- Read the provider's own amounts in `costroid reconcile`'s rows: `provider_effective`, `provider_list`, the report's freshness stamp, and FOCUS 1.4's rounding tolerance on the list figures.
- Set `ENABLE_PROMPT_CACHING_1H` and have Claude Code's unsplit cache writes on an API key read as one-hour writes.

### Fixes

- A fork's follow-up posts only when exactly one open pull request from the fork's branch has the finished run's head commit, and its check concludes `neutral` at most. A fork could otherwise pass or fail a check on a commit of someone else's.
- The report and ingest stages fetch the agent job's artifact into a directory of their own before they install anything, and the install refuses an artifact that holds anything but the rows and the run object. An artifact entry could otherwise replace the binary the report then ran.
- A Claude Code run no longer fails on one odd line. A stream or session file line of a type Costroid does not price is skipped and counted in one warning. A count written as `10.0` or `"10"` reads as 10. A session file cut off inside its last line keeps its rows and counts the request in flight as unrecorded.
- When the reader refuses a stream, the run still writes its result object, with the reason `reader_refused` and the reader's first error line, so the check says why.
- Cache writes in Claude Code's OTLP usage count once. The span's copy used to land a second time, at an assumed lifetime.
- The sticky comment is matched by the token's own account, `github-actions[bot]` by default, instead of any bot.
- In the gateway recipe, the close job runs after the mint job, so a key minted late is deleted at once.

## 1.0.0-alpha.8 (2026-11-06)

### Fixes

- A `claude-code-action` stream line whose `message` is a plain string no longer refuses the run.
- The gateway and fork follow-up recipes pin `1.0.0-alpha.7`, the first release that holds their commands.

## 1.0.0-alpha.7 (2026-11-06)

### Breaking changes

- The result object is `costroid.result/0.3`, which renames `budget` to `cap` and adds the reason `fork_no_rows`.

### What you can now do

- Stop Claude Code at a budget before a request is paid. `costroid gateway` gives each CI run its own LiteLLM virtual key, with the run's budget, an expiry and the run in its metadata, and deletes it when the job ends. The rows read `gateway-precheck` with an overshoot bound. [`docs/gateway.md`](docs/gateway.md) has the design and the recipe.
- Post the `costroid` check on a fork's pull request. The follow-up recipe runs from your default branch once the agent's workflow completes, reads the fork's rows as data, and posts the check.
- Install `costroid` with the action and do nothing else, with `stage: install`.
- Read why a run wrote no rows in the comment. `costroid run` writes its result object beside the rows file, and the report reads the run's reason from it.

### Fixes

- The comment names the run's cap apart from the policy's budget, with the grade that held the run.
- The agent guard of `costroid lint-recipes` no longer applies to a workflow that only runs on `schedule` or `workflow_dispatch`.

## 1.0.0-alpha.6 (2026-11-06)

### Breaking changes

- An informational row's `SkuPriceId` reads `<provider>:informational:<class>`. It used to read `<provider>/<model>/<class>/informational`.

### What you can now do

- Hold a pull request to a cost policy. `policy.run_budget_usd`, `policy.max_run_cost_usd` and `policy.enforce` in `costroid.yml` on the base branch decide the check's conclusion. A failure warns for the first 7 days after the workflow is added. [`docs/policy.md`](docs/policy.md) has the rules.
- Give the check's verdict offline with `costroid policy`.
- Read one result object from `costroid run --json`, `costroid report --json`, the check run and the action's new outputs, from `cost` to `conclusion` and `result`. [`docs/agents.md`](docs/agents.md) shows it.
- Read the cap in USD on the comment's budget line, with the weakest enforcement grade and what is left or over.

### Fixes

- `claude-code-action` runs no longer warn that the session files disagree with the result.
- Codex subagent threads count toward the rows and the cap.
- A run that writes no rows states a reason: `no_usage`, or `concurrent_run` when another run held the agent's state directory.
- A Codex run stopped inside a turn says how many requests were in flight.
- The GitLab template pins the release the GitHub recipes pin and takes the binary's SHA-256 from the signed `SHA256SUMS`.

## 1.0.0-alpha.5 (2026-11-04)

### What you can now do

- Call the action from another action, as the GitHub Marketplace listing `Costroid/costroid-action` does.

### Fixes

- The action installs when another action calls it. It read an empty ref in that case and refused.

## 1.0.0-alpha.4 (2026-11-04)

### What you can now do

- Pin the action by its commit alone. It installs the release that commit belongs to and checks it by its cosign signature, so `version` and `sha256` are needed only for another release.
- Keep `claude-code-action` and report what its runs cost, with [`recipes/github-claude-code-action.yml`](recipes/github-claude-code-action.yml).
- Check each figure in the comment. Each one has a collapsed trace of catalog, token class, price id, unit price, tokens and amount, and a figure priced at the catalog's fallback tier is marked. The comment names the run's head commit and links the CI run.
- Read the export, required-check and troubleshooting pages.

### Fixes

- The comment withholds token counts with the amounts, because a count times the public catalog gives the amount back.
- Sessions that a hook or a plugin starts inside a run count toward its cost.
- `costroid.yml` keys that the report does not read print one warning each.
- Rows whose own arithmetic does not hold are refused.

## 1.0.0-alpha.3 (2026-11-04)

### What you can now do

- Read the execution file of a `claude-code-action` run with the action's `read` stage, held to a minimum agent version.

### Fixes

- The report job holds `contents: read`, so it reads `costroid.yml` on a private repository.
- A run that one reading covers whole counts as one run in the summary.

## 1.0.0-alpha.2 (2026-11-04)

### Fixes

- The agent's stream goes to a file on the runner, not to the job log, which is public on a public repository. `show-output: true` prints it.
- The report step passes the budget to the comment.

## 1.0.0-alpha.1 (2026-11-04)

The first release.

- `costroid run` runs Claude Code or Codex under a budget and writes one FOCUS row per request per token class, priced at list from the pinned catalog `0.1.0`.
- `costroid ingest-run` reads the execution file of a `claude-code-action` run.
- `costroid report` posts one `costroid` check run and one sticky comment on a GitHub pull request, and one sticky note on a GitLab merge request.
- The action and the recipes for Claude Code, Codex and GitLab CI, with `costroid lint-recipes` and `costroid validate`.
- `costroid serve` keeps rows in Postgres, under a CI OIDC token or an ingest key from `costroid keys`, behind an egress allowlist that `costroid egress report` reports on. It ships as an image, a Compose file and a Helm chart.
- `costroid reconcile` compares rows with Anthropic's and OpenAI's usage and cost reports, per credential per UTC day.
- Binaries, the image and the SBOM are signed with cosign, keyless.
