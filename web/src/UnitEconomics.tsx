// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { EmptyIcon } from "./icons";
import type { Range } from "./range";
import { rangeQuery } from "./range";
import { ErrorState, LoadingSkeleton, StatCard } from "./ViewState";

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
    <section className="unit-economics" aria-labelledby="economics-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Business efficiency</p>
          <h2 id="economics-title">Unit economics</h2>
        </div>
      </div>
      {metricsState.status === "loading" && (
        <LoadingSkeleton label="Loading business metrics…" />
      )}
      {metricsState.status === "error" && (
        <ErrorState>
          Failed to load business metrics: {metricsState.message}
        </ErrorState>
      )}
      {metricsState.status === "ready" &&
        (metricsState.metrics.length === 0 ? (
          <EmptyState />
        ) : (
          <>
            <label className="metric-control">
              Business metric
              <select
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
              <LoadingSkeleton label="Loading unit economics…" />
            )}
            {economicsState.status === "error" && (
              <ErrorState>
                Failed to load unit economics: {economicsState.message}
              </ErrorState>
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
      <div className="state-content">
        <EmptyIcon className="state-icon" size={30} />
        <p className="state-title">No business metrics yet</p>
        <p className="state-message">
          Import a strict CSV, then reload this page:
        </p>
        <pre>
          <code>costroid metrics import --path &lt;file.csv&gt;</code>
        </pre>
        <p>
          Stop <code>costroid serve</code> while importing because the embedded
          store is single-writer.
        </p>
      </div>
    </div>
  );
}

function EconomicsTable({ economics }: { economics: UnitEconomicsResponse }) {
  return (
    <div>
      <div className="stat-grid">
        <StatCard label="Covered days" value={economics.period.coveredDays} />
        <StatCard
          label="Period cost"
          value={economics.period.cost}
          subtitle={economics.currency}
        />
        <StatCard
          label="Period quantity"
          value={economics.period.quantity}
          subtitle={economics.metric}
        />
        <StatCard
          label="Period unit cost"
          value={economics.period.unitCost ?? "—"}
          subtitle={`${economics.currency} / ${economics.metric}`}
        />
      </div>
      <div className="table-panel">
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
    </div>
  );
}
