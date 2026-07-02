// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { cleanup, render, screen } from "@testing-library/react";
import DailyCosts from "./DailyCosts";
import type { components } from "./api/schema";

type DailyCostsResponse = components["schemas"]["DailyCosts"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("DailyCosts", () => {
  it("renders totals, legend, and table from the API response", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      total: "9.3618",
      days: [
        {
          date: "2026-05-01",
          total: "4.6809",
          services: [
            { serviceName: "AWS Lambda", cost: "0.1896" },
            { serviceName: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { serviceName: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
        {
          date: "2026-05-02",
          total: "4.6809",
          services: [
            { serviceName: "AWS Lambda", cost: "0.1896" },
            { serviceName: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { serviceName: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts />);

    // Period total.
    expect(await screen.findByText("9.3618")).toBeTruthy();
    // Per-day totals on the column caps.
    expect(screen.getAllByText("4.6809").length).toBeGreaterThanOrEqual(2);
    // Legend lists every service once.
    expect(screen.getAllByText("AWS Lambda").length).toBeGreaterThanOrEqual(1);
    expect(
      screen.getAllByText("Amazon Elastic Compute Cloud").length,
    ).toBeGreaterThanOrEqual(1);
    // The chart itself is rendered.
    expect(
      screen.getByRole("img", { name: "Stacked daily cost by service" }),
    ).toBeTruthy();
    // The table view carries the exact per-service values.
    expect(screen.getAllByText("3.6288").length).toBeGreaterThanOrEqual(1);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/costs/daily",
      expect.anything(),
    );
  });

  it("shows the ingest hint when the store is empty", async () => {
    const empty: DailyCostsResponse = { currency: "", total: "0", days: [] };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, empty))),
    );

    render(<DailyCosts />);

    expect(await screen.findByText(/No cost data yet/)).toBeTruthy();
    expect(
      screen.getByText(/costroid ingest --connector aws-focus --path/),
    ).toBeTruthy();
    // The single-writer store means the server must be stopped first.
    expect(screen.getByText(/single process at a time/)).toBeTruthy();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<DailyCosts />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
  });
});
