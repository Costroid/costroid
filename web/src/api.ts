// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

// Thin, transport-only API seam. Each helper owns exactly one endpoint's URL
// construction, calls the GLOBAL fetch, checks res.ok (throwing the endpoint's
// verbatim error string), and returns the parsed JSON. It holds NO cache,
// store, or state — every view keeps its own useEffect / AbortController /
// loading-error state and its post-await commit guard. The demo build swaps
// this module for ./api.demo via a Vite alias, so the two files share this
// exact export surface.

import type { components } from "./api/schema";
import { rangeQuery } from "./range";

type Meta = components["schemas"]["Meta"];
type SyncStatusResponse = components["schemas"]["SyncStatusResponse"];
type DailyCosts = components["schemas"]["DailyCosts"];
type CostsSummary = components["schemas"]["CostsSummary"];
type Anomalies = components["schemas"]["Anomalies"];
type DailyTokenUsage = components["schemas"]["DailyTokenUsage"];
type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];
type BusinessMetrics = components["schemas"]["BusinessMetrics"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

// CostGroupBy is the shared grouping enum for the costs and anomalies endpoints.
export type CostGroupBy = "service" | "provider" | "allocation";

// RangeParams is the inclusive [start, end] range threaded through most views;
// "" on either side means unbounded on that side.
export type RangeParams = { start: string; end: string };

export async function getMeta(signal?: AbortSignal): Promise<Meta> {
  const res = await fetch("/api/v1/meta", { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/meta returned ${res.status}`);
  }
  return (await res.json()) as Meta;
}

export async function getSyncStatus(
  signal?: AbortSignal,
): Promise<SyncStatusResponse> {
  const res = await fetch("/api/v1/sync/status", { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/sync/status returned ${res.status}`);
  }
  return (await res.json()) as SyncStatusResponse;
}

export async function getCostsDaily(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  signal?: AbortSignal,
): Promise<DailyCosts> {
  const q = rangeQuery(params.start, params.end);
  let url =
    `/api/v1/costs/daily${q}` +
    (params.groupBy !== "service"
      ? `${q ? "&" : "?"}groupBy=${params.groupBy}`
      : "");
  if (params.currency) {
    url += `${url.includes("?") ? "&" : "?"}currency=${encodeURIComponent(params.currency)}`;
  }
  if (params.provider) {
    url += `${url.includes("?") ? "&" : "?"}provider=${encodeURIComponent(params.provider)}`;
  }
  const res = await fetch(url, { signal });
  if (!res.ok) {
    // Costs is the ONLY endpoint that reads the body: surface the server's
    // message (e.g. the unconfigured-allocation 400 body) so the error state is
    // actionable. Every other helper is status-only.
    const body = (await res.text()).trim();
    throw new Error(
      `GET /api/v1/costs/daily returned ${res.status}` +
        (body ? `: ${body}` : ""),
    );
  }
  return (await res.json()) as DailyCosts;
}

export async function getCostsSummary(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  signal?: AbortSignal,
): Promise<CostsSummary> {
  const q = rangeQuery(params.start, params.end);
  let url =
    `/api/v1/costs/summary${q}` +
    (params.groupBy !== "service"
      ? `${q ? "&" : "?"}groupBy=${params.groupBy}`
      : "");
  if (params.currency) {
    url += `${url.includes("?") ? "&" : "?"}currency=${encodeURIComponent(params.currency)}`;
  }
  if (params.provider) {
    url += `${url.includes("?") ? "&" : "?"}provider=${encodeURIComponent(params.provider)}`;
  }
  const res = await fetch(url, { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/costs/summary returned ${res.status}`);
  }
  return (await res.json()) as CostsSummary;
}

export async function getAnomalies(
  params: RangeParams & {
    groupBy: CostGroupBy;
    currency?: string;
    provider?: string;
  },
  signal?: AbortSignal,
): Promise<Anomalies> {
  const q = rangeQuery(params.start, params.end);
  let url =
    `/api/v1/anomalies${q}` +
    (params.groupBy !== "service"
      ? `${q ? "&" : "?"}groupBy=${params.groupBy}`
      : "");
  if (params.currency) {
    url += `${url.includes("?") ? "&" : "?"}currency=${encodeURIComponent(params.currency)}`;
  }
  if (params.provider) {
    url += `${url.includes("?") ? "&" : "?"}provider=${encodeURIComponent(params.provider)}`;
  }
  const res = await fetch(url, { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/anomalies returned ${res.status}`);
  }
  return (await res.json()) as Anomalies;
}

export async function getTokensDaily(
  params: RangeParams,
  signal?: AbortSignal,
): Promise<DailyTokenUsage[]> {
  const url = `/api/v1/usage/tokens/daily${rangeQuery(params.start, params.end)}`;
  const res = await fetch(url, { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/usage/tokens/daily returned ${res.status}`);
  }
  return (await res.json()) as DailyTokenUsage[];
}

export async function getUsageMetricsDaily(
  params: RangeParams,
  signal?: AbortSignal,
): Promise<DailyUsageMetric[]> {
  const url = `/api/v1/usage/metrics/daily${rangeQuery(params.start, params.end)}`;
  const res = await fetch(url, { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/usage/metrics/daily returned ${res.status}`);
  }
  return (await res.json()) as DailyUsageMetric[];
}

export async function getBusinessMetrics(
  signal?: AbortSignal,
): Promise<BusinessMetrics> {
  const res = await fetch("/api/v1/business-metrics", { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/business-metrics returned ${res.status}`);
  }
  return (await res.json()) as BusinessMetrics;
}

export async function getUnitEconomicsDaily(
  params: RangeParams & {
    metric: string;
    currency?: string;
    provider?: string;
  },
  signal?: AbortSignal,
): Promise<UnitEconomics> {
  const rangeSuffix = rangeQuery(params.start, params.end).replace("?", "&");
  let url =
    `/api/v1/unit-economics/daily?metric=${encodeURIComponent(params.metric)}` +
    rangeSuffix;
  if (params.currency) {
    url += `&currency=${encodeURIComponent(params.currency)}`;
  }
  if (params.provider) {
    url += `&provider=${encodeURIComponent(params.provider)}`;
  }
  const res = await fetch(url, { signal });
  if (!res.ok) {
    throw new Error(`GET /api/v1/unit-economics/daily returned ${res.status}`);
  }
  return (await res.json()) as UnitEconomics;
}
