// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import * as api from "./api";
import * as demoApi from "./api.demo";

describe("api.demo export surface", () => {
  it("matches the production transport seam", () => {
    expect(Object.keys(demoApi).sort()).toEqual(Object.keys(api).sort());
  });
});

describe("api.demo getCostsDaily", () => {
  it("returns equal fixture data with ignored filters and hides provider controls", async () => {
    const params = { start: "", end: "", groupBy: "service" as const };

    const withoutCurrency = await demoApi.getCostsDaily(params);
    const withCurrency = await demoApi.getCostsDaily({
      ...params,
      currency: "EUR",
    });
    const withProvider = await demoApi.getCostsDaily({
      ...params,
      provider: "Amazon Web Services",
    });

    expect(withCurrency).toEqual(withoutCurrency);
    expect(withProvider).toEqual(withoutCurrency);
    expect(withoutCurrency.provider).toBe("");
    expect(withoutCurrency.providers).toEqual([]);
    expect(withoutCurrency.days.length).toBeGreaterThan(0);
    expect(withoutCurrency.currencies.length).toBeGreaterThan(0);
  });
});

describe("api.demo getAnomalies", () => {
  it("returns the same fixture with and without currency or provider filters", async () => {
    const params = { start: "", end: "", groupBy: "service" as const };

    const unfiltered = await demoApi.getAnomalies(params);
    const withCurrency = await demoApi.getAnomalies({
      ...params,
      currency: "EUR",
    });
    const withProvider = await demoApi.getAnomalies({
      ...params,
      provider: "Amazon Web Services",
    });

    expect(withCurrency).toBe(unfiltered);
    expect(withProvider).toBe(unfiltered);
  });
});

describe("api.demo getSyncStatus", () => {
  it("returns the enabled four-source captured fixture", async () => {
    const status = await demoApi.getSyncStatus();

    expect(status.enabled).toBe(true);
    expect(status.sources).toHaveLength(4);
  });
});
