**Over budget, warn-only: this run is over its run budget, priced at list (List-equivalent, not billed). After 2026-11-09 09:00 UTC this fails the check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

Warn-only until 2026-11-09 09:00 UTC: for 7 days from the commit that added this workflow to the base branch, a rule that fails warns. Add `enforce: true` under `policy` in `costroid.yml` to enforce now.

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap |

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.
