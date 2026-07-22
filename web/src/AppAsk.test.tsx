// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import {
  act,
  cleanup,
  fireEvent,
  render,
  screen,
  waitFor,
} from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { components } from "./api/schema";
import App from "./App";

type QueryPlan = components["schemas"]["QueryPlan"];

function response(status: number, body: unknown): Response {
  return {
    ok: status >= 200 && status < 300,
    status,
    json: () => Promise.resolve(body),
    text: () => Promise.resolve(typeof body === "string" ? body : ""),
  } as Response;
}

function plan(endpoint: QueryPlan["endpoint"]): QueryPlan {
  return {
    endpoint,
    start: "2026-06-01",
    end: "2026-06-30",
    groupBy:
      endpoint === "costs-daily" ||
      endpoint === "costs-summary" ||
      endpoint === "anomalies"
        ? "tag"
        : null,
    tagKey:
      endpoint === "costs-daily" ||
      endpoint === "costs-summary" ||
      endpoint === "anomalies"
        ? "environment"
        : null,
    currency: endpoint === "tokens" || endpoint === "usage" ? null : "USD",
    provider:
      endpoint === "tokens" || endpoint === "usage"
        ? "ignored provider"
        : "AWS",
    metric: endpoint === "unit-economics" ? "requests served" : null,
  };
}

function appFetch(plans: Array<QueryPlan | Record<string, unknown>>) {
  const queue = [...plans];
  return vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
    const url = String(input);
    const parsed = new URL(url, "http://x");
    const path = parsed.pathname;
    if (path === "/api/v1/query" && init?.method === "POST") {
      return Promise.resolve(response(200, queue.shift()));
    }
    if (path === "/api/v1/meta") {
      return Promise.resolve(
        response(200, {
          name: "costroid",
          version: "0.1.0-test",
          focusVersion: "1.4",
          demo: false,
          naturalLanguageQueryConfigured: true,
        }),
      );
    }
    if (path === "/api/v1/costs/daily") {
      return Promise.resolve(
        response(200, {
          currency: parsed.searchParams.get("currency") ?? "",
          currencies: ["USD"],
          provider: parsed.searchParams.get("provider") ?? "",
          providers: ["AWS"],
          tagKeys: ["environment"],
          total: "0",
          days: [],
        }),
      );
    }
    if (path === "/api/v1/costs/summary") {
      return Promise.resolve(
        response(200, {
          currency: parsed.searchParams.get("currency") ?? "",
          currencies: ["USD"],
          provider: parsed.searchParams.get("provider") ?? "",
          providers: ["AWS"],
          total: "0",
          keys: [],
        }),
      );
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        response(200, {
          currency: parsed.searchParams.get("currency") ?? "",
          parameters: {
            k: "3",
            consistencyConstant: "1.4826",
            windowDays: 30,
            minObservations: 10,
            relativeFloor: "0.1",
            groupBy: parsed.searchParams.get("groupBy") ?? "service",
            tagKey: parsed.searchParams.get("tagKey") ?? "",
          },
          anomalies: [],
        }),
      );
    }
    if (path === "/api/v1/insights") {
      return Promise.resolve(
        response(200, {
          currency: "",
          currencies: ["USD"],
          parameters: {
            k: "3",
            consistencyConstant: "1.4826",
            windowDays: 30,
            minObservations: 10,
            relativeFloor: "0.1",
            divisionScale: 18,
          },
          insights: [],
        }),
      );
    }
    if (path === "/api/v1/business-metrics") {
      return Promise.resolve(
        response(200, {
          metrics: [
            {
              name: "requests served",
              firstDay: "2026-01-01",
              lastDay: "2026-07-01",
            },
          ],
        }),
      );
    }
    if (path === "/api/v1/unit-economics/daily") {
      return Promise.resolve(
        response(200, {
          currency: parsed.searchParams.get("currency") ?? "",
          currencies: ["USD"],
          provider: parsed.searchParams.get("provider") ?? "",
          providers: ["AWS"],
          metric: parsed.searchParams.get("metric") ?? "",
          period: {
            coveredDays: 0,
            cost: "0",
            quantity: "0",
          },
          days: [],
        }),
      );
    }
    if (path === "/api/v1/usage/tokens/daily") {
      return Promise.resolve(response(200, []));
    }
    if (path === "/api/v1/usage/metrics/daily") {
      return Promise.resolve(response(200, []));
    }
    if (path === "/api/v1/sync/status") {
      return Promise.resolve(response(200, { enabled: false, sources: [] }));
    }
    return Promise.resolve(response(404, null));
  });
}

async function submitQuestion(question = "Show the answer") {
  const input = await screen.findByLabelText("Ask a question");
  fireEvent.change(input, { target: { value: question } });
  fireEvent.click(screen.getByRole("button", { name: "Ask" }));
}

function firstURL(
  fetchMock: ReturnType<typeof appFetch>,
  prefix: string,
): string | undefined {
  return fetchMock.mock.calls
    .map(([input]) => String(input))
    .find((url) => url.startsWith(prefix));
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  window.history.replaceState(null, "", "/");
});

describe("dashboard question plan navigation", () => {
  it.each([
    [
      "costs-daily",
      "Costs",
      "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=tag&tagKey=environment&currency=USD&provider=AWS",
    ],
    [
      "costs-summary",
      "Costs",
      "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=tag&tagKey=environment&currency=USD&provider=AWS",
    ],
    [
      "anomalies",
      "Costs",
      "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=tag&tagKey=environment&currency=USD&provider=AWS",
    ],
    [
      "tokens",
      "Tokens",
      "/api/v1/usage/tokens/daily?start=2026-06-01&end=2026-06-30",
    ],
    [
      "usage",
      "Usage",
      "/api/v1/usage/metrics/daily?start=2026-06-01&end=2026-06-30",
    ],
    [
      "unit-economics",
      "Unit economics",
      "/api/v1/unit-economics/daily?metric=requests%20served&start=2026-06-01&end=2026-06-30&currency=USD&provider=AWS",
    ],
  ] as const)(
    "applies every supported %s field to the target view first request",
    async (endpoint, view, expectedURL) => {
      const fetchMock = appFetch([plan(endpoint)]);
      vi.stubGlobal("fetch", fetchMock);
      render(<App />);
      await screen.findByLabelText("Ask a question");
      fetchMock.mockClear();

      await submitQuestion();

      await waitFor(() =>
        expect(firstURL(fetchMock, expectedURL)).toBe(expectedURL),
      );
      expect(
        screen.getByRole("button", { name: view }).getAttribute("aria-current"),
      ).toBe("page");
    },
  );

  it("applies two consecutive plans to the same displayed view", async () => {
    const first = { ...plan("costs-daily"), groupBy: "region", tagKey: null };
    const second = {
      ...plan("costs-daily"),
      groupBy: "provider",
      tagKey: null,
      provider: "AWS",
    };
    const fetchMock = appFetch([first, second]);
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await screen.findByLabelText("Ask a question");
    fetchMock.mockClear();

    await submitQuestion("First question");
    await waitFor(() =>
      expect(firstURL(fetchMock, "/api/v1/costs/daily")).toBe(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=region&currency=USD&provider=AWS",
      ),
    );
    fetchMock.mockClear();

    await submitQuestion("Second question");
    await waitFor(() =>
      expect(firstURL(fetchMock, "/api/v1/costs/daily")).toBe(
        "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=provider&currency=USD&provider=AWS",
      ),
    );
  });

  it("normalizes null plan fields before the URL and target request", async () => {
    const fetchMock = appFetch([
      {
        endpoint: "costs-daily",
        start: null,
        end: null,
        groupBy: null,
        tagKey: null,
        currency: null,
        provider: null,
        metric: null,
      },
    ]);
    vi.stubGlobal("fetch", fetchMock);
    render(<App />);
    await screen.findByLabelText("Ask a question");
    fetchMock.mockClear();

    await submitQuestion();

    await waitFor(() =>
      expect(firstURL(fetchMock, "/api/v1/costs/daily")).toBe(
        "/api/v1/costs/daily",
      ),
    );
    expect(window.location.hash).toBe("#view=costs");
    expect(window.location.hash).not.toContain("null");
  });
});

it("keeps one live region mounted while idle, translating, and resolved", async () => {
  let resolveQuery = (_value: Response) => {};
  const heldQuery = new Promise<Response>((resolve) => {
    resolveQuery = resolve;
  });
  const fetchMock = appFetch([]);
  const fetchWithHeldQuery = vi.fn(
    (input: RequestInfo | URL, init?: RequestInit) =>
      new URL(String(input), "http://x").pathname === "/api/v1/query"
        ? heldQuery
        : fetchMock(input, init),
  );
  vi.stubGlobal("fetch", fetchWithHeldQuery);
  render(<App />);
  const liveRegion = screen.getByRole("status", { name: "Question status" });
  expect(liveRegion.textContent).toBe("");
  const input = await screen.findByLabelText("Ask a question");
  await waitFor(() =>
    expect(liveRegion.textContent).toBe("Ready for a question."),
  );

  fireEvent.change(input, { target: { value: "Show tokens" } });
  fireEvent.click(screen.getByRole("button", { name: "Ask" }));
  expect(liveRegion.textContent).toBe("Translating question.");

  act(() => {
    resolveQuery(response(200, plan("tokens")));
  });
  await waitFor(() =>
    expect(liveRegion.textContent).toBe(
      "Interpreted as: tokens for 2026-06-01 to 2026-06-30.",
    ),
  );
  expect(screen.getByRole("status", { name: "Question status" })).toBe(
    liveRegion,
  );
});
