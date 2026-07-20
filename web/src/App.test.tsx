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

const emptyCosts = {
  currency: "",
  currencies: [],
  provider: "",
  providers: [],
  total: "0",
  days: [],
};

const emptySummary = {
  currency: "",
  currencies: [],
  provider: "",
  providers: [],
  total: "0",
  keys: [],
};

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
  window.history.replaceState(null, "", "/");
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

    window.location.hash = skipLink.getAttribute("href") ?? "";
    expect(window.location.hash).toBe("#view-panel");
    expect(screen.getByRole("heading", { name: "Overview" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Costs" }));
    await waitFor(() => expect(window.location.hash).toBe("#view=costs"));
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

  it("mounts the deep-linked view and applies the range to its first requests", async () => {
    window.location.hash = "#view=costs&start=2026-06-01&end=2026-06-30";
    const fetchMock = mockFetch();
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    expect(
      await screen.findByRole("heading", { name: "Daily cost by service" }),
    ).toBeTruthy();
    await waitFor(() => {
      const urls = fetchMock.mock.calls.map(([input]) => String(input));
      expect(urls).toContain(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30",
      );
      expect(urls).toContain(
        "/api/v1/anomalies?start=2026-06-01&end=2026-06-30",
      );
    });
  });

  it("keeps the view in the hash and omits the default view", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.click(screen.getByRole("button", { name: "Costs" }));
    await waitFor(() => expect(window.location.hash).toBe("#view=costs"));

    fireEvent.click(screen.getByRole("button", { name: "Overview" }));
    await waitFor(() => expect(window.location.hash).toBe(""));
  });

  it("keeps range edits in the hash", async () => {
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    await screen.findByRole("heading", { name: "Overview" });

    fireEvent.change(screen.getByLabelText(/start date/i), {
      target: { value: "2026-06-01" },
    });
    fireEvent.change(screen.getByLabelText(/end date/i), {
      target: { value: "2026-06-30" },
    });

    await waitFor(() =>
      expect(window.location.hash).toBe("#start=2026-06-01&end=2026-06-30"),
    );
  });

  it("uses replaceState so view and range interactions do not grow history", async () => {
    window.location.hash = "#view=costs";
    vi.stubGlobal("fetch", mockFetch());
    render(<App />);
    const historyLength = window.history.length;
    await screen.findByRole("heading", { name: "Daily cost by service" });

    fireEvent.click(screen.getByRole("button", { name: "Overview" }));
    fireEvent.change(screen.getByLabelText(/start date/i), {
      target: { value: "2026-06-01" },
    });
    fireEvent.change(screen.getByLabelText(/end date/i), {
      target: { value: "2026-06-30" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Tokens" }));

    await waitFor(() =>
      expect(window.location.hash).toBe(
        "#view=tokens&start=2026-06-01&end=2026-06-30",
      ),
    );
    expect(window.history.length).toBe(historyLength);
  });

  it("snaps a deep-linked non-preset demo range to the full window", async () => {
    window.location.hash = "#start=2026-06-01&end=2026-06-30";
    vi.stubGlobal("fetch", mockFetch(true));

    render(<App />);

    expect(
      await screen.findByText("Showing 2026-01-12 → 2026-07-11"),
    ).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "Full window" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    await waitFor(() =>
      expect(window.location.hash).toBe("#start=2026-01-12&end=2026-07-11"),
    );
  });

  it("keeps a production plain load hash-free after settling", async () => {
    vi.stubGlobal("fetch", mockFetch(false));
    render(<App />);

    await screen.findByRole("heading", { name: "Overview" });
    await screen.findByText("Showing all time");
    expect(window.location.hash).toBe("");
  });

  it("writes the full-window range after a demo plain load settles", async () => {
    vi.stubGlobal("fetch", mockFetch(true));
    render(<App />);

    expect(
      await screen.findByText("Showing 2026-01-12 → 2026-07-11"),
    ).toBeTruthy();
    await waitFor(() =>
      expect(window.location.hash).toBe("#start=2026-01-12&end=2026-07-11"),
    );
  });

  it("reapplies shared filters across Costs and Overview while dormant keys persist", async () => {
    window.location.hash =
      "#view=costs&groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=requests";
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const parsed = new URL(url, "http://x");
      const currency = parsed.searchParams.get("currency") ?? "";
      const provider = parsed.searchParams.get("provider") ?? "";
      if (parsed.pathname === "/api/v1/meta") {
        return Promise.resolve(
          fakeResponse(200, {
            name: "costroid",
            version: "0.1.0-test",
            focusVersion: "1.4",
            demo: false,
          }),
        );
      }
      if (parsed.pathname === "/api/v1/costs/daily") {
        return Promise.resolve(
          fakeResponse(200, {
            ...emptyCosts,
            currency,
            currencies: ["USD"],
            provider,
            providers: ["Amazon Web Services", "Microsoft"],
          }),
        );
      }
      if (parsed.pathname === "/api/v1/costs/summary") {
        return Promise.resolve(
          fakeResponse(200, {
            ...emptySummary,
            currency,
            currencies: ["USD"],
            provider,
            providers: ["Amazon Web Services", "Microsoft"],
          }),
        );
      }
      if (parsed.pathname === "/api/v1/anomalies") {
        return Promise.resolve(
          fakeResponse(200, {
            currency,
            parameters: {
              k: "3",
              consistencyConstant: "1.4826",
              windowDays: 30,
              minObservations: 10,
              relativeFloor: "0.1",
              groupBy: parsed.searchParams.get("groupBy") ?? "service",
            },
            anomalies: [],
          }),
        );
      }
      if (parsed.pathname === "/api/v1/business-metrics") {
        return Promise.resolve(fakeResponse(200, { metrics: [] }));
      }
      return Promise.resolve(fakeResponse(404, null));
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<App />);

    await screen.findByRole("heading", { name: "Daily cost by allocation" });
    const costsURL =
      "/api/v1/costs/daily?groupBy=allocation&currency=USD&provider=Amazon%20Web%20Services";
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([input]) => String(input) === costsURL),
      ).toHaveLength(1),
    );

    fireEvent.click(screen.getByRole("button", { name: "Overview" }));
    await screen.findByRole("heading", { name: "Overview" });
    await waitFor(() =>
      expect(fetchMock).toHaveBeenCalledWith(
        "/api/v1/costs/summary?currency=USD&provider=Amazon%20Web%20Services",
        expect.anything(),
      ),
    );
    expect(window.location.hash).toBe(
      "#groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=requests",
    );

    fireEvent.click(screen.getByRole("button", { name: "Costs" }));
    await screen.findByRole("heading", { name: "Daily cost by allocation" });
    await waitFor(() =>
      expect(
        fetchMock.mock.calls.filter(([input]) => String(input) === costsURL),
      ).toHaveLength(2),
    );
    expect(window.location.hash).toBe(
      "#view=costs&groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=requests",
    );
  });
});
