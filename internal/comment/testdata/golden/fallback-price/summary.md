**No budget set: this run cost USD 180.10 at list (Measured), USD 175.10 over its USD 5.00 cap. No rule decides this check.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Catalog | Enforcement | Input, uncached | Cache read | Cache write | Output |
| --- | --- | --- | --- | --- | --- | --- | --- |
| USD 180.10, fallback price | Measured | `0.1.0` | the wrapper stops the process at the cap | 0 | 0 | 0 | 2000000 |

Cap: USD 5.00 (the wrapper stops the process at the cap). Cost: USD 180.10, Measured. Over the cap by USD 175.10 and 1000000 tokens.

Fallback price: `gpt-9-preview` is not in catalog `0.1.0` for the tier and token class used, so `openai:fallback:output` priced it.

<details>
<summary>Policy, agents, models and catalog</summary>

No policy ran on this change.

Agents: `codex`. Models: `gpt-5.5`, `gpt-9-preview`. 2 rows, catalog `0.1.0`, prices read 2026-11-01.

</details>
