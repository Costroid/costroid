# Accuracy

Costroid prices an agent's run from the agent's own token counts. This page compares those figures with the provider's usage and cost reports, one UTC day at a time, for the API key that Costroid's own test repository runs an agent on.

## Method

Each day a scheduled job in a private repository runs one small Claude Code task under `costroid run`, with a budget of 0.10 USD. Two days later a second job saves the provider's usage and cost reports for that day, cut to that key's own records, beside every row the key's runs wrote that day. The rows, the reports and the time they were saved are that day's inputs. Each week they are committed to the private repository.

Other workflows in that repository spend on the same key. Each one that runs under Costroid keeps its rows, and the second job gathers them. A run that spends on the key outside Costroid, such as a raw recording of an agent, writes no row. The day names each such run, the cause `shared_key` lists its run id, and the day fails: the provider's figures hold requests no row covers.

Measured cost is what the day's rows on the key cost at list price, from Costroid's own rows. It reads on a day with no provider report as well, so the test's spend is always on the page.

`go run ./scripts/accuracy -write docs/accuracy.md` re-runs `costroid reconcile` on each day's inputs and writes this page. A day passes when every token class matches the usage report within 1%, every price the cost report implies equals the catalog's to the last digit the report states, each token class's list-price dollars land within 1% of the cost report's where the report splits them by class, and the day's list-price dollars land within 10% of the cost report, or within 10% once the residual's named causes are taken out. [Compare a ledger with a provider's report](reconcile.md) defines each step and each cause.

Each day names the Costroid release its agent ran under, which the agent's job records on the day it runs. A day fails when it names no release or a build that is no release. It also fails when its release was superseded more than 7 days before the day. Several releases can ship in one day, so the rule counts days, not releases: a release passes when it was the newest at some moment in the 7 days before the day or on the day itself.

No line is edited by hand. `go run ./scripts/accuracy -check docs/accuracy.md -days 14` recomputes every line from the inputs, refuses the page when one line differs, and passes only when the last 14 days on it are consecutive and each passed. The last column is the SHA-256 of a listing of each input file's SHA-256 and path, in the form `sha256sum` prints, so a line names the exact inputs it was computed from.

## By agent version

No day has been reconciled yet.

## Days

No day has been reconciled yet.
