**Within limit: this run cost USD 0.10 at list (Measured), within 1.5 times the USD 0.10 P95 of this agent's latest 20 runs. A ledger checked the rows.**

Checked: a ledger checked the rows the verdict read against the job's OIDC token.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.10 | Measured | `0.1.0` | the wrapper stops the process at the cap | 0 | 0 | 0 | 1000000 |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (the wrapper stops the process at the cap). Cost: USD 0.10, Measured. Under the cap by USD 4.90.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `codex`. Models: `gpt-5.5`. 1 row, catalog `0.1.0`, prices read 2026-11-01.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
