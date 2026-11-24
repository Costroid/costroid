**No budget set: this run cost USD 180.10 at list (Measured), USD 175.10 over its USD 5.00 cap. No rule decides this check.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Cost | Basis | Enforcement |
| --- | --- | --- |
| USD 180.10, fallback price | Measured | the wrapper stops the process at the cap |

Cap: USD 5.00 (the wrapper stops the process at the cap). Cost: USD 180.10 at list (Measured), USD 175.10 and 1000000 tokens over the cap.

Fallback price: `gpt-9-preview` is not in catalog `0.1.0` for the tier and token class used, so `openai:fallback:output` priced it.

<details>
<summary>Policy, agents, models, catalog and tokens</summary>

No policy ran on this change.

Agents: `codex`. Models: `gpt-5.5`, `gpt-9-preview`. 2 rows, catalog `0.1.0`, prices read 2026-11-01.

Tokens: 0 uncached input, 0 cache read, 0 cache write and 2000000 output.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.

</details>
