// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getBusinessMetrics, getUnitEconomicsDaily } from "./api";
import { EmptyIcon } from "./icons";
import type { Range } from "./range";
import { ErrorState, LoadingSkeleton, StatCard } from "./ViewState";

type BusinessMetricInfo = components["schemas"]["BusinessMetricInfo"];
type UnitEconomicsResponse = components["schemas"]["UnitEconomics"];

type MetricsState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; metrics: BusinessMetricInfo[] };

type EconomicsFetchParams = {
  metric: string;
  start: string;
  end: string;
  currency: string;
};

type EconomicsState =
  | { status: "idle" }
  | { status: "loading" }
  | { status: "error"; message: string; params: EconomicsFetchParams }
  | {
      status: "ready";
      economics: UnitEconomicsResponse;
      params: EconomicsFetchParams;
    };

export default function UnitEconomics({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [metricsState, setMetricsState] = useState<MetricsState>({
    status: "loading",
  });
  const [selectedMetric, setSelectedMetric] = useState("");
  const [currency, setCurrency] = useState<string>("");
  const [economicsState, setEconomicsState] = useState<EconomicsState>({
    status: "idle",
  });
  const { start, end } = range;

  useEffect(() => {
    const controller = new AbortController();
    async function loadMetrics() {
      try {
        const body = await getBusinessMetrics(controller.signal);
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
    const params = { metric: selectedMetric, start, end, currency };
    setEconomicsState({ status: "loading" });
    async function loadEconomics() {
      try {
        const body = await getUnitEconomicsDaily(
          {
            metric: selectedMetric,
            start,
            end,
            ...(currency ? { currency } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        // A valid requested currency is echoed even when it has no rows. Reconcile
        // against the available-currency LIST so a range/metric change cannot
        // strand the view on an empty, no-longer-selectable series.
        const nextCurrency =
          currency !== "" && !body.currencies.includes(currency)
            ? (body.currencies[0] ?? "")
            : currency;
        if (nextCurrency !== currency) {
          setCurrency(nextCurrency);
          return;
        }
        setEconomicsState({ status: "ready", economics: body, params });
      } catch (err) {
        if (controller.signal.aborted) return;
        setEconomicsState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params,
        });
      }
    }
    void loadEconomics();
    return () => controller.abort();
  }, [selectedMetric, start, end, currency]);

  // A metric/range/currency change commits before its passive effect can replace
  // the old response with an explicit loading state. Hide that stale terminal
  // response synchronously so the controls and table never disagree for a frame.
  const economics: EconomicsState =
    (economicsState.status === "ready" || economicsState.status === "error") &&
    (economicsState.params.metric !== selectedMetric ||
      economicsState.params.start !== start ||
      economicsState.params.end !== end ||
      economicsState.params.currency !== currency)
      ? { status: "loading" }
      : economicsState;

  return (
    <section className="unit-economics" aria-labelledby="economics-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Business efficiency</p>
          <h2 id="economics-title">Unit economics</h2>
        </div>
        {metricsState.status === "ready" && metricsState.metrics.length > 0 && (
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
        )}
        {economics.status === "ready" &&
          economics.economics.currencies.length > 1 && (
            <div
              className="cost-group-control"
              role="group"
              aria-label="Currency"
            >
              <span>Currency</span>
              {economics.economics.currencies.map((code) => (
                <button
                  key={code}
                  type="button"
                  aria-pressed={
                    (currency || economics.economics.currency) === code
                  }
                  onClick={() => setCurrency(code)}
                >
                  {code}
                </button>
              ))}
            </div>
          )}
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
            {economics.status === "loading" && (
              <LoadingSkeleton label="Loading unit economics…" />
            )}
            {economics.status === "error" && (
              <ErrorState>
                Failed to load unit economics: {economics.message}
              </ErrorState>
            )}
            {economics.status === "ready" && (
              <EconomicsTable economics={economics.economics} />
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
