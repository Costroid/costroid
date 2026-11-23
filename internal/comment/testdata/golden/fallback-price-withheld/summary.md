**No budget set: this run was priced at list (Measured). No rule decides this check. Amounts are withheld here.**

Unchecked: the agent's own job wrote these figures, and nothing outside it checked them.

### Costroid

| Basis | Catalog | Enforcement |
| --- | --- | --- |
| Measured, fallback price | `0.1.0` | the wrapper stops the process at the cap |

Fallback price: `gpt-9-preview` is not in catalog `0.1.0` for the tier and token class used, so `openai:fallback:output` priced it.

Amounts and token counts are off on this public repository (`comment.show_cost: false`). The step's outputs and the check run's machine block carry them.

Costroid's own test, not this run: it runs `claude-code` alone on an Anthropic API key, and this run is not that, so no day on [the accuracy page](https://docs.costroid.com/accuracy) speaks for these figures. Nothing has compared this run with a provider's report.
