**Over budget, approved by octocat: this run cost USD 0.001619725 at list (List-equivalent, not billed), over the USD 0.001 run budget. The approval covers commit 9f2c6e1 and ends with a new push.**

`octocat` approved commit `9f2c6e1` at 2026-11-12 09:30 UTC, so the check concludes neutral. The policy's verdict stays failure, and a new push ends the approval.

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |

<details>
<summary>Cap, budget, policy, agents, models and catalog</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

Run budget: USD 0.001, from `policy.run_budget_usd` in `costroid.yml`. The verdict checks this budget, never the cap.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

</details>
