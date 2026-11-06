# Accuracy

Costroid prices an agent's run from the agent's own token counts. This page compares those figures with the provider's usage and cost reports, one UTC day at a time, for the API key that Costroid's own test repository runs an agent on.

## Method

Each day a scheduled job in a private repository runs one small Claude Code task under `costroid run`. Two days later a second job saves the provider's usage and cost reports for that day beside every row the key's runs wrote that day. The rows, the reports and the time they were saved are that day's inputs. They stay private, because the provider's report names every key of the organization.

`go run ./scripts/accuracy -write docs/accuracy.md` re-runs `costroid reconcile` on each day's inputs and writes this page. A day passes when every token class matches the usage report within 1%, and the list-price dollars land within 10% of the cost report, or within 10% once the residual's named causes are taken out. [Compare a ledger with a provider's report](reconcile.md) defines both steps and each cause.

No line is edited by hand. `go run ./scripts/accuracy -check docs/accuracy.md -days 14` recomputes every line from the inputs, refuses the page when one line differs, and passes only when the last 14 days on it are consecutive and each passed. The last column is the SHA-256 of a listing of each input file's SHA-256 and path, in the form `sha256sum` prints, so a line names the exact inputs it was computed from.

## By agent version

No day has been reconciled yet.

## Days

No day has been reconciled yet.
