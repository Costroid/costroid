---
description: Show what the costroid run that started this agent has spent so far, its cap and what is left
allowed-tools: mcp__plugin_costroid_costroid__run_status
---

Call the run_status tool of the costroid server and report its answer.

Quote each amount as the decimal string the tool returned, with its currency and its cost_basis beside it. Never round or convert an amount, and add two amounts only when their currency and cost_basis both match. A list_equivalent amount is a subscription run priced at the API's list rate, which nobody billed, so say "not billed" beside it.

When status is null, no costroid run has started an agent in this job. Say so, and give the reason the missing field states.
