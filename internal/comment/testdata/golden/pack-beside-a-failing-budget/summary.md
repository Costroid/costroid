**Over budget: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

The policy pack `.costroid/policy.rego` returned:

- `no_opus_on_docs` failed with `model_on_docs_change`: `Opus models are not used on a change to docs/ alone.`
- `large_run` warned with `over_team_estimate`: `This run cost more than the team estimate in .costroid/estimates.md.`

[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check.

### Costroid

| Cost | Basis | Enforcement |
| --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | client-side estimate, can pass the cap |

<details>
<summary>Cap, budget, policy, agents, models, catalog and tokens</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725 at list (List-equivalent, not billed), USD 4.998380275 under the cap.

Run budget: USD 0.001, from `policy.run_budget_usd` in `costroid.yml`. The verdict checks this budget, never the cap.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Tokens: 8 uncached input, 15605 cache read, 7945 cache write and 272 output.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
