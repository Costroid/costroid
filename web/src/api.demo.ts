// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Demo build seam: the same export surface as ./api, but served entirely from
// fixtures captured off `costroid demo` (see internal/demofixtures). It never
// makes /api requests: base fixtures are inlined, while provider-filtered
// fixtures load on demand as same-origin static chunks. The Vite `--mode demo`
// alias swaps ./api for this file, so every view's fetch flows here instead.
// Each fixture already carries the server's exact period aggregates and
// verbatim decimal strings — nothing is re-aggregated or reformatted here
// (that would risk money-exactness). This module deliberately does NOT import
// ./api: under the demo alias that specifier resolves back to this very file.

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

// Vite inlines the base fixtures into the demo entry chunk and emits filtered
// fixtures as lazy chunks. In the normal build this module is not imported, so
// neither glob nor any fixture reaches the production bundle.
const modules = import.meta.glob("./demo/fixtures/*.json", {
  eager: true,
}) as Record<string, { default: unknown }>;
const filteredModules = import.meta.glob(
  "./demo/fixtures/filtered/*.json",
) as Record<string, () => Promise<{ default: unknown }>>;

function fixture<T>(name: string): T {
  const mod = modules[`./demo/fixtures/${name}.json`];
  if (!mod) {
    throw new Error(`missing demo fixture: ${name}.json`);
  }
  return mod.default as T;
}

const knownProviders = new Set(
  fixture<DailyCosts>("costs.full.service").providers,
);

function providerSlug(provider: string): string {
  return provider
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^-+|-+$/g, "");
}

async function filteredFixture<T>(name: string): Promise<T> {
  const load = filteredModules[`./demo/fixtures/filtered/${name}.json`];
  if (!load) {
    throw new Error(`missing demo fixture: filtered/${name}.json`);
  }
  const mod = await load();
  return mod.default as T;
}

function resolveProviderFixture<T>(
  baseName: string,
  provider: string,
): Promise<T> {
  if (provider && knownProviders.has(provider)) {
    return filteredFixture<T>(`${baseName}.${providerSlug(provider)}`);
  }
  return Promise.resolve(fixture<T>(baseName));
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
  const provider = params.provider ?? "";
  return resolveProviderFixture<DailyCosts>(
    `costs.${preset}.${params.groupBy}`,
    provider,
  );
}

export function getCostsSummary(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  _signal?: AbortSignal,
): Promise<CostsSummary> {
  const preset = presetOf(params.start, params.end);
  const provider = params.provider ?? "";
  return resolveProviderFixture<CostsSummary>(
    `costs-summary.${preset}.${params.groupBy}`,
    provider,
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
  const provider = params.provider ?? "";
  return resolveProviderFixture<Anomalies>(
    `anomalies.${preset}.${params.groupBy}`,
    provider,
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
  params: RangeParams & {
    metric: string;
    currency?: string;
    provider?: string;
  },
  _signal?: AbortSignal,
): Promise<UnitEconomics> {
  // Only one business metric is captured; the range selects the fixture.
  const provider = params.provider ?? "";
  return resolveProviderFixture<UnitEconomics>(
    `unit-economics.${presetOf(params.start, params.end)}`,
    provider,
  );
}
