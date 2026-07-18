// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import App from "./App";

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
  } as Response;
}

const emptyCosts = { currency: "", currencies: [], total: "0", days: [] };

const emptySummary = { currency: "", currencies: [], total: "0", keys: [] };

function mockFetch(demo = false) {
  return vi.fn((input: RequestInfo | URL) => {
    const url = String(input);
    const path = new URL(url, "http://x").pathname;
    if (path === "/api/v1/meta") {
      return Promise.resolve(
        fakeResponse(200, {
          name: "costroid",
          version: "0.1.0-test",
          focusVersion: "1.4",
          demo,
        }),
      );
    }
    if (path === "/api/v1/usage/tokens/daily") {
      return Promise.resolve(fakeResponse(200, []));
    }
    if (path === "/api/v1/usage/metrics/daily") {
      return Promise.resolve(fakeResponse(200, []));
    }
    if (path === "/api/v1/business-metrics") {
      return Promise.resolve(fakeResponse(200, { metrics: [] }));
    }
    if (path === "/api/v1/sync/status") {
      return Promise.resolve(
        fakeResponse(200, { enabled: false, sources: [] }),
      );
    }
    if (path === "/api/v1/costs/summary") {
      return Promise.resolve(fakeResponse(200, emptySummary));
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        fakeResponse(200, {
          currency: "",
          parameters: {
            k: "3",
            consistencyConstant: "1.4826",
            windowDays: 30,
            minObservations: 10,
            relativeFloor: "0.1",
            groupBy: "service",
          },
          anomalies: [],
        }),
      );
    }
    // costs/daily and any other path
    return Promise.resolve(fakeResponse(200, emptyCosts));
  });
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("App", () => {
  it("renders meta values fetched from the API", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    expect(await screen.findByText("costroid")).toBeTruthy();
    expect(screen.getByText("0.1.0-test")).toBeTruthy();
    expect(screen.getByText("1.4")).toBeTruthy();
    expect(fetch).toHaveBeenCalledWith("/api/v1/meta", expect.anything());
  });

  it("renders no synthetic-data banner, even in demo mode", async () => {
    vi.stubGlobal("fetch", mockFetch(true));

    render(<App />);

    expect(await screen.findByText("costroid")).toBeTruthy();
    expect(screen.queryByText(/DEMO/)).toBeNull();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<App />);

    const alerts = await screen.findAllByRole("alert");
    expect(alerts.some((alert) => alert.textContent?.includes("500"))).toBe(
      true,
    );
  });

  it("renders a skip link targeting the view panel", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    const skipLink = screen.getByRole("link", { name: "Skip to content" });
    expect(skipLink.getAttribute("href")).toBe("#view-panel");
    expect(document.getElementById("view-panel")).toBeTruthy();
    await screen.findByRole("heading", { name: "Overview" });
  });

  it("defaults to the Overview view", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Overview" }),
    ).toBeTruthy();
    const overviewButton = screen.getByRole("button", { name: "Overview" });
    expect(overviewButton.getAttribute("aria-current")).toBe("page");
  });

  it("registers Overview first in the nav and defaults to Overview", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    const nav = screen.getByRole("navigation", { name: "Dashboard views" });
    const buttons = nav.querySelectorAll("button");
    expect(buttons[0]?.textContent).toContain("Overview");
    expect(
      screen
        .getByRole("button", { name: "Overview" })
        .getAttribute("aria-current"),
    ).toBe("page");
    expect(
      screen
        .getByRole("button", { name: "Costs" })
        .getAttribute("aria-current"),
    ).toBeNull();
  });

  it("switches to the Costs view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.click(screen.getByRole("button", { name: "Costs" }));

    expect(
      await screen.findByRole("heading", { name: "Daily cost by service" }),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Costs" })
        .getAttribute("aria-current"),
    ).toBe("page");
  });

  it("switches to the Tokens view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.click(screen.getByRole("button", { name: "Tokens" }));

    expect(
      await screen.findByRole("heading", {
        name: "Daily token usage by service",
      }),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Tokens" })
        .getAttribute("aria-current"),
    ).toBe("page");
    // Empty store → AI-connector ingest hint.
    expect(await screen.findByText(/No token usage yet/)).toBeTruthy();
  });

  it("switches to the Usage view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.click(screen.getByRole("button", { name: "Usage" }));

    expect(
      await screen.findByRole("heading", { name: "Daily usage metrics" }),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Usage" })
        .getAttribute("aria-current"),
    ).toBe("page");
    expect(await screen.findByText(/No usage metrics yet/)).toBeTruthy();
  });

  it("switches to the Unit economics view on click", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.click(screen.getByRole("button", { name: "Unit economics" }));

    expect(
      await screen.findByRole("heading", { name: "Unit economics" }),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Unit economics" })
        .getAttribute("aria-current"),
    ).toBe("page");
    expect(await screen.findByText(/No business metrics yet/)).toBeTruthy();
  });

  it("registers Sources last and switches to it on click", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    const nav = screen.getByRole("navigation", { name: "Dashboard views" });
    const buttons = nav.querySelectorAll("button");
    expect(buttons[buttons.length - 1]?.textContent).toContain("Sources");

    fireEvent.click(screen.getByRole("button", { name: "Sources" }));

    expect(
      await screen.findByRole("heading", { name: "Scheduled ingestion" }),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Sources" })
        .getAttribute("aria-current"),
    ).toBe("page");
  });

  it("threads the selected date range to the active view", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    expect(await screen.findByText("Showing all time")).toBeTruthy();
    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.change(screen.getByLabelText(/start date/i), {
      target: { value: "2026-05-01" },
    });
    expect(await screen.findByText("Showing from 2026-05-01")).toBeTruthy();
    expect(
      screen.getByText("Showing from 2026-05-01").getAttribute("aria-live"),
    ).toBe("polite");

    fireEvent.change(screen.getByLabelText(/end date/i), {
      target: { value: "2026-05-31" },
    });

    expect(
      await screen.findByText("Showing 2026-05-01 → 2026-05-31"),
    ).toBeTruthy();
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/costs/summary?start=2026-05-01&end=2026-05-31&groupBy=provider",
        expect.anything(),
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Tokens" }));
    await screen.findByRole("heading", {
      name: "Daily token usage by service",
    });
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/usage/tokens/daily?start=2026-05-01&end=2026-05-31",
        expect.anything(),
      ),
    );
  });

  it("renders an end-only range indicator without a dangling arrow", async () => {
    vi.stubGlobal("fetch", mockFetch());

    render(<App />);

    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.change(screen.getByLabelText(/end date/i), {
      target: { value: "2026-05-31" },
    });

    expect(await screen.findByText("Showing through 2026-05-31")).toBeTruthy();
    expect(screen.queryByText("Showing  → 2026-05-31")).toBeNull();
  });
});
