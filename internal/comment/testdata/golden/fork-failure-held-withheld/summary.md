**Over budget, on a fork's own rows: this run is over its run budget, priced at list (List-equivalent, not billed). A fork's rows are the fork's own claim, so the check is neutral. Amounts are withheld here.**

This pull request comes from a fork, and the fork's own run wrote these rows. Its check concludes neutral at most, so a rule that fails here warns and does not block a merge.

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap |

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.

Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 8 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report.
