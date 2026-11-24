**Over budget, demoted to a warning: this pull request's runs are over the pull request budget. Reviewers approved 7 of the last 20 pull requests it failed, so it warns until 2026-12-12 09:00 UTC and the check is neutral.**

Pull request budget demoted to a warning: by the ledger's own record, reviewers from at least 2 accounts approved 7 of the last 20 pull requests it failed, more than one in five. It warns until 2026-12-12 09:00 UTC, then fails again. Add `auto_demote: false` under `policy` in `costroid.yml` to keep every rule as written.

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

Pull request budget: USD 0.001619725 over this pull request's runs is over the USD 0.001 budget. The total adds every basis together, at list.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
