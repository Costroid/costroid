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
  it("returns the same fixture with and without a currency parameter", async () => {
    const params = { start: "", end: "", groupBy: "service" as const };

    const withoutCurrency = await demoApi.getCostsDaily(params);
    const withCurrency = await demoApi.getCostsDaily({
      ...params,
      currency: "EUR",
    });

    expect(withCurrency).toBe(withoutCurrency);
  });
});

describe("api.demo getSyncStatus", () => {
  it("returns the enabled four-source captured fixture", async () => {
    const status = await demoApi.getSyncStatus();

    expect(status.enabled).toBe(true);
    expect(status.sources).toHaveLength(4);
  });
});
