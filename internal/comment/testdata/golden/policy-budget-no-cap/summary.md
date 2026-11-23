**Within budget, by the job's own figures: this run cost USD 0.001619725 at list (List-equivalent, not billed) of the USD 5.00 run budget. Nothing outside the job checked the figures, so the check is neutral.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |

<details>
<summary>Cap, budget, policy, agents, models and catalog</summary>

Cap: client-side estimate, can pass the cap. The rows do not record its amount, and the report was not given it.

Run budget: USD 5.00, from `policy.run_budget_usd` in `costroid.yml`. The verdict checks this budget, never the cap.

Over budget, the rule would warn until the warn-only period ends, then fail. [The policy page](https://docs.costroid.com/policy) says what lets a budget pass. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
