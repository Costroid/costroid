**Pack rule failed (no_opus_on_docs): this run cost USD 0.001619725 at list (List-equivalent, not billed). The rule is this repository's own, read from the base branch.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

The policy pack `.costroid/policy.rego` returned:

- `no_opus_on_docs` failed with `model_on_docs_change`: `Opus models are not used on a change to docs/ alone.`

[The approval page](https://docs.costroid.com/approval) says how a reviewer approves a failed check.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 0.001619725 | List-equivalent, not billed | `0.4.3` | client-side estimate, can pass the cap | 8 | 15605 | 7945 | 272 |

<details>
<summary>Cap, policy, agents, models and catalog</summary>

Cap: USD 5.00 (client-side estimate, can pass the cap). Cost: USD 0.001619725, List-equivalent, not billed. Under the cap by USD 4.998380275.

A change to `costroid.yml` in this pull request applies after the merge, because the verdict reads the base branch's file.

Agents: `claude-code`. Models: `claude-haiku-5-5`. 19 rows, catalog `0.4.3`, prices read 2026-11-21.

</details>
