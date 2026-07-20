// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

const STATE_KEYS = [
  "view",
  "start",
  "end",
  "groupBy",
  "currency",
  "provider",
  "metric",
] as const;

const VIEWS = [
  "overview",
  "costs",
  "tokens",
  "usage",
  "unit-economics",
  "sources",
] as const;

const GROUPINGS = [
  "service",
  "provider",
  "allocation",
  "subaccount",
  "region",
] as const;
const DATE = /^\d{4}-\d{2}-\d{2}$/;
const CURRENCY = /^[A-Z]{3}$/;

type View = (typeof VIEWS)[number];
type GroupBy = (typeof GROUPINGS)[number];

export type UrlState = {
  view?: View;
  start?: string;
  end?: string;
  groupBy?: GroupBy;
  currency?: string;
  provider?: string;
  metric?: string;
};

function includes<T extends string>(
  values: readonly T[],
  value: string,
): value is T {
  return values.some((candidate) => candidate === value);
}

export function readUrlState(): UrlState {
  const params = new URLSearchParams(window.location.hash.slice(1));
  const state: UrlState = {};
  const view = params.get("view");
  const start = params.get("start");
  const end = params.get("end");
  const groupBy = params.get("groupBy");
  const currency = params.get("currency");
  const provider = params.get("provider");
  const metric = params.get("metric");

  if (view !== null && includes(VIEWS, view)) state.view = view;
  if (start !== null && DATE.test(start)) state.start = start;
  if (end !== null && DATE.test(end)) state.end = end;
  if (groupBy !== null && includes(GROUPINGS, groupBy)) {
    state.groupBy = groupBy;
  }
  if (currency !== null && CURRENCY.test(currency)) state.currency = currency;
  if (
    provider !== null &&
    provider !== "" &&
    new TextEncoder().encode(provider).byteLength <= 8192
  ) {
    state.provider = provider;
  }
  if (metric !== null && metric !== "") state.metric = metric;

  return state;
}

export function writeUrlState(partial: UrlState): void {
  const next: UrlState = { ...readUrlState(), ...partial };
  const params = new URLSearchParams();

  for (const key of STATE_KEYS) {
    const value = next[key];
    if (
      value === undefined ||
      value === "" ||
      (key === "view" && value === "overview") ||
      (key === "groupBy" && value === "service")
    ) {
      continue;
    }
    params.append(key, value);
  }

  const encoded = params.toString();
  const hash = encoded === "" ? "" : `#${encoded}`;
  if (window.location.hash === hash) return;

  window.history.replaceState(
    null,
    "",
    window.location.pathname + window.location.search + hash,
  );
}
