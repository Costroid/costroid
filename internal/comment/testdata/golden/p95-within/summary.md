**Within limit, by the job's own figures: this run cost USD 0.001619725 at list (List-equivalent, not billed), within 1.5 times the USD 0.0012 P95 of this agent's latest 20 runs. Nothing outside the job checked the figures, so the check is neutral.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

[The policy page](https://docs.costroid.com/policy) says what lets a rule pass. A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
