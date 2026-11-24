**Over budget, demoted to a warning: this run is over its run budget, priced at list (List-equivalent, not billed). After 2026-12-12 09:00 UTC this fails the check. Amounts are withheld here.**

Run budget demoted to a warning: by the ledger's own record, reviewers from at least 2 accounts approved 7 of the last 20 pull requests it failed, more than one in five. It warns until 2026-12-12 09:00 UTC, then fails again. Add `auto_demote: false` under `policy` in `costroid.yml` to keep every rule as written.

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap |

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.
