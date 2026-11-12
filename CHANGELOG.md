# Changelog

Each release is dated by its tag, in UTC. Every release so far is a pre-release. [`docs/verify.md`](docs/verify.md) checks one.

## 1.0.0-alpha.24 (2026-11-12)

### Breaking changes

- A rows entry coarser than one request, whose context tier nothing settles, is priced at the highest tier its input could reach, and its rows say the figure may read high. It was priced at the lowest tier with a note that it may read low. Such figures move up. A Claude Haiku 5.5 run of 143642 input tokens over 8 requests, with no `costUSD` from Claude Code, reads USD 0.044186675 where it read USD 0.008837335. A Codex turn of 272001 input tokens and 1000 output tokens on `gpt-5.6-sol` takes the prices above 272000 tokens. An entry that states no whole input is bounded by its own input tokens. Claude Code's own figure still picks the lower tier when it matches it.
- A model or tier a sheet does not name, priced in a region with a data residency multiplier, takes the multiplier on the fallback price. Such figures move up: an unnamed model's output reads USD 660 per million tokens on OpenAI `regional`, where it read USD 600, and USD 88 on Anthropic `us`, where it read USD 75, so the fallback stays at or above every regional price. A model the sheet prices and a multiplier leaves out keeps its global price.
- A price a residency multiplier raised names its region in `SkuPriceId`, in place of `global`, as in `anthropic:claude-opus-5-5:us:fast:all:output`. The id of a global row stood beside a multiplied price before. Fallback ids keep their shape.
- An amount in `costroid.yml` is read only when it is written as a plain decimal. Hex (`0x10`), octal (`0o17`, and a leading zero such as `017`), underscores (`1_000`) and a leading `+` were read as whole numbers, and are now refused, in the policy, in `costroid validate` and in the schema where JSON can tell. A budget of `017` read as USD 15. A refused amount leaves the policy unread, as before.
- The Helm chart and the Compose file run the release the recipes pin by default, no longer 1.0.0-alpha.1. Each release moves that default. To stay on another release, set `image.tag` in the chart or `COSTROID_IMAGE` for Compose.

### What you can now do

- Read where a run stands against its budget in the verdict line: `Run budget: unverified rows cannot satisfy a budget. The run cost USD 0.0391529 of the USD 5 budget.` When the check is neutral only because the agent's own job wrote the rows, the line says so, states what a run over budget concludes, and links [`docs/policy.md`](docs/policy.md), which says what lets a budget pass. With no rule set, it names `policy.run_budget_usd`.
- See the day a catalog's prices were read beside its version: the comment and `costroid version` print `catalog 0.4.1, prices read 2026-11-12`, the date the release's sheets state, and the oldest one when they state several.
- Start with two commands that need no key and spend nothing: the README leads with `costroid demo` and `costroid lint`, then names the two recipes to paste with no install. [`docs/README.md`](docs/README.md) lists every page by task.
- Trace a price to its source: [`docs/catalog.md`](docs/catalog.md) says where the catalog is published, how each price names its page and the day it was read, when a figure takes the fallback tier, and which catalog a binary carries.
- Run Copilot CLI on a Gemini model: a request Copilot serves on `gemini-*` is priced at Google's sheet, in `costroid run` and in gh-aw's usage files. Google's sheet prices no cache write, so a cache write on a Gemini model counts as uncached input, and the row says so.
- Write an amount unquoted: `run_budget_usd: 2.50` is read as the decimal 2.50, from its text, never through a float. It was refused, which left every rule unread and the check `neutral`. The schema takes a number for each amount.
- Read what changed on each release's page: its notes open with that version's section of this changelog, above the install text and the image digest.
- Pin the Marketplace listing, `Costroid/costroid-action`, to its own commit: the listing's release notes name the commit of its tag and show a job that calls it, and its README gives the `git ls-remote` line that prints it. The listing carries a `SECURITY.md` that sends reports to this repository's private vulnerability reporting.
- Add Costroid to an `openai/codex-action` workflow whose step sets no `codex-home`, such as one that sets only `openai-api-key` and `prompt`. `costroid init github --existing` gives the step what [`recipes/github-codex-action.yml`](recipes/github-codex-action.yml) has: a step that makes the directory for the job under `runner.temp`, and the `codex-home` input. A `codex-home` the `read` step cannot read is still refused, with what to do.
- Run `costroid doctor` on a GitLab project. It checks the template that installs Costroid, `.gitlab/costroid.yml` or a local file `.gitlab-ci.yml` includes, against the rules of `costroid lint-recipes`, and the release and commit it pins. It runs no `gh` there and never names `init github`.
- Run `costroid result`, and the plugin's `/costroid:result`, in a clone with no `GITHUB_REPOSITORY`: the repository is the one the git remote `origin`, or the only remote, names on GitHub, and `result` says which it read.
- Load per-pull-request rows into Harness, IBM Cloudability or Ternary: `costroid export --format harness` writes the FOCUS 1.1 CSV Harness's console takes, one file per billing period; `--format cloudability` writes a FOCUS 1.1 drop, a gzip CSV and its `costroid-Manifest.json`, for the bucket `--root-dir` names; `--format ternary` writes one FOCUS 1.2 CSV per UTC day. Each keeps every amount and currency as the row states it, and the repository, pull request, run and basis in `Tags`. [`docs/export.md`](docs/export.md#files-for-another-tool) quotes each tool's page.
- Write FOCUS 1.5's candidate columns: `costroid export --format focus-1.5-cr` fills `SkuPriceDetails` with the model and each class's `TokenDirection` and `TokenCacheAction`, keeps a cache write's lifetime in `x_CacheWriteLifetime`, fills `CredentialId` from a stated credential, and writes the schema metadata that declares `1.5-cr`. A `SubAccountId` that reads like a key refuses the file. Ratification may change the candidate, and the format is then named `focus-1.5`.
- Bring rows to Microsoft FinOps hubs: Costroid writes no Parquet, and [`docs/export.md`](docs/export.md#microsoft-finops-hubs) shows the folder the hubs page names and a DuckDB command that converts the `ternary` files.
- Price Claude Fable 5.2, Claude Mythos 5.2 and Gemini 4 Carbon at their own rows, from catalog 0.4.1. 1.0.0-alpha.23 priced them at the fallback tier. Fable 5.2 and Mythos 5.2 read USD 15 input and USD 75 output per million tokens there, about 1.9 times their standard USD 8 and USD 40, and below their fast-mode USD 16 and USD 80. They now read USD 8 input, USD 40 output, USD 10 and USD 16 for 5-minute and 1-hour cache writes and USD 0.20 for cache reads, and USD 16 and USD 80 in fast mode. Gemini 4 Carbon read Google's fallback of USD 7.20 input and USD 36.00 output. At the standard tier it now reads USD 2.00 input, USD 10.00 output and USD 0.10 cache read through 2026-11-30, and USD 4.00, USD 20.00 and USD 0.20 from 2026-12-01.
- The Anthropic fallback is USD 16 input, USD 80 output, USD 20 for a 5-minute cache write and USD 32 for a 1-hour cache write per million tokens, set by Claude Fable 5.2's fast-mode prices. It was USD 15, USD 75, USD 18.75 and USD 30. A model or tier the Anthropic sheet does not name now prices at these figures.
- Promotional prices in catalog 0.4.1 that end with no later price, and the day the sheet's fallback prices the model from: OpenAI's `gpt-5.6-sol` on 2026-12-01 and `gpt-6-astra-law` on 2027-01-01, GitHub Copilot's `gemini-4-argon` on 2026-12-01 and `gemini-3.8-flash` on 2027-01-01, and Cursor's `gpt-5.6-sol` on 2026-11-22. Cursor's end date is new; catalog 0.3.0 held that price with no end.

### Fixes

- The comment's first line names the cap that held the run, `Cap: USD 5 (agent-flag: ...)`, and over the cap says by how much. A run nothing held reads `No cap held it.`, and its table cell `none: no cap held the run`, so a cap is never called a budget.
- `Unverified: the agent's job wrote these figures.` is printed from the verdict's trust, the one the machine block's `policy.trust` states. Rows a ledger checked against the job's OIDC token read `Verified: a ledger checked the rows the verdict read against the job's OIDC token.`
- Each thing is said once above the fold: the first line, the verdict line, the table and the run line. The cap against the cost, the policy's limits and where they are set, and the agents, models, rows and catalog sit in one folded block. Warnings, failure reasons, agent errors, withheld-amounts notices, reconciliations and an agent's own figure stay above it. The check run's summary and the GitLab note carry the same text. The machine block is unchanged.
- The docs no longer say that OpenCode, the SDK recipes or `init --pr` need a later release: each runs on the release the recipes pin. A docs test fails on such a sentence.
- The docs name one row contract, on [`docs/export.md`](docs/export.md#the-row-contract): `focus-row/v0.5` with `tags/v0.4` by integration contract v0.3, and `focus-row/v0.3` with `tags/v0.2` by v0.2. A docs test reads both from the code.
- [`docs/existing-workflow.md`](docs/existing-workflow.md) shows the workflow `init --existing` writes, cap lines included, and no longer contradicts itself on where the cap goes.
- The `costroid.yml` that `init` writes leaves out `policy.monthly_budget_usd`, which no check reads yet, and names the schema at the release it pins in place of `main`.
- The troubleshooting page lists every line of `costroid-install.sh`, and `GITHUB_TOKEN is not set`, `gh repo view failed` and `run needs --budget-usd`.
- OpenCode runs read the grade `none`. The wrapper still stops the run at the cap between steps, but no recorded run has proven that stop, and a grade is claimed only on a recorded stop. Each row's note says so.
- A Copilot CLI request on a model no sheet prices wrote no row, and the run kept `wrapper-watchdog` though the watch could not count it. The run now reads `none`, and the grade's note and the warning name the model, where the warning said the provider `"copilot"` had no sheet.
- One map names the provider whose sheet prices a model, from the start of its name, and Copilot CLI, gh-aw and `costroid serve`'s telemetry reader share it. Telemetry no longer refuses `o3`, and Copilot CLI no longer drops `codex-*`.
- `costroid serve` reads a GenAI span whose cache counts pass its `gen_ai.usage.input_tokens` as one from a sender that leaves the cache out of its input, and notes the reading on each of the request's rows. It refused such a span before.
- `costroid serve` refuses an `x_CostroidOvershootBound` or a stated spend longer than 64 characters before reading it as a number. A long run of trailing zeros took seconds of CPU per value.
- A decimal literal is at most 64 characters, and its trailing zeros are cut in time linear in its length. A figure read from a JSON number is refused when its plain form is longer than that.
- `costroid reconcile` proves the price of a row written by 1.0.0-alpha.23 or earlier in a data residency region, on a model with several context tiers. Such a row carries the global price id beside the regional price, and it read "not proven" once regional ids named their region. A global id now names the same tier's id in the row's own region, and only that.
- `costroid serve` prices a row whose `SkuPriceId` states no context tier at the highest tier its input could reach, and the row says it may read high: the sheet's top tier, or its fallback when the top tier has an upper bound. It took the lowest tier. A tier bounded on both sides, such as `over-256000-to-500000` on the Cursor sheet, now prices as stated; it also took the lowest tier.
- A release waits for `ci`: a tag on a commit with no successful `ci` run on a push to `main` publishes nothing, and neither does a version with no section in this changelog.
- The image is pushed by its digest, signed and attested, and only then tagged with its version, so a failed signature leaves no version tag on ghcr.io.
- The image's binary records the commit it was built from, as the release binaries do, so `costroid init` run from the image pins that commit without reading the tag from github.com.
- `costroid doctor` tells a repository that is not on GitHub yet, which `gh` answers with a 404, from a missing sign-in. One line says to push the repository. Only a missing or refused sign-in says `run gh auth login`. Outside a git repository, doctor says so in a `fail` line, checks this machine alone and exits 1.
- `costroid run` refuses a command no adapter reads before it creates a file. It used to leave an empty rows file and a result object with the reason `empty_rows_file`, so the report said the agent printed no usage. The report now finds no rows file, which is what happened.
- `costroid result` with no `GITHUB_TOKEN` prints the line that sets one, `export GITHUB_TOKEN="$(gh auth token)"`. It never runs `gh` itself.
- `costroid --help` lists `demo`, `lint`, `init` and `doctor` first, in the order a first reader meets them, and no longer under other commands.

## 1.0.0-alpha.23 (2026-11-11)

### What you can now do

- The comment says when a Claude Code run signed in on a Claude subscription. Beside the figure of a run whose `apiKeySource` is `none`, that holds a `rate_limit_event`, and that no gateway bills, it prints `subscription: list-equivalent, no provider report to reconcile`. The figure, its basis and every total are unchanged. `none` alone never marks a run, so a subscription run without the event stays unmarked. The rule rests on a recorded `claude-code-action` run on a subscription's token ([`docs/adapters.md`](docs/adapters.md)).

## 1.0.0-alpha.22 (2026-11-11)

### Security

- Built with Go 1.27.4, which fixes GO-2026-6859: an HTTP/1 server could crash on a header race in `net/http`'s `MaxBytesReader`. `costroid serve` is an HTTP server and its webhook route reads bodies that way, so a self-hosted `serve` should move to this release. The binary, the container image and every release file are built with it.
- `golang.org/x/crypto` is v0.58.0. GO-2026-5932, in its OpenPGP package, never reached Costroid's code; the bump leaves no known vulnerability in the module graph.

## 1.0.0-alpha.21 (2026-11-11)

### What you can now do

- Group an agent's own telemetry by pull request without a vendor exporter: the `claude-code-action` and `codex-action` recipes set `OTEL_RESOURCE_ATTRIBUTES` on the agent step with `vcs.change.id`, `vcs.ref.head.revision`, `vcs.ref.base.revision`, `vcs.repository.url.full`, `cicd.pipeline.run.id` and `cicd.pipeline.task.run.id`, the values `costroid run` already gives the agents it starts. `costroid lint-recipes` rejects an agent step outside `costroid run` that lacks one; `costroid lint` notes it.
- `costroid export` sends by integration contract v0.3, rows `focus-row/v0.5` and tags `tags/v0.4`, to a platform that states it, and by v0.2 otherwise, saying which. Outside GitHub Actions, a token that may not read the platform's contract keeps sending by v0.2, with one warning. The contract is published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).

## 1.0.0-alpha.20 (2026-11-10)

### What you can now do

- Know whether a run's repository is public: rows from `costroid run` and `ingest-run` carry `costroid/repo_visibility`, `public`, `private` or `internal`, read from the GitHub event's `repository.visibility` (else `repository.private`) or GitLab's `CI_PROJECT_VISIBILITY`. No network call is made for it, and when the CI system does not say, the tag is left out. `costroid serve` stores it. The row contracts `tags/v0.4` and `focus-row/v0.5` are published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).
- `costroid lint` knows Copilot CLI and OpenCode by their command: a `costroid run -- copilot -p ...` or `costroid run -- opencode run ...` step with no `--agent` is checked as that agent, with the same rule `costroid run` uses to pick it.

## 1.0.0-alpha.19 (2026-11-09)

### Breaking changes

- The result object is `costroid.result/0.9`. `agent_stated` also takes OpenCode's own cost in USD and Copilot CLI's credits in nano-AIU, each from its own source. `costroid approve` and the readers still take 0.5 to 0.8.

### What you can now do

- See an agent's own figure beside Costroid's: the comment states OpenCode's cost for the run "by its own prices, which Costroid never adds to its own figures", and Copilot CLI's credits, "which Costroid never converts to dollars".
- `costroid run --agent copilot` names Copilot CLI, as `--agent opencode` names OpenCode. `costroid lint` checks a workflow that runs `costroid run --agent copilot` for Copilot CLI's provider variables and its tool rules.
- Register a GitHub App whose private key never touches a disk: `costroid app manifest` prints the App's minimum permissions, and `costroid app create` runs GitHub's manifest flow and imports the key GitHub returns straight into an AWS KMS key, writing no copy. With `COSTROID_GITHUB_APP_ID` and `COSTROID_GITHUB_APP_KMS_KEY` set, `costroid serve` signs the App's tokens through that key, and the approve button on a check posts. [`docs/github-app.md`](docs/github-app.md) has the steps.

### Fixes

- An unknown agent's error names every agent `costroid run` takes.

## 1.0.0-alpha.18 (2026-11-09)

### What you can now do

- Run OpenCode under a budget: `costroid run --agent opencode -- opencode run ...` reads each step OpenCode prints, prices it and stops the run at the cap. Costroid adds `--title` when the command has none, so OpenCode sends no title request of its own that no row would count, and reads the agent's version. Rows read `measured` on a provider key with no OpenCode sign-in for that provider. OpenCode's own cost for the run is stated beside Costroid's figure and never replaces it. A `task` subagent spends in a session of its own, which the stream does not show, so a run that calls it is graded `none` and says so. `recipes/github-opencode.yml` runs it in a pull request.
- Run Copilot CLI with your own model provider under a budget: `costroid run -- copilot -p ...` names a new session, reads its session file as each request lands and stops the run at the cap. Each model's rows must add up to Copilot CLI's own totals for the session, or the run is refused with the model and the field. Copilot's own credits are stated as it writes them, never converted to dollars. Rows read `measured` on your own provider key; a request Copilot served on its own model routing is priced at the provider's list rate and reads `list_equivalent`. Only a run on your own key has been recorded.

## 1.0.0-alpha.17 (2026-11-09)

### What you can now do

- Receive a GitHub App's webhook in `costroid serve`: set `COSTROID_GITHUB_WEBHOOK_SECRET` and `POST /webhooks/github` checks each delivery's signature before it reads the body, refuses a delivery it has already taken (for 72 hours, GitHub's redelivery window, across instances and restarts), and keeps the App's installations and their repositories. The migration `0008` adds three tables that hold ids and names, never a token. The approve button on a check re-concludes it as `costroid approve` does; it posts only once the App's key is configured. [`docs/serve.md`](docs/serve.md#the-github-apps-webhook) has the details.
- `costroid doctor` lists the secrets of the environment a job names, so a key stored as an environment secret reads as found.
- The GitLab template ran end to end on a self-managed GitLab CE 19.5.1 with GitLab Runner 19.5.1: over budget the merge is refused, a second push edits the one note, under budget the merge goes through. It has not run on gitlab.com. `go test -tags gitlab ./e2e/gitlab` runs it again. [`docs/gitlab.md`](docs/gitlab.md) says what it proved.

### Fixes

- With a LiteLLM gateway, `costroid run` and `costroid gateway close` read the key's spend only after it settles. LiteLLM writes spend every 10 to 15 seconds, and the run stopped waiting after 12, so it could warn that the gateway counted USD 0 when the rows did not, and close could attest that USD 0. A figure settles when it changes and then holds, or holds for 30 seconds; one that never settles carries a warning.
- On GitLab, the report says it commented on a merge request.

## 1.0.0-alpha.16 (2026-11-08)

### What you can now do

- Label a run's rows: `costroid run` and `ingest-run` take `--tag work_item=ENG-142`, `--tag arm=b` and `--tag team=platform`, written to `Tags` as `costroid/<key>`. Any other key, a value outside the allowlist or a value shaped like a secret exits 125. `costroid serve` stores rows carrying the three tags. [`docs/tags.md`](docs/tags.md) has the rules.
- Merge Costroid's rows with your provider's own FOCUS data without counting a run twice: `--focus-profile strict` writes `BilledCost` and `EffectiveCost` as 0 and carries the figure in `x_CostroidMeasuredCost`. The default rows are unchanged; send those to Costroid Platform. [`docs/focus-profile.md`](docs/focus-profile.md) says why.
- Send Gemini CLI's telemetry to `costroid serve`: its `gemini_cli.api_response` logs are priced, with cached tokens as cache reads, thinking tokens as output and tool results as input. A request whose counts do not add up to Gemini's own total carries a note that its figure may read low or high. This dialect is built from Gemini CLI's source at v0.67.0, not from a recorded run.
- The row contracts `tags/v0.3` and `focus-row/v0.4` are published in [Costroid/costroid-schemas](https://github.com/Costroid/costroid-schemas).

### Fixes

- `costroid reconcile` and the accuracy page print one percentage: a provider figure of zero reads `n/a`, and a negative one keeps its sign.

## 1.0.0-alpha.15 (2026-11-08)

### Breaking changes

- The result object is `costroid.result/0.8`. It adds `recorded`: the CI run and the day of a recorded run whose figures a report shows in place of the pull request's own, and null otherwise. `costroid approve` and the readers still take 0.5 to 0.7.
- `costroid report --preview` refuses a `--rows` path that holds no file, with exit 125, instead of printing a comment with no figures.
- `costroid demo` prints plain text for a terminal. `--markdown` prints the comment as before.
- The pull request comment's account link goes to https://costroid.com/account/ when the report names no installation and no platform.

### What you can now do

- Show a recorded run on a pull request that spends nothing: `costroid report --recorded RUN_ID,DATE` opens the check, the comment and the job summary with one line that says the figures are that run's, with its date, not the pull request's. A recorded report is left out of the pull request's total.
- Send a rows file to an OpenTelemetry Collector or a tracing backend: `costroid emit-otlp --rows FILE` sends one span per model request over OTLP/HTTP, with the GenAI attributes and the request's exact cost. [`docs/otlp.md`](docs/otlp.md) lists every attribute.
- Read a gh-aw run's cap: `ingest-run --from gh-aw=DIR` also reads `info/aw_info.json` beside `DIR`, so the comment's Enforcement cell names the AI credit cap gh-aw checks. gh-aw's firewall checks it before each request, so a run can end past it; when gh-aw's stated credits pass the cap, the cell states by how much, in AI credits. `recipes/github-gh-aw-follow-up.yml` downloads both artifacts. The reader admits every file gh-aw v1.0.1 to v1.2.1 writes to its `usage` artifact.
- `costroid demo --state neutral|capped|failure|skipped` shows each kind of comment.
- `costroid run` without `--out` writes `costroid-rows.jsonl` in the working directory, the name the action uses, and says so before the agent starts.

### Fixes

- `costroid reconcile` judges a run that crosses 00:00 UTC by token class across its days.
- A preview states its own sentences in the comment, instead of rewriting the comment's.
- `costroid serve` reads `costroid.cost.basis` from OTLP spans.
- A per-run entry's context tier is chosen where its rows are priced, and a coarse row whose tier is unknown says its figure may read low.
- A command's help prints on stdout when `-h` follows other flags.

## 1.0.0-alpha.14 (2026-11-08)

### Breaking changes

- `costroid reconcile` stops before any request when the rows name a gateway run key and no `--upstream-credential SOURCE=ID` says which provider key the gateway sends with. Give the flag once per source.

### What you can now do

- Install in one step: the release carries `costroid-install.sh`, which checks the release's signature with cosign and the binary against `SHA256SUMS` before it installs.
- `go install github.com/Costroid/costroid/cmd/costroid@<release>` prints its release, and `init` pins the commit that release's tag names.
- Reconcile a service account under Workload Identity Federation by its actor, and a gateway's rows under the provider key the gateway sends with. A run that crosses 00:00 UTC is compared across its days and named as the cause, never as a miss.

### Fixes

- A run whose rows fell to per-model grain could read up to five times its bill. A per-model sum of input tokens took the long-context tier: one `claude-code-action` run on `claude-haiku-5-5` read USD 0.044186675 against Claude Code's own USD 0.008837335. A sum of several requests now never selects a long-context tier; Claude Code's own per-model cost decides it, and where nothing decides, the rows take the standard tier and say the figure may read low.
- Session files that hold every conversation request now stand when the result also counts Claude Code's one request outside the conversation; that request becomes an entry of its own, and each request keeps its own tier.
- A Codex turn whose summed input passes a context boundary takes the standard tier with that note, instead of the tier above.

## 1.0.0-alpha.13 (2026-11-08)

### Breaking changes

- The result object is `costroid.result/0.7`. It adds `agent_stated`, an agent's own figure in its own unit, and the run reasons `unmeasured_spend` and `no_run_rows`. `costroid approve` and the readers still take 0.5 and 0.6, and refuse an older object that carries a value only a newer version defines.
- The report states the cap the run recorded, from the run's result object. The report step needs no budget input; `--budget-usd` is read only when no result object exists, and a figure that disagrees with the object is ignored with a warning. Copy the recipes again.
- Runs price with catalog 0.3.0. Its fallback for a model the sheet does not name is the highest price of each token class across every row, so an unknown OpenAI model's output is priced at USD 600 per million tokens, labelled as the fallback. Gemini 2.5 Pro and 2.5 Flash bill at their successors' rates from 2026-12-01, as Google states.
- A gateway run gives Claude Code a settings file of its own, so a repository's `.claude/settings.json` cannot send requests around the gateway or choose the key through `apiKeyHelper`.
- `costroid connect` binds a GitLab namespace only on gitlab.com.
- `costroid lint-recipes` flags a workflow that states two different caps, and checks the plugin marketplace.

### What you can now do

- Read a GitHub Agentic Workflows run: `ingest-run --from gh-aw` reads its `usage` artifact, `follow-up --ingested` posts the check, and `recipes/github-gh-aw-follow-up.yml` wires both. gh-aw's AI credits appear on a line of their own, never added to a dollar figure.
- Ask Costroid from an agent: `costroid mcp` serves four read-only tools over stdio, and `costroid result --pr N` prints the newest check's result object.
- Install the Claude Code plugin from this repository: `/plugin marketplace add Costroid/costroid`, then `/plugin install costroid@costroid`.
- Run the Claude Agent SDK or the Codex SDK under a budget: `costroid run --agent claude-code-sdk|codex-sdk -- <program>`, with `recipes/github-claude-agent-sdk.yml` and `recipes/github-codex-sdk.yml`. `--agent` also places Costroid's flags after an `npx`, `bunx` or `node` package.
- Go from nothing to an open pull request: `costroid init github --pr` sets the key secret, commits, pushes, opens the pull request and prints the comment's URL, naming each command before it runs. `--skills` writes an agent skill; `--platform-url` also writes the connect workflow; `--existing` takes `--budget-usd`.
- Write the rows for another tool: `costroid export --format focus-csv`, `vantage` or `datadog`. No amount is converted or summed across currencies.
- `costroid doctor` sends a `claude-code-action` repository to `init --existing`, names a missing remote, reads aliased triggers, and checks the platform token's scopes and the owner's binding.
- The image and the release carry `NOTICE` and `LICENSE`, and `NOTICE` names every module the binary includes.
- `costroid init github --agent claude-agent-sdk` and `--agent codex-sdk` write the SDK recipes, and `costroid lint` checks an SDK job as an agent job, egress included.
- Every help page opens with `Usage:`, names every flag and ends with its exit codes. `costroid` with no command prints usage and exits 2.
- CI verifies the embedded catalog's signature with cosign against the catalog's release workflow, and checks every sheet against the signed sums.

### Fixes

- A request reported in two OTLP batches is stored as one write row.
- Every lint finding stays on one line, whatever the value it quotes.
- `costroid connect` retries once when the platform's key set is unavailable, after its `Retry-After`.
- An empty budget from an environment expression reads as no budget, never as zero.
- The SDK agent reads a session line written in the run's first millisecond.
- `gateway close`'s help gave the wrong default for `--retry-after`; it is 0.

## 1.0.0-alpha.12 (2026-11-08)

### Breaking changes

- The GitHub recipes run `step-security/harden-runner` on the agent's job in block mode, with its telemetry off. The job reaches only the model provider, GitHub and the hosts the install needs. Copy the recipes again. The LiteLLM recipe needs the repository variable `COSTROID_GATEWAY_HOST`.
- The `codex` recipe reads the secret `OPENAI_API_KEY`, which the action gives Codex as `CODEX_API_KEY`.
- `costroid init` writes its files at the repository's root, from any directory inside it. Outside a repository it refuses with exit 2.
- `costroid reconcile` exits 1 on a price that misses the catalog, as it does on a token miss.
- A gateway run refuses an agent command that routes around the gateway: Codex's `--oss`, `--local-provider` and its own provider or base URL, and Claude Code's `--settings` naming a base URL, a token or a cloud provider.
- With no account named, the comment's account link opens [docs.costroid.com/account](https://docs.costroid.com/account). On GitLab the link names `gitlab-namespace:<id>` on gitlab.com and no id on a self-managed instance.

### What you can now do

- Hold `claude-code-action` to a cap: pass `--max-budget-usd` in `claude_args` and the same figure to the `read` stage as `budget-usd`, with the new `claude-args` input. The grade then reads `agent-flag`, and `none` with the reason when the two differ. `costroid lint` notes an action step without the flag.
- Run Codex through a LiteLLM gateway on a key made for the run. Codex takes the gateway's address and provider on its command line, which override its own configuration, and its rows read `measured`. Claude Code's gateway rows read `measured` too.
- Watch a run while it works: `costroid run` writes `$COSTROID_STATUS_FILE`, by default `$RUNNER_TEMP/costroid-status.json`, after each priced request. It holds spend per basis, the cap, the grade and counts, never content. Its schema is `costroid.status/0.1`.
- Fail a GitLab merge request's pipeline on its verdict: set `COSTROID_GATE`, and the report job exits 1 on `failure` and 3, a warning, on `neutral`. [`docs/gitlab.md`](docs/gitlab.md) has the template.
- Send the rows to Costroid Platform from the recipes, bind the repository's owner with `costroid connect` and the new `recipes/github-connect.yml`, and read each batch's verdict from `costroid export`.
- Post the agent's own answer as a second comment, opt-in with `post-review: "true"`. The comment shows it as plain text and says Costroid did not write or check it.
- Prove each price from the provider's own figures: `costroid reconcile` reads Anthropic's cost report by model and token type and OpenAI's by line item, and compares each class's dollars within 1%.
- Sign in with a Claude subscription: `costroid init github --agent claude-code-action --auth subscription`.
- Verify a release's build provenance with `gh attestation verify`, on each release file and on the image.

### Fixes

- A per-run row prices Claude Code's input at the context tier of the model's whole input, carries fast mode and a region seen on any message, and composes fast mode's cache prices from the fast input price.
- The follow-up names its own check run in its result object.
- `costroid approve` re-concludes a check that an earlier release wrote as `costroid.result/0.5`.

## 1.0.0-alpha.11 (2026-11-07)

### Breaking changes

- A rows artifact that holds anything besides the rows and the run object now fails the `costroid` check with the run reason `artifact_refused`, naming the entries it refused. It used to skip the report, so no check posted. An approval does not turn it neutral.
- `costroid report` skips an `--installation-id` in neither form `github:<id>` nor a bare number with a warning, and posts without the account link.
- `costroid lint-recipes` refuses `id-token: write` on a gateway job that names no environment, `environment: ""` included.

### What you can now do

- Check a repository's setup with `costroid doctor`: each problem on one line, with its fix. A secret the repository's own list lacks is a warning, because an organization or environment secret does not show there.
- Pass a budget on `costroid serve` only on spend the gateway measured: `costroid gateway close` attests the key's spend, and a budget concludes `success` only on that figure or a reconciled provider report, never on the agent's own counts.
- End the warn-only period at the twentieth pull request with a measured cost, counted from the earlier `costroid` checks.
- Read an empty `costroid.yml`, a file that is only `---`, `~` or `null`, or a section with no value as the defaults, in `validate` and every reader alike.
- Read `openai/codex-action` rows as `measured`: that action takes only an API key.
- See which bases a pull request's budget adds together, on the comment's line.

### Fixes

- The Anthropic usage reader refuses a negative token count, as the OpenAI reader does.
- `costroid validate` refuses `approval.expires_days` written as a float.
- `costroid lint-recipes` reports an agent it does not know instead of stopping.
- A data race between the agent's stderr and the run's own warnings, seen under `go test -race`.

## 1.0.0-alpha.10 (2026-11-07)

### Breaking changes

- The result object is `costroid.result/0.6`. It adds `rows_sha256` and `rows_file_sha256`, which name the rows a report was built from, a `lifetime` object with the pull request's total, the policy's new rule names, and the run reason `artifact_refused`.
- `costroid validate` refuses an amount written as a float, such as `5.0`, as the policy does. Write `5` or `"5.00"`.
- The GitLab template runs `costroid:report` and `costroid:ingest` in the `.post` stage, follows the agent's label rule, and pins `COSTROID_COMMIT` beside `COSTROID_VERSION`. Copy the template again.
- `costroid` with no command prints a short usage. `costroid help <command>` prints one command's flags.

### What you can now do

- Read what the run cost first: the comment and the check open with the figure, its basis and its grade.
- See the pull request's total across its runs, one line per currency and basis, read from the earlier `costroid` checks. Repeated reports are counted once.
- Hold a pull request to a budget across its runs (`policy.pr_budget_usd`), a model allowlist (`policy.models`), and an increase in USD or percent. Budgets accept whole numbers.
- Approve a failing check with a pull-request review from a writer who is not the author. `recipes/github-approval.yml` re-posts the check as `neutral`, naming the approver.
- Write the workflow with `costroid init`, pinned to the release the binary belongs to, and add Costroid to an existing workflow with `costroid init --existing`.
- Lint your own agent workflows with `costroid lint`: an agent reachable by an untrusted actor is a high finding. `claude-code-action`'s own check of who triggered it counts as a guard.
- Measure `openai/codex-action` runs with `recipes/github-codex-action.yml`. The action takes the key from `CODEX_API_KEY` or `OPENAI_API_KEY`.
- Print the comment for a rows file with `costroid report --preview`, or a recorded run's with `costroid demo`.
- Trust that a run leaves nothing behind: on Linux, `costroid run` kills and reaps every process the agent left, `setsid` children included.

## 1.0.0-alpha.9 (2026-11-07)

### Breaking changes

- The action's `stage` input has no default. A step that leaves it out fails at install and lists the six stages, one line each. It used to read `all`.
- The gateway's sealing key pair is drawn at random. Run `costroid gateway keygen`, store the private half as the secret `COSTROID_GATEWAY_SEALING_KEY` of the `costroid-gateway` environment and the public half as the variable `COSTROID_GATEWAY_PUBLIC_KEY`, then rotate the admin key. `costroid gateway mint` refuses a request sealed to a key derived from the admin key, as earlier releases derived it. [`docs/gateway.md`](docs/gateway.md) has the steps.
- The gateway's URL must be `https`, except on `localhost` and `127.0.0.1`.
- The fork follow-up's job needs `pull-requests: read`. `costroid lint-recipes` refuses a follow-up job without it, and one that writes anything but checks.
- `costroid lint-recipes` refuses three more shapes: a checkout in an agent's job without `persist-credentials: false`, a `download-artifact` step that unpacks into `runner.temp` itself or above it, and a job that holds the gateway's admin key or sealing key without naming an environment.
- The result object is `costroid.result/0.5`. It adds the reason `agent_skipped`, the run reasons `reader_refused` and `agent_error`, and the fields `reader_error`, `agent_error`, `agent_skipped` and `ci_run`. Each new field is present, and null until something states it.
- On GitHub, `costroid report` posts on the `pull_request` event alone. On any other event it posts nothing and prints one line that says why.
- The ledger grades a row `exact` only when the gateway job that minted its key attested that key for the row's run. A stated `SubAccountId` alone no longer earns `exact`. For rows to earn it, the mint job needs `id-token: write` and the two ingest variables. Rows already stored keep their grade.

### What you can now do

- See why an agent wrote no usage. A run whose agent ended in an error states `agent_error` with the error's class and its fix, in one line and in the comment. The action's new `agent-result` input lets the report say that the agent's job was skipped, instead of pointing at a job log that does not exist.
- Let the action install the agent. The new `agent-version` input installs that version when the agent is not on the runner, by default the version the recipes install. The `run` stage names every empty input in one line.
- Run any Codex version whose rollout file keeps the shape Costroid checks, with the watchdog and per-request rows. A version with no recorded fixture adds a note that names it.
- Change a budget on the base branch and have every open pull request use it at its next report. The report reads `costroid.yml` at the base branch's head, not at the merge-base, and records the commit as `config_ref`.
- Read the provider's own amounts in `costroid reconcile`'s rows: `provider_effective`, `provider_list`, the report's freshness stamp, and FOCUS 1.4's rounding tolerance on the list figures.
- Set `ENABLE_PROMPT_CACHING_1H` and have Claude Code's unsplit cache writes on an API key read as one-hour writes.

### Fixes

- A fork's follow-up posts only when exactly one open pull request from the fork's branch has the finished run's head commit, and its check concludes `neutral` at most. A fork could otherwise pass or fail a check on a commit of someone else's.
- The report and ingest stages fetch the agent job's artifact into a directory of their own before they install anything, and the install refuses an artifact that holds anything but the rows and the run object. An artifact entry could otherwise replace the binary the report then ran.
- A Claude Code run no longer fails on one odd line. A stream or session file line of a type Costroid does not price is skipped and counted in one warning. A count written as `10.0` or `"10"` reads as 10. A session file cut off inside its last line keeps its rows and counts the request in flight as unrecorded.
- When the reader refuses a stream, the run still writes its result object, with the reason `reader_refused` and the reader's first error line, so the check says why.
- Cache writes in Claude Code's OTLP usage count once. The span's copy used to land a second time, at an assumed lifetime.
- The sticky comment is matched by the token's own account, `github-actions[bot]` by default, instead of any bot.
- In the gateway recipe, the close job runs after the mint job, so a key minted late is deleted at once.

## 1.0.0-alpha.8 (2026-11-06)

### Fixes

- A `claude-code-action` stream line whose `message` is a plain string no longer refuses the run.
- The gateway and fork follow-up recipes pin `1.0.0-alpha.7`, the first release that holds their commands.

## 1.0.0-alpha.7 (2026-11-06)

### Breaking changes

- The result object is `costroid.result/0.3`, which renames `budget` to `cap` and adds the reason `fork_no_rows`.

### What you can now do

- Stop Claude Code at a budget before a request is paid. `costroid gateway` gives each CI run its own LiteLLM virtual key, with the run's budget, an expiry and the run in its metadata, and deletes it when the job ends. The rows read `gateway-precheck` with an overshoot bound. [`docs/gateway.md`](docs/gateway.md) has the design and the recipe.
- Post the `costroid` check on a fork's pull request. The follow-up recipe runs from your default branch once the agent's workflow completes, reads the fork's rows as data, and posts the check.
- Install `costroid` with the action and do nothing else, with `stage: install`.
- Read why a run wrote no rows in the comment. `costroid run` writes its result object beside the rows file, and the report reads the run's reason from it.

### Fixes

- The comment names the run's cap apart from the policy's budget, with the grade that held the run.
- The agent guard of `costroid lint-recipes` no longer applies to a workflow that only runs on `schedule` or `workflow_dispatch`.

## 1.0.0-alpha.6 (2026-11-06)

### Breaking changes

- An informational row's `SkuPriceId` reads `<provider>:informational:<class>`. It used to read `<provider>/<model>/<class>/informational`.

### What you can now do

- Hold a pull request to a cost policy. `policy.run_budget_usd`, `policy.max_run_cost_usd` and `policy.enforce` in `costroid.yml` on the base branch decide the check's conclusion. A failure warns for the first 7 days after the workflow is added. [`docs/policy.md`](docs/policy.md) has the rules.
- Give the check's verdict offline with `costroid policy`.
- Read one result object from `costroid run --json`, `costroid report --json`, the check run and the action's new outputs, from `cost` to `conclusion` and `result`. [`docs/agents.md`](docs/agents.md) shows it.
- Read the cap in USD on the comment's budget line, with the weakest enforcement grade and what is left or over.

### Fixes

- `claude-code-action` runs no longer warn that the session files disagree with the result.
- Codex subagent threads count toward the rows and the cap.
- A run that writes no rows states a reason: `no_usage`, or `concurrent_run` when another run held the agent's state directory.
- A Codex run stopped inside a turn says how many requests were in flight.
- The GitLab template pins the release the GitHub recipes pin and takes the binary's SHA-256 from the signed `SHA256SUMS`.

## 1.0.0-alpha.5 (2026-11-04)

### What you can now do

- Call the action from another action, as the GitHub Marketplace listing `Costroid/costroid-action` does.

### Fixes

- The action installs when another action calls it. It read an empty ref in that case and refused.

## 1.0.0-alpha.4 (2026-11-04)

### What you can now do

- Pin the action by its commit alone. It installs the release that commit belongs to and checks it by its cosign signature, so `version` and `sha256` are needed only for another release.
- Keep `claude-code-action` and report what its runs cost, with [`recipes/github-claude-code-action.yml`](recipes/github-claude-code-action.yml).
- Check each figure in the comment. Each one has a collapsed trace of catalog, token class, price id, unit price, tokens and amount, and a figure priced at the catalog's fallback tier is marked. The comment names the run's head commit and links the CI run.
- Read the export, required-check and troubleshooting pages.

### Fixes

- The comment withholds token counts with the amounts, because a count times the public catalog gives the amount back.
- Sessions that a hook or a plugin starts inside a run count toward its cost.
- `costroid.yml` keys that the report does not read print one warning each.
- Rows whose own arithmetic does not hold are refused.

## 1.0.0-alpha.3 (2026-11-04)

### What you can now do

- Read the execution file of a `claude-code-action` run with the action's `read` stage, held to a minimum agent version.

### Fixes

- The report job holds `contents: read`, so it reads `costroid.yml` on a private repository.
- A run that one reading covers whole counts as one run in the summary.

## 1.0.0-alpha.2 (2026-11-04)

### Fixes

- The agent's stream goes to a file on the runner, not to the job log, which is public on a public repository. `show-output: true` prints it.
- The report step passes the budget to the comment.

## 1.0.0-alpha.1 (2026-11-04)

The first release.

- `costroid run` runs Claude Code or Codex under a budget and writes one FOCUS row per request per token class, priced at list from the pinned catalog `0.1.0`.
- `costroid ingest-run` reads the execution file of a `claude-code-action` run.
- `costroid report` posts one `costroid` check run and one sticky comment on a GitHub pull request, and one sticky note on a GitLab merge request.
- The action and the recipes for Claude Code, Codex and GitLab CI, with `costroid lint-recipes` and `costroid validate`.
- `costroid serve` keeps rows in Postgres, under a CI OIDC token or an ingest key from `costroid keys`, behind an egress allowlist that `costroid egress report` reports on. It ships as an image, a Compose file and a Helm chart.
- `costroid reconcile` compares rows with Anthropic's and OpenAI's usage and cost reports, per credential per UTC day.
- Binaries, the image and the SBOM are signed with cosign, keyless.
