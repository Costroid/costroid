---
description: Show how the newest costroid check on a pull request concluded and what its run cost
argument-hint: PULL_REQUEST_NUMBER
allowed-tools: mcp__plugin_costroid_costroid__pull_result
---

Call the pull_result tool of the costroid server with pull_request set to $ARGUMENTS, and report the result object it returns. When $ARGUMENTS is not a pull request's number, ask for the number before you call the tool.

Give the conclusion first: success, neutral or failure. Then quote each figure as the decimal string the tool returned, with its currency and its cost_basis beside it. Never round or convert an amount, and add two amounts only when their currency and cost_basis both match. There is no total across currencies.

The tool reads the check run, never the pull request comment. Do not read the comment in its place.
