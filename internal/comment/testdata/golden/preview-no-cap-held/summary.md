**No budget set: this run cost USD 0.10 at list (Measured). No rule decides this check.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.10 | Measured | `0.1.0` | no cap held the run | 0 | 0 | 0 | 1000000 |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

No cap held this run. Cap: USD 0.05, which nothing enforced. Cost: USD 0.10, Measured. Over the cap by USD 0.05 and 1000000 tokens.

No rule applied: `costroid.yml` in this directory sets no budget and no limit. Set `policy.run_budget_usd` to hold each run to a budget.

Agents: `codex`. Models: `gpt-5.5`. 1 row, catalog `0.1.0`, prices read 2026-11-01.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
