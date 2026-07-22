// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { afterEach, describe, expect, it, vi } from "vitest";
import * as api from "./api";
import * as demoApi from "./api.demo";
import baseAnomalies from "./demo/fixtures/anomalies.full.service.json";
import baseSummary from "./demo/fixtures/costs-summary.full.service.json";
import baseCosts from "./demo/fixtures/costs.full.service.json";
import baseTagCosts from "./demo/fixtures/costs.full.tag.environment.json";
import baseRegionCosts from "./demo/fixtures/costs.last30.region.json";
import amazonAnomalies from "./demo/fixtures/filtered/anomalies.full.service.amazon-web-services.json";
import amazonSummary from "./demo/fixtures/filtered/costs-summary.full.service.amazon-web-services.json";
import amazonCosts from "./demo/fixtures/filtered/costs.full.service.amazon-web-services.json";
import amazonTagCosts from "./demo/fixtures/filtered/costs.full.tag.environment.amazon-web-services.json";
import amazonSubaccountCosts from "./demo/fixtures/filtered/costs.last30.subaccount.amazon-web-services.json";
import googleAnomalies from "./demo/fixtures/filtered/anomalies.full.service.google.json";
import amazonEconomics from "./demo/fixtures/filtered/unit-economics.full.amazon-web-services.json";
import baseEconomics from "./demo/fixtures/unit-economics.full.json";
import insightsFull from "./demo/fixtures/insights.full.json";
import insightsLast30 from "./demo/fixtures/insights.last30.json";
import { DEMO_PRESETS } from "./demo/ranges";

const fullServiceRange = { start: "", end: "", groupBy: "service" as const };

describe("api.demo export surface", () => {
  it("matches the production transport seam", () => {
    expect(Object.keys(demoApi).sort()).toEqual(Object.keys(api).sort());
  });

  it("matches the production postQuery call shape without fetching", async () => {
    const sameSignature: typeof api.postQuery = demoApi.postQuery;
    const fetchSpy = vi.spyOn(globalThis, "fetch");
    const controller = new AbortController();

    expect(demoApi.postQuery.length).toBe(api.postQuery.length);
    expect(demoApi.postQuery.length).toBe(2);
    const plan = await sameSignature("Which costs changed?", controller.signal);

    expect(plan).toEqual({
      endpoint: "costs-daily",
      start: null,
      end: null,
      groupBy: null,
      tagKey: null,
      currency: null,
      provider: null,
      metric: null,
    });
    expect(fetchSpy).not.toHaveBeenCalled();
  });
});

describe("api.demo getInsights", () => {
  it("returns the full-precision magnitude from the captured fixture verbatim", async () => {
    const full = DEMO_PRESETS.find((p) => p.id === "full")!;
    const body = await demoApi.getInsights({
      start: full.start,
      end: full.end,
    });
    expect(body).toBe(insightsFull);
    expect(body.insights[0]?.magnitude).toBe(
      insightsFull.insights[0].magnitude,
    );
    expect(body.insights[0]?.magnitude).toBe("194348.36");
    expect(body.insights[0]?.evidence[2]?.value).toBe(
      insightsFull.insights[0].evidence[2].value,
    );
    expect(body.insights[0]?.evidence[2]?.value).toBe("0.201595594066514939");
  });

  it("selects the preset fixture by range", async () => {
    const last30 = DEMO_PRESETS.find((p) => p.id === "last30")!;
    const body = await demoApi.getInsights({
      start: last30.start,
      end: last30.end,
    });
    expect(body).toBe(insightsLast30);
  });
});

describe("api.demo provider-filtered fixtures", () => {
  it("returns every captured filtered response verbatim", async () => {
    const provider = "Amazon Web Services";

    const costs = await demoApi.getCostsDaily({
      ...fullServiceRange,
      provider,
    });
    const summary = await demoApi.getCostsSummary({
      ...fullServiceRange,
      provider,
    });
    const anomalies = await demoApi.getAnomalies({
      ...fullServiceRange,
      provider,
    });
    const economics = await demoApi.getUnitEconomicsDaily({
      start: "",
      end: "",
      metric: "requests served",
      provider,
    });

    expect(costs).toBe(amazonCosts);
    expect(summary).toBe(amazonSummary);
    expect(anomalies).toBe(amazonAnomalies);
    expect(economics).toBe(amazonEconomics);
    expect(costs.provider).toBe(provider);
    expect(costs.providers).toEqual(baseCosts.providers);
    expect(typeof costs.total).toBe("string");
    expect(costs.total).not.toBe(baseCosts.total);
  });

  it("returns base fixtures verbatim for absent and unknown providers", async () => {
    const absentCosts = await demoApi.getCostsDaily(fullServiceRange);
    const unknownCosts = await demoApi.getCostsDaily({
      ...fullServiceRange,
      provider: "Bogus",
    });
    const absentSummary = await demoApi.getCostsSummary(fullServiceRange);
    const unknownSummary = await demoApi.getCostsSummary({
      ...fullServiceRange,
      provider: "Bogus",
    });
    const absentAnomalies = await demoApi.getAnomalies(fullServiceRange);
    const unknownAnomalies = await demoApi.getAnomalies({
      ...fullServiceRange,
      provider: "Bogus",
    });
    const economicsParams = {
      start: "",
      end: "",
      metric: "requests served",
    };
    const absentEconomics =
      await demoApi.getUnitEconomicsDaily(economicsParams);
    const unknownEconomics = await demoApi.getUnitEconomicsDaily({
      ...economicsParams,
      provider: "Bogus",
    });

    expect(absentCosts).toBe(baseCosts);
    expect(unknownCosts).toBe(absentCosts);
    expect(absentSummary).toBe(baseSummary);
    expect(unknownSummary).toBe(absentSummary);
    expect(absentAnomalies).toBe(baseAnomalies);
    expect(unknownAnomalies).toBe(absentAnomalies);
    expect(absentEconomics).toBe(baseEconomics);
    expect(unknownEconomics).toBe(absentEconomics);
    expect(absentCosts.provider).toBe("");
    expect(absentCosts.providers).toEqual(baseCosts.providers);
    expect(absentSummary.provider).toBe("");
    expect(absentSummary.providers).toEqual(baseCosts.providers);
    expect(absentEconomics.provider).toBe("");
    expect(absentEconomics.providers).toEqual(baseCosts.providers);
  });

  it("resolves the complete provider, preset, and grouping matrix", async () => {
    expect(baseCosts.providers).toHaveLength(5);
    expect(DEMO_PRESETS).toHaveLength(3);
    const groupings = [
      "service",
      "provider",
      "allocation",
      "subaccount",
      "region",
    ] as const;
    let resolutions = 0;

    for (const provider of baseCosts.providers) {
      for (const preset of DEMO_PRESETS) {
        const range = { start: preset.start, end: preset.end };
        for (const groupBy of groupings) {
          await demoApi.getCostsDaily({ ...range, groupBy, provider });
          resolutions += 1;
          await demoApi.getCostsSummary({ ...range, groupBy, provider });
          resolutions += 1;
          await demoApi.getAnomalies({ ...range, groupBy, provider });
          resolutions += 1;
        }
        await demoApi.getCostsDaily({
          ...range,
          groupBy: "tag",
          tagKey: "environment",
          provider,
        });
        resolutions += 1;
        await demoApi.getCostsSummary({
          ...range,
          groupBy: "tag",
          tagKey: "environment",
          provider,
        });
        resolutions += 1;
        await demoApi.getAnomalies({
          ...range,
          groupBy: "tag",
          tagKey: "environment",
          provider,
        });
        resolutions += 1;
        await demoApi.getUnitEconomicsDaily({
          ...range,
          metric: "requests served",
          provider,
        });
        resolutions += 1;
      }
    }

    expect(resolutions).toBe(285);
  });

  it("returns base and provider-filtered drill-down fixtures by identity", async () => {
    const last30 = DEMO_PRESETS.find((preset) => preset.id === "last30")!;
    const range = { start: last30.start, end: last30.end };

    const region = await demoApi.getCostsDaily({
      ...range,
      groupBy: "region",
    });
    const subaccount = await demoApi.getCostsDaily({
      ...range,
      groupBy: "subaccount",
      provider: "Amazon Web Services",
    });

    expect(region).toBe(baseRegionCosts);
    expect(subaccount).toBe(amazonSubaccountCosts);
  });

  it("returns known tag-key fixtures by identity", async () => {
    const base = await demoApi.getCostsDaily({
      ...fullServiceRange,
      groupBy: "tag",
      tagKey: "environment",
    });
    const filtered = await demoApi.getCostsDaily({
      ...fullServiceRange,
      groupBy: "tag",
      tagKey: "environment",
      provider: "Amazon Web Services",
    });

    expect(base).toBe(baseTagCosts);
    expect(filtered).toBe(amazonTagCosts);
  });

  it("returns the base service fixture for an unknown tag key", async () => {
    const costs = await demoApi.getCostsDaily({
      ...fullServiceRange,
      groupBy: "tag",
      tagKey: "unknown key",
    });

    expect(costs).toBe(baseCosts);
  });

  it("returns provider-rescored Google anomalies", async () => {
    const filtered = await demoApi.getAnomalies({
      ...fullServiceRange,
      provider: "Google",
    });

    expect(filtered).toBe(googleAnomalies);
    expect(filtered.anomalies.length).toBeGreaterThan(0);
    expect(filtered).not.toEqual(baseAnomalies);
  });
});

describe("api.demo missing filtered fixture", () => {
  afterEach(() => {
    vi.doUnmock("./demo/fixtures/costs.full.service.json");
    vi.resetModules();
  });

  it("throws when a provider is known but its filtered fixture is missing", async () => {
    const provider = "Fabricated Provider";
    vi.resetModules();
    vi.doMock("./demo/fixtures/costs.full.service.json", () => ({
      default: {
        ...baseCosts,
        providers: [...baseCosts.providers, provider],
      },
    }));
    const isolatedDemoApi = await import("./api.demo");

    await expect(
      isolatedDemoApi.getCostsDaily({ ...fullServiceRange, provider }),
    ).rejects.toThrow(
      "missing demo fixture: filtered/costs.full.service.fabricated-provider.json",
    );
  });

  it("throws when a tag key is known but its filtered fixture is missing", async () => {
    const tagKey = "missing key";
    vi.resetModules();
    vi.doMock("./demo/fixtures/costs.full.service.json", () => ({
      default: {
        ...baseCosts,
        tagKeys: [...baseCosts.tagKeys, tagKey],
      },
    }));
    const isolatedDemoApi = await import("./api.demo");

    await expect(
      isolatedDemoApi.getCostsDaily({
        ...fullServiceRange,
        groupBy: "tag",
        tagKey,
        provider: "Amazon Web Services",
      }),
    ).rejects.toThrow(
      "missing demo fixture: filtered/costs.full.tag.missing-key.amazon-web-services.json",
    );
  });
});

describe("api.demo getSyncStatus", () => {
  it("returns the enabled four-source captured fixture", async () => {
    const status = await demoApi.getSyncStatus();

    expect(status.enabled).toBe(true);
    expect(status.sources).toHaveLength(4);
  });
});
