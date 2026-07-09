// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { describe, expect, it } from "vitest";
import { rangeQuery } from "./range";

describe("rangeQuery", () => {
  it("returns an empty string for an open range", () => {
    expect(rangeQuery("", "")).toBe("");
  });

  it("returns a start and end query for a bounded range", () => {
    expect(rangeQuery("2026-05-01", "2026-05-31")).toBe(
      "?start=2026-05-01&end=2026-05-31",
    );
  });
});
