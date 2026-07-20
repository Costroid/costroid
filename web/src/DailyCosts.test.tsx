// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import DailyCosts from "./DailyCosts";
import { HEIGHT, MARGIN } from "./viz";
import type { components } from "./api/schema";

type DailyCostsResponse = components["schemas"]["DailyCosts"];
type Anomaly = components["schemas"]["Anomaly"];
type AnomaliesResponse = components["schemas"]["Anomalies"];

function fakeResponse(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : ""),
  } as Response;
}

// anomaliesBody builds a valid /api/v1/anomalies response with the given flags.
function anomaliesBody(
  flags: Anomaly[] = [],
  currency = "USD",
): AnomaliesResponse {
  return {
    currency,
    parameters: {
      k: "3",
      consistencyConstant: "1.4826",
      windowDays: 30,
      minObservations: 10,
      relativeFloor: "0.1",
      groupBy: "service",
    },
    anomalies: flags,
  };
}

function dailyBody(
  currency: string,
  currencies: string[],
  total: string,
  key = "AWS Lambda",
): DailyCostsResponse {
  return {
    currency,
    currencies,
    provider: "",
    providers: ["Amazon Web Services"],
    total,
    days: [
      {
        date: "2026-05-01",
        total,
        services: [{ key, cost: total }],
      },
    ],
  };
}

function providerDailyBody(
  provider: string,
  providers: string[],
  total: string,
  key = "Shared Compute",
): DailyCostsResponse {
  return {
    ...dailyBody("USD", ["USD"], total, key),
    provider,
    providers,
  };
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

// Mirrors MARGIN.top in DailyCosts.tsx: the top y-axis tick sits exactly
// here, so no bar segment may reach a smaller y.
const MARGIN_TOP = 20;

/** Topmost y coordinate drawn by a segmentPath `d` string. */
function pathTop(d: string): number {
  const move = /^M[\d.-]+,([\d.-]+)/.exec(d);
  if (!move) {
    throw new Error(`unparseable segment path: ${d}`);
  }
  let top = Number(move[1]);
  // Rounded-top segments start at y+r and arc up by r.
  const arc = / a([\d.-]+),/.exec(d);
  if (arc) {
    top -= Number(arc[1]);
  }
  return top;
}

function renderChart(costs: DailyCostsResponse): ReturnType<typeof render> {
  vi.stubGlobal(
    "fetch",
    vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
  );
  return render(<DailyCosts />);
}

function fetchedURLs(): string[] {
  return vi.mocked(fetch).mock.calls.map(([input]) => String(input));
}

describe("DailyCosts", () => {
  it("applies hash filters to the first costs and anomaly requests from a loading frame", async () => {
    window.location.hash =
      "#groupBy=provider&currency=USD&provider=Amazon+Web+Services&metric=requests";
    const fetchMock = vi.fn((input: RequestInfo | URL) => {
      const url = String(input);
      const params = new URL(url, "http://x").searchParams;
      const requestedCurrency = params.get("currency") ?? "";
      const requestedProvider = params.get("provider") ?? "";
      if (url.startsWith("/api/v1/anomalies")) {
        const body = anomaliesBody([], requestedCurrency);
        body.parameters.groupBy = "provider";
        return Promise.resolve(fakeResponse(200, body));
      }
      return Promise.resolve(
        fakeResponse(200, {
          ...providerDailyBody(
            requestedProvider,
            ["Amazon Web Services", "Microsoft"],
            "3",
            "Amazon Web Services",
          ),
          currency: requestedCurrency,
          currencies: ["USD", "EUR"],
        }),
      );
    });
    vi.stubGlobal("fetch", fetchMock);

    render(<DailyCosts />);

    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    await waitFor(() => {
      const urls = fetchedURLs();
      expect(urls).toContain(
        "/api/v1/costs/daily?groupBy=provider&currency=USD&provider=Amazon%20Web%20Services",
      );
      expect(urls).toContain(
        "/api/v1/anomalies?groupBy=provider&currency=USD&provider=Amazon%20Web%20Services",
      );
    });
    expect(window.location.hash).toBe(
      "#groupBy=provider&currency=USD&provider=Amazon+Web+Services&metric=requests",
    );
  });

  it("writes filter interactions in canonical form and omits grouping default", async () => {
    const providers = ["Amazon Web Services", "Microsoft"];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        const params = new URL(url, "http://x").searchParams;
        const requestedCurrency = params.get("currency") ?? "";
        const requestedProvider = params.get("provider") ?? "";
        if (url.startsWith("/api/v1/anomalies")) {
          return Promise.resolve(
            fakeResponse(200, anomaliesBody([], requestedCurrency || "EUR")),
          );
        }
        return Promise.resolve(
          fakeResponse(200, {
            ...providerDailyBody(requestedProvider, providers, "3"),
            currency: requestedCurrency || "EUR",
            currencies: ["EUR", "USD"],
          }),
        );
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Currency" });

    fireEvent.click(screen.getByRole("button", { name: "Provider" }));
    await screen.findByRole("group", { name: "Currency" });
    fireEvent.click(screen.getByRole("button", { name: "USD" }));
    await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      screen.getByRole("button", { name: "Amazon Web Services" }),
    );

    await waitFor(() =>
      expect(window.location.hash).toBe(
        "#groupBy=provider&currency=USD&provider=Amazon+Web+Services",
      ),
    );
    fireEvent.click(screen.getByRole("button", { name: "Service" }));
    await waitFor(() =>
      expect(window.location.hash).toBe(
        "#currency=USD&provider=Amazon+Web+Services",
      ),
    );
  });

  it("shows display-precision day values on focus with the exact value in the SVG title", async () => {
    const { container } = renderChart({
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "256.9833670123456789",
      days: [
        {
          date: "2026-05-01",
          total: "256.9833670123456789",
          services: [{ key: "OpenAI API", cost: "256.9833670123456789" }],
        },
      ],
    });

    const hitTarget = await screen.findByLabelText("2026-05-01 cost details");
    fireEvent.focus(hitTarget);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.textContent).toContain("256.98 USD");
    expect(tooltip.textContent).toContain("OpenAI API");
    expect(tooltip.textContent).not.toContain("256.9833670123456789");
    // The exact wire value survives in the segment's native SVG title.
    expect(
      container.querySelector(".viz-segment title")?.textContent,
    ).toContain("256.9833670123456789 USD");
  });

  it("associates and Escape-dismisses the day tooltip on keyboard focus", async () => {
    renderChart({
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "10.00",
      days: [
        {
          date: "2026-05-01",
          total: "10.00",
          services: [{ key: "OpenAI API", cost: "10.00" }],
        },
      ],
    });

    const hitTarget = await screen.findByLabelText("2026-05-01 cost details");
    fireEvent.focus(hitTarget);
    const tooltip = screen.getByRole("tooltip");
    expect(tooltip.id).toBe("costs-tooltip");
    expect(hitTarget.getAttribute("aria-describedby")).toBe("costs-tooltip");

    fireEvent.keyDown(hitTarget, { key: "Escape" });
    expect(screen.queryByRole("tooltip")).toBeNull();
    expect(hitTarget.getAttribute("aria-describedby")).toBeNull();
  });

  it("renders totals, legend, and table from the API response", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "9.3618",
      days: [
        {
          date: "2026-05-01",
          total: "4.6809",
          services: [
            { key: "AWS Lambda", cost: "0.1896" },
            { key: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { key: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
        {
          date: "2026-05-02",
          total: "4.6809",
          services: [
            { key: "AWS Lambda", cost: "0.1896" },
            { key: "Amazon Elastic Compute Cloud", cost: "3.6288" },
            { key: "Amazon Simple Storage Service", cost: "0.8625" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    const { container } = render(<DailyCosts />);

    // Period total at display precision, exact value in the title.
    const total = await screen.findByText("9.36");
    expect(total.getAttribute("title")).toBe("9.3618 USD");
    // Per-day totals on the column caps (display precision).
    expect(screen.getAllByText("4.68").length).toBeGreaterThanOrEqual(2);
    // Legend structurally lists every service once.
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(3);
    // The chart itself is rendered.
    expect(
      screen.getByRole("group", { name: "Stacked daily cost by service" }),
    ).toBeTruthy();
    // The table view shows display precision with exact values in titles.
    expect(screen.getAllByTitle("3.6288 USD").length).toBeGreaterThanOrEqual(1);
    expect(screen.getAllByText("3.63").length).toBeGreaterThanOrEqual(1);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/costs/daily",
      expect.anything(),
    );
    expect(screen.queryByRole("group", { name: "Currency" })).toBeNull();
    expect(screen.queryByText("Currency")).toBeNull();
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();
    expect(screen.getByRole("button", { name: "Download CSV" })).toBeTruthy();
  });

  it("downloads the displayed daily costs as CSV", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "3.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
        {
          date: "2026-05-03",
          total: "2.00",
          services: [{ key: "AWS Lambda", cost: "2.00" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(200, anomaliesBody())
            : fakeResponse(200, costs),
        ),
      ),
    );
    class StubURL extends URL {}
    const createURL = vi.fn((_blob: Blob) => "blob:test");
    const revokeURL = vi.fn();
    StubURL.createObjectURL = createURL;
    StubURL.revokeObjectURL = revokeURL;
    vi.stubGlobal("URL", StubURL);
    let downloadName = "";
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        downloadName = this.download;
      });

    render(<DailyCosts />);
    await screen.findByRole("group", {
      name: "Stacked daily cost by service",
    });
    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));

    expect(createURL).toHaveBeenCalledTimes(1);
    const blob = createURL.mock.calls[0][0] as Blob;
    expect(blob.type).toBe("text/csv;charset=utf-8");
    expect(downloadName).toBe(
      "costroid-daily-costs-service-USD-2026-05-01_2026-05-03.csv",
    );
    expect(revokeURL).toHaveBeenCalledWith("blob:test");
    clickSpy.mockRestore();
  });

  it("renders a currency selector only for mixed-currency responses", async () => {
    const costs = dailyBody("EUR", ["EUR", "USD"], "3.123456789012345679");
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(200, anomaliesBody([], "EUR"))
            : fakeResponse(200, costs),
        ),
      ),
    );

    render(<DailyCosts />);

    expect(await screen.findByRole("group", { name: "Currency" })).toBeTruthy();
    expect(
      screen.getByRole("button", { name: "EUR" }).getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen.getByRole("button", { name: "USD" }).getAttribute("aria-pressed"),
    ).toBe("false");
    expect(
      fetchedURLs().filter((url) => url.startsWith("/api/v1/costs/daily")),
    ).toEqual(["/api/v1/costs/daily"]);
  });

  it("refetches for the selected currency and renders response values verbatim", async () => {
    const initial = dailyBody("EUR", ["EUR", "USD"], "3.123456789012345679");
    const selected = dailyBody(
      "USD",
      ["EUR", "USD"],
      "30.987654321098765434",
      "Selected service",
    );
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/v1/anomalies")) {
          return Promise.resolve(
            fakeResponse(
              200,
              anomaliesBody([], url.includes("currency=USD") ? "USD" : "EUR"),
            ),
          );
        }
        return Promise.resolve(
          fakeResponse(200, url.includes("currency=USD") ? selected : initial),
        );
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Currency" });

    fireEvent.click(screen.getByRole("button", { name: "USD" }));

    expect(
      (await screen.findAllByTitle("30.987654321098765434 USD")).length,
    ).toBeGreaterThan(0);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/costs/daily?currency=USD"),
    );
    fireEvent.focus(screen.getByLabelText("2026-05-01 cost details"));
    expect(screen.getByRole("tooltip").textContent).toContain("30.99 USD");
    await waitFor(() =>
      expect(
        fetchedURLs().filter((url) => url.startsWith("/api/v1/anomalies")),
      ).toEqual([
        "/api/v1/anomalies?currency=EUR",
        "/api/v1/anomalies?currency=USD",
      ]),
    );
  });

  it("does not commit an aborted stale currency response", async () => {
    const initial = dailyBody("EUR", ["EUR", "USD"], "1.000000000000000001");
    const fresh = dailyBody("USD", ["EUR", "USD"], "2.000000000000000002");
    // Echoes the requested USD (faithful to the server); it is discarded on
    // abort regardless, but the mock must not misrepresent the contract.
    const stale = dailyBody("USD", ["EUR", "USD"], "9.999999999999999999");
    let resolveStale!: (response: Response) => void;
    const staleResponse = new Promise<Response>((resolve) => {
      resolveStale = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("start=2026-05-01") && url.includes("currency=USD")) {
          return staleResponse;
        }
        if (url.includes("start=2026-06-01")) {
          return Promise.resolve(fakeResponse(200, fresh));
        }
        return Promise.resolve(fakeResponse(200, initial));
      }),
    );
    const { rerender } = render(
      <DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );
    await screen.findByRole("group", { name: "Currency" });

    fireEvent.click(screen.getByRole("button", { name: "USD" }));
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31&currency=USD",
      ),
    );
    rerender(<DailyCosts range={{ start: "2026-06-01", end: "2026-06-30" }} />);
    expect(
      (await screen.findAllByTitle("2.000000000000000002 USD")).length,
    ).toBeGreaterThan(0);

    await act(async () => {
      resolveStale(fakeResponse(200, stale));
      await staleResponse;
    });

    expect(
      screen.getAllByTitle("2.000000000000000002 USD").length,
    ).toBeGreaterThan(0);
    expect(screen.queryByTitle("9.999999999999999999 USD")).toBeNull();
    expect(
      screen.getByRole("button", { name: "USD" }).getAttribute("aria-pressed"),
    ).toBe("true");
  });

  it("recovers to an in-range currency when the range drops the selection", async () => {
    const initial = dailyBody("EUR", ["EUR", "USD"], "1.000000000000000001");
    const selected = dailyBody("USD", ["EUR", "USD"], "2.000000000000000002");
    // Faithful to the real server: a requested currency absent from the range
    // is ECHOED back with an EMPTY series (verified live — GET ?currency=USD on
    // a EUR-only range returns {currency:"USD",currencies:["EUR"],days:[],
    // total:"0"}). The client must NOT trust this echo as its selection.
    const droppedUsd: DailyCostsResponse = {
      currency: "USD",
      currencies: ["EUR"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "0",
      days: [],
    };
    const recovered = dailyBody("EUR", ["EUR"], "4.000000000000000004");
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("start=2026-06-01") && url.includes("currency=EUR")) {
          return Promise.resolve(fakeResponse(200, recovered));
        }
        if (url.includes("start=2026-06-01") && url.includes("currency=USD")) {
          return Promise.resolve(fakeResponse(200, droppedUsd));
        }
        if (url.includes("currency=USD")) {
          return Promise.resolve(fakeResponse(200, selected));
        }
        return Promise.resolve(fakeResponse(200, initial));
      }),
    );
    const { rerender } = render(
      <DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );
    await screen.findByRole("group", { name: "Currency" });
    fireEvent.click(screen.getByRole("button", { name: "USD" }));
    expect(
      (await screen.findAllByTitle("2.000000000000000002 USD")).length,
    ).toBeGreaterThan(0);

    // Narrow to a window where USD has no rows and only EUR remains — a
    // single-currency range, so the selector is hidden and offers no manual
    // escape from the stale USD filter.
    rerender(<DailyCosts range={{ start: "2026-06-01", end: "2026-06-30" }} />);

    // The client must snap to the first in-range currency (EUR) and render its
    // real series — NOT strand on the echoed-but-empty USD response.
    expect(
      (await screen.findAllByTitle("4.000000000000000004 EUR")).length,
    ).toBeGreaterThan(0);
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&currency=EUR",
      ),
    );
    // Single-currency range hides the selector entirely.
    expect(screen.queryByRole("group", { name: "Currency" })).toBeNull();
  });

  it("renders the provider selector, requests the encoded provider, overlays it, and names its CSV", async () => {
    const providers = ["Amazon Web Services", "Microsoft"];
    const initial = providerDailyBody("", providers, "3.000000000000000003");
    const selected = providerDailyBody(
      "Amazon Web Services",
      providers,
      "1.111111111111111111",
      "AWS Compute",
    );
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        return Promise.resolve(
          fakeResponse(
            200,
            url.includes("provider=Amazon%20Web%20Services")
              ? selected
              : initial,
          ),
        );
      }),
    );
    class StubURL extends URL {}
    StubURL.createObjectURL = vi.fn((_blob: Blob) => "blob:provider-test");
    StubURL.revokeObjectURL = vi.fn();
    vi.stubGlobal("URL", StubURL);
    let downloadName = "";
    const clickSpy = vi
      .spyOn(HTMLAnchorElement.prototype, "click")
      .mockImplementation(function (this: HTMLAnchorElement) {
        downloadName = this.download;
      });

    render(<DailyCosts />);

    expect(await screen.findByRole("group", { name: "Provider" })).toBeTruthy();
    expect(
      screen
        .getByRole("button", { name: "All providers" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen
        .getByRole("button", { name: "Amazon Web Services" })
        .getAttribute("aria-pressed"),
    ).toBe("false");

    fireEvent.click(
      screen.getByRole("button", { name: "Amazon Web Services" }),
    );

    expect(
      (await screen.findAllByTitle("1.111111111111111111 USD")).length,
    ).toBeGreaterThan(0);
    expect(
      screen
        .getByRole("button", { name: "Amazon Web Services" })
        .getAttribute("aria-pressed"),
    ).toBe("true");
    expect(
      screen
        .getByRole("button", { name: "All providers" })
        .getAttribute("aria-pressed"),
    ).toBe("false");
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?provider=Amazon%20Web%20Services",
      ),
    );
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/anomalies?currency=USD&provider=Amazon%20Web%20Services",
      ),
    );

    fireEvent.click(screen.getByRole("button", { name: "Download CSV" }));
    expect(downloadName).toBe(
      "costroid-daily-costs-service-amazon-web-services-USD-2026-05-01_2026-05-01.csv",
    );
    clickSpy.mockRestore();
  });

  it("shows loading synchronously while a provider refetch is pending", async () => {
    const providers = ["Amazon Web Services", "Microsoft"];
    const initial = providerDailyBody("", providers, "3");
    const selected = providerDailyBody("Amazon Web Services", providers, "1");
    let resolveSelected!: (response: Response) => void;
    const heldSelected = new Promise<Response>((resolve) => {
      resolveSelected = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("provider=Amazon%20Web%20Services")) {
          return heldSelected;
        }
        return Promise.resolve(fakeResponse(200, initial));
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Provider" });

    fireEvent.click(
      screen.getByRole("button", { name: "Amazon Web Services" }),
    );
    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(
      screen.queryByRole("group", { name: /Stacked daily cost/ }),
    ).toBeNull();

    resolveSelected(fakeResponse(200, selected));
    expect((await screen.findAllByTitle("1 USD")).length).toBeGreaterThan(0);
  });

  it("commits no stale all-providers chart frame on a provider switch", async () => {
    // Provider twin of the grouping-switch frame test below: a native
    // button.click() OUTSIDE act commits the synchronous re-render (provider
    // already selected) without flushing the passive effect, so the committed
    // frame is exactly what the stale-view derivation produces. Dropping the
    // provider term from that derivation commits one frame of the old
    // all-providers chart under the newly pressed selection; this pins the
    // loading frame instead. (An act-wrapped fireEvent flushes the effect
    // first and makes this vacuous.)
    const providers = ["Amazon Web Services", "Microsoft"];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("provider=Amazon%20Web%20Services")) {
          return Promise.resolve(
            fakeResponse(
              200,
              providerDailyBody("Amazon Web Services", providers, "1"),
            ),
          );
        }
        return Promise.resolve(
          fakeResponse(200, providerDailyBody("", providers, "3")),
        );
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Provider" });

    screen.getByRole("button", { name: "Amazon Web Services" }).click();
    await Promise.resolve();

    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(
      screen.queryByRole("group", { name: /Stacked daily cost/ }),
    ).toBeNull();
  });

  it("snaps a dropped provider selection to All providers", async () => {
    const initialProviders = ["Amazon Web Services", "Microsoft"];
    const initial = providerDailyBody("", initialProviders, "3");
    const selected = providerDailyBody(
      "Amazon Web Services",
      initialProviders,
      "1",
    );
    const dropped: DailyCostsResponse = {
      currency: "",
      currencies: [],
      provider: "Amazon Web Services",
      providers: ["Microsoft"],
      total: "0",
      days: [],
    };
    const recovered = providerDailyBody("", ["Microsoft"], "4", "Azure");
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("start=2026-06-01")) {
          return Promise.resolve(
            fakeResponse(
              200,
              url.includes("provider=Amazon%20Web%20Services")
                ? dropped
                : recovered,
            ),
          );
        }
        return Promise.resolve(
          fakeResponse(
            200,
            url.includes("provider=Amazon%20Web%20Services")
              ? selected
              : initial,
          ),
        );
      }),
    );
    const { rerender } = render(
      <DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );
    await screen.findByRole("group", { name: "Provider" });
    fireEvent.click(
      screen.getByRole("button", { name: "Amazon Web Services" }),
    );
    expect((await screen.findAllByTitle("1 USD")).length).toBeGreaterThan(0);

    rerender(<DailyCosts range={{ start: "2026-06-01", end: "2026-06-30" }} />);

    expect((await screen.findAllByTitle("4 USD")).length).toBeGreaterThan(0);
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&provider=Amazon%20Web%20Services",
      ),
    );
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30",
      ),
    );
    await waitFor(() => expect(window.location.hash).toBe(""));
    expect(screen.queryByRole("group", { name: "Provider" })).toBeNull();
  });

  it("fetches daily costs for a provided range", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect((await screen.findAllByText("1.00")).length).toBeGreaterThan(0);
    expect(fetch).toHaveBeenCalledWith(
      "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31",
      expect.anything(),
    );
  });

  it("refetches and renders daily costs grouped by provider", async () => {
    const serviceCosts: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    const providerCosts: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1.333333333333333334",
      days: [
        {
          date: "2026-05-01",
          total: "1.333333333333333334",
          services: [
            {
              key: "Amazon Web Services",
              cost: "1.000000000000000001",
            },
            { key: "OpenAI", cost: "0.333333333333333333" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(
            200,
            String(input).includes("groupBy=provider")
              ? providerCosts
              : serviceCosts,
          ),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    expect((await screen.findAllByText("AWS Lambda")).length).toBeGreaterThan(
      0,
    );
    fireEvent.click(screen.getByRole("button", { name: "Provider" }));

    expect(
      await screen.findByRole("group", {
        name: "Stacked daily cost by provider",
      }),
    ).toBeTruthy();
    expect(
      (await screen.findAllByText("Amazon Web Services")).length,
    ).toBeGreaterThan(0);
    expect((await screen.findAllByText("OpenAI")).length).toBeGreaterThan(0);
    expect(
      (await screen.findAllByTitle("0.333333333333333333 USD")).length,
    ).toBeGreaterThan(0);
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(2);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/costs/daily?groupBy=provider"),
    );
    expect(fetchedURLs()).toContain("/api/v1/costs/daily");
  });

  it("appends provider grouping after the range query", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "Amazon Web Services", cost: "1.00" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );

    render(<DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />);

    expect(
      (await screen.findAllByText("Amazon Web Services")).length,
    ).toBeGreaterThan(0);
    fireEvent.click(screen.getByRole("button", { name: "Provider" }));

    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31&groupBy=provider",
      ),
    );
    expect(fetchedURLs()).toContain(
      "/api/v1/costs/daily?start=2026-05-01&end=2026-05-31",
    );
  });

  it("refetches daily costs when the range changes", async () => {
    const response = (total: string): DailyCostsResponse => ({
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total,
      days: [
        {
          date: "2026-05-01",
          total,
          services: [{ key: "AWS Lambda", cost: total }],
        },
      ],
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, response("1.00")))),
    );

    const { rerender } = render(
      <DailyCosts range={{ start: "2026-05-01", end: "2026-05-31" }} />,
    );

    expect((await screen.findAllByText("1.00")).length).toBeGreaterThan(0);
    rerender(<DailyCosts range={{ start: "2026-06-01", end: "2026-06-30" }} />);

    // Each param set fetches both costs and anomalies, so assert the new-range
    // costs URL was fetched rather than a brittle total call count.
    await waitFor(() =>
      expect(fetch).toHaveBeenCalledWith(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30",
        expect.anything(),
      ),
    );
  });

  it("shows loading synchronously while a grouping refetch is pending", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1",
      days: [
        {
          date: "2026-05-01",
          total: "1",
          services: [{ key: "AWS Lambda", cost: "1" }],
        },
      ],
    };
    let resolveProviderCosts!: (response: Response) => void;
    const providerCosts = new Promise<Response>((resolve) => {
      resolveProviderCosts = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.includes("/api/v1/anomalies")) {
          return Promise.resolve(fakeResponse(200, anomaliesBody()));
        }
        if (url.includes("groupBy=provider")) {
          return providerCosts; // the grouping refetch stays pending
        }
        return Promise.resolve(fakeResponse(200, costs));
      }),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Stacked daily cost by service" });

    fireEvent.click(screen.getByRole("button", { name: "Provider" }));
    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(
      screen.queryByRole("group", { name: /Stacked daily cost/ }),
    ).toBeNull();

    resolveProviderCosts(fakeResponse(200, costs));
    expect(
      await screen.findByRole("group", {
        name: "Stacked daily cost by provider",
      }),
    ).toBeTruthy();
  });

  it("commits no [new heading + stale chart] frame on a grouping switch", async () => {
    // The pre-effect committed frame is what this pins. A native button.click()
    // OUTSIDE act commits React's synchronous re-render (groupBy already
    // "provider") but does NOT flush the passive effect (which would setState to
    // loading). After exactly ONE microtask the mismatch frame has committed;
    // asserting there catches the buggy [provider heading + old service chart]
    // frame. (An act-wrapped fireEvent flushes the effect first and makes this
    // vacuous — hence the raw dispatch + single microtask.)
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1",
      days: [
        {
          date: "2026-05-01",
          total: "1",
          services: [{ key: "AWS Lambda", cost: "1" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(200, costs))),
    );
    render(<DailyCosts />);
    await screen.findByRole("group", { name: "Stacked daily cost by service" });

    screen.getByRole("button", { name: "Provider" }).click();
    await Promise.resolve();

    // The committed frame shows the NEW heading and the loading state — never the
    // stale service chart.
    expect(
      screen.getByRole("heading", { name: "Daily cost by provider" }),
    ).toBeTruthy();
    expect(screen.getByText("Loading daily costs…")).toBeTruthy();
    expect(
      screen.queryByRole("group", { name: /Stacked daily cost/ }),
    ).toBeNull();
  });

  it("keeps credit days inside the plot and reports net totals", async () => {
    // Day 1's positive segments sum to 5.00 while its net total is only
    // 1.00: with a net-derived y-scale the positive stack would overflow
    // the top tick.
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "3.50",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [
            { key: "Amazon Elastic Compute Cloud", cost: "3.00" },
            { key: "Amazon Simple Storage Service", cost: "2.00" },
            { key: "Savings Plan Credit", cost: "-4.00" },
          ],
        },
        {
          date: "2026-05-02",
          total: "2.50",
          services: [{ key: "Amazon Elastic Compute Cloud", cost: "2.50" }],
        },
      ],
    };
    const { container } = renderChart(costs);

    // Net day total and net grand total are reported as text.
    expect(await screen.findByText("3.50")).toBeTruthy();
    expect(screen.getByText(/Period total \(net\)/)).toBeTruthy();
    expect(screen.getAllByText("1.00").length).toBeGreaterThanOrEqual(1);

    // Only the positive segments render: 2 on day 1, 1 on day 2 — the
    // credit renders no bar segment.
    const paths = [...container.querySelectorAll("path")];
    expect(paths.length).toBe(3);
    for (const path of paths) {
      expect(path.querySelector("title")?.textContent).not.toContain(
        "Savings Plan Credit",
      );
    }

    // No segment renders above the top tick / outside the plot area.
    for (const path of paths) {
      expect(pathTop(path.getAttribute("d") ?? "")).toBeGreaterThanOrEqual(
        MARGIN_TOP - 1e-6,
      );
    }
  });

  it("formats y-axis ticks without float noise on small ranges", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "0.30",
      days: [
        {
          date: "2026-05-01",
          total: "0.30",
          services: [
            { key: "AWS Lambda", cost: "0.15" },
            { key: "Amazon Simple Storage Service", cost: "0.15" },
          ],
        },
      ],
    };
    renderChart(costs);

    // The 0.1-step ticks must read 0.1/0.2/0.3, never an accumulated
    // float like 0.30000000000000004.
    expect(await screen.findByText("0.3")).toBeTruthy();
    expect(screen.getByText("0.1")).toBeTruthy();
    expect(screen.queryByText(/0\.30000/)).toBeNull();
  });

  it("renders a positive sub-gap segment below a larger segment", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "100.000001",
      days: [
        {
          date: "2026-05-01",
          total: "100.000001",
          services: [
            { key: "Tiny lower segment", cost: "0.000001" },
            { key: "Large top segment", cost: "100" },
          ],
        },
      ],
    };
    const { container } = renderChart(costs);
    await screen.findByRole("group", { name: /Stacked daily cost/ });
    const tiny = [...container.querySelectorAll("path")].find(
      (path) =>
        path.querySelector("title")?.textContent ===
        "Tiny lower segment: 0.000001 USD (2026-05-01)",
    );
    expect(tiny).toBeTruthy();
  });

  it("anchors a clamped sub-pixel bottom segment to its bin bottom", async () => {
    // A 0.0001 service stacked UNDER a 2.4192 one: the tiny bottom segment's
    // natural height-minus-gap is far below the 1px floor, so drawnHeight clamps
    // up to 1. The clamped sliver's BOTTOM edge (y + drawnHeight) must land
    // exactly at its bin bottom — which, for the bottom segment, is the zero
    // baseline — never protruding below it. This asserts the drawn GEOMETRY, not
    // mere existence.
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "2.4193",
      days: [
        {
          date: "2026-05-01",
          total: "2.4193",
          services: [
            { key: "Tiny lower segment", cost: "0.0001" },
            { key: "Large top segment", cost: "2.4192" },
          ],
        },
      ],
    };
    const { container } = renderChart(costs);
    await screen.findByRole("group", { name: /Stacked daily cost/ });

    const tiny = [...container.querySelectorAll("path")].find(
      (path) =>
        path.querySelector("title")?.textContent ===
        "Tiny lower segment: 0.0001 USD (2026-05-01)",
    );
    expect(tiny).toBeTruthy();
    // The bottom (non-top) segment path is `M{x},{y} h{w} v{h} h{-w} Z`.
    const match = /^M[\d.-]+,([\d.-]+) h[\d.-]+ v([\d.-]+)/.exec(
      tiny?.getAttribute("d") ?? "",
    );
    expect(match).toBeTruthy();
    const y = Number(match![1]);
    const drawnHeight = Number(match![2]);
    // segmentBottom of the bottom segment is the plot baseline.
    const baseline = HEIGHT - MARGIN.bottom;
    expect(y + drawnHeight).toBe(baseline);
    // The sliver stays inside its bin: above the baseline, below the top tick.
    expect(y).toBeGreaterThanOrEqual(MARGIN.top);
    expect(y).toBeLessThanOrEqual(baseline);
  });

  it("keeps a service's color stable when the service set changes", async () => {
    const day = (services: { key: string; cost: string }[]) => ({
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "9",
      days: [{ date: "2026-05-01", total: "9", services }],
    });

    const fillOf = (container: HTMLElement, service: string) => {
      const path = [...container.querySelectorAll("path")].find((p) =>
        p.querySelector("title")?.textContent?.startsWith(service),
      );
      expect(path).toBeTruthy();
      return path?.getAttribute("fill");
    };

    const first = renderChart(
      day([
        { key: "AWS Lambda", cost: "3" },
        { key: "Amazon Elastic Compute Cloud", cost: "3" },
        { key: "Amazon Simple Storage Service", cost: "3" },
      ]),
    );
    await screen.findByRole("group", { name: /Stacked daily cost/ });
    const before = fillOf(first.container, "AWS Lambda");
    cleanup();
    vi.unstubAllGlobals();

    const second = renderChart(
      day([
        { key: "AWS Lambda", cost: "9" },
        { key: "Amazon S3 Glacier", cost: "0.5" },
      ]),
    );
    await screen.findByRole("group", { name: /Stacked daily cost/ });
    const after = fillOf(second.container, "AWS Lambda");

    expect(before).toMatch(/^var\(--viz-series-\d\)$/);
    expect(after).toBe(before);
  });

  it("thins x-axis date labels to at most ~12 for long ranges", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "30",
      days: Array.from({ length: 30 }, (_, i) => ({
        date: `2026-05-${String(i + 1).padStart(2, "0")}`,
        total: "1",
        services: [{ key: "AWS Lambda", cost: "1" }],
      })),
    };
    const { container } = renderChart(costs);
    await screen.findByRole("group", { name: /Stacked daily cost/ });

    const dateLabels = [...container.querySelectorAll("text")].filter((t) =>
      /^\d{2}-\d{2}$/.test(t.textContent ?? ""),
    );
    expect(dateLabels.length).toBeGreaterThan(0);
    expect(dateLabels.length).toBeLessThanOrEqual(12);
  });

  it("shows the ingest hint when the store is empty", async () => {
    const empty: DailyCostsResponse = {
      currency: "",
      currencies: [],
      provider: "",
      providers: [],
      total: "0",
      days: [],
    };
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
    expect(screen.queryByRole("button", { name: "Download CSV" })).toBeNull();
  });

  it("shows an error state when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.resolve(fakeResponse(500, null))),
    );

    render(<DailyCosts />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("500");
    expect(screen.queryByRole("button", { name: "Download CSV" })).toBeNull();
  });

  it("refetches and renders daily costs grouped by allocation", async () => {
    const serviceCosts: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "1.00",
      days: [
        {
          date: "2026-05-01",
          total: "1.00",
          services: [{ key: "AWS Lambda", cost: "1.00" }],
        },
      ],
    };
    const allocationCosts: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "32.7663",
      days: [
        {
          date: "2026-05-01",
          total: "32.7663",
          services: [
            { key: "compute", cost: "25.4016" },
            { key: "Unallocated", cost: "6.0375" },
          ],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          fakeResponse(
            200,
            String(input).includes("groupBy=allocation")
              ? allocationCosts
              : serviceCosts,
          ),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    expect((await screen.findAllByText("AWS Lambda")).length).toBeGreaterThan(
      0,
    );
    fireEvent.click(screen.getByRole("button", { name: "Allocation" }));

    expect(
      await screen.findByRole("group", {
        name: "Stacked daily cost by allocation",
      }),
    ).toBeTruthy();
    // The allocation label and the reserved Unallocated bucket both render, with
    // exact money.
    expect((await screen.findAllByText("compute")).length).toBeGreaterThan(0);
    expect((await screen.findAllByText("Unallocated")).length).toBeGreaterThan(
      0,
    );
    expect((await screen.findAllByTitle("25.4016 USD")).length).toBeGreaterThan(
      0,
    );
    expect(container.querySelectorAll(".viz-legend li")).toHaveLength(2);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/costs/daily?groupBy=allocation"),
    );
    expect(fetchedURLs()).toContain("/api/v1/costs/daily");
  });

  it("renders the server error body on an allocation 400", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() =>
        Promise.resolve(
          fakeResponse(
            400,
            "no allocation rules configured (start serve with --allocation-rules or set $COSTROID_ALLOCATION_RULES)",
          ),
        ),
      ),
    );

    render(<DailyCosts />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("returned 400");
    expect(alert.textContent).toContain("no allocation rules configured");
  });

  it("scores a mixed-currency anomaly overlay in the displayed currency", async () => {
    const costs: DailyCostsResponse = {
      currency: "EUR",
      currencies: ["EUR", "USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "3.123456789012345679",
      days: [
        {
          date: "2026-05-01",
          total: "3.123456789012345679",
          services: [{ key: "Shared Compute", cost: "3.123456789012345679" }],
        },
      ],
    };
    const flags: Anomaly[] = [
      {
        date: "2026-05-01",
        scope: "total",
        direction: "increase",
        observed: "3.123456789012345679",
        median: "1",
        mad: "0.1",
        scaledMad: "0.14826",
        threshold: "0.44478",
        deviation: "2.123456789012345679",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/costs/daily")) {
          return Promise.resolve(fakeResponse(200, costs));
        }
        if (url === "/api/v1/anomalies?currency=EUR") {
          return Promise.resolve(
            fakeResponse(200, anomaliesBody(flags, "EUR")),
          );
        }
        return Promise.resolve(fakeResponse(500, null));
      }),
    );

    const { container } = render(<DailyCosts />);
    await screen.findByRole("group", { name: /Stacked daily cost/ });

    await waitFor(() =>
      expect(
        container.querySelectorAll(".viz-chart .viz-anomaly"),
      ).toHaveLength(1),
    );
    const marker = container.querySelector(".viz-chart .viz-anomaly");
    expect(marker?.getAttribute("data-date")).toBe("2026-05-01");
    expect(marker?.getAttribute("data-direction")).toBe("increase");
    expect(
      fetchedURLs().filter((url) => url.startsWith("/api/v1/anomalies")),
    ).toEqual(["/api/v1/anomalies?currency=EUR"]);
    expect(screen.queryByText(/Anomaly overlay unavailable/)).toBeNull();
  });

  it("removes stale provider markers before the selected overlay resolves", async () => {
    const providers = ["Amazon Web Services", "Microsoft"];
    const initial = providerDailyBody("", providers, "3");
    const selected = providerDailyBody("Amazon Web Services", providers, "1");
    const flags: Anomaly[] = [
      {
        date: "2026-05-01",
        scope: "total",
        direction: "increase",
        observed: "3",
        median: "1",
        mad: "0.1",
        scaledMad: "0.14826",
        threshold: "0.44478",
        deviation: "2",
      },
    ];
    let resolveSelectedAnomalies!: (response: Response) => void;
    const heldSelectedAnomalies = new Promise<Response>((resolve) => {
      resolveSelectedAnomalies = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url.startsWith("/api/v1/costs/daily")) {
          return Promise.resolve(
            fakeResponse(
              200,
              url.includes("provider=Amazon%20Web%20Services")
                ? selected
                : initial,
            ),
          );
        }
        if (url.includes("provider=Amazon%20Web%20Services")) {
          return heldSelectedAnomalies;
        }
        return Promise.resolve(fakeResponse(200, anomaliesBody(flags)));
      }),
    );

    const { container } = render(<DailyCosts />);
    await waitFor(() =>
      expect(
        container.querySelector(
          '.viz-chart .viz-anomaly[data-direction="increase"]',
        ),
      ).toBeTruthy(),
    );

    let sawStaleProviderMarker = false;
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (
            node instanceof Element &&
            (node.matches('.viz-anomaly[data-direction="increase"]') ||
              node.querySelector('.viz-anomaly[data-direction="increase"]') !==
                null)
          ) {
            sawStaleProviderMarker = true;
          }
        }
      }
    });
    observer.observe(container, { childList: true, subtree: true });

    fireEvent.click(
      screen.getByRole("button", { name: "Amazon Web Services" }),
    );
    expect((await screen.findAllByTitle("1 USD")).length).toBeGreaterThan(0);
    await waitFor(() =>
      expect(fetchedURLs()).toContain(
        "/api/v1/anomalies?currency=USD&provider=Amazon%20Web%20Services",
      ),
    );
    await Promise.resolve();

    expect(container.querySelector(".viz-chart .viz-anomaly")).toBeNull();
    expect(sawStaleProviderMarker).toBe(false);
    observer.disconnect();

    resolveSelectedAnomalies(fakeResponse(200, anomaliesBody()));
  });

  it("removes stale currency markers before the switched overlay resolves", async () => {
    const eurCosts: DailyCostsResponse = {
      currency: "EUR",
      currencies: ["EUR", "USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "3.123456789012345679",
      days: [
        {
          date: "2026-05-01",
          total: "3.123456789012345679",
          services: [{ key: "Shared Compute", cost: "3.123456789012345679" }],
        },
      ],
    };
    const usdCosts: DailyCostsResponse = {
      currency: "USD",
      currencies: ["EUR", "USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "30.987654321098765434",
      days: [
        {
          date: "2026-05-01",
          total: "30.987654321098765434",
          services: [{ key: "Shared Compute", cost: "30.987654321098765434" }],
        },
      ],
    };
    const eurFlags: Anomaly[] = [
      {
        date: "2026-05-01",
        scope: "total",
        direction: "increase",
        observed: "3.123456789012345679",
        median: "1",
        mad: "0.1",
        scaledMad: "0.14826",
        threshold: "0.44478",
        deviation: "2.123456789012345679",
      },
    ];
    const usdFlags: Anomaly[] = [
      {
        date: "2026-05-01",
        scope: "total",
        direction: "decrease",
        observed: "30.987654321098765434",
        median: "40",
        mad: "1",
        scaledMad: "1.4826",
        threshold: "4.4478",
        deviation: "-9.012345678901234566",
      },
    ];
    let resolveUSDAnomalies!: (response: Response) => void;
    const heldUSDAnomalies = new Promise<Response>((resolve) => {
      resolveUSDAnomalies = resolve;
    });
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        if (url === "/api/v1/costs/daily") {
          return Promise.resolve(fakeResponse(200, eurCosts));
        }
        if (url === "/api/v1/costs/daily?currency=USD") {
          return Promise.resolve(fakeResponse(200, usdCosts));
        }
        if (url === "/api/v1/anomalies?currency=EUR") {
          return Promise.resolve(
            fakeResponse(200, anomaliesBody(eurFlags, "EUR")),
          );
        }
        if (url === "/api/v1/anomalies?currency=USD") {
          return heldUSDAnomalies;
        }
        return Promise.resolve(fakeResponse(500, null));
      }),
    );

    const { container } = render(<DailyCosts />);
    await waitFor(() =>
      expect(
        container.querySelector(
          '.viz-chart .viz-anomaly[data-direction="increase"]',
        ),
      ).toBeTruthy(),
    );

    // Mutation-pin the synchronous mismatch frame: after USD costs commit but
    // before the USD overlay effect enters loading, the held EUR flags must not
    // be added to the new chart even for one transient render.
    let sawStaleEURMarker = false;
    const observer = new MutationObserver((records) => {
      for (const record of records) {
        for (const node of record.addedNodes) {
          if (
            node instanceof Element &&
            (node.matches('.viz-anomaly[data-direction="increase"]') ||
              node.querySelector('.viz-anomaly[data-direction="increase"]') !==
                null)
          ) {
            sawStaleEURMarker = true;
          }
        }
      }
    });
    observer.observe(container, { childList: true, subtree: true });

    fireEvent.click(screen.getByRole("button", { name: "USD" }));
    expect(
      (await screen.findAllByTitle("30.987654321098765434 USD")).length,
    ).toBeGreaterThan(0);
    await waitFor(() =>
      expect(fetchedURLs()).toContain("/api/v1/anomalies?currency=USD"),
    );
    await Promise.resolve();

    expect(
      container.querySelector(
        '.viz-chart .viz-anomaly[data-direction="increase"]',
      ),
    ).toBeNull();
    expect(sawStaleEURMarker).toBe(false);
    expect(container.querySelector(".viz-chart .viz-anomaly")).toBeNull();
    observer.disconnect();

    resolveUSDAnomalies(fakeResponse(200, anomaliesBody(usdFlags, "USD")));
    await waitFor(() =>
      expect(
        container.querySelector(
          '.viz-chart .viz-anomaly[data-direction="decrease"]',
        ),
      ).toBeTruthy(),
    );
    const usdMarker = container.querySelector(
      '.viz-chart .viz-anomaly[data-direction="decrease"]',
    );
    expect(usdMarker?.getAttribute("data-date")).toBe("2026-05-01");
  });

  it("marks flagged days with a direction-aware overlay and a math tooltip", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "400",
      days: [
        {
          date: "2026-06-01",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
        {
          date: "2026-06-02",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
        {
          date: "2026-06-03",
          total: "200",
          services: [{ key: "AWS Lambda", cost: "200" }],
        },
      ],
    };
    const flags: Anomaly[] = [
      {
        date: "2026-06-03",
        scope: "total",
        direction: "increase",
        observed: "200",
        median: "100",
        mad: "2",
        scaledMad: "2.9652",
        threshold: "8.8956",
        deviation: "100",
      },
      {
        date: "2026-06-03",
        scope: "key",
        key: "AWS Lambda",
        direction: "increase",
        observed: "200",
        median: "100",
        mad: "2",
        scaledMad: "2.9652",
        threshold: "8.8956",
        deviation: "100",
      },
    ];
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(200, anomaliesBody(flags))
            : fakeResponse(200, costs),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);
    await screen.findByRole("group", { name: /Stacked daily cost/ });

    // A chart marker appears EXACTLY on the flagged day (one per day, keyed by
    // data-date) — never on the mundane days.
    await waitFor(() =>
      expect(
        [...container.querySelectorAll(".viz-chart .viz-anomaly")].map((m) =>
          m.getAttribute("data-date"),
        ),
      ).toEqual(["2026-06-03"]),
    );
    const marker = container.querySelector(".viz-chart .viz-anomaly")!;
    expect(marker.getAttribute("data-direction")).toBe("increase");
    // The tooltip carries the API's decimal strings verbatim (no reformatting).
    const tooltip = marker.querySelector("title")?.textContent ?? "";
    expect(tooltip).toContain("observed 200");
    expect(tooltip).toContain("median 100");
    expect(tooltip).toContain("threshold 8.8956");

    // The table row carries a badge per flag (total + key), listing scope/key and
    // direction.
    const badges = [...container.querySelectorAll(".viz-anomaly-badge")];
    expect(badges.map((b) => b.textContent)).toEqual([
      "total increase",
      "AWS Lambda increase",
    ]);
  });

  it("keeps the chart alive when the anomaly fetch fails", async () => {
    const costs: DailyCostsResponse = {
      currency: "USD",
      currencies: ["USD"],
      provider: "",
      providers: ["Amazon Web Services"],
      total: "100",
      days: [
        {
          date: "2026-06-01",
          total: "100",
          services: [{ key: "AWS Lambda", cost: "100" }],
        },
      ],
    };
    vi.stubGlobal(
      "fetch",
      vi.fn((input: RequestInfo | URL) =>
        Promise.resolve(
          String(input).includes("/api/v1/anomalies")
            ? fakeResponse(500, null)
            : fakeResponse(200, costs),
        ),
      ),
    );

    const { container } = render(<DailyCosts />);

    // The chart still renders despite the anomaly fetch failing...
    expect(
      await screen.findByRole("group", { name: /Stacked daily cost/ }),
    ).toBeTruthy();
    // ...with a non-blocking notice and no markers, and NOT the cost error alert.
    await screen.findByText(/Anomaly overlay unavailable/);
    expect(container.querySelectorAll(".viz-chart .viz-anomaly")).toHaveLength(
      0,
    );
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
