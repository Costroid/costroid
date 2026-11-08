# Tag a run with its work item, arm or team

`--tag` writes a label on every row of a run, so a ledger can group runs by the issue they worked on, the arm of an experiment they belong to, or the team they are counted to. `costroid run` and `costroid ingest-run` take it, once per key.

```sh
costroid run --budget-usd 5 --out rows.jsonl --tag work_item=ENG-142 --tag arm=b --tag team=platform -- claude -p "Fix the failing test."
```

Every row's `Tags` then holds the three tags beside the ones Costroid writes:

```json
{ "costroid/arm": "b", "costroid/team": "platform", "costroid/work_item": "ENG-142" }
```

## The keys

The row contract, [`tags/v0.3`](https://github.com/Costroid/costroid-schemas/tree/main/tags/v0.3), defines three keys a caller names. `--tag KEY=VALUE` writes the tag `costroid/KEY`.

| Key         | Tag                  | Value                                                                                                         |
| ----------- | -------------------- | ------------------------------------------------------------------------------------------------------------- |
| `work_item` | `costroid/work_item` | An issue key: an upper-case project key, a hyphen and a number from 1, as in `ENG-142`. At most 64 characters |
| `arm`       | `costroid/arm`       | The arm of an experiment, as in `control` or `b`                                                              |
| `team`      | `costroid/team`      | The team the run is counted to                                                                                |

`work_item` matches `^[A-Z][A-Z0-9_]*-[1-9][0-9]*$`, and nothing else. `arm` and `team` take 1 to 128 letters, digits and `.`, `_`, `:`, `/`, `+` and `-`, opening with a letter or a digit: the allowlist every string from a pull request passes before Costroid prints it.

## What is refused

The command exits 125 before it writes a row, and prints why, for:

- a key other than `work_item`, `arm` and `team`. The row contract defines no other, because a free-form key and value are where content would hide;
- a value that opens with `sk-`, as provider keys do. `--credential` refuses the same shape. The value is never printed;
- a value outside the allowlist, or a `work_item` that is not an issue key;
- one key given twice.

## Where the tags go

The tags travel with the rows. Costroid Platform keeps every tag under `costroid/`. The ledger that `costroid serve` hosts stores the three tags with the row, and holds each value to the rule above: a row whose value misses it is refused with 422 `ROW_INVALID`, and the refusal never repeats the value. It refuses any other key under `costroid/` the same way.

The comment and `costroid report` do not show the tags.
