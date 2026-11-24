# Changelog

Each release is dated by its tag, in UTC. Every release so far is a pre-release. [`docs/verify.md`](docs/verify.md) checks one.

## 1.0.0-alpha.34 (2026-11-24)

### Read this if you ask `costroid serve` for a verdict

Two faults were found in the verdict `costroid serve` answers at `POST /v1/pulls/{pr}/verdict`, each by a test written to look for it. Both are closed in this release. Neither reached the check or the comment the report job posts, which do not ask the service.

**A budget passed on part of what the gateway counted.** The service summed a pull request's gateway figure only over the keys its rows name. A run whose rows were not on the pull request took its key's count with it: left out by the pull request's own workflow, or lost by a failed ingest job. In the test, two runs with USD 0.5 and USD 10 counted against a budget of USD 5 read `success`, with `pr_budget` `pass` at USD 0.5. Now the attesting run, which a pull request cannot edit, names the pull requests GitHub lists for the run a key is for, and the ledger sums the count of every key attested for a pull request, whether its rows arrived or not. A key with no count yet keeps the budget from passing, and a count over the limit fails `pr_budget` with no rows at all.

**A verdict concluded `success` on settings the caller chose.** The service read `costroid.yml` at the commit the request names, or took the file the request states where it has no GitHub App. A pull request's own job could name its own head, whose file holds a looser budget or names no policy pack, and be answered `success` where the base's file answers `failure`. Now the service concludes `success` only on settings it read itself at the head of the pull request's base branch. Anything else is `neutral` at best, a `failure` stays a `failure`, and `override_record` says why.

What to do, in this order:

1. Upgrade `costroid serve`. It applies migration 0013 when it starts.
2. Then move the gateway recipe's pin to this release, so `costroid gateway mint` names the pull requests. A newer `mint` against an older ledger still attests, and warns that the ledger is older.
3. Give the ledger its GitHub App, if a verdict's `success` matters to you. Without the App the service reads no file itself, so its verdict is `neutral` at best from this release on. [`docs/serve.md`](docs/serve.md) states the rule under "Success only at the base".

Not closed in this release, and stated so you can weigh it:

- An attestation that names no pull request reads as before, by its rows: one stored by an earlier release, and a run GitHub lists no pull request for. For such a key, a run whose rows do not arrive still takes its count with it.
- The first statement of a row id stands whole, whoever sent it, so a sender of the same tenant who knows a run's row ids can state them first.
- GitHub sends no `requested` event for a re-run of a run. [`recipes/github-gateway.yml`](recipes/github-gateway.yml) mints on that event, so a re-run of an agent's run gets no key.

### Breaking changes

- `costroid serve` concludes `success` only on settings it read at the head of the pull request's base branch through its GitHub App. A ledger with no App, a `config_ref` that is not the base's head, and a pull request the host did not answer conclude `neutral` at best. A base branch that moved during a run reads `not_base`; asking again cures it.
- `pr_budget` in the service's verdict fails with `over_limit` when the gateway's counts for the pull request are over the limit, with no rows and while another key is uncounted. It read `unproven`. A sum within the limit never passes while a count is missing.
- A customer's policy pack is read only at the base's head. A verdict off the base reads no pack and states `customer_pack` as a pack it did not read.
- `costroid validate` compiles the policy pack beside `costroid.yml` and exits 1 on one that does not compile, where it said `valid`. Where the pack's file is not there to read, it says so and the file is still valid. Its `--json` object is `costroid.validate/0.2` and adds `pack`.
- Migration `0013_credential_pulls.sql` adds the table `credential_pulls`. `costroid serve` applies it when it starts.

### What you can now do

- Install the chart without a crash loop. `costroid serve` waits for its database at start, every 2 s for up to 110 s, under the chart's startup probe of 120 s. With the bundled Postgres a first install restarted the service two to four times; it restarts none now. A wrong password, a missing role or a failed migration still end the start at once.
- Trust the chart on a real cluster. On every pull request, CI installs it on a kind cluster in both shapes, fresh and as an upgrade from the last release's chart and published image, and reads each run's total back exactly. [`scripts/chart-install.sh`](scripts/chart-install.sh) runs the same walk on your machine.
- Read under a policy pack that is not valid what to do next.

### Fixed

- Reinstalling the chart with its bundled Postgres locked the service out of its own database: `helm uninstall` removed the chart's Secret while Postgres kept its volume and its roles' passwords. The Secret is now kept on uninstall. A chart from an earlier release still removes it: run `helm upgrade` with this chart before you uninstall. [`docs/ledger.md`](docs/ledger.md) says how to start empty.

## 1.0.0-alpha.33 (2026-11-24)

### Read this if you run `costroid serve` with both telemetry and rows

`costroid serve` counted one request's cache writes twice when telemetry stored them first at an assumed lifetime and `POST /v1/rows` then sent them at the stated one. The two rows take different ids, and a row sent to the rows entrance names no request, so the ledger could not match them. In the test that found it, one such request raised a month's total from 1.9375 to 2.9375 USD. This release closes it: the ledger records the row ids each request's cache writes take, and leaves out a later write that carries one of them in the same run.

Rows stored before this release are matched as before, so a request whose first write was stored by an earlier release can still be counted twice by a later one. One case is not closed: two statements of one request's cache writes, both sent to `POST /v1/rows`, at different lifetimes. [`docs/serve.md`](docs/serve.md) states the rule a ledger keeps and that limit.

### Breaking changes

- Migration `0012_request_write_ids.sql` adds the table `request_write_ids`. `costroid serve` applies it when it starts.
- A GitHub App registered with `costroid app` now asks for `pull_requests: read`, where it asked for `write`: nothing `costroid serve` does posts on a pull request. An App registered from an earlier release keeps `write` until its owner sets Pull requests to Read-only in the App's settings; its tokens ask for read either way. [`docs/github-app.md`](docs/github-app.md) gives the steps.

### What you can now do

- Read in one place what Costroid can read and change. [`permissions.json`](permissions.json) lists every call Costroid makes to GitHub and GitLab, every `gh` and `git` command it runs under your own sign-in, and every permission each recipe's job and the GitHub App ask, each marked read or write with its reason and its basis: the publisher's own page, or a recorded GitHub Actions run. [`docs/permissions.md`](docs/permissions.md) is the same list for a person. A test holds the file to the code in both directions, so a call cannot be added without it.
- Give the merged job less. [`recipes/github-merged.yml`](recipes/github-merged.yml) no longer asks for `contents: read`: listing a commit's pull requests needs `pull-requests: read` alone, which a recorded run on a private repository showed. `costroid lint` still takes a copy of the recipe from an earlier release.

### Fixed

- Which of two statements of one row in a single batch stands is decided in the batch, the earlier one. It rested on the order the database inserts in.
- [`docs/troubleshooting.md`](docs/troubleshooting.md) told a reader to give the `report` job `contents: read` when the repository's visibility could not be read. Every workflow token reads it, so no permission is missing: the rows now name the real causes and the cure for each.
- `docs/github-app.md` said the App posts Costroid's comment on a pull request. It never did.

## 1.0.0-alpha.32 (2026-11-24)

### Read this if `costroid.yml` writes a switch with a capital letter

`policy.enforce`, `approval.allow_author`, `comment.show_cost` and `comment.receipts` read `True` and `TRUE` as false, though `costroid validate` took them. So under `enforce: True` the warn-only period ran on, under `allow_author: True` the author could not approve, under `show_cost: True` amounts were withheld, and under `receipts: True` receipts were off. Each switch now reads as the boolean YAML writes: `true`, `True` and `TRUE` are true, and `false`, `False` and `FALSE` are false. A file that writes `true` and `false` in lower case reads as it did.

### Breaking changes

- `costroid gateway mint` and `costroid gateway close` exit 1 when a ledger of the team's own took the key's attestation or the gateway's count and refused it. They exited 125 up to 1.0.0-alpha.31. A ledger that cannot be reached still exits 125. [`docs/exit-codes.md`](docs/exit-codes.md) states the rule: a service that took the request and refused it is exit 1.
- Migration `0011_rule_demotions.sql` adds the table `rule_demotions` and the column `base_branch` to `failing_verdicts`, which no release wrote to. `costroid serve` applies it when it starts.

### What you can now do

- Let a ledger demote a cost rule that reviewers keep overriding. `costroid serve` now keeps its own record of the failing verdicts it answered and the approvals it verified, and a cost rule whose failures were approved more than one time in five warns for 30 days where it would fail. The record needs 10 failing pull requests and approvals from 2 accounts, is kept per base branch, and never counts the pull request being judged. A failing verdict is recorded only when the service read `costroid.yml` itself at the head of the pull request's base branch, as GitHub names it, and an approval only after GitHub confirmed the approver's write role. A check run, a rows file and a request body never reach the record, and without a ledger no rule is demoted. A demoted rule concludes `neutral` at best. [`docs/policy.md`](docs/policy.md#demotion-by-the-override-rate) states the rule.
- Know where a demotion shows, and where it does not. It shows in the verdict the service answers at `POST /v1/pulls/{pr}/verdict`. The check and the comment the report job posts are not changed by it. Of the six cost rules only `pr_budget` can fail at the service, so only it is demoted today.
- Turn demotion off: `policy.auto_demote: false` in `costroid.yml` on the base branch keeps every rule as written and re-arms each at once. The default is `true`. A release before this one refuses the whole `policy` mapping when it holds the key, and runs no rule, as for each key added before.
- Read from the verdict's `override_record` whether it reached the ledger's record, and why not when it did not.
- Send to a Costroid Platform that speaks `integration/v0.7`. `costroid export`, `costroid merged` and `costroid reconcile --export` send by v0.7 to a platform that states it, and fall back as before. By v0.7 a batch belongs to one attempt of its run, and on a `pull_request` run a row that names another pull request than the run token's goes as `unverified`. A refusal of a batch bound to another attempt says to drop `--batch-id` on GitHub Actions, where `export` names a batch per attempt.
- State a gateway key to Costroid Platform. With `COSTROID_PLATFORM_URL` set, `costroid gateway mint --for-triggering-run` and `close --for-triggering-run` attest the run's key and the gateway's settled count to the platform as well as to a ledger. A platform that does not offer the statement, one that has not bound the installation, and one that refuses a run no protected branch holds each warn, and the step goes on. Any other refusal names its code and the contract's sentence and exits 1. A server error is sent again for up to 150 s. No request follows a redirect. [`recipes/github-gateway.yml`](recipes/github-gateway.yml) passes the platform's address, its token and the owner's installation to both jobs, and states nothing where they are not set.

### Changed

- An object with a demoted result is written as `costroid.result/0.14`. Its title names no amount and says the check is neutral, and the rule's own line gives the amounts. Every other object is the version it was.
- `costroid merged` names a GitHub key set that stays unavailable: after its resends it prints the code `INSTALLATION_PROOF_KEYS_UNAVAILABLE` with its sentence and exits 1.
- The help of `costroid export`, `costroid merged` and `costroid reconcile` names the versions each sends by.

## 1.0.0-alpha.31 (2026-11-23)

### Read this if you run `costroid serve` with the gateway

Release 1.0.0-alpha.30 said that on a private repository on GitHub Free, Pro or Team, a pull request's own run can reach the environment that holds the gateway's keys, so an attested key and an attested count were the word of whoever can push a branch. This release closes that. A ledger now takes an attestation and a count only from a run that a pull request cannot edit: one that `workflow_run` started from a protected branch.

Until you move to the new recipe, your rows read `windowed` where they read `exact`, and a budget no longer passes on the gateway's count. Nothing stops running: `costroid gateway mint` and `close` warn and go on, and the gateway still holds each run to its budget. To earn the grade again:

1. Copy [`recipes/github-gateway.yml`](recipes/github-gateway.yml) to `.github/workflows/costroid-gateway.yml` on the default branch.
2. Remove the `gateway-mint` and `gateway-close` jobs from the agent's workflow.
3. Let the `costroid-gateway` environment admit the default branch alone.
4. Protect the default branch with a ruleset.

On a private repository on GitHub Free no branch can be protected. There the recipe runs, warns that the default branch is not protected, and its rows read `windowed`. That is the true reading for such a repository. [`docs/gateway.md`](docs/gateway.md) states the rule and its limit.

### Breaking changes

- `costroid serve` takes a key's attestation and a gateway's count only under a token that states `event_name` `workflow_run`, a `ref` under `refs/heads/`, and `ref_protected` as the JSON string `"true"`, from a job in the gateway's environment, for another run than the one that sends it. Any other token is refused with 403 `CREDENTIAL_RUN_UNPROTECTED`, and nothing is stored.
- A key attested from a job of the pull request's own run, the setup `recipes/github-claude-code-litellm.yml` shipped until now, reads `windowed` where it read `exact`, and its count no longer lets a budget rule pass.
- A row reads `exact` only when the head it states is the head the protected run attested, which GitHub's `workflow_run` event named for its run.
- A ledger keeps one gateway figure per key, written once. The same string sent again is answered 204. Another figure is refused with 409 `CREDENTIAL_SPEND_HELD`. The larger of two figures is no longer kept. `costroid gateway close` no longer sends a figure that has not settled: it warns and sends none.
- A ledger keeps one attestation per attesting run. A second key from the same run is refused with 409.
- `costroid gateway mint` refuses a key the gateway already holds. It calls LiteLLM's `/key/list` on every mint, so an admin key limited to named routes needs that route.
- `costroid lint` and `costroid lint-recipes` gain the rule `gateway-trigger`: the gateway's admin or sealing key in a workflow that any event but `workflow_run` starts is a finding.
- Migration `0010_protected_attestation.sql` adds the tables `credential_attestations` and `credential_spend`, and the service's role loses every grant on `minted_credentials` and `gateway_spend`. `costroid serve` applies it when it starts. A row stored `exact` before this release keeps that grade: it was graded under the environment's rules alone, and no count kept before this release reaches a verdict.

### What you can now do

- Mint and close a run's gateway key from a workflow that a pull request cannot edit: `costroid gateway mint --for-triggering-run` and `costroid gateway close --for-triggering-run`. The run, its attempt and its head come from GitHub's `workflow_run` event. A fork's run, and a run that another event than `pull_request` started, are refused. The key's budget is `policy.run_budget_usd` in `costroid.yml` at the pull request's base, where that file sets one.
- Read from a warning why a run's rows read `windowed`, and what to do: move to the new recipe, or protect the default branch.
- Keep a ledger's own record of failing verdicts and approvals: migration `0009_override_record.sql` adds an append-only record. Nothing writes to it or reads a rule's demotion from it in this release.

### Fixed

- `docs/gateway.md` and `docs/serve.md` describe the rule a ledger applies, in place of the paragraph of 1.0.0-alpha.30 that described the weakness.

## 1.0.0-alpha.30 (2026-11-23)

### Read this if you run `costroid serve` with the gateway on a private repository

`docs/gateway.md` and `docs/serve.md` rest an attested key, and a budget that passes on the gateway's count, on the protection rules of the environment that holds the gateway's keys, and they named required reviewers. On GitHub Free, Pro and Team that rule exists only for public repositories. On a private repository on one of those plans, a pull request's own run reaches the environment once its branch rule admits `refs/pull/*/merge`, so an attested key and an attested count are the word of whoever can push a branch. The pages now say so. No behaviour changes in this release: treat the `exact` grade and a pass on the gateway's count as that person's statement on such a repository. A later release moves the two attesting jobs to a workflow that a pull request cannot edit.

### Breaking changes

For anything that matched a line's text or read a stream.

- `costroid run` writes one more line on standard error, first, just before it starts the agent: `costroid: running <agent> under a cap of USD <amount>`, such as `costroid: running claude-code under a cap of USD 5.00`. `--quiet` drops it. A run that starts no agent does not print it. A script that read the first line of `run`'s standard error as the summary's reads the run's result object instead, from `--json` or from the file beside the rows.
- `costroid validate` on a valid file ends, on standard error, on `Next: costroid policy --rows FILE holds a run's rows to this file.`
- `costroid serve` prints its help page on standard output when `--help` or `-h` follows other flags. It printed it on standard error there.
- On a fork's pull request whose rows are within a budget, the comment's fold, the check's summary and the job summary read `Over budget, the check would stay neutral, because a fork's rows are the fork's own claim.` They read `Over budget, the rule would fail.` The title, the conclusion, the machine block and the outputs are unchanged.
- The folded details of a comment with figures, and the check's and the job's summaries, gain one closing paragraph, described below. No title, conclusion, output or machine block reads it.

### What you can now do

- Read Costroid's own accuracy record where you read its figures. The fold of a comment with figures ends on one line about the test Costroid runs on its own repository, as this release carries it: how many of the last 14 days on [`docs/accuracy.md`](docs/accuracy.md) passed, failed days included, and how close the token counts came on the days that passed. A day counts only when the agent ran alone on its key, because a provider reports a key's day and never an agent's. The line speaks only for a measured run of the agent and provider the test ran; a subscription run, a run of several agents or providers, another agent or a fallback price is told that no day speaks for its figures. Every form closes: "Nothing has compared this run with a provider's report."
- See that `costroid run` has started the agent, and under what cap, before the agent prints anything.
- Read the amount and the budget in the title of the check the approve button of `costroid serve` posts, on a repository whose comment shows amounts, as in the one `costroid approve` posts.
- Trust the examples: every example of what a command prints, in the README and in `docs/`, is held by a test to a run of the command or to a recorded state.

### Fixed

- Six documented examples no longer matched their command and now do: the `doctor` and `lint` objects of `docs/output.md`, the pull request's total in `docs/agents.md`, the verdict's times in `docs/policy.md`, the receipt's line in `docs/receipt.md`, and a refusal in `docs/gateway.md`.
- The entry for 1.0.0-alpha.27 said every dollar figure on the accuracy page equals the provider's. They are equal to the digits the provider's report states: the page shows 0.001932885 beside 0.00193288 on 2026-11-12.
- `costroid serve` logs a warning about `costroid.yml` as a line of its JSON log, where a plain line could enter it.

## 1.0.0-alpha.29 (2026-11-22)

### Breaking changes

Standard output and standard error. [`docs/output.md`](docs/output.md) states what every command prints on each. Standard output now holds only what a command was asked to print, and everything a command says about its work is on standard error. A script that read one stream alone reads these lines on the other; `2>&1` reads both as before.

- `costroid lint` prints its high and medium findings on standard output, above its notes and its count. They were on standard error, and the notes came first. `costroid lint-recipes` prints each rejected line on standard output.
- The `Next:` line of `costroid demo`, `costroid init`, `costroid doctor` and `costroid validate` is on standard error. It was on standard output.
- `costroid init` says what it wrote and what is left on standard error: each `costroid: wrote ...` line, `costroid: <file> exists, and init left it as it is`, GitLab's include line, what each push can spend, the commands left, the notes. Standard output is empty unless `--existing` prints its patch there. With `--pr`, each `$ <command>` line and init's own lines are on standard error too.
- `costroid export` says what it sent or wrote on standard error: `costroid: batch <id>, <n> rows, sha256 ...`, `costroid: batch <id> ingested, <n> rows`, `costroid: pull request <n> of <repository>: <link>`, `costroid: <file> holds no rows, so nothing was sent`, and with `--format --out` `costroid: wrote <n> rows as <format> to <path>`. `--format` without `--out` still prints the file on standard output. The action's `ingest` stage prints the same lines as before: it reads both streams as one.
- `costroid merged` says what the platform stored on standard error, with or without `--json`. `costroid connect` prints `costroid: <installation> is bound to tenant <tenant>, proven at <time>` there, `costroid emit-otlp` its `costroid: sent <n> spans ...` line, `costroid reconcile --export` its two lines about what the platform took, and `costroid app create` the address to open and the lines about the App it created.

Command lines and exit codes.

- `costroid serve`, `costroid keys create`, `costroid egress report`, and `costroid gateway keygen`, `public-key`, `request`, `mint` and `close` refuse an argument they do not read: `costroid: <command> takes no argument`, exit 2, and none of their work is done. They ignored it. `costroid report` and `costroid follow-up` still ignore one, so a job that passes one keeps running, and their help says so.
- Ctrl-C ends every command but `costroid serve` with exit 130. `costroid run` first stops the agent and every process it started, writes the rows of what was spent, and then exits 130; it exited with the agent's own code. A command waiting on standard input or on a prompt ends too. `costroid serve` drains and exits 0, as before.
- A rows file with a row that states the tag `costroid/origin` is refused whole. `costroid report` and `costroid follow-up` state no figure, with reason `invalid_row`. `costroid policy`, `costroid export`, `costroid reconcile` and `costroid emit-otlp` exit 125 before they send or write anything. Only the report builds such rows, in memory, from a receipt.

### What you can now do

- State what your own machine spent on a branch: `costroid receipt` prints the token counts of your Claude Code sessions on the branch as a comment for its pull request. Post it with `costroid receipt | gh pr comment <number> --body-file -`, or paste it. It reads no prompt, answer, tool input or output, or file contents, prints no amount, refuses to run in a CI job, and sends nothing. [`docs/receipt.md`](docs/receipt.md) says what it reads and what the posted comment reveals.
- Show a receipt on the pull request: the action's report stage takes `receipts: "true"`. The report reads the newest unedited receipt the pull request's author posted, prices it at the catalog it carries, and states it on one line on the comment, the check and the job summary, apart from the run's cost and marked unchecked. No budget, rule, total, title, conclusion or output reads it. `comment.receipts: false` in `costroid.yml` on the base branch turns it off, and a pull request cannot turn it back on.
- Read the new result by its version: a report that read a receipt writes `costroid.result/0.13`. Every other report writes the object it wrote before, byte for byte.
- Silence a command: `-q` and `--quiet` on every command keep warnings and errors on standard error and drop every other line. Standard output and the exit code do not change.
- Read three more commands from a script: `costroid doctor --json`, `costroid lint --json` and `costroid validate --json` print one object on standard output, `costroid.doctor/0.1`, `costroid.lint/0.1` and `costroid.validate/0.1`. Standard error and the exit code are as without the flag. A field is added under the same first number and never removed or renamed under it.
- Give a bug report what it needs: `costroid version --verbose` adds the build's commit, the operating system and architecture, the result versions this release writes, and whether the directory holds `costroid.yml`. It prints no secret, no value of the environment and no path. `costroid version --json` prints the same facts as `costroid.version/0.1`.
- See that a command is waiting: `costroid export`, `merged`, `connect`, `reconcile`, `doctor`, `app create` and `gateway mint` say `costroid: asking <host>` before their first request to a host.
- Have `costroid lint` and `costroid lint-recipes` name a `receipts` input on a stage that posts no report (`receipts-input`).

### Changed

- `costroid doctor` with nothing left to fix ends on `Next: gh pr create --fill opens the pull request that Costroid comments on.`, and on GitLab on `Next: open a merge request, as https://docs.costroid.com/recipes#gitlab-ci says.`

## 1.0.0-alpha.28 (2026-11-22)

### Breaking changes

Exit codes. [`docs/exit-codes.md`](docs/exit-codes.md) states what every command exits with.

- A wrong command line exits 2 on every command but `costroid run`: an unknown command or subcommand, a flag the command does not have, a flag without its value, a value of the wrong form, a required flag or argument left out, an argument too many. It exited 125. A script that tested for 125 to catch a mistyped flag tests for 2. `costroid run` keeps 125 for its own command line, because it passes the agent's exit code through and every code below 124 is the agent's.
- `costroid init` outside a git repository exits 125. It exited 2.
- `costroid version` and `costroid --version` refuse a flag or an argument they do not read and exit 2. They printed the version and exited 0.
- `costroid gateway`, `costroid app`, `costroid keys` and `costroid egress` with no subcommand print a short page on standard output and exit 2. They printed the whole help page on standard error and exited 125. `costroid keys revoke` with no id, or with one that is not 64 hexadecimal characters, exits 2 and prints two lines.
- Unchanged: 0, 1 and 124 wherever they stood; 3 under `costroid report --exit-on-verdict`; the agent's own code under `costroid run`; 1 where Costroid Platform or an endpoint refuses what `export`, `merged`, `connect` or `emit-otlp` sent.

Help and errors, for anything that matched their text.

- `costroid` alone and `costroid help` print a new screen of 22 lines that lists every command once, in four groups. The lines `Usage:`, `costroid run --budget-usd N -- <agent command>`, `Other commands:` and `Ledger commands:` are gone.
- Every `costroid help <command>` page opens with a sentence, then `Usage:`, examples, a line for each flag, what the command reads from the environment and its exit codes, and ends on `Documentation:` and an address.
- A flag a command refuses prints `costroid: <flag> is not a flag of costroid <command>`, `costroid: <flag> needs a value` or `costroid: <flag> takes <form>, not <value>`, then at most two more lines. `flag provided but not defined: -name`, `flag needs an argument: -name`, `invalid value "v" for flag -name` and the help page after them are no longer printed.
- `costroid: unknown command "x"` is followed by `The nearest command is <name>.` or `No command is near it.` Nothing is run by guess.
- An error that ends a command can have more than one line. The first is `costroid: ` and what is wrong. Under a finding about the command line stands `Run costroid help <command> to see its usage.` Under an error that names no next step stands `See https://docs.costroid.com/troubleshooting.` A script that read the last line of standard error finds the error on the first.
- `costroid init`, `costroid doctor`, `costroid demo`, `costroid run`, `costroid policy` and `costroid report --preview` no longer put the command's name and a colon before an error, and `costroid result` no longer prints `scm:` inside one.
- A file or a program the system could not find is named in words: `<path> does not exist`, `<path> is a directory`, `<program> is not on PATH`. `open <path>: no such file or directory` and its kin are no longer printed by the commands of a first run.
- `costroid validate` and `costroid lint-recipes` read a word that opens with a dash as a flag and refuse it. Put `--` before a path that opens with a dash.
- `costroid demo` prints, above its last paragraph, `Next: costroid init github --agent claude writes the workflow that posts this.` `costroid init`, `costroid validate` on a valid file, and `costroid doctor` with no fail and no warn end on a line that opens `Next:`. A script that read the last line of `doctor` or `validate` finds its line by its prefix (`costroid: doctor:`, `<file>: valid`).
- `costroid init` with a host and no `--agent` prints `costroid: name the agent with --agent`, then each agent of the host once with what its recipe does, and an example that runs as printed. With neither a host nor an agent, in a repository whose remote names no host, it asks for both in one refusal.
- `costroid doctor` counts an agent that is not installed on this machine as `ok` where a workflow or a GitLab template of the repository installs it. It prints one line for a `gh` that is not signed in, where it printed up to three, and one line for a repository with no remote.
- `costroid run` ends on `costroid: cap USD 5.00 (client-side estimate, can pass the cap)`, where it read `costroid: budget USD 5, agent-flag: client-side estimate, can pass the cap`, and on `costroid: over the cap by USD …`, where it read `costroid: over budget by USD …`. `costroid run` and `costroid ingest-run` state the cost as `costroid: USD 0.001619725 at list (List-equivalent, not billed)`.

The check and the comment.

- The check `costroid follow-up` posts for a fork's pull request whose rows break a rule is titled `Over budget, on a fork's own rows: … A fork's rows are the fork's own claim, so the check is neutral.`, in the warn-only period and after it. It no longer says the check will fail when the period ends.
- On a repository whose comment shows amounts, the check `costroid approve` posts names the amount in its title and no longer ends "Amounts are withheld here."
- On GitLab the note says "merge request" where it said "pull request", and the rule's line opens "Merge request budget:". Keys, rule ids, tags and outputs keep `pr` and `pull_request`.
- `policy_pack` is no longer a reserved key. `costroid validate` and the report refuse a value that is not a path from the repository's root ending `.rego`, and a `costroid.yml` with such a value runs no rule.

The install script.

- The script no longer stops on a machine without cosign. With no cosign 3 or later on `PATH`, it downloads cosign v3.0.6 into its temporary directory, runs it only when its SHA-256 is the one the script holds for that machine, and removes it. It exits 0 where it exited 1. `costroid: cosign is not on PATH, and nothing is installed without its check: …` is no longer printed.
- The script never runs a cosign on `PATH` that states a version older than 3, or none: it names it and downloads the pinned one. A cosign older than 3 refused a genuine release.
- `costroid: <directory> is not on your PATH: add it, or run <directory>/costroid` is now `costroid: <directory> is not on your PATH. To add it in this shell, run:`, followed by an `export PATH=…` line. The script's last line is `Next: … demo prints the comment a pull request gets.`
- The by-hand block in `docs/install.md` checks with `sha256sum -c` and installs into `~/.local/bin` without `sudo`.

### What you can now do

- Hold a run to a multiple of what nearly every earlier run cost: `policy.max_p95_factor: 2` fails a run that costs more than twice the P95 of the same agent's latest 20 runs on the pull requests merged into the base branch. The factor is a plain decimal of at least 1 and below 10000. With fewer than 20 runs the rule does not run and never passes: it concludes `unproven` with reason `no_baseline`. The rule is `run_p95`; its result states `p95_usd`, `limit_factor` and `baseline_runs`.
- Add rules of your own: `policy_pack: .costroid/policy.rego` names one Rego file in the repository. The report, a fork's follow-up, `costroid policy` and `costroid serve` read it at the commit they read `costroid.yml` at, which on a pull request is the base branch's, run it in a child process, and add its warnings and failures to the check. A pack never makes a check pass. [`docs/policy.md`](docs/policy.md) holds a complete example, what a pack may call and see, and what each fault concludes.
- Try a pack before a pull request does: `costroid policy --rows rows.jsonl --config base.yml --pack base-policy.rego`.
- Run a pack on a ledger of your own: `costroid serve` runs one when the verdict request names the GitHub App's `installation` and the service read `costroid.yml` and the pack itself. It runs at most 4 at once and answers `503 PACK_BUSY` past that.
- Read the new results by their version: a verdict that holds a `run_p95` result is written as `costroid.result/0.11`, and one that holds a pack as `costroid.result/0.12`. Every other report writes 0.9 or 0.10 as before. A reader that accepts a fixed list of versions adds these two when the repository sets either key.
- Read on the comment when a pull request changes its pack or `policy_pack`: the change applies after the merge, because the verdict reads the base branch's file.
- Read what every command exits with on one page, [`docs/exit-codes.md`](docs/exit-codes.md), and on each command's help page.
- Read an example on every help page. A mistyped command, subcommand or flag names the nearest one.
- Pipe to two more commands: `costroid validate -` and `costroid emit-otlp --rows -` read standard input.
- Install on a machine that has no cosign, with the release's signature still checked on every install.
- See the example comment with nothing installed: `docker run --rm ghcr.io/costroid/costroid:<release> demo`.
- Follow the README from its first line: it installs before its first command, and every command on its first screen runs with no key.
- Check the install path yourself: `make install-walk` follows the README and the install page in fresh `ubuntu:24.04`, `debian:12` and `alpine:3.20` containers and fails on a command that does not run as written.

A branch that pins an older release: a pull request's workflow file is the branch's own, so a branch can pin a release that does not read `policy_pack`. That release warns that the key is reserved, runs Costroid's rules, and runs no pack.

### Fixed

- `costroid help validate` said a policy amount written as a float, such as `5.0`, is a problem. `validate` and the check both take it. The page now says what they do.
- GitHub's contents API is asked for a nested path segment by segment.
- `docs/troubleshooting.md` told a reader whose cosign refused a signature to report a forged release. A cosign older than 3 refuses a genuine one; the row names that cause first.
- Two tests no longer take each other's port.

## 1.0.0-alpha.27 (2026-11-21)

### Breaking changes

- The `costroid` check's title is a sentence that says what is true of the run, such as `Within budget, by the job's own figures: this run cost USD 0.0391529 at list (Measured) of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.` It read `Verdict: neutral`. The comment and the job summary open with the same sentence, and the line `**Verdict: neutral.**` is gone. A script that matched on the title or on that line reads the `conclusion` output or the result object instead. The conclusion values, the exit codes, the machine block and the step's outputs are unchanged. [`docs/policy.md`](docs/policy.md#what-the-check-says) lists what the sentence can say.
- `costroid policy` no longer opens with `Verdict: <conclusion>.` It prints each rule its last line does not name, then ends on the same sentence. Its exit codes and `--json` are unchanged.
- An amount a person reads shows at least two decimal places: `USD 5.00`, where the comment read `USD 5`. No digit is rounded or dropped. The machine block, the outputs and `--json` keep the exact string, so a script reads an amount from them.
- The comment's table has no Attribution column, and its Enforcement cell states the note without the grade's id: `client-side estimate, can pass the cap`, where it read `agent-flag: client-side estimate, can pass the cap`. The grades are in the machine block as before. The job summary's table leads with Cost and Basis.
- `Unverified:` and `Verified:` read `Unchecked:` and `Checked:`. A report with no figures reads `No figure (agent_error): …`, where it read ``This report shows no figures. Reason `agent_error`: …``. A unit price reads `per million tokens`. `Run cost threshold` reads `Run cost limit`.
- The increase rules read their baseline from merged pull requests. `run_increase` and `run_increase_percent` compare a run with the median of the same agent's latest 20 runs on the pull requests merged into the base branch, read from the `costroid` check runs on each merged pull request's head. A pull request's own earlier runs no longer count. In 1.0.0-alpha.25 and 1.0.0-alpha.26 they were the baseline, so a pull request set its own baseline: costly early runs raised the median, and a later run over it concluded `unproven` instead of failing. A repository whose latest 30 merges hold fewer than 10 runs of the agent now concludes `unproven` with reason `no_baseline`.
- `costroid policy --history` no longer gives a baseline. It gives the pull request's total and the count that ends the warn-only period. `--baseline FILE` takes the merged pull requests' result objects. Where `costroid.yml` sets `max_increase_usd` or `max_increase_percent`, `--history` without `--baseline` is refused with a sentence.
- `costroid reconcile` compares a run that crosses midnight UTC as one for dollars as well as tokens. The span's first day holds the joined dollar comparison. Each later day's row carries `span` and the span's state, with `tokens` and `dollars` null. A reader of `--out` that summed a later day's dollars now meets null there. Read the span's dollars on the row of `span.from`.
- No residual names `run_spans_days` any more. The days of a span are compared together, so nothing moves out of them. `span.cause` still reads `run_spans_days`.
- Every day of a span reads `price_miss` when a price misses on any of its days. The exit code, the miss count under the table and the dollar table count a span once.
- A row whose figure is past the contract's form is no longer written. That day stops with a line naming the figure, every other day is still written, and `reconcile` exits 125.

### What you can now do

- Read what the check found without opening it: the title names the figure, its basis and the budget. `costroid report --preview` and `costroid demo` end on the same sentence.
- When several rules ran, read which one decided in the title, and each other rule's line under it.
- Read on a failed check where a reviewer's approval is described.
- See a run that went past its cap above the fold: its cap line stands under the table.
- Send Costroid Platform the fact that a pull request merged: `costroid merged`, in a job on a push to the default branch, sends one merge fact per pull request the push closed, by `integration/v0.6`, or by `integration/v0.5` to a platform that states no later version. A fact whose commit is the push's tip, sent with the job's OIDC token from a protected branch, is stored as `token_verified`, and the command names the branch. For any other fact, the command says why it reads `unverified`.
- See what would be sent first: `costroid merged --dry-run` prints each merge id and document and reaches no platform. `--json` prints the platform's status, `run_identity` and `ref` for each fact.
- Send reconciliation rows to Costroid Platform with `costroid reconcile --out FILE --export`. It sends what `--out` writes, by `integration/v0.6`, or by `integration/v0.5` to a platform that states no later version, to the platform `COSTROID_PLATFORM_URL` names, under `COSTROID_PLATFORM_TOKEN`. Bodies are cut at the limits `GET /quotas` states, a server error sends the same body again for up to 60 s, and 402, 409 `INSTALLATION_NOT_BOUND` and 429 keep the rows and warn. On GitHub Actions a job with `id-token: write` sends its run token beside `--installation-id`. The admin key never goes to the platform.
- Read `currency` on every reconciliation row: the currency of every amount in it, `USD`.
- Rely on the form of every amount: a decimal string of at most 30 places and 64 characters, never rounded to fit. Token counts are digits only, at most 20.
- When any part of the baseline cannot be read, the report leaves it out and warns `the baseline is left out`. The pull request's total still posts.
- `costroid lint-recipes` allows `id-token: write` on a job that runs `costroid merged`, which sends the job's OIDC token as each merge fact's run token.
- A new `costroid lint-recipes` rule, `merged-job`, holds a job that runs `costroid merged` to `contents: read`, `pull-requests: read` and `id-token: write` and nothing else, to a workflow that runs on `push` alone, to a job `if` that joins the default-branch test with `&&`, to no agent, checkout or fetch, and to an integration token read from one secret in the step's `env`.
- A token refused by a platform that serves `integration/v0.6` alone is reported as that refusal, not as a platform that serves no version.

## 1.0.0-alpha.26 (2026-11-21)

### What you can now do

- Price GPT-6.2 Astra and GPT-6.2 Sol at their own rows, from catalog 0.4.3, on OpenAI and GitHub Copilot. 1.0.0-alpha.25 priced them at each sheet's fallback tier. GPT-6.2 Astra now reads USD 8.00 input, USD 40.00 output and USD 0.40 cache read per million tokens up to 272,000 input tokens, and USD 16.00, USD 60.00 and USD 0.80 above. GPT-6.2 Sol reads USD 2.00, USD 10.00 and USD 0.10, then USD 4.00, USD 15.00 and USD 0.20. OpenAI's sheet also prices both at the batch, fast and Ultrafast tiers. Their Ultrafast prices double on 2027-03-01, as OpenAI's page states: GPT-6.2 Astra from USD 24.00, USD 120.00 and USD 1.20 to USD 48.00, USD 240.00 and USD 2.40 up to 272,000 input tokens.
- Price GPT-6.2 Astra for Law at its own rows, under the promotion OpenAI's page states through 2026-12-31: USD 8.00 input, USD 40.00 output and USD 0.40 cache read per million tokens up to 272,000 input tokens, and USD 16.00, USD 60.00 and USD 0.80 above. GPT-6 Astra for Law is now priced by context length: its short-context prices are unchanged, and above 272,000 input tokens it reads USD 20.00, USD 75.00 and USD 2.00.
- Price Gemini 4 Neon at its own rows, from catalog 0.4.3: USD 3.00 input, USD 15.00 output and USD 0.15 cache read per million tokens at Google's standard tier, on GitHub Copilot and on Cursor; USD 1.50, USD 7.50 and USD 0.075 at Google's batch and flex tiers; USD 5.40, USD 27.00 and USD 0.27 at its priority tier. 1.0.0-alpha.25 priced it at each sheet's fallback tier.
- Price MAI Code 2 and MAI Code 2 Flash on GitHub Copilot at their own rows: MAI Code 2 at USD 0.50 input, USD 2.50 output and USD 0.025 cache read per million tokens, and USD 1.00, USD 5.00 and USD 0.05 from 2027-05-17; MAI Code 2 Flash at USD 0.05, USD 0.25 and USD 0.005, and USD 0.10, USD 0.50 and USD 0.01 from 2027-05-17, as GitHub's page states.
- GPT-6 Rosalind is cheaper in catalog 0.4.3, as a promotion OpenAI's page states through 2026-12-31: USD 10.00 input, USD 50.00 output and USD 1.00 cache read per million tokens up to 272,000 input tokens, where catalog 0.4.2 read USD 15.00, USD 75.00 and USD 1.50. The page states no price after 2026-12-31, so from 2027-01-01 a request for GPT-6 Rosalind, GPT-6 Astra for Law or GPT-6.2 Astra for Law prices at OpenAI's fallback, USD 150.00 input, USD 600.00 output and USD 12.00 cache read, and its row says it was priced at the fallback tier.
- GitHub's page no longer lists GPT-5.6 Terra and Claude Opus 5. Catalog 0.4.3 keeps their rows as catalog 0.4.2 read them on 2026-11-14, so they price as before.
- On Cursor, the US data residency multiplier of 1.1 now applies to Gemini 3.8 Flash, Gemini 4 Argon, Gemini 4 Carbon, Gemini 4 Hydrogen and Gemini 4 Neon. Catalog 0.4.2 priced those models' US requests at their global price.
- The catalog's [diff report](https://github.com/Costroid/costroid-catalog/blob/v0.4.3/reports/0.4.3-diff.md) lists every row that 0.4.3 adds, removes or changes.
- `costroid export` sends by integration contract v0.4 to a platform that states it, and by v0.3 or v0.2 otherwise, saying which.
- On 409 `EXPORT_BATCH_RUN_BOUND`, `costroid export` says the batch is bound to the run that sent it and that only that run restates it, keeps the rows file, and exits 1. It never says to send again.
- Read Costroid's figures beside the provider's reports in [`docs/accuracy.md`](docs/accuracy.md): sixteen days of the test key, the last eight of which pass, with every token class equal to the provider's count, and every dollar figure equal to the provider's to the digits its report states.

## 1.0.0-alpha.25 (2026-11-14)

### Breaking changes

- `approval.expires_days` is read only when it is written in plain digits, such as `7`. Hex (`0x7`), octal (`0o7`, and a leading zero such as `017`), underscores (`1_0`) and a sign were read as whole numbers, and are now refused, by the approval rule and by `costroid validate`, in one sentence that names the key. `expires_days: 017` read as 15 days. With a refused value no approval counts, as with any other invalid approval setting.

### What you can now do

- Hold a run to its agent's earlier runs: `run_increase` and `run_increase_percent` compare the run with the median of the same agent's latest 20 earlier runs on the pull request, read from the `costroid` check runs on its commits. The median is the nearest-rank P50, one of the runs' costs as an exact decimal. With fewer than 10 such runs both rules still conclude `unproven` with reason `no_baseline`, as they did on every run before. A run whose figures span two currencies, or that names no agent or several, joins no baseline. [`docs/policy.md`](docs/policy.md#the-baseline) says how a run is counted.
- Give `costroid policy` the pull request's history: `--history FILE` takes the result objects of its earlier check runs, one per line, and the command reads the pull request's total, the count that ends the warn-only period and the baseline from them, as the report does. Without it, `pr_budget` and the increase rules do not run offline.
- Read each performance bar, how it is measured and its last reading in [`docs/performance.md`](docs/performance.md).
- Time the start: `make bench-start` runs `costroid --version` and `costroid policy` 30 times each, a fresh process every time, and fails when a p95 reaches 100 ms or 1 second. CI runs it on two cores on every push to `main` and every pull request.
- Hold ingest to its floor: `make bench-ingest` fails when one stream writes under 10,000 rows a second.
- Show an infrastructure estimate beside the agent's cost: `costroid ingest-run --from focus=FILE` adds a FOCUS cost file's rows, from your own estimator step, to the rows file with the basis `estimate`. The figure is read from `ListCost`, or from the one other FOCUS cost column the file holds; every row needs a currency and a charge period, and the file's tags are dropped. The comment states the rows under the agent's figure, one line per currency and period, such as ``Infrastructure, estimated: EUR 25.488 for 2026-12, from `infrastructure`.``, folds them into a table of resource, service, period and amount, and says the agent figure and the estimate are not added together. [`docs/infrastructure.md`](docs/infrastructure.md) lists the columns and the refusals.
- Give the action's `run` or `read` step `infrastructure-file`, with `infrastructure-source` and `infrastructure-optional`, and the estimate rides to the report job with the agent's rows. [`recipes/github-claude-code-action-infrastructure.yml`](recipes/github-claude-code-action-infrastructure.yml) runs an estimator before `claude-code-action`.
- With `--optional`, or `infrastructure-optional: "true"`, a run whose estimator wrote no file, as on a fork's pull request with no secret, does not fail: the comment reads `Infrastructure estimate: not available`, with the reason.
- Send an estimate row to a ledger: `costroid serve` stores it as its sender states it, shows it in the per-pull-request view as its own total under the label Estimate, and keeps it out of every budget and verdict. It refused such a row before.
- Send a day's rows to Vantage or to Datadog's Custom Costs once a day: [`recipes/github-vantage-upload.yml`](recipes/github-vantage-upload.yml) and [`recipes/github-datadog-upload.yml`](recipes/github-datadog-upload.yml) read the `costroid-rows` artifacts the repository's runs kept on the UTC day before, leave out a fork's, write the platform's file with `costroid export`, and upload it with your own token, which reaches `curl` on standard input alone. Datadog counts a file sent twice twice, so send each day once. [`docs/export.md`](docs/export.md#send-the-days-file) quotes each platform's API page and says what a second send of a day does.
- Read the estimate from the machine block: the check run's result object states each infrastructure figure in `estimates`, apart from the agent's `figures`, with its source, currency, charge period, basis, rows and amount, and an estimate that was not available with its reason. Such an object reads `costroid.result/0.10`, [`result/v0.10`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.10). A report with no estimate writes the same `costroid.result/0.9` object as before. `costroid result`, `costroid approve` and the pull request's total read both; a release before this one reads no `0.10` object.
- The GitLab template keeps `costroid-rows.infrastructure.json` in the agent jobs' artifacts, so the report can say that an estimate was not available.
- Price Gemini 4 Hydrogen at its own rows, from catalog 0.4.2. 1.0.0-alpha.24 priced it at each sheet's fallback tier: Google's USD 7.20 input, USD 36.00 output and USD 0.72 cache read per million tokens, GitHub Copilot's USD 20.00, USD 75.00 and USD 2.00, and Cursor's USD 30, USD 150 and USD 3. It now reads USD 1.50 input, USD 7.50 output and USD 0.075 cache read at Google's standard tier, on GitHub Copilot and on Cursor; USD 0.75, USD 3.75 and USD 0.0375 at Google's batch, flex and Contributor tiers; and USD 2.70, USD 13.50 and USD 0.135 at Google's priority tier.
- Google's Contributor tier ends in catalog 0.4.2 for Gemini 3.8 Flash, Gemini 3.5 Flash-Lite and Gemini 3.1 Pro Preview, because Google's pricing page no longer lists that tier for them, and with it the Contributor prices Gemini 2.5 Pro and Gemini 2.5 Flash were to take from 2026-12-01. A request at that tier for those models now prices at Google's fallback, USD 7.20 input, USD 36.00 output and USD 0.72 cache read per million tokens, and its row says it was priced at the fallback tier. Catalog 0.4.1 priced Gemini 3.8 Flash there at USD 0.375, USD 1.875 and USD 0.0375. Gemini 4 Argon and Gemini 4 Hydrogen keep their Contributor prices. The catalog's [diff report](https://github.com/Costroid/costroid-catalog/blob/v0.4.2/reports/0.4.2-diff.md) lists the 30 rows that end.

### Fixes

- `costroid validate` refuses a `policy.models` entry that YAML reads as anything but a string, such as an unquoted `2026-11-14`, which is a date, or `!x codex`, which carries a tag, in the policy's own sentence. It passed such a list, and the report then refused it and read no rule. An alias under `policy` or `approval`, such as `run_budget_usd: *shared`, now reads as the value `&shared` marks, as `validate` read it. The policy and the approval rule refused it. A YAML merge key, `<<`, is a key of that name to `validate`, as the policy, the approval rule and the comment read it, so `validate` names it as a problem; it merged the mapping the key named.
- An estimate row never raises or satisfies an agent's budget: the policy reads an agent's rows alone, in the report and in the ledger.
- `costroid lint-recipes` and `costroid lint` name, under `infrastructure-file`, an estimate input of the action that does nothing or fails its step.
- OpenCode runs read the grade `wrapper-watchdog` again, on a recorded stop: under a USD 0.001 cap, OpenCode 1.19.4 made one step at USD 0.001438275, and the wrapper stopped it before a second step. The wrapper stops OpenCode between steps, so one step can pass the cap; that run passed it by USD 0.000438275, and each row's note says so. A run that calls OpenCode's `task` tool still reads `none`.
- The first link under the comment and the GitLab note reads `Costroid Platform (not open yet)`. It read `Your account on costroid.com`, and no account opens there yet. Where it leads is unchanged.
- `costroid app create` sends the browser the page that says GitHub created the App before it stops listening on the local address. It stopped at once, so the page could be cut short, and a browser that asked again was refused.
- `costroid init github` prints `git push --set-upstream origin try-costroid` before `gh pr create --fill`, as `init gitlab` prints it.
- `costroid policy` names the `costroid.yml` it read: ``No rule applied: `costroid.yml` in this directory sets no budget and no threshold.``, or the path `--config` gives. It said `on the base branch`, which only the report reads. `costroid report --preview` already said `in this directory`.
- A run outside CI whose rows name no billing account prints one `costroid: note:` line that names `--billing-account`, where it printed a warning. In GitHub Actions and GitLab CI the line is still a warning.
- `costroid mcp --help` says `pull_result` reads the repository the git remote names when neither `--repo` nor `GITHUB_REPOSITORY` names one, as `costroid result` does. [`docs/serve.md`](docs/serve.md#prices) says how the ledger reads an estimate row's `ListCost` and a `SkuPriceId` it cannot use, and that a stated spend is at most 64 characters.

## 1.0.0-alpha.24 (2026-11-12)

### Breaking changes

- A rows entry coarser than one request, whose context tier nothing settles, is priced at the highest tier its input could reach, and its rows say the figure may read high. It was priced at the lowest tier with a note that it may read low. Such figures move up. A Claude Haiku 5.5 run of 143642 input tokens over 8 requests, with no `costUSD` from Claude Code, reads USD 0.044186675 where it read USD 0.008837335. A Codex turn of 272001 input tokens and 1000 output tokens on `gpt-5.6-sol` takes the prices above 272000 tokens. An entry that states no whole input is bounded by its own input tokens. Claude Code's own figure still picks the lower tier when it matches it.
- A model or tier a sheet does not name, priced in a region with a data residency multiplier, takes the multiplier on the fallback price. Such figures move up: an unnamed model's output reads USD 660 per million tokens on OpenAI `regional`, where it read USD 600, and USD 88 on Anthropic `us`, where it read USD 75, so the fallback stays at or above every regional price. A model the sheet prices and a multiplier leaves out keeps its global price.
- A price a residency multiplier raised names its region in `SkuPriceId`, in place of `global`, as in `anthropic:claude-opus-5-5:us:fast:all:output`. The id of a global row stood beside a multiplied price before. Fallback ids keep their shape.
- An amount in `costroid.yml` is read only when it is written as a plain decimal. Hex (`0x10`), octal (`0o17`, and a leading zero such as `017`), underscores (`1_000`) and a leading `+` were read as whole numbers, and are now refused, in the policy, in `costroid validate` and in the schema where JSON can tell. A budget of `017` read as USD 15. A refused amount leaves the policy unread, as before.
- The Helm chart and the Compose file run the release the recipes pin by default, no longer 1.0.0-alpha.1. Each release moves that default. To stay on another release, set `image.tag` in the chart or `COSTROID_IMAGE` for Compose.

### Security

- A holder of an ingest token could stall a self-hosted `costroid serve` with one long number: a decimal with tens of thousands of trailing zeros took seconds of CPU for each value, and a row could carry one. `costroid serve` now refuses an `x_CostroidOvershootBound` or a stated spend longer than 64 characters before reading it as a number. A self-hosted `serve` should move to this release.
- A decimal literal is at most 64 characters, and its trailing zeros are cut in time linear in its length. A figure read from a JSON number is refused when its plain form is longer than that.

### What you can now do

- Read where a run stands against its budget in the verdict line: `Run budget: unverified rows cannot satisfy a budget. The run cost USD 0.0391529 of the USD 5 budget.` When the check is neutral only because the agent's own job wrote the rows, the line says so, states what a run over budget concludes, and links [`docs/policy.md`](docs/policy.md), which says what lets a budget pass. With no rule set, it names `policy.run_budget_usd`.
- See the day a catalog's prices were read beside its version: the comment and `costroid version` print `catalog 0.4.1, prices read 2026-11-12`, the date the release's sheets state, and the oldest one when they state several.
- Start with two commands that need no key and spend nothing: the README leads with `costroid demo` and `costroid lint`, then names the two recipes to paste with no install. [`docs/README.md`](docs/README.md) lists every page by task.
- Trace a price to its source: [`docs/catalog.md`](docs/catalog.md) says where the catalog is published, how each price names its page and the day it was read, when a figure takes the fallback tier, and which catalog a binary carries.
- Run Copilot CLI on a Gemini model: a request Copilot serves on `gemini-*` is priced at Google's sheet, in `costroid run` and in gh-aw's usage files. Google's sheet prices no cache write, so a cache write on a Gemini model counts as uncached input, and the row says so.
- Write an amount unquoted: `run_budget_usd: 2.50` is read as the decimal 2.50, from its text, never through a float. It was refused, which left every rule unread and the check `neutral`. The schema takes a number for each amount.
- Read what changed on each release's page: its notes open with that version's section of this changelog, above the install text and the image digest.
- Pin the Marketplace listing, `Costroid/costroid-action`, to its own commit: the listing's release notes name the commit of its tag and show a job that calls it, and its README gives the `git ls-remote` line that prints it. The listing carries a `SECURITY.md` that sends reports to this repository's private vulnerability reporting.
- Add Costroid to an `openai/codex-action` workflow whose step sets no `codex-home`, such as one that sets only `openai-api-key` and `prompt`. `costroid init github --existing` gives the step what [`recipes/github-codex-action.yml`](recipes/github-codex-action.yml) has: a step that makes the directory for the job under `runner.temp`, and the `codex-home` input. A `codex-home` the `read` step cannot read is still refused, with what to do.
- Run `costroid doctor` on a GitLab project. It checks the template that installs Costroid, `.gitlab/costroid.yml` or a local file `.gitlab-ci.yml` includes, against the rules of `costroid lint-recipes`, and the release and commit it pins. It runs no `gh` there and never names `init github`.
- Run `costroid result`, and the plugin's `/costroid:result`, in a clone with no `GITHUB_REPOSITORY`: the repository is the one the git remote `origin`, or the only remote, names on GitHub, and `result` says which it read.
- Load per-pull-request rows into Harness, IBM Cloudability or Ternary: `costroid export --format harness` writes the FOCUS 1.1 CSV Harness's console takes, one file per billing period; `--format cloudability` writes a FOCUS 1.1 drop, a gzip CSV and its `costroid-Manifest.json`, for the bucket `--root-dir` names; `--format ternary` writes one FOCUS 1.2 CSV per UTC day. Each keeps every amount and currency as the row states it, and the repository, pull request, run and basis in `Tags`. [`docs/export.md`](docs/export.md#files-for-another-tool) quotes each tool's page.
- Write FOCUS 1.5's candidate columns: `costroid export --format focus-1.5-cr` fills `SkuPriceDetails` with the model and each class's `TokenDirection` and `TokenCacheAction`, keeps a cache write's lifetime in `x_CacheWriteLifetime`, fills `CredentialId` from a stated credential, and writes the schema metadata that declares `1.5-cr`. A `SubAccountId` that reads like a key refuses the file. Ratification may change the candidate, and the format is then named `focus-1.5`.
- Bring rows to Microsoft FinOps hubs: Costroid writes no Parquet, and [`docs/export.md`](docs/export.md#microsoft-finops-hubs) shows the folder the hubs page names and a DuckDB command that converts the `ternary` files.
- Price Claude Fable 5.2, Claude Mythos 5.2 and Gemini 4 Carbon at their own rows, from catalog 0.4.1. 1.0.0-alpha.23 priced them at the fallback tier. Fable 5.2 and Mythos 5.2 read USD 15 input and USD 75 output per million tokens there, about 1.9 times their standard USD 8 and USD 40, and below their fast-mode USD 16 and USD 80. They now read USD 8 input, USD 40 output, USD 10 and USD 16 for 5-minute and 1-hour cache writes and USD 0.20 for cache reads, and USD 16 and USD 80 in fast mode. Gemini 4 Carbon read Google's fallback of USD 7.20 input and USD 36.00 output. At the standard tier it now reads USD 2.00 input, USD 10.00 output and USD 0.10 cache read through 2026-11-30, and USD 4.00, USD 20.00 and USD 0.20 from 2026-12-01.
- The Anthropic fallback is USD 16 input, USD 80 output, USD 20 for a 5-minute cache write and USD 32 for a 1-hour cache write per million tokens, set by Claude Fable 5.2's fast-mode prices. It was USD 15, USD 75, USD 18.75 and USD 30. A model or tier the Anthropic sheet does not name now prices at these figures.
- Promotional prices in catalog 0.4.1 that end with no later price, and the day the sheet's fallback prices the model from: OpenAI's `gpt-5.6-sol` on 2026-12-01 and `gpt-6-astra-law` on 2027-01-01, GitHub Copilot's `gemini-4-argon` on 2026-12-01 and `gemini-3.8-flash` on 2027-01-01, and Cursor's `gpt-5.6-sol` on 2026-11-22. Cursor's end date is new; catalog 0.3.0 held that price with no end.

### Fixes

- The comment's first line names the cap that held the run, `Cap: USD 5 (agent-flag: ...)`, and over the cap says by how much. A run nothing held reads `No cap held it.`, and its table cell `none: no cap held the run`, so a cap is never called a budget.
- `Unverified: the agent's job wrote these figures.` is printed from the verdict's trust, the one the machine block's `policy.trust` states. Rows a ledger checked against the job's OIDC token read `Verified: a ledger checked the rows the verdict read against the job's OIDC token.`
- Each thing is said once above the fold: the first line, the verdict line, the table and the run line. The cap against the cost, the policy's limits and where they are set, and the agents, models, rows and catalog sit in one folded block. Warnings, failure reasons, agent errors, withheld-amounts notices, reconciliations and an agent's own figure stay above it. The check run's summary and the GitLab note carry the same text. The machine block is unchanged.
- The docs no longer say that OpenCode, the SDK recipes or `init --pr` need a later release: each runs on the release the recipes pin. A docs test fails on such a sentence.
- The docs name one row contract, on [`docs/export.md`](docs/export.md#the-row-contract): `focus-row/v0.5` with `tags/v0.4` by integration contract v0.3, and `focus-row/v0.3` with `tags/v0.2` by v0.2. A docs test reads both from the code.
- [`docs/existing-workflow.md`](docs/existing-workflow.md) shows the workflow `init --existing` writes, cap lines included, and no longer contradicts itself on where the cap goes.
- The `costroid.yml` that `init` writes leaves out `policy.monthly_budget_usd`, which no check reads yet, and names the schema at the release it pins in place of `main`.
- The troubleshooting page lists every line of `costroid-install.sh`, and `GITHUB_TOKEN is not set`, `gh repo view failed` and `run needs --budget-usd`.
- OpenCode runs read the grade `none`. The wrapper still stops the run at the cap between steps, but no recorded run has proven that stop, and a grade is claimed only on a recorded stop. Each row's note says so.
- A Copilot CLI request on a model no sheet prices wrote no row, and the run kept `wrapper-watchdog` though the watch could not count it. The run now reads `none`, and the grade's note and the warning name the model, where the warning said the provider `"copilot"` had no sheet.
- One map names the provider whose sheet prices a model, from the start of its name, and Copilot CLI, gh-aw and `costroid serve`'s telemetry reader share it. Telemetry no longer refuses `o3`, and Copilot CLI no longer drops `codex-*`.
- `costroid serve` reads a GenAI span whose cache counts pass its `gen_ai.usage.input_tokens` as one from a sender that leaves the cache out of its input, and notes the reading on each of the request's rows. It refused such a span before.
- `costroid reconcile` proves the price of a row written by 1.0.0-alpha.23 or earlier in a data residency region, on a model with several context tiers. Such a row carries the global price id beside the regional price, and it read "not proven" once regional ids named their region. A global id now names the same tier's id in the row's own region, and only that.
- `costroid serve` prices a row whose `SkuPriceId` states no context tier at the highest tier its input could reach, and the row says it may read high: the sheet's top tier, or its fallback when the top tier has an upper bound. It took the lowest tier. A tier bounded on both sides, such as `over-256000-to-500000` on the Cursor sheet, now prices as stated; it also took the lowest tier.
- A release waits for `ci`: a tag on a commit with no successful `ci` run on a push to `main` publishes nothing, and neither does a version with no section in this changelog.
- The image is pushed by its digest, signed and attested, and only then tagged with its version, so a failed signature leaves no version tag on ghcr.io.
- The image's binary records the commit it was built from, as the release binaries do, so `costroid init` run from the image pins that commit without reading the tag from github.com.
- `costroid doctor` tells a repository that is not on GitHub yet, which `gh` answers with a 404, from a missing sign-in. One line says to push the repository. Only a missing or refused sign-in says `run gh auth login`. Outside a git repository, doctor says so in a `fail` line, checks this machine alone and exits 1.
- `costroid run` refuses a command no adapter reads before it creates a file. It used to leave an empty rows file and a result object with the reason `empty_rows_file`, so the report said the agent printed no usage. The report now finds no rows file, which is what happened.
- `costroid result` with no `GITHUB_TOKEN` prints the line that sets one, `export GITHUB_TOKEN="$(gh auth token)"`. It never runs `gh` itself.
- `costroid --help` lists `demo`, `lint`, `init` and `doctor` first, in the order a first reader meets them, and no longer under other commands.

## 1.0.0-alpha.23 (2026-11-11)

### What you can now do

- The comment says when a Claude Code run signed in on a Claude subscription. Beside the figure of a run whose `apiKeySource` is `none`, that holds a `rate_limit_event`, and that no gateway bills, it prints `subscription: list-equivalent, no provider report to reconcile`. The figure, its basis and every total are unchanged. `none` alone never marks a run, so a subscription run without the event stays unmarked. The rule rests on a recorded `claude-code-action` run on a subscription's token ([`docs/adapters.md`](docs/adapters.md)).

## 1.0.0-alpha.22 (2026-11-11)

### Security

- Built with Go 1.27.4, which fixes GO-2026-6859: an HTTP/1 server could crash on a header race in `net/http`'s `MaxBytesReader`. `costroid serve` is an HTTP server and its webhook route reads bodies that way, so a self-hosted `serve` should move to this release. The binary, the container image and every release file are built with it.
- `golang.org/x/crypto` is v0.58.0. GO-2026-5932, in its OpenPGP package, never reached Costroid's code; the bump leaves no known vulnerability in the module graph.

## 1.0.0-alpha.21 (2026-11-11)

### What you can now do

- Group an agent's own telemetry by pull request without a vendor exporter: the `claude-code-action` and `codex-action` recipes set `OTEL_RESOURCE_ATTRIBUTES` on the agent step with `vcs.change.id`, `vcs.ref.head.revision`, `vcs.ref.base.revision`, `vcs.repository.url.full`, `cicd.pipeline.run.id` and `cicd.pipeline.task.run.id`, the values `costroid run` already gives the agents it starts. `costroid lint-recipes` rejects an agent step outside `costroid run` that lacks one; `costroid lint` notes it.
- `costroid export` sends by integration contract v0.3, rows `focus-row/v0.5` and tags `tags/v0.4`, to a platform that states it, and by v0.2 otherwise, saying which. Outside GitHub Actions, a token that may not read the platform's contract keeps sending by v0.2, with one warning. The contract is published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).

## 1.0.0-alpha.20 (2026-11-10)

### What you can now do

- Know whether a run's repository is public: rows from `costroid run` and `ingest-run` carry `costroid/repo_visibility`, `public`, `private` or `internal`, read from the GitHub event's `repository.visibility` (else `repository.private`) or GitLab's `CI_PROJECT_VISIBILITY`. No network call is made for it, and when the CI system does not say, the tag is left out. `costroid serve` stores it. The row contracts `tags/v0.4` and `focus-row/v0.5` are published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).
- `costroid lint` knows Copilot CLI and OpenCode by their command: a `costroid run -- copilot -p ...` or `costroid run -- opencode run ...` step with no `--agent` is checked as that agent, with the same rule `costroid run` uses to pick it.

## 1.0.0-alpha.19 (2026-11-09)

### Breaking changes

- The result object is `costroid.result/0.9`. `agent_stated` also takes OpenCode's own cost in USD and Copilot CLI's credits in nano-AIU, each from its own source. `costroid approve` and the readers still take 0.5 to 0.8.

### What you can now do

- See an agent's own figure beside Costroid's: the comment states OpenCode's cost for the run "by its own prices, which Costroid never adds to its own figures", and Copilot CLI's credits, "which Costroid never converts to dollars".
- `costroid run --agent copilot` names Copilot CLI, as `--agent opencode` names OpenCode. `costroid lint` checks a workflow that runs `costroid run --agent copilot` for Copilot CLI's provider variables and its tool rules.
- Register a GitHub App whose private key never touches a disk: `costroid app manifest` prints the App's minimum permissions, and `costroid app create` runs GitHub's manifest flow and imports the key GitHub returns straight into an AWS KMS key, writing no copy. With `COSTROID_GITHUB_APP_ID` and `COSTROID_GITHUB_APP_KMS_KEY` set, `costroid serve` signs the App's tokens through that key, and the approve button on a check posts. [`docs/github-app.md`](docs/github-app.md) has the steps.

### Fixes

- An unknown agent's error names every agent `costroid run` takes.

## 1.0.0-alpha.18 (2026-11-09)

### What you can now do

- Run OpenCode under a budget: `costroid run --agent opencode -- opencode run ...` reads each step OpenCode prints, prices it and stops the run at the cap. Costroid adds `--title` when the command has none, so OpenCode sends no title request of its own that no row would count, and reads the agent's version. Rows read `measured` on a provider key with no OpenCode sign-in for that provider. OpenCode's own cost for the run is stated beside Costroid's figure and never replaces it. A `task` subagent spends in a session of its own, which the stream does not show, so a run that calls it is graded `none` and says so. `recipes/github-opencode.yml` runs it in a pull request.
- Run Copilot CLI with your own model provider under a budget: `costroid run -- copilot -p ...` names a new session, reads its session file as each request lands and stops the run at the cap. Each model's rows must add up to Copilot CLI's own totals for the session, or the run is refused with the model and the field. Copilot's own credits are stated as it writes them, never converted to dollars. Rows read `measured` on your own provider key; a request Copilot served on its own model routing is priced at the provider's list rate and reads `list_equivalent`. Only a run on your own key has been recorded.

## 1.0.0-alpha.17 (2026-11-09)

### What you can now do

- Receive a GitHub App's webhook in `costroid serve`: set `COSTROID_GITHUB_WEBHOOK_SECRET` and `POST /webhooks/github` checks each delivery's signature before it reads the body, refuses a delivery it has already taken (for 72 hours, GitHub's redelivery window, across instances and restarts), and keeps the App's installations and their repositories. The migration `0008` adds three tables that hold ids and names, never a token. The approve button on a check re-concludes it as `costroid approve` does; it posts only once the App's key is configured. [`docs/serve.md`](docs/serve.md#the-github-apps-webhook) has the details.
- `costroid doctor` lists the secrets of the environment a job names, so a key stored as an environment secret reads as found.
- The GitLab template ran end to end on a self-managed GitLab CE 19.5.1 with GitLab Runner 19.5.1: over budget the merge is refused, a second push edits the one note, under budget the merge goes through. It has not run on gitlab.com. `go test -tags gitlab ./e2e/gitlab` runs it again. [`docs/gitlab.md`](docs/gitlab.md) says what it proved.

### Fixes

- With a LiteLLM gateway, `costroid run` and `costroid gateway close` read the key's spend only after it settles. LiteLLM writes spend every 10 to 15 seconds, and the run stopped waiting after 12, so it could warn that the gateway counted USD 0 when the rows did not, and close could attest that USD 0. A figure settles when it changes and then holds, or holds for 30 seconds; one that never settles carries a warning.
- On GitLab, the report says it commented on a merge request.

## 1.0.0-alpha.16 (2026-11-08)

### What you can now do

- Label a run's rows: `costroid run` and `ingest-run` take `--tag work_item=ENG-142`, `--tag arm=b` and `--tag team=platform`, written to `Tags` as `costroid/<key>`. Any other key, a value outside the allowlist or a value shaped like a secret exits 125. `costroid serve` stores rows carrying the three tags. [`docs/tags.md`](docs/tags.md) has the rules.
- Merge Costroid's rows with your provider's own FOCUS data without counting a run twice: `--focus-profile strict` writes `BilledCost` and `EffectiveCost` as 0 and carries the figure in `x_CostroidMeasuredCost`. The default rows are unchanged; send those to Costroid Platform. [`docs/focus-profile.md`](docs/focus-profile.md) says why.
- Send Gemini CLI's telemetry to `costroid serve`: its `gemini_cli.api_response` logs are priced, with cached tokens as cache reads, thinking tokens as output and tool results as input. A request whose counts do not add up to Gemini's own total carries a note that its figure may read low or high. This dialect is built from Gemini CLI's source at v0.67.0, not from a recorded run.
- The row contracts `tags/v0.3` and `focus-row/v0.4` are published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).

### Fixes

- `costroid reconcile` and the accuracy page print one percentage: a provider figure of zero reads `n/a`, and a negative one keeps its sign.

## 1.0.0-alpha.15 (2026-11-08)

### Breaking changes

- The result object is `costroid.result/0.8`. It adds `recorded`: the CI run and the day of a recorded run whose figures a report shows in place of the pull request's own, and null otherwise. `costroid approve` and the readers still take 0.5 to 0.7.
- `costroid report --preview` refuses a `--rows` path that holds no file, with exit 125, instead of printing a comment with no figures.
- `costroid demo` prints plain text for a terminal. `--markdown` prints the comment as before.
- The pull request comment's account link goes to https://costroid.com/account/ when the report names no installation and no platform.

### What you can now do

- Show a recorded run on a pull request that spends nothing: `costroid report --recorded RUN_ID,DATE` opens the check, the comment and the job summary with one line that says the figures are that run's, with its date, not the pull request's. A recorded report is left out of the pull request's total.
- Send a rows file to an OpenTelemetry Collector or a tracing backend: `costroid emit-otlp --rows FILE` sends one span per model request over OTLP/HTTP, with the GenAI attributes and the request's exact cost. [`docs/otlp.md`](docs/otlp.md) lists every attribute.
- Read a gh-aw run's cap: `ingest-run --from gh-aw=DIR` also reads `info/aw_info.json` beside `DIR`, so the comment's Enforcement cell names the AI credit cap gh-aw checks. gh-aw's firewall checks it before each request, so a run can end past it; when gh-aw's stated credits pass the cap, the cell states by how much, in AI credits. `recipes/github-gh-aw-follow-up.yml` downloads both artifacts. The reader admits every file gh-aw v1.0.1 to v1.2.1 writes to its `usage` artifact.
- `costroid demo --state neutral|capped|failure|skipped` shows each kind of comment.
- `costroid run` without `--out` writes `costroid-rows.jsonl` in the working directory, the name the action uses, and says so before the agent starts.

### Fixes

- `costroid reconcile` judges a run that crosses 00:00 UTC by token class across its days.
- A preview states its own sentences in the comment, instead of rewriting the comment's.
- `costroid serve` reads `costroid.cost.basis` from OTLP spans.
- A per-run entry's context tier is chosen where its rows are priced, and a coarse row whose tier is unknown says its figure may read low.
- A command's help prints on stdout when `-h` follows other flags.

## 1.0.0-alpha.14 (2026-11-08)

### Breaking changes

- `costroid reconcile` stops before any request when the rows name a gateway run key and no `--upstream-credential SOURCE=ID` says which provider key the gateway sends with. Give the flag once per source.

### What you can now do

- Install in one step: the release carries `costroid-install.sh`, which checks the release's signature with cosign and the binary against `SHA256SUMS` before it installs.
- `go install github.com/Costroid/costroid/cmd/costroid@<release>` prints its release, and `init` pins the commit that release's tag names.
- Reconcile a service account under Workload Identity Federation by its actor, and a gateway's rows under the provider key the gateway sends with. A run that crosses 00:00 UTC is compared across its days and named as the cause, never as a miss.

### Fixes

- A run whose rows fell to per-model grain could read up to five times its bill. A per-model sum of input tokens took the long-context tier: one `claude-code-action` run on `claude-haiku-5-5` read USD 0.044186675 against Claude Code's own USD 0.008837335. A sum of several requests now never selects a long-context tier; Claude Code's own per-model cost decides it, and where nothing decides, the rows take the standard tier and say the figure may read low.
- Session files that hold every conversation request now stand when the result also counts Claude Code's one request outside the conversation; that request becomes an entry of its own, and each request keeps its own tier.
- A Codex turn whose summed input passes a context boundary takes the standard tier with that note, instead of the tier above.

## 1.0.0-alpha.13 (2026-11-08)

### Breaking changes

- The result object is `costroid.result/0.7`. It adds `agent_stated`, an agent's own figure in its own unit, and the run reasons `unmeasured_spend` and `no_run_rows`. `costroid approve` and the readers still take 0.5 and 0.6, and refuse an older object that carries a value only a newer version defines.
- The report states the cap the run recorded, from the run's result object. The report step needs no budget input; `--budget-usd` is read only when no result object exists, and a figure that disagrees with the object is ignored with a warning. Copy the recipes again.
- Runs price with catalog 0.3.0. Its fallback for a model the sheet does not name is the highest price of each token class across every row, so an unknown OpenAI model's output is priced at USD 600 per million tokens, labelled as the fallback. Gemini 2.5 Pro and 2.5 Flash bill at their successors' rates from 2026-12-01, as Google states.
- A gateway run gives Claude Code a settings file of its own, so a repository's `.claude/settings.json` cannot send requests around the gateway or choose the key through `apiKeyHelper`.
- `costroid connect` binds a GitLab namespace only on gitlab.com.
- `costroid lint-recipes` flags a workflow that states two different caps, and checks the plugin marketplace.

### What you can now do

- Read a GitHub Agentic Workflows run: `ingest-run --from gh-aw` reads its `usage` artifact, `follow-up --ingested` posts the check, and `recipes/github-gh-aw-follow-up.yml` wires both. gh-aw's AI credits appear on a line of their own, never added to a dollar figure.
- Ask Costroid from an agent: `costroid mcp` serves four read-only tools over stdio, and `costroid result --pr N` prints the newest check's result object.
- Install the Claude Code plugin from this repository: `/plugin marketplace add Costroid/costroid`, then `/plugin install costroid@costroid`.
- Run the Claude Agent SDK or the Codex SDK under a budget: `costroid run --agent claude-code-sdk|codex-sdk -- <program>`, with `recipes/github-claude-agent-sdk.yml` and `recipes/github-codex-sdk.yml`. `--agent` also places Costroid's flags after an `npx`, `bunx` or `node` package.
- Go from nothing to an open pull request: `costroid init github --pr` sets the key secret, commits, pushes, opens the pull request and prints the comment's URL, naming each command before it runs. `--skills` writes an agent skill; `--platform-url` also writes the connect workflow; `--existing` takes `--budget-usd`.
- Write the rows for another tool: `costroid export --format focus-csv`, `vantage` or `datadog`. No amount is converted or summed across currencies.
- `costroid doctor` sends a `claude-code-action` repository to `init --existing`, names a missing remote, reads aliased triggers, and checks the platform token's scopes and the owner's binding.
- The image and the release carry `NOTICE` and `LICENSE`, and `NOTICE` names every module the binary includes.
- `costroid init github --agent claude-agent-sdk` and `--agent codex-sdk` write the SDK recipes, and `costroid lint` checks an SDK job as an agent job, egress included.
- Every help page opens with `Usage:`, names every flag and ends with its exit codes. `costroid` with no command prints usage and exits 2.
- CI verifies the embedded catalog's signature with cosign against the catalog's release workflow, and checks every sheet against the signed sums.

### Fixes

- A request reported in two OTLP batches is stored as one write row.
- Every lint finding stays on one line, whatever the value it quotes.
- `costroid connect` retries once when the platform's key set is unavailable, after its `Retry-After`.
- An empty budget from an environment expression reads as no budget, never as zero.
- The SDK agent reads a session line written in the run's first millisecond.
- `gateway close`'s help gave the wrong default for `--retry-after`; it is 0.

## 1.0.0-alpha.12 (2026-11-08)

### Breaking changes

- The GitHub recipes run `step-security/harden-runner` on the agent's job in block mode, with its telemetry off. The job reaches only the model provider, GitHub and the hosts the install needs. Copy the recipes again. The LiteLLM recipe needs the repository variable `COSTROID_GATEWAY_HOST`.
- The `codex` recipe reads the secret `OPENAI_API_KEY`, which the action gives Codex as `CODEX_API_KEY`.
- `costroid init` writes its files at the repository's root, from any directory inside it. Outside a repository it refuses with exit 2.
- `costroid reconcile` exits 1 on a price that misses the catalog, as it does on a token miss.
- A gateway run refuses an agent command that routes around the gateway: Codex's `--oss`, `--local-provider` and its own provider or base URL, and Claude Code's `--settings` naming a base URL, a token or a cloud provider.
- With no account named, the comment's account link opens [docs.costroid.com/account](https://docs.costroid.com/account). On GitLab the link names `gitlab-namespace:<id>` on gitlab.com and no id on a self-managed instance.

### What you can now do

- Hold `claude-code-action` to a cap: pass `--max-budget-usd` in `claude_args` and the same figure to the `read` stage as `budget-usd`, with the new `claude-args` input. The grade then reads `agent-flag`, and `none` with the reason when the two differ. `costroid lint` notes an action step without the flag.
- Run Codex through a LiteLLM gateway on a key made for the run. Codex takes the gateway's address and provider on its command line, which override its own configuration, and its rows read `measured`. Claude Code's gateway rows read `measured` too.
- Watch a run while it works: `costroid run` writes `$COSTROID_STATUS_FILE`, by default `$RUNNER_TEMP/costroid-status.json`, after each priced request. It holds spend per basis, the cap, the grade and counts, never content. Its schema is `costroid.status/0.1`.
- Fail a GitLab merge request's pipeline on its verdict: set `COSTROID_GATE`, and the report job exits 1 on `failure` and 3, a warning, on `neutral`. [`docs/gitlab.md`](docs/gitlab.md) has the template.
- Send the rows to Costroid Platform from the recipes, bind the repository's owner with `costroid connect` and the new `recipes/github-connect.yml`, and read each batch's verdict from `costroid export`.
- Post the agent's own answer as a second comment, opt-in with `post-review: "true"`. The comment shows it as plain text and says Costroid did not write or check it.
- Prove each price from the provider's own figures: `costroid reconcile` reads Anthropic's cost report by model and token type and OpenAI's by line item, and compares each class's dollars within 1%.
- Sign in with a Claude subscription: `costroid init github --agent claude-code-action --auth subscription`.
- Verify a release's build provenance with `gh attestation verify`, on each release file and on the image.

### Fixes

- A per-run row prices Claude Code's input at the context tier of the model's whole input, carries fast mode and a region seen on any message, and composes fast mode's cache prices from the fast input price.
- The follow-up names its own check run in its result object.
- `costroid approve` re-concludes a check that an earlier release wrote as `costroid.result/0.5`.

## 1.0.0-alpha.11 (2026-11-07)

### Breaking changes

- A rows artifact that holds anything besides the rows and the run object now fails the `costroid` check with the run reason `artifact_refused`, naming the entries it refused. It used to skip the report, so no check posted. An approval does not turn it neutral.
- `costroid report` skips an `--installation-id` in neither form `github:<id>` nor a bare number with a warning, and posts without the account link.
- `costroid lint-recipes` refuses `id-token: write` on a gateway job that names no environment, `environment: ""` included.

### What you can now do

- Check a repository's setup with `costroid doctor`: each problem on one line, with its fix. A secret the repository's own list lacks is a warning, because an organization or environment secret does not show there.
- Pass a budget on `costroid serve` only on spend the gateway measured: `costroid gateway close` attests the key's spend, and a budget concludes `success` only on that figure or a reconciled provider report, never on the agent's own counts.
- End the warn-only period at the twentieth pull request with a measured cost, counted from the earlier `costroid` checks.
- Read an empty `costroid.yml`, a file that is only `---`, `~` or `null`, or a section with no value as the defaults, in `validate` and every reader alike.
- Read `openai/codex-action` rows as `measured`: that action takes only an API key.
- See which bases a pull request's budget adds together, on the comment's line.

### Fixes

- The Anthropic usage reader refuses a negative token count, as the OpenAI reader does.
- `costroid validate` refuses `approval.expires_days` written as a float.
- `costroid lint-recipes` reports an agent it does not know instead of stopping.
- A data race between the agent's stderr and the run's own warnings, seen under `go test -race`.

## 1.0.0-alpha.10 (2026-11-07)

### Breaking changes

- The result object is `costroid.result/0.6`. It adds `rows_sha256` and `rows_file_sha256`, which name the rows a report was built from, a `lifetime` object with the pull request's total, the policy's new rule names, and the run reason `artifact_refused`.
- `costroid validate` refuses an amount written as a float, such as `5.0`, as the policy does. Write `5` or `"5.00"`.
- The GitLab template runs `costroid:report` and `costroid:ingest` in the `.post` stage, follows the agent's label rule, and pins `COSTROID_COMMIT` beside `COSTROID_VERSION`. Copy the template again.
- `costroid` with no command prints a short usage. `costroid help <command>` prints one command's flags.

### What you can now do

- Read what the run cost first: the comment and the check open with the figure, its basis and its grade.
- See the pull request's total across its runs, one line per currency and basis, read from the earlier `costroid` checks. Repeated reports are counted once.
- Hold a pull request to a budget across its runs (`policy.pr_budget_usd`), a model allowlist (`policy.models`), and an increase in USD or percent. Budgets accept whole numbers.
- Approve a failing check with a pull-request review from a writer who is not the author. `recipes/github-approval.yml` re-posts the check as `neutral`, naming the approver.
- Write the workflow with `costroid init`, pinned to the release the binary belongs to, and add Costroid to an existing workflow with `costroid init --existing`.
- Lint your own agent workflows with `costroid lint`: an agent reachable by an untrusted actor is a high finding. `claude-code-action`'s own check of who triggered it counts as a guard.
- Measure `openai/codex-action` runs with `recipes/github-codex-action.yml`. The action takes the key from `CODEX_API_KEY` or `OPENAI_API_KEY`.
- Print the comment for a rows file with `costroid report --preview`, or a recorded run's with `costroid demo`.
- Trust that a run leaves nothing behind: on Linux, `costroid run` kills and reaps every process the agent left, `setsid` children included.

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
