# Read what a command prints

Every `costroid` command writes to two streams. Standard output holds what the command was asked to print, and nothing else, so it can be saved or piped as it is. Standard error holds what Costroid says about its work: warnings, errors, what it did, what it waits on and what to run next. The exit code is on [`exit-codes.md`](exit-codes.md).

## What is on standard output

| Command | Standard output |
| ------- | --------------- |
| `costroid demo` | The comment, then the verdict sentence. With `--markdown`, the comment alone |
| `costroid lint`, `costroid lint-recipes` | One line per finding, the notes last, then the count |
| `costroid doctor` | One line per check, then the count |
| `costroid validate` | One line per problem, or that the file is valid, or that there is none |
| `costroid policy` | The verdict's lines, then the verdict sentence |
| `costroid receipt` | The receipt, as the body of the comment to post |
| `costroid version` | The release and the catalog |
| `costroid result` | The check's result |
| `costroid run` | The agent's own output |
| `costroid report --preview` | The comment |
| `costroid init --existing` | The patch |
| `costroid init --pr` | What the commands it runs print on their own standard output |
| `costroid export --format`, without `--out` | The file |
| `costroid merged --dry-run` | One object per merge fact |
| `costroid reconcile` | The comparison |
| `costroid egress report` | The report |
| `costroid keys create` | The key |
| `costroid gateway keygen`, `public-key` | The public half of the sealing key |
| `costroid gateway request`, `mint` | The key's id |
| `costroid gateway close` | `deleted` or `absent` |
| `costroid app manifest` | The manifest |
| `costroid mcp` | The protocol |
| `costroid help`, `--help` | The help page |

Every other command prints nothing on standard output unless `--json` is given: `init`, `report`, `ingest-run`, `follow-up`, `approve`, `review`, `export` to Costroid Platform or to `--out`, `merged`, `connect`, `emit-otlp`, `serve`, `keys revoke` and `app create` say what they did on standard error.

## At a terminal and in a pipe

A program reads a line whole, so into a pipe, a file or a job's log every command prints each line unbroken: one check of `costroid doctor` to a line, one sentence of `costroid policy` to a line, one `costroid:` line of `costroid init` to a line.

At a terminal `costroid doctor`, `costroid init` and `costroid policy` wrap those lines at 80 columns for the person there. A check of `doctor` and a `costroid:` line of `init` carry on under their own text, so the state or the name stays the first word. `policy` sets its sentences a blank line apart, and the last paragraph is the verdict sentence. Nothing but the line breaks differs, and no word is broken: a path or an address wider than a line keeps a line to itself. The object `--json` prints is the same in both.

## `--quiet`

`-q` or `--quiet`, on any command, keeps warnings and errors on standard error and drops every other line there. Standard output and the exit code do not change.

```sh
costroid report --rows rows.jsonl --quiet
```

Two lines stay under `--quiet`, because the command then waits on a person: the prompt of `costroid init --pr` for the key, and the address `costroid app create` asks you to open. `costroid serve --quiet` keeps the warnings and errors of its log.

## `--json`

`--json` prints one object on standard output and nothing else there. Standard error and the exit code are as without the flag.

| Command | Object | Described in |
| ------- | ------ | ------------ |
| `costroid run`, `costroid report`, `costroid result` | The result object, `costroid.result/<version>` | [`agents.md`](agents.md) |
| `costroid policy` | `costroid.policy/0.1`: the verdict, as the check's machine block carries it | [`policy.md`](policy.md) |
| `costroid reconcile` | `costroid.reconcile/0.1`: the reconciliation rows, the credentials no row names, the days a figure stopped and the count of misses | [`reconcile.md`](reconcile.md) |
| `costroid merged` | `costroid.merged/0.1`, one object per fact sent, one a line. `--dry-run` prints `costroid.merged-fact/0.1`, one per fact it would send | [`export.md`](export.md) |
| `costroid egress report` | `costroid.egress/0.1`: the counters | [`ledger.md`](ledger.md) |
| `costroid doctor` | `costroid.doctor/0.1` | This page |
| `costroid lint`, `costroid lint-recipes` | `costroid.lint/0.1` | This page |
| `costroid validate` | `costroid.validate/0.2` | This page |
| `costroid version` | `costroid.version/0.1` | This page |

Every object in this table opens with `schema`, its name and version. For each but the result object, a later release may add a field under the same first number, and raises the second: a reader ignores a field it does not know. A field is removed, renamed or given another meaning only under a new first number: a reader refuses a first number it does not know.

`costroid lint-recipes --json` prints the object of `costroid lint`. A line it rejects carries no severity of its own, so the object states it with the severity `costroid lint` gives the same finding, such as `medium` for an action that is not pinned, and `high` where the rule is `lint-recipes`' alone. `lint-recipes` exits 1 on any rejected line, whatever its severity, so read its exit code or the length of `findings`, not `count.high`.

`costroid demo` and `costroid receipt` have no `--json`. Each prints a comment for a person to read, not a report: `demo` the comment of a recorded run, and `receipt` the comment its author posts. The figures of a real run are in the result object.

A JSON Schema for each object is in [`cmd/costroid/testdata/objects`](../cmd/costroid/testdata/objects), and a test holds each example below to it.

### `costroid doctor --json`

Each check has the `status` it ended in, `ok`, `warn` or `fail`, and the `line` doctor prints for it. `count` says how many ended each way.

```json
{
  "schema": "costroid.doctor/0.1",
  "checks": [
    { "status": "ok", "line": "ok: costroid 1.0.0-alpha.34" },
    { "status": "ok", "line": "ok: costroid.yml: valid" },
    {
      "status": "warn",
      "line": "warn: no workflow in .github/workflows calls Costroid's action: run costroid init github --agent claude"
    },
    {
      "status": "warn",
      "line": "warn: claude is not on PATH, so costroid run cannot start it on this machine, though CI installs its own: install it with npm install --global @anthropic-ai/claude-code@2.1.316"
    },
    {
      "status": "ok",
      "line": "ok: ANTHROPIC_API_KEY is not set in this shell, so a local run of claude uses its own sign-in, priced as https://docs.costroid.com/bases says"
    }
  ],
  "count": { "fail": 0, "warn": 2, "ok": 3 }
}
```

### `costroid lint --json`

Each finding has its `rule`, its `severity`, which is `high`, `medium` or `note`, its `file`, its `line` and its `message`. `files` counts the files read, and `agents` those that start an agent.

```json
{
  "schema": "costroid.lint/0.1",
  "files": 1,
  "agents": 1,
  "findings": [
    {
      "rule": "agent-id-token",
      "severity": "high",
      "file": ".github/workflows/review.yml",
      "line": 11,
      "message": "job review holds id-token: write, so an injected agent can mint a token that claims this repository's identity"
    },
    {
      "rule": "agent-budget",
      "severity": "note",
      "file": ".github/workflows/review.yml",
      "line": 21,
      "message": "claude_args give Claude Code no --max-budget-usd, so no cap stops a run, whatever it spends: add --max-budget-usd and your cap in USD to claude_args, such as --max-budget-usd 5"
    },
    {
      "rule": "join-attributes",
      "severity": "note",
      "file": ".github/workflows/review.yml",
      "line": 21,
      "message": "claude starts outside costroid run, so its step must set cicd.pipeline.run.id, cicd.pipeline.task.run.id, vcs.change.id, vcs.ref.base.revision, vcs.ref.head.revision and vcs.repository.url.full in OTEL_RESOURCE_ATTRIBUTES, or its telemetry names no pull request: docs/adapters.md#join-attributes lists the values"
    }
  ],
  "count": { "high": 1, "medium": 0, "note": 2 }
}
```

### `costroid validate --json`

`state` is `valid`, `invalid` or `absent`. `absent` means no file was named and the directory holds no `costroid.yml`, so the defaults apply. Each problem has a `pointer`, a JSON pointer into the file that is empty for the whole document, and a `message`.

`pack` is null unless a valid file names a policy pack. Then it holds the `path` `policy_pack` names, the `file` validate looked for beside the file, empty for standard input, and a `state`: `valid` when the pack compiles as the check compiles it, `invalid` when it does not or is over 64 KiB, which exits 1, and `unread` when there was no file to compile. `message` gives the fault, or why validate did not read the pack.

```json
{
  "schema": "costroid.validate/0.2",
  "file": "costroid.yml",
  "state": "invalid",
  "problems": [{ "pointer": "/policy", "message": "additional properties 'no_such_key' not allowed" }],
  "pack": null
}
```

### `costroid version --json`

`costroid version --verbose` prints the same facts as lines, for a bug report. The object holds no secret, no value of the environment and no path but the name `costroid.yml`. `commit` is null for a build that records none, and `prices_read` for a catalog whose sheets state no day.

```json
{
  "schema": "costroid.version/0.1",
  "release": "1.0.0-alpha.34",
  "commit": "e323c83959bedbdd9e3ad48db50babd8677715f4",
  "os": "linux",
  "arch": "amd64",
  "catalog": { "version": "0.4.3", "prices_read": "2026-11-21" },
  "result_schemas": ["costroid.result/0.9", "costroid.result/0.10", "costroid.result/0.11", "costroid.result/0.12", "costroid.result/0.13", "costroid.result/0.14"],
  "config": { "file": "costroid.yml", "found": false }
}
```

## A step that waits

`costroid export`, `merged`, `connect`, `reconcile`, `doctor`, `result`, `app create` and `gateway mint` say `costroid: asking <host>` on standard error before their first request to a host, once per host. Two of them can then wait long, and say so every 10 s while they do: `costroid export` for the platform's verdict on a batch, up to 60 s, and `costroid gateway mint` and `close` for the platform to take a statement it answered with a server error, up to 150 s. The line reads `costroid: still waiting for <what>, <seconds> s of at most <seconds> s`. It is a plain line, the same on a terminal and in a job's log. `costroid doctor` says `costroid: asking GitHub through gh` before it first runs `gh`. `costroid run` says `costroid: running <agent> under a cap of USD <amount>` before it starts the agent. `--quiet` drops all four lines.

## What `costroid run` leaves

`costroid run` writes two files, and one line of its summary names both: `costroid: <count> rows in <rows file>, catalog <version>, and the result object in <object>`. The object stands beside the rows and is named after them, so `--out rows.jsonl` gives `rows.run.json`. `costroid ingest-run` writes the same two and prints the same line. While the agent works, `run` also writes the status file [`agents.md`](agents.md) describes, where `COSTROID_STATUS_FILE` or `RUNNER_TEMP` names a place for it.

A run of Claude Code leaves one more file that no line names: `costroid-run.lock`, empty, in the agent's config directory, which is `CLAUDE_CONFIG_DIR` or `.claude` in your home directory. `run` creates that directory when it is not there. The file is the lock that holds one run to the directory, as [`adapters.md`](adapters.md) describes. It stays after the run on purpose: a lock is held on the file itself, so a second run that had opened a lock file since removed would hold nothing, and two runs could count each other's sessions.

## Ctrl-C

Ctrl-C ends a command, and Costroid exits 130. `costroid run` first stops the agent and every process it started: it sends them SIGTERM, and SIGKILL 5 seconds later to any that is left. It then writes the rows of what the agent spent, and exits 130. `costroid serve` stops as it does on SIGTERM: it finishes the requests it holds, and exits 0.
