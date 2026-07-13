// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import { getCostsDaily } from "./api.demo";

describe("api.demo getCostsDaily", () => {
  it("returns the same fixture with and without a currency parameter", async () => {
    const params = { start: "", end: "", groupBy: "service" as const };

    const withoutCurrency = await getCostsDaily(params);
    const withCurrency = await getCostsDaily({ ...params, currency: "EUR" });

    expect(withCurrency).toBe(withoutCurrency);
  });
});
