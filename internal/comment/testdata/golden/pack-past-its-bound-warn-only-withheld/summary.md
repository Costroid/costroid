**Pack stopped (pack_bound): the policy pack passed its evaluation bound on this run's rows. This run was priced at list (List-equivalent, not billed). After 2026-11-09 09:00 UTC this fails the check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

Warn-only until 2026-11-09 09:00 UTC: for 7 days from the commit that added this workflow to the base branch, a rule that fails warns. Add `enforce: true` under `policy` in `costroid.yml` to enforce now.

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap |

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.

Costroid's own test, not this run: `claude-code` 2.1.316 on Anthropic passed 8 of the 14 days it ran, 2026-11-06 to 2026-11-19 (UTC), and on each day that passed every token class matched Anthropic's usage report count for count. [The accuracy page](https://docs.costroid.com/accuracy) lists every day. Nothing has compared this run with a provider's report.
