# Post the agent's answer on the pull request

The recipes ask the agent for the bugs and the security problems a change introduces. The agent's job holds `contents: read` and nothing more, so it cannot post what it found, and Costroid's report shows only what the run cost. With `post-review: "true"`, Costroid posts the agent's own answer as a second comment, apart from the report.

It is off by default. Nothing posts the answer unless the workflow turns it on.

## Turn it on

Add `post-review: "true"` to two steps that use `Costroid/costroid/action`: the agent's step (`stage: run` or `stage: read`) and the report's step. A report's step without it refuses an artifact that holds the answer, and its check fails with `artifact_refused`. The ingest step needs nothing: it reads the rows alone, takes an artifact that holds the answer whatever `post-review` says, and deletes the answer unread.

```yaml
        with:
          stage: report
          post-review: "true"
```

The report's job in each agent recipe already holds `pull-requests: write`, which is all the second comment needs.

## What the agent's job keeps

After the run, the agent's step reads the answer the run ended with and writes it, and nothing else, to `costroid-review.md` in the `costroid-rows` artifact:

| Agent | Where the answer is read | The answer |
| ----- | ------------------------ | ---------- |
| `claude -p`, under `stage: run` | The stream the action keeps on the runner | The `result` field of the last `result` message |
| `claude-code-action`, under `stage: read` | Its `execution_file` | The same |
| `codex exec`, under `stage: run` | The stream the action keeps on the runner | The last agent message of the last turn that completed |
| `codex-action`, under `stage: read` | Its `codex-home` | The `last_agent_message` of the last completed turn, in a thread no other thread started |

Nothing the agent wrote or read on the way is kept: no reasoning, no reply before the last, no command and no file. A run that stopped at its cap, failed or never finished has no answer, and the step writes no file. The step never fails the agent's job.

Under `stage: run` the answer is read from the file the stream goes to. With `show-output: true` the stream goes to the job log instead, and no answer is kept.

## What the comment shows

The comment opens with its own marker, `<!-- costroid:review -->`, apart from the report's. Each run edits the comment the token's account posted earlier, so a pull request has one at most. A comment someone else wrote is never edited, even if it holds the marker.

It says the text is the agent's own answer from its run on the head commit, and that Costroid did not write or check it. The answer follows in a fenced code block, as plain text:

- The fence is backticks or tildes, whichever the answer holds the shorter run of, and longer than that run, so no line of the answer can close it. No link, mention, image, issue reference or HTML in it renders.
- Control characters and the characters that reverse the direction of text show as `�`.
- The comment shows the first 30000 bytes of the answer, and a line says when it cut. The artifact keeps up to 1 MiB.

When the agent's job kept no answer, no comment is created. If an earlier run posted an answer, that comment is edited to say this run kept none, so an answer from an earlier commit never stands for a later one. The agent's job's log says why.

## What it does not do

Costroid never reads this comment, or its own report, back into a prompt. Each run starts from the `prompt` in the workflow. Build that prompt from the workflow file alone, never from pull request text or from a comment.

The answer is the agent's text, written after it read the pull request. An injected agent can write anything into it. Costroid shows it as data and labels it as the agent's. It is not a review by Costroid, and it is not checked.

On a public repository, the comment and the artifact are public. The answer can quote any file the agent read.

`costroid help review` gives the command's flags. [`docs/troubleshooting.md`](troubleshooting.md#posting-the-agents-answer) lists each line it prints.
