# Show your own sessions on the pull request

Costroid's check counts what the agent spent in CI. Work you did with Claude Code on your own machine, on the same branch, never reaches a CI run. `costroid receipt` counts the tokens of those sessions and prints them as a comment. You post the comment on the pull request, and the next report prices it and states it on one line, apart from the run's cost.

No budget, rule, total or verdict reads a receipt. The check, its title, its conclusion and the step's outputs are the same bytes with or without one.

## Privacy

`costroid receipt` reads token counts and the few facts beside them from the session files on your machine: the model, with its service tier and region, the agent's version, the session's id, the times it ran, of which the receipt keeps the days, the working directory it ran in and the branch it recorded. It compares the working directory with the repository's and never prints it. It does not read a prompt, an answer, a tool's input or output, or a file's contents, and it sends nothing anywhere. It prints the receipt. You read it and post it yourself. Once posted, everyone who can read the pull request can read it. Token counts with their days say when, and roughly how much, you worked with an agent on this branch. If you do not want that on the pull request, do not post the receipt: the check, the budget and the run's figures are the same without it.

## Print a receipt

Run the command in the repository, on the pull request's branch:

```sh
costroid receipt > receipt.md
```

Standard output is the comment's body alone. Standard error says how many requests it counted and how many it left out, and why. Read the body, then post it as a comment of its own. GitHub's own CLI posts it as printed:

```sh
costroid receipt | gh pr comment <number> --body-file -
```

Or paste the body into a new comment in the browser. A browser can store a comment with CRLF line endings, and a copy can gain or lose a final line feed: the report reads each CRLF as LF and ignores blank lines after the closing fence, and changes nothing else, so a space added at the end of a line refuses the receipt.

The command opens no connection and holds no token. It refuses to run in a CI job, where the run's own rows already hold the agent's sessions, and on a detached head, which has no branch. This release reads Claude Code alone.

## What it counts

Claude Code writes one session file per session under `CLAUDE_CONFIG_DIR`, or `~/.claude` when that is not set. Each line records the working directory and the git branch of the request it answers. A request counts when both hold:

- The working directory its line recorded is the repository's root, or under it, for any worktree `git worktree list` names.
- The branch its line recorded is the branch checked out.

A session that moved to another branch counts up to the line where it moved. A subagent's requests count under the session that started it, by the same rule.

The oldest entry of the branch's reflog on your machine is the lower bound: a request made before it is not counted, so a branch name used again does not count the earlier branch's sessions. When the reflog holds no entry for the branch, the receipt states `since` as null and has no lower bound.

The receipt holds one entry per session, model, service tier and region: the requests counted, the largest input of any one of them, the token counts by billed class, and the first and last UTC day. A receipt holds at most 150 entries in one comment of at most 65,536 bytes. Past that, the oldest sessions are left out whole, and `sessions_omitted` counts them.

What a receipt cannot see, so its count can read low: planning on the base branch before the branch existed, a session on another machine, in another clone or in a container, a session Claude Code deleted under its `cleanupPeriodDays` setting, a session that wrote no file, and a request Claude Code wrote to no session file. Work on the branch unrelated to the change counts, so it can read high. A receipt is never evidence that a pull request was cheap.

The receipt's contract is [`receipt/v0.1`](https://github.com/Costroid/costroid-schemas/tree/main/receipt/v0.1): every string is a closed word, a day or a pattern, and a key the contract does not define refuses the whole receipt. A receipt states counts and never an amount.

## Turn it on

A workflow asks for receipts with `receipts: "true"` on the step that uses `Costroid/costroid/action` with `stage: report`:

```yaml
        with:
          stage: report
          receipts: "true"
```

It is off by default, and only the report's step reads it: `costroid lint-recipes` and `costroid lint` name a step of another stage that sets it. The report job needs no permission it does not hold: it lists the pull request's comments already to find its own.

The report reads the newest comment that opens with `<!-- costroid:receipt -->` and that the pull request's author posted. It reads no other account's comment, and never takes Costroid's own comment for a receipt. A fork's pull request gets no receipt in this release, and neither does a merge request on GitLab.

The line appears on the first report after the comment is posted: push a commit, or re-run the report job. A receipt posted, edited or deleted after a report leaves that report as it was.

## What the line says

The comment, the check run's summary and the job summary carry the same line under the run's figures:

```text
Developer-machine sessions, unchecked: USD 11.4619213 at list, 2 sessions of Claude Code from 2026-11-18 to 2026-11-20 (UTC), as `octocat` stated them in [a comment](https://github.com/example/widgets/pull/7#issuecomment-2471000001) on 2026-11-20. No budget reads this figure, and it is not added to the run's.
```

Every figure, login, day and link in that line is an example.

The report prices the counts at the catalog its own binary carries, never at a price the receipt states. The basis is list-equivalent: a session file does not say how the session was billed. A model the catalog does not name takes the catalog's fallback price, never zero. A session the run's own rows also hold is left out of the receipt, so no session counts twice: `sessions`, in the line and in the result object, counts the sessions read after those are left out, and the line names how many were left out when any were.

Where the workflow asks for receipts and the author posted none, the line says so:

```text
No developer-machine sessions stated for this pull request. The figures here are the CI run's alone.
```

A receipt the report refuses reads none of its sessions, and the line says why:

| Code                   | Why                                                                                         |
| ---------------------- | ------------------------------------------------------------------------------------------- |
| `receipt_edited`       | The comment was edited after it was posted. A new count is a new comment                    |
| `receipt_other_branch` | The receipt names another branch than the pull request's                                    |
| `receipt_invalid`      | The comment is not a valid receipt. `costroid receipt` prints a valid one                   |
| `receipts_off`         | `costroid.yml` on the base branch turns receipts off                                        |

Where amounts are withheld, the line names the sessions, the days and the comment, and no amount.

The result object states the receipt under `receipts`, as [`result/v0.13`](https://github.com/Costroid/costroid-schemas/tree/main/result/v0.13#receipts) defines it: the comment's id, its creation time and the SHA-256 of its body, then the sessions, the days and the figure, or the code it was refused for. It names no login. A report that holds no receipt writes the object it wrote before, byte for byte.

## Turn it off

Set `comment.receipts` to `false` in `costroid.yml` on the base branch:

```yaml
comment:
  receipts: false
```

The report reads the base branch's file, so a pull request cannot turn receipts back on. A receipt posted where they are off is refused for `receipts_off`, and none of its sessions is read.
