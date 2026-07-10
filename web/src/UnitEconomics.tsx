// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import type { Range } from "./range";
import { rangeQuery } from "./range";

type BusinessMetricInfo = components["schemas"]["BusinessMetricInfo"];
type UnitEconomicsResponse = components["schemas"]["UnitEconomics"];

type MetricsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; metrics: BusinessMetricInfo[] };

type EconomicsState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; economics: UnitEconomicsResponse };

export default function UnitEconomics({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [metricsState, setMetricsState] = useState<MetricsState>({
    status: "loading",
  });
  const [selectedMetric, setSelectedMetric] = useState("");
  const [economicsState, setEconomicsState] = useState<EconomicsState>({
    status: "idle",
  });
  const { start, end } = range;

  useEffect(() => {
    const controller = new AbortController();
    async function loadMetrics() {
      try {
        const res = await fetch("/api/v1/business-metrics", {
          signal: controller.signal,
        });
        if (!res.ok) {
          throw new Error(
            `GET /api/v1/business-metrics returned ${res.status}`,
          );
        }
        const body =
          (await res.json()) as components["schemas"]["BusinessMetrics"];
        if (controller.signal.aborted) return;
        setMetricsState({ status: "ready", metrics: body.metrics });
        setSelectedMetric((current) => current || body.metrics[0]?.name || "");
      } catch (err) {
        if (controller.signal.aborted) return;
        setMetricsState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }
    void loadMetrics();
    return () => controller.abort();
  }, []);

  useEffect(() => {
    if (selectedMetric === "") {
      setEconomicsState({ status: "idle" });
      return;
    }
    const controller = new AbortController();
    setEconomicsState({ status: "loading" });
    async function loadEconomics() {
      try {
        const rangeSuffix = rangeQuery(start, end).replace("?", "&");
        const url =
          `/api/v1/unit-economics/daily?metric=${encodeURIComponent(selectedMetric)}` +
          rangeSuffix;
        const res = await fetch(url, { signal: controller.signal });
        if (!res.ok) {
          throw new Error(
            `GET /api/v1/unit-economics/daily returned ${res.status}`,
          );
        }
        const body = (await res.json()) as UnitEconomicsResponse;
        if (controller.signal.aborted) return;
        setEconomicsState({ status: "ready", economics: body });
      } catch (err) {
        if (controller.signal.aborted) return;
        setEconomicsState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }
    void loadEconomics();
    return () => controller.abort();
  }, [selectedMetric, start, end]);

  return (
    <section className="unit-economics">
      <h2>Unit economics</h2>
      {metricsState.status === "loading" && <p>Loading business metrics…</p>}
      {metricsState.status === "error" && (
        <p role="alert">
          Failed to load business metrics: {metricsState.message}
        </p>
      )}
      {metricsState.status === "ready" &&
        (metricsState.metrics.length === 0 ? (
          <EmptyState />
        ) : (
          <>
            <label>
              Business metric
              <select
                style={{ marginInlineStart: "0.5rem" }}
                value={selectedMetric}
                onChange={(event) => setSelectedMetric(event.target.value)}
              >
                {metricsState.metrics.map((metric) => (
                  <option key={metric.name} value={metric.name}>
                    {metric.name}
                  </option>
                ))}
              </select>
            </label>
            {economicsState.status === "loading" && (
              <p>Loading unit economics…</p>
            )}
            {economicsState.status === "error" && (
              <p role="alert">
                Failed to load unit economics: {economicsState.message}
              </p>
            )}
            {economicsState.status === "ready" && (
              <EconomicsTable economics={economicsState.economics} />
            )}
          </>
        ))}
    </section>
  );
}

function EmptyState() {
  return (
    <div className="viz-empty">
      <p>
        No business metrics yet. Import a strict CSV, then reload this page:
      </p>
      <pre>
        <code>costroid metrics import --path &lt;file.csv&gt;</code>
      </pre>
      <p>
        Stop <code>costroid serve</code> while importing because the embedded
        store is single-writer.
      </p>
    </div>
  );
}

function EconomicsTable({ economics }: { economics: UnitEconomicsResponse }) {
  return (
    <div>
      <dl>
        <dt>Covered days</dt>
        <dd>{economics.period.coveredDays}</dd>
        <dt>Period cost</dt>
        <dd>
          {economics.period.cost} {economics.currency}
        </dd>
        <dt>Period quantity</dt>
        <dd>{economics.period.quantity}</dd>
        <dt>Period unit cost</dt>
        <dd>{economics.period.unitCost ?? "—"}</dd>
      </dl>
      <table>
        <thead>
          <tr>
            <th scope="col">Date</th>
            <th scope="col">Cost</th>
            <th scope="col">Quantity</th>
            <th scope="col">Unit cost</th>
          </tr>
        </thead>
        <tbody>
          {economics.days.map((day) => (
            <tr key={day.date}>
              <th scope="row">{day.date}</th>
              <td>{day.cost ?? "—"}</td>
              <td>{day.quantity ?? "—"}</td>
              <td>{day.unitCost ?? "—"}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
