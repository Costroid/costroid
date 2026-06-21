# Costroid — known limitations & methodology caveats

Costroid is honest about what it can and cannot know. Cost is always an **estimate**
(your tokens × current prices), never the authoritative bill — design for reconciliation
against the provider invoice. This page records the methodology caveats that ride the
data; it grows as milestones land (M6 consolidates the full set).

## Sidechain (sub-agent) attribution

Claude Code transcripts mark sub-agent turns with a top-level `isSidechain: true`.
Costroid **keeps counting** every sidechain turn's tokens (they are real, billable usage)
but **annotates** them rather than trusting their attribution:

- `x_Sidechain = true` on every meter row from a sidechain turn.
- `x_AttributionConfidence = "uncertain"` (vs `"confident"` for a mainline turn).

Why uncertain: a sub-agent turn's `model` / `project` (`cwd`) as logged may reflect the
orchestrating session rather than the sub-agent's own context, so per-model / per-project
*attribution* of sidechain rows can be slightly off. The **totals are not affected** —
the tokens are counted exactly once (the `(message.id, requestId)` de-dup is unchanged).

Reconciliation note (verified vs ccusage on real logs, 2026-06): mainline usage matches
ccusage to the cent for every model; the only residual is `claude-opus-4-8` landing
~0.08% under ccusage, located entirely in how much sub-agent (sidechain) cache-read each
tool retains after de-dup — a known, benign methodology difference, not an under-count of
distinct billable turns. The provider invoice is the ground truth (reconciliation lane).

## Collector version

Every FOCUS row is stamped with `x_CollectorVersion` (the Costroid version that minted
it). Token-attribution methodology can shift between versions; the stamp lets a
replayed/exported ledger record which normalization logic produced each row.

## FOCUS import currency (multi-currency, M2)

The cloud-lane importer carries a bill's **native** `BillingCurrency` faithfully and
**never auto-converts** it (no runtime FX — that would be an undated, drifting estimate).
A non-USD row is kept in its own currency and **excluded** from the USD totals
(`grand_total_usd` / `lane_total_usd`) rather than blended in; per-currency subtotals are
surfaced via `total_by_currency`. Cross-currency sums are refused exactly as cross-lane
sums are — there is no single blended number. (Converting to a common currency would
require a dated, pinned FX snapshot under the same R8 discipline as pricing; that is out
of scope for M2.)

## Source-priced cloud rows carry the foreign per-token rate (M2)

A FOCUS-imported cloud row that carries an authoritative cost is **source-priced**: that
cost is preserved exactly (`x_Estimated = false`). As of the M2 cloud lane, when the
foreign export also carries its own pricing detail (`SkuPriceId` + `ListUnitPrice` /
`ContractedUnitPrice` + `PricingQuantity`), Costroid **carries it through verbatim**, so a
source-priced row is *fully* priced — not just costed. (When the export has **no**
`SkuPriceId`, FOCUS requires the pricing-detail columns be null, so they stay null; the
cost is still exact.) A *usage-only* imported row — no source cost — is instead
re-estimated from the layered catalog like a local log, and gets a catalog `SkuPriceId` +
rate + the `x_PricingSnapshotId` provenance stamp. (Source-authoritative rows carry **no**
`x_PricingSnapshotId` — they are the bill, not an estimate against a snapshot.)

## Live AWS / Bedrock API path — not built (file import only)

Costroid ingests AWS Data Exports FOCUS and Bedrock Application Inference Profile data
**only as a user-provided exported file** (`costroid import`), parsed pure-local in
`costroid-providers` / `costroid-core` — there is **no live AWS/Bedrock API call** anywhere
in the tool. A *live* path (calling the AWS Data Exports / Cost Explorer / Bedrock APIs,
reading AWS credentials) is **not built in M2**: it is triple-gated — it would live **only**
in `costroid-connect` behind the off-by-default `connect` feature (keychain-only secrets),
it is **C4-gated** (needs a real AWS account), and it needs its **own** human sign-off before
any code. The default `costroid` build stays byte-for-byte no-network (proven by the
forbidden-crates + offline-acceptance gates, which ban the network crates any AWS SDK would
pull). To true the synthetic AWS fixtures against a **real** export, run the conformance
gate locally with `COSTROID_REAL_AWS_FOCUS=/path/to/real-focus-1.2.csv` (a present-but-SKIP
leg; a real export never enters the repo — privacy + offline CI).

## Bedrock workload attribution is the profile ID only

Amazon Bedrock **Application Inference Profile** spend is attributed by the bounded
**system inference-profile id** (`x_InferenceProfileId`) — never the user-chosen profile
*name* or cost-allocation *tags*, which are free text and would violate R4. The importer
reads only a dedicated bounded id column; a profile name in the source (or `ResourceId` /
`Tags`) is not a field it reads, so serde drops it at parse. The real AWS Data Exports
column carrying the id is C4-truable (localized to `FocusV12Mapping`); the synthetic
fixtures use `x_InferenceProfileId`.

## Local-inference economics — measured vs estimated, package vs wall (M3)

The local-inference lane (`costroid bench`, the `local_inference` FOCUS lane) computes a
**cost per token** for running a model on your own hardware (energy + amortized hardware,
§3.2). It is honest about how much it can know:

- **Measured vs estimated is stamped on every row** (R6/R10): `x_MeasurementMode` ∈
  `measured_wallmeter` / `measured_sysfs` / `measured_lhm` / `estimated`, with `x_Estimated`
  cleared **only** when the energy was really measured. By default `costroid bench` runs in
  **estimated** mode (no hardware) — power comes from a dated, stamped, **overridable** profile
  (`crates/costroid-power/profiles/hardware.v1.json`), never a measured number.
- **No source isolates GPU-only watts on this APU.** The Strix Halo's iGPU shares a power rail
  with the CPU, so **every on-chip reading — Linux `power1_average` sysfs and the Windows
  LibreHardwareMonitor "Package" sensor alike — is whole-APU *package* power** (it overlaps the
  CPU and is time-averaged), not GPU-only. The **wall meter** measures **true total-system
  draw** (typically **~20–40% higher** than package power) and is the most honest figure — so
  the measured ladder *leads* with it (`measured_wallmeter`), and the on-chip readers are the
  optional package-grade convenience.
- **At low volume, local usually LOSES on pure cost.** Amortized hardware dominates a lightly
  used machine; local inference wins on **privacy, unlimited use, and experimentation**, not on
  $/token until volume is high. Costroid presents **ranges + methodology, never a single hero
  number** (the break-even crossover is M4).
- **The assumptions are dated, stamped, and overridable** (R8): the electricity rate (default
  `0.16 USD/kWh`, a `global-household-average-template` — set your own, e.g. the Turkey EPDK
  tariff, via `--electricity-rate` or the `[power]` config), the hardware price, and the
  amortization lifetime. The winning profile id rides `x_HardwareProfile` (`"{id}@{as_of}"`).
- **Throughput (tok/s) and quality are not measured here.** The bundled Gemma 4 manifest's
  `estimated_tok_s` is a community-analog **estimate** (flagged `tok_s_estimated`); the quality
  score is **as published** (cited, never re-derived — R10). A real captured **joules/token**
  on the actual gfx1151 hardware is the **M3b** on-hardware step (a wall meter on the Strix
  Halo is the primary path; the sysfs / LibreHardwareMonitor live reads are optional). **CI
  never asserts a real power number** — the deterministic tests run on synthetic power fixtures.

## FOCUS v1.2 import fixtures are a metadata subset

The committed `fixtures/focus/v1.2/synthetic-v12-*` and `synthetic-aws-v12.csv` are a
deliberate metadata subset (only the columns the importer reads), **not complete FOCUS
1.2 documents** — so for those the conformance gate validates the 1.3 *output*, not the
v1.2 *input*. The v1.2 input-validation leg is now wired (T9): a complete-document
fixture (`synthetic-aws-v12-full.csv`) validates against the vendored 1.2 ruleset
(`scripts/focus-ruleset-1.2/`, EXACT-matched against
`scripts/focus_known_failures_v12.txt`); the subset fixtures remain for the importer
round-trip.

## Break-even is a range, with a "never" / "infeasible" outcome (M4)

The local-vs-cloud break-even (`costroid breakeven`, `costroid_core::breakeven`) is **never a single
hero number**. Because its inputs are estimates (the energy-only rate `e`, the blended cloud rate `c`,
the electricity rate, the hardware price, the depreciation period, and the utilization), it reports a
**headline plus a sensitivity band** (the crossover recomputed across a range of those inputs) plus a
full **assumption stamp** (R6/R8) — exactly what it assumed, dated. Specific caveats:

- **It can be `Never`.** When the blended cloud per-token rate is at or below the local **energy floor**
  (`c ≤ e`), the hardware capex is **never** recovered no matter how many tokens you push. Costroid says
  so honestly (`Never`, with the reason) — it never fabricates a finite crossover.
- **It can be `Infeasible`.** When the break-even volume `V*` exceeds the machine's tokens/day ceiling
  (`estimated_tok_s × utilization × 86400`), the hardware physically cannot generate enough tokens to
  break even; that is reported as `Infeasible`, not as a reachable number.
- **One break-even lifetime.** The amortization basis is the `depreciation_period` (the calendar-fixed
  capex term); the model manifest's per-run `hardware_lifetime_seconds` is a **separate** per-run
  attribution and is never reused as the break-even period. Mixing the two is a typed error, not a
  silent blend — so the capex is amortized over exactly one declared lifetime.
- **The DeepSWE-Bench `$/task` overlay is reference-only.** It is a labeled, dated empirical cloud-side
  reference shown beside the verdict — never part of the per-token crossover math.

Every `NEVER` / `INFEASIBLE` verdict carries a textual cue (never color-alone). The full derivation is
in [`methodology.md`](methodology.md) §5.

## Interface caveats — the loopback web UI (M5)

The local web app (`costroid-server`) is **loopback-only by construction**: it binds `127.0.0.1`
(only `--port` varies, never the host), makes **no outbound call**, and embeds all assets first-party
with **zero external/CDN references** — its guarantee is *loopback-bind, no egress* (a 127.0.0.1 listen
does create a socket; the proof is the absence of egress, not the absence of a socket). It is a
**separate binary, never linked into the `costroid` CLI** (which stays byte-for-byte no-network) and
links **neither** the local-inference engine nor the credential/network crate.

- **The break-even web view is text + table only (M5).** It renders the verdict sentence, the R6
  sensitivity range, and the assumption stamp — deliberately **not** a crossover plot in M5. A
  **static-SVG crossover plot is deferred to M6+** (the timeline + comparison views already ship inline
  SVG, so the rendering primitive exists; adding the plot is additive). This is a scope note, not an
  omission.
- **The comparison view's cloud side is a counterfactual estimate.** It is the cloud **list price** for
  the same workload (labeled as such, with the pricing-snapshot id), not a bill you were charged.
- **It reads only the stored ledger.** The server projects the persisted three-lane FOCUS rows; the
  energy rate `e` comes from the stored `local_inference` rows via the pure
  `local_energy_only_rate` helper (no `core → power` edge), on the **total (in+out) token basis** shared
  with the CLI (a cross-interface test locks the two to one value).

## Uncertain-row annotation — where the cue surfaces

Several rows carry an explicit **uncertain** annotation rather than a silent best-guess. Each maps to a
real column / behavior in the code, and Costroid keeps counting the tokens while flagging the
attribution (never color-alone — the cue is a text/shape annotation):

| What is uncertain | Column / behavior | Where it surfaces |
|---|---|---|
| **Sub-agent (sidechain) attribution** — a sub-agent turn's logged `model`/`project` may reflect the orchestrator, so per-model/per-project attribution can be slightly off (totals unaffected) | `x_Sidechain = true`, `x_AttributionConfidence = "uncertain"` (vs `"confident"`) | annotated on the row; the `--plain`/screen-reader path spells the uncertainty in words (never a colored alarm) |
| **Sub-agent undercount residual** — `claude-opus-4-8` lands ~0.08% under ccusage, entirely in sidechain cache-read retained after de-dup | the de-dup methodology (a benign methodology difference, documented above) | recorded in the reconciliation lane; the invoice is the ground truth |
| **Estimated (not measured) local energy** — package-vs-wall: every on-chip reading is whole-APU package power, ~20–40% below true wall draw | `x_MeasurementMode == "estimated"` (vs `measured_*`), `x_Estimated = true` | stamped per row; the break-even view renders the measurement mode; the unverified/estimated state draws a **neutral** meter + a text cue, never a confident colored fill |
| **Cross-check-failed limit reading** | (provider quota cross-check) | the limit meter draws **neutral** with a ` ? unverified` text cue (DESIGN-SYSTEM §components), never amber/red |

The non-color cue is the load-bearing signal in every case: `--plain`/`NO_COLOR` strip all color and the
uncertainty is still readable as text. See [`methodology.md`](methodology.md) for the package-vs-wall
detail and [DESIGN-SYSTEM.md](DESIGN-SYSTEM.md) for the unverified-state rendering.
