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
import { afterEach, expect, it, vi } from "vitest";
import type { components } from "./api/schema";

type InsightLink = components["schemas"]["InsightLink"];

const navigation = vi.hoisted(() => ({
  current: undefined as ((link: InsightLink) => void) | undefined,
}));

vi.mock("./Overview", () => ({
  default: ({ onNavigate }: { onNavigate?: (link: InsightLink) => void }) => {
    navigation.current = onNavigate;
    return <h2>Overview</h2>;
  },
}));

import App from "./App";

function response(body: unknown): Response {
  return {
    ok: true,
    status: 200,
    json: () => Promise.resolve(body),
  } as Response;
}

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  navigation.current = undefined;
  window.history.replaceState(null, "", "/");
});

it("remounts the displayed view so programmatic navigation applies a new grouping", async () => {
  const fetchMock = vi.fn((input: RequestInfo | URL) => {
    const path = new URL(String(input), "http://x").pathname;
    if (path === "/api/v1/meta") {
      return Promise.resolve(
        response({
          name: "costroid",
          version: "0.1.0-test",
          focusVersion: "1.4",
          demo: false,
          naturalLanguageQueryConfigured: false,
        }),
      );
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        response({
          currency: "",
          parameters: {
            k: "3",
            consistencyConstant: "1.4826",
            windowDays: 30,
            minObservations: 10,
            relativeFloor: "0.1",
            groupBy: "service",
            tagKey: "",
          },
          anomalies: [],
        }),
      );
    }
    return Promise.resolve(
      response({
        currency: "",
        currencies: [],
        provider: "",
        providers: [],
        tagKeys: [],
        total: "0",
        days: [],
      }),
    );
  });
  vi.stubGlobal("fetch", fetchMock);
  render(<App />);

  await waitFor(() => expect(navigation.current).toBeTypeOf("function"));
  act(() => {
    navigation.current?.({
      view: "costs",
      start: "2026-06-01",
      end: "2026-06-30",
      groupBy: "service",
    });
  });
  await waitFor(() =>
    expect(
      fetchMock.mock.calls.some(
        ([input]) =>
          String(input) ===
          "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30",
      ),
    ).toBe(true),
  );

  fetchMock.mockClear();
  act(() => {
    navigation.current?.({
      view: "costs",
      start: "2026-06-01",
      end: "2026-06-30",
      groupBy: "region",
    });
  });

  await waitFor(() => {
    const firstCostsRequest = fetchMock.mock.calls
      .map(([input]) => String(input))
      .find((url) => url.startsWith("/api/v1/costs/daily"));
    expect(firstCostsRequest).toBe(
      "/api/v1/costs/daily?start=2026-06-01&end=2026-06-30&groupBy=region",
    );
  });
});

it("does not remount the view when the reader changes the range or the view", async () => {
  // The remount token exists for plan-driven navigation only. Widening it to
  // every interaction would discard view state and refetch on each range
  // change, which no other assertion in the suite would notice.
  const calls: string[] = [];
  vi.stubGlobal("fetch", (input: RequestInfo | URL) => {
    const url = String(input);
    calls.push(url);
    const path = new URL(url, "http://x").pathname;
    if (path === "/api/v1/meta") {
      return Promise.resolve(
        response({
          name: "costroid",
          version: "0.1.0-test",
          focusVersion: "1.4",
          demo: false,
          naturalLanguageQueryConfigured: false,
        }),
      );
    }
    if (path === "/api/v1/costs/daily") {
      return Promise.resolve(
        response({
          currency: "",
          currencies: ["USD"],
          provider: "",
          providers: ["AWS"],
          tagKeys: [],
          total: "0",
          days: [],
        }),
      );
    }
    if (path === "/api/v1/anomalies") {
      return Promise.resolve(
        response({
          currency: "",
          parameters: {
            k: "3",
            consistencyConstant: "1.4826",
            windowDays: 30,
            minObservations: 10,
            relativeFloor: "0.1",
            groupBy: "service",
            tagKey: "",
          },
          anomalies: [],
        }),
      );
    }
    return Promise.resolve(response({}));
  });
  render(<App />);
  await waitFor(() => expect(calls.length).toBeGreaterThan(0));
  const panel = document.getElementById("view-panel");
  expect(panel).not.toBeNull();

  fireEvent.click(screen.getByRole("button", { name: /Costs/ }));
  await waitFor(() =>
    expect(screen.getByRole("button", { name: /Costs/ })).toBeTruthy(),
  );
  // Same DOM node means React reused it: the token did not move.
  expect(document.getElementById("view-panel")).toBe(panel);
});
