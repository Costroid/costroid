// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Demo build seam: the same export surface as ./api, but served entirely from
// fixtures captured off `costroid demo` (see internal/demofixtures). Zero
// network. The Vite `--mode demo` alias swaps ./api for this file, so every
// view's fetch flows here instead. Each fixture already carries the server's
// exact period aggregates and verbatim decimal strings — nothing is
// re-aggregated or reformatted here (that would risk money-exactness). This
// module deliberately does NOT import ./api: under the demo alias that
// specifier resolves back to this very file.

import type { components } from "./api/schema";
import { DEMO_PRESETS, type DemoPresetId } from "./demo/ranges";

type Meta = components["schemas"]["Meta"];
type SyncStatusResponse = components["schemas"]["SyncStatusResponse"];
type DailyCosts = components["schemas"]["DailyCosts"];
type CostsSummary = components["schemas"]["CostsSummary"];
type Anomalies = components["schemas"]["Anomalies"];
type DailyTokenUsage = components["schemas"]["DailyTokenUsage"];
type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];
type BusinessMetrics = components["schemas"]["BusinessMetrics"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

// These mirror ./api's shared enums. They are re-declared (not imported) so the
// demo alias never makes this module import itself.
export type CostGroupBy = "service" | "provider" | "allocation";
export type RangeParams = { start: string; end: string };

// Vite inlines every captured fixture into the demo bundle (eager glob), so
// there is no runtime request. In the normal build this module is not imported,
// so the glob — and the fixtures — never reach the production bundle.
const modules = import.meta.glob("./demo/fixtures/*.json", {
  eager: true,
}) as Record<string, { default: unknown }>;

function fixture<T>(name: string): T {
  const mod = modules[`./demo/fixtures/${name}.json`];
  if (!mod) {
    throw new Error(`missing demo fixture: ${name}.json`);
  }
  return mod.default as T;
}

// presetOf maps a [start, end] range to the captured preset it belongs to.
// All-time ("", "") or any unrecognized range falls back to the full window.
function presetOf(start: string, end: string): DemoPresetId {
  const match = DEMO_PRESETS.find((p) => p.start === start && p.end === end);
  return match ? match.id : "full";
}

export function getMeta(_signal?: AbortSignal): Promise<Meta> {
  return Promise.resolve(fixture<Meta>("meta"));
}

export function getSyncStatus(
  _signal?: AbortSignal,
): Promise<SyncStatusResponse> {
  return Promise.resolve(fixture<SyncStatusResponse>("sync-status"));
}

export function getCostsDaily(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  _signal?: AbortSignal,
): Promise<DailyCosts> {
  const preset = presetOf(params.start, params.end);
  const costs = fixture<DailyCosts>(`costs.${preset}.${params.groupBy}`);
  // Deliberate demo-mode omission: fixtures cannot be filtered by provider, so
  // empty the selector source to keep every rendered control functional.
  return Promise.resolve({ ...costs, provider: "", providers: [] });
}

export function getCostsSummary(
  params: RangeParams & { groupBy: CostGroupBy; currency?: string },
  _signal?: AbortSignal,
): Promise<CostsSummary> {
  const preset = presetOf(params.start, params.end);
  return Promise.resolve(
    fixture<CostsSummary>(`costs-summary.${preset}.${params.groupBy}`),
  );
}

export function getAnomalies(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  _signal?: AbortSignal,
): Promise<Anomalies> {
  const preset = presetOf(params.start, params.end);
  return Promise.resolve(
    fixture<Anomalies>(`anomalies.${preset}.${params.groupBy}`),
  );
}

export function getTokensDaily(
  params: RangeParams,
  _signal?: AbortSignal,
): Promise<DailyTokenUsage[]> {
  return Promise.resolve(
    fixture<DailyTokenUsage[]>(`tokens.${presetOf(params.start, params.end)}`),
  );
}

export function getUsageMetricsDaily(
  params: RangeParams,
  _signal?: AbortSignal,
): Promise<DailyUsageMetric[]> {
  return Promise.resolve(
    fixture<DailyUsageMetric[]>(
      `usage-metrics.${presetOf(params.start, params.end)}`,
    ),
  );
}

export function getBusinessMetrics(
  _signal?: AbortSignal,
): Promise<BusinessMetrics> {
  return Promise.resolve(fixture<BusinessMetrics>("business-metrics"));
}

export function getUnitEconomicsDaily(
  params: RangeParams & { metric: string; currency?: string },
  _signal?: AbortSignal,
): Promise<UnitEconomics> {
  // Only one business metric is captured; the range selects the fixture.
  return Promise.resolve(
    fixture<UnitEconomics>(
      `unit-economics.${presetOf(params.start, params.end)}`,
    ),
  );
}
