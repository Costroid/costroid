**Pack rule warned (large_run): this run was priced at list (List-equivalent, not billed). The rule is this repository's own, and a warning never fails the check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

The policy pack `.costroid/policy.rego` returned:

- `large_run` warned with `over_team_estimate`

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap |

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.
