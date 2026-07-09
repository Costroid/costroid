// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  render,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import UsageMetrics from "./UsageMetrics";
import type { components } from "./api/schema";

type DailyUsageMetric = components["schemas"]["DailyUsageMetric"];

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

describe("UsageMetrics", () => {
  it("renders one table section per unit and never sums across units", async () => {
    const tokensQty = "1234567890125856789";
    const bytesQty = "4096";
    const callsQty = "42";
    // Distinctive cross-unit arithmetic sum that must never appear.
    const crossSum = String(
      Number(tokensQty) + Number(bytesQty) + Number(callsQty),
    );

    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "gpt-4o",
        serviceTier: "",
        metricName: "uncached_input_tokens",
        unit: "Tokens",
        quantity: tokensQty,
      },
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "usage_bytes",
        unit: "Bytes",
        quantity: bytesQty,
      },
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "web_search_num_requests",
        unit: "Calls",
        quantity: callsQty,
      },
      {
        date: "2026-05-02",
        serviceName: "claude-opus-4-6",
        serviceTier: "priority",
        metricName: "uncached_input_tokens",
        unit: "Tokens",
        quantity: "999",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<UsageMetrics />);

    // Structural: each unit is its own section heading.
    expect(await screen.findByRole("heading", { name: "Tokens" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Bytes" })).toBeTruthy();
    expect(screen.getByRole("heading", { name: "Calls" })).toBeTruthy();
    expect(screen.getAllByRole("table")).toHaveLength(3);

    const tokensSection = screen
      .getByRole("heading", { name: "Tokens" })
      .closest(".usage-metrics-unit");
    expect(tokensSection).toBeTruthy();
    expect(
      within(tokensSection as HTMLElement).getByText("Range total"),
    ).toBeTruthy();
    expect(
      within(tokensSection as HTMLElement).getByText("1234567890125857788"),
    ).toBeTruthy();
    const bytesSection = screen
      .getByRole("heading", { name: "Bytes" })
      .closest(".usage-metrics-unit");
    expect(bytesSection).toBeTruthy();
    expect(
      within(bytesSection as HTMLElement).queryByText("Range total"),
    ).toBeNull();

    // Verbatim quantities.
    expect(screen.getAllByText(tokensQty).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(bytesQty).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText(callsQty).length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("999").length).toBeGreaterThanOrEqual(1);

    // Empty tier renders as em dash.
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(1);
    // Non-empty tier renders as the tier string.
    expect(screen.getByText("priority")).toBeTruthy();

    // Negative: cross-unit sum never appears as displayed text.
    expect(screen.queryAllByText(crossSum)).toHaveLength(0);

    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/usage/metrics/daily",
      expect.anything(),
    );
  });

  it("fetches daily usage metrics for a provided range", async () => {
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "web_search_num_requests",
        unit: "Calls",
        quantity: "42",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<UsageMetrics range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect((await screen.findAllByText("42")).length).toBeGreaterThan(0);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/usage/metrics/daily?start=2026-05-01&end=2026-05-31",
      expect.anything(),
    );
  });

  it("refetches daily usage metrics when the range changes", async () => {
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "web_search_num_requests",
        unit: "Calls",
        quantity: "42",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    const { rerender } = render(
      <UsageMetrics range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    expect((await screen.findAllByText("42")).length).toBeGreaterThan(0);
    rerender(
      <UsageMetrics range={{ start: "2026-06-01", end: "2026-06-30" }} />,
    );

    await waitFor(() => expect(fetch).toHaveBeenCalledTimes(2));
    expect(fetch).toHaveBeenNthCalledWith(
      2,
      "/api/v1/usage/metrics/daily?start=2026-06-01&end=2026-06-30",
      expect.anything(),
    );
  });

  it("omits usage range totals for unknown and non-integer units", async () => {
    const rows: DailyUsageMetric[] = [
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "odd_count",
        unit: "Unknown",
        quantity: "7",
      },
      {
        date: "2026-05-01",
        serviceName: "OpenAI API",
        serviceTier: "",
        metricName: "num_model_requests",
        unit: "Requests",
        quantity: "1.5",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, rows))),
    );

    render(<UsageMetrics />);

    const unknownSection = (
      await screen.findByRole("heading", { name: "Unknown" })
    ).closest(".usage-metrics-unit");
    expect(unknownSection).toBeTruthy();
    expect(
      within(unknownSection as HTMLElement).queryByText("Range total"),
    ).toBeNull();
    const requestsSection = screen
      .getByRole("heading", { name: "Requests" })
      .closest(".usage-metrics-unit");
    expect(requestsSection).toBeTruthy();
    expect(
      within(requestsSection as HTMLElement).queryByText("Range total"),
    ).toBeNull();
  });

  it("shows the AI-connector ingest hint when the store is empty", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, []))),
    );

    render(<UsageMetrics />);

    expect(await screen.findByText(/No usage metrics yet/)).toBeTruthy();
    expect(screen.getByText(/costroid credentials set/)).toBeTruthy();
    expect(screen.getByText(/openai-cost\|anthropic-cost/)).toBeTruthy();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<UsageMetrics />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
  });
});
