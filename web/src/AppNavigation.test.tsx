// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { act, cleanup, render, waitFor } from "@testing-library/react";
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
