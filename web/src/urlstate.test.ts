// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import { readUrlState, writeUrlState } from "./urlstate";

afterEach(() => {
  vi.restoreAllMocks();
  window.history.replaceState(null, "", "/");
});

describe("readUrlState", () => {
  it("accepts each valid key", () => {
    window.location.hash =
      "#view=unit-economics&start=2026-06-01&end=2026-06-30&groupBy=allocation&currency=USD&provider=Amazon+Web+Services&metric=active+users";

    expect(readUrlState()).toEqual({
      view: "unit-economics",
      start: "2026-06-01",
      end: "2026-06-30",
      groupBy: "allocation",
      currency: "USD",
      provider: "Amazon Web Services",
      metric: "active users",
    });
  });

  it.each([
    "overview",
    "costs",
    "tokens",
    "usage",
    "unit-economics",
    "sources",
  ])("accepts the %s view", (view) => {
    window.location.hash = `#view=${view}`;
    expect(readUrlState()).toEqual({ view });
  });

  it("drops invalid values for every key", () => {
    window.location.hash =
      "#view=other&start=2026-6-01&end=June&groupBy=account&currency=usd&provider=&metric=";

    expect(readUrlState()).toEqual({});

    window.location.hash = `#provider=${"é".repeat(4097)}`;
    expect(readUrlState()).toEqual({});
  });

  it("tolerates a non-state fragment", () => {
    window.location.hash = "#view-panel";
    expect(readUrlState()).toEqual({});
  });

  it.each([
    "service",
    "provider",
    "allocation",
    "subaccount",
    "region",
  ] as const)("accepts the plain %s grouping value", (groupBy) => {
    window.location.hash = `#groupBy=${groupBy}`;
    expect(readUrlState()).toEqual({ groupBy });
  });

  it("decodes a tag grouping and key from the groupBy value", () => {
    window.location.hash = "#groupBy=tag%3Ateam";
    expect(readUrlState()).toEqual({ groupBy: "tag", tagKey: "team" });
  });

  it("drops tag groupings with an empty or overlong key", () => {
    window.location.hash = "#groupBy=tag%3A";
    expect(readUrlState()).toEqual({});

    window.location.hash = `#groupBy=${encodeURIComponent(`tag:${"é".repeat(4097)}`)}`;
    expect(readUrlState()).toEqual({});
  });
});

describe("writeUrlState", () => {
  it("writes URLSearchParams encoding in canonical key order", () => {
    writeUrlState({
      metric: "active users",
      provider: "Amazon Web Services",
      currency: "USD",
      groupBy: "provider",
      end: "2026-06-30",
      start: "2026-06-01",
      view: "costs",
    });

    expect(window.location.hash).toBe(
      "#view=costs&start=2026-06-01&end=2026-06-30&groupBy=provider&currency=USD&provider=Amazon+Web+Services&metric=active+users",
    );
  });

  it("omits default and empty values", () => {
    writeUrlState({
      view: "overview",
      start: "",
      end: "",
      groupBy: "service",
      currency: "",
      provider: "",
      metric: "",
    });

    expect(window.location.hash).toBe("");
  });

  it("merges owned keys while preserving other state keys and dropping unknown keys", () => {
    window.location.hash =
      "#view=costs&start=2026-06-01&metric=requests&unknown=value";

    writeUrlState({ groupBy: "provider", provider: "Google Cloud" });

    expect(window.location.hash).toBe(
      "#view=costs&start=2026-06-01&groupBy=provider&provider=Google+Cloud&metric=requests",
    );
  });

  it("removes the hash when merged state contains only defaults", () => {
    window.location.hash = "#view=costs&groupBy=provider&currency=USD";

    writeUrlState({
      view: "overview",
      groupBy: "service",
      currency: "",
    });

    expect(window.location.pathname + window.location.search).toBe("/");
    expect(window.location.hash).toBe("");
  });

  it("does not call replaceState when the canonical hash is unchanged", () => {
    window.location.hash =
      "#view=costs&provider=Amazon+Web+Services&metric=requests";
    const replaceState = vi.spyOn(window.history, "replaceState");

    writeUrlState({ view: "costs", provider: "Amazon Web Services" });

    expect(replaceState).not.toHaveBeenCalled();
  });

  it("encodes a tag grouping and key inside groupBy", () => {
    writeUrlState({ groupBy: "tag", tagKey: "team" });
    expect(window.location.hash).toBe("#groupBy=tag%3Ateam");
  });
});
