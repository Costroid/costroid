# Bundled power/cost-assumption profiles (M3, R8)

`hardware.v1.json` is the **dated, stamped, overridable** assumption set the local-inference
**estimated mode** uses (COSTROID-NEXT.md §3.2 cost model, §6.4). It is a **vendored data
artifact**, compiled into `costroid-power` via `include_str!` — **never fetched** at build or
runtime (R8), mirroring `costroid-core/pricing/`'s posture.

## What it carries

- **Hardware profile(s)** — id, description, `load_watts` (the estimated average inference
  draw; `load_watts_range` records the community span), `idle_watts`, `hardware_price`,
  `hardware_lifetime_seconds`, `memory_bandwidth_gbps`.
- **A default electricity rate** — `value` + `currency` + `as_of` + `label`.

## Honesty (R6 / R10)

Every value is an **ESTIMATE**, flagged `"estimated": true`, sourced from the
community-measured Strix Halo ranges in COSTROID-NEXT.md §5.2 (load ~137–174 W, idle
~10–20 W) — **never a measured number** (a real captured figure is the M3b on-hardware
handoff). The default electricity rate `0.16 USD/kWh` is a deliberately-round
**`global-household-average-template`** so estimated local rows land in the USD lane by
default; set your own (e.g. the Turkey EPDK residential tariff) via `costroid bench
--electricity-rate <v>` or the `[power]` config section.

## Provenance

| Field | Value |
|---|---|
| Source | Community-measured ranges, COSTROID-NEXT.md §5.2 (hand-authored, not fetched) |
| `as_of` | 2026-06-20 |
| Integrity | `hardware.v1.json.sha256` (fail-closed `sha256sum -c` in CI via `scripts/check_power_profiles.sh`) |
| License | Costroid's own (Apache-2.0) — synthesized assumptions, no third-party data |
| Stamp | `x_HardwareProfile = "{id}@{as_of}"` (e.g. `strix-halo-128gb@2026-06-20`) on every local row |

## Refresh

These are hand-authored assumptions, not a fetched dataset; to revise a value, edit the JSON,
bump `as_of`, and regenerate the sidecar:

```bash
cd crates/costroid-power/profiles && sha256sum hardware.v1.json > hardware.v1.json.sha256
```

The loader test `bundled_power_profiles_parses_with_pinned_as_of` (in `src/profile.rs`) pins
the recorded `as_of`; `scripts/check_power_profiles.sh` verifies the bytes match the sidecar.
After editing a bundled JSON, a local re-verify needs `cargo clean -p costroid-power` (the
`include_str!` warm-cache hazard; CARGO_INCREMENTAL=0 is insufficient).
