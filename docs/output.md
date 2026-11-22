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
| `costroid policy` | The verdict, as the check's machine block carries it | [`policy.md`](policy.md) |
| `costroid merged` | One object per fact sent, one a line | [`export.md`](export.md) |
| `costroid egress report` | The counters | [`ledger.md`](ledger.md) |
| `costroid doctor` | `costroid.doctor/0.1` | This page |
| `costroid lint` | `costroid.lint/0.1` | This page |
| `costroid validate` | `costroid.validate/0.1` | This page |
| `costroid version` | `costroid.version/0.1` | This page |

Each of the four objects on this page opens with `schema`, its name and version. A later release may add a field under the same first number, and raises the second: a reader ignores a field it does not know. A field is removed, renamed or given another meaning only under a new first number: a reader refuses a first number it does not know. A JSON Schema for each object is in [`cmd/costroid/testdata/objects`](../cmd/costroid/testdata/objects), and a test holds each example below to it.

### `costroid doctor --json`

Each check has the `status` it ended in, `ok`, `warn` or `fail`, and the `line` doctor prints for it. `count` says how many ended each way.

```json
{
  "schema": "costroid.doctor/0.1",
  "checks": [
    { "status": "ok", "line": "ok: costroid 1.0.0-alpha.28" },
    { "status": "ok", "line": "ok: costroid.yml: valid" },
    {
      "status": "warn",
      "line": "warn: the repository has no remote, so no pull request can be opened from it: create it on GitHub and add it as origin"
    }
  ],
  "count": { "fail": 0, "warn": 1, "ok": 2 }
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
      "message": "job claude holds id-token: write, so an injected agent can mint a token that claims this repository's identity"
    }
  ],
  "count": { "high": 1, "medium": 0, "note": 0 }
}
```

### `costroid validate --json`

`state` is `valid`, `invalid` or `absent`. `absent` means no file was named and the directory holds no `costroid.yml`, so the defaults apply. Each problem has a `pointer`, a JSON pointer into the file that is empty for the whole document, and a `message`.

```json
{
  "schema": "costroid.validate/0.1",
  "file": "costroid.yml",
  "state": "invalid",
  "problems": [{ "pointer": "/policy", "message": "additional properties 'no_such_key' not allowed" }]
}
```

### `costroid version --json`

`costroid version --verbose` prints the same facts as lines, for a bug report. The object holds no secret, no value of the environment and no path but the name `costroid.yml`. `commit` is null for a build that records none, and `prices_read` for a catalog whose sheets state no day.

```json
{
  "schema": "costroid.version/0.1",
  "release": "1.0.0-alpha.28",
  "commit": "378ac22d7fd8e3ad6bd2cb1b8e3e3d5b3a6f0f11",
  "os": "linux",
  "arch": "amd64",
  "catalog": { "version": "0.4.3", "prices_read": "2026-11-21" },
  "result_schemas": ["costroid.result/0.9", "costroid.result/0.10", "costroid.result/0.11", "costroid.result/0.12"],
  "config": { "file": "costroid.yml", "found": false }
}
```

## A step that waits

`costroid export`, `merged`, `connect`, `reconcile`, `doctor`, `app create` and `gateway mint` say `costroid: asking <host>` on standard error before their first request to a host, once per host, and then nothing while they wait. `costroid doctor` says `costroid: asking GitHub through gh` before it first runs `gh`. `--quiet` drops both lines.

## Ctrl-C

Ctrl-C ends a command, and Costroid exits 130. `costroid run` first stops the agent and every process it started: it sends them SIGTERM, and SIGKILL 5 seconds later to any that is left. It then writes the rows of what the agent spent, and exits 130. `costroid serve` stops as it does on SIGTERM: it finishes the requests it holds, and exits 0.
