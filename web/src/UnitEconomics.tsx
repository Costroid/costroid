// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getBusinessMetrics, getUnitEconomicsDaily } from "./api";
import { EmptyIcon } from "./icons";
import { Money } from "./money";
import type { Range } from "./range";
import { ErrorState, LoadingSkeleton, StatCard } from "./ViewState";
import { HEIGHT, lineChartGeometry, MARGIN, WIDTH, yTicks } from "./viz";

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
  // Geometry only: Number() for the y-scale of unitCost strings (D40).
  // Uncovered days (no unitCost) open gaps in the line.
  const values = economics.days.map((d) =>
    d.unitCost === undefined || d.unitCost === null ? null : Number(d.unitCost),
  );
  const nums = values.filter(
    (v): v is number => v !== null && Number.isFinite(v),
  );
  const hasChart = nums.length > 0;
  const ticks = hasChart ? yTicks(Math.max(...nums)) : [];
  const top = hasChart ? ticks[ticks.length - 1].value || 1 : 1;
  const geometry = lineChartGeometry(values, top);
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;
  const yOf = (value: number) => baseline - (value / top) * plotHeight;
  // Long ranges: label every k-th day so at most ~12 date labels render.
  const labelEvery = Math.max(1, Math.ceil(economics.days.length / 12));

  return (
    <div>
      <div className="stat-grid">
        <StatCard label="Covered days" value={economics.period.coveredDays} />
        <StatCard
          label="Period cost"
          value={
            <Money
              value={economics.period.cost}
              currency={economics.currency}
            />
          }
          subtitle={economics.currency}
        />
        <StatCard
          label="Period quantity"
          value={economics.period.quantity}
          subtitle={economics.metric}
        />
        <StatCard
          label="Period unit cost"
          value={
            <Money
              value={economics.period.unitCost}
              currency={economics.currency}
            />
          }
          subtitle={`${economics.currency} / ${economics.metric}`}
        />
      </div>
      {hasChart && (
        <div className="viz-panel">
          <div className="chart-wrapper">
            <svg
              viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
              role="img"
              aria-label={`Daily unit cost for ${economics.metric}`}
              className="viz-chart"
            >
              {ticks.map((tick) => (
                <g key={tick.label}>
                  <line
                    x1={MARGIN.left}
                    x2={WIDTH - MARGIN.right}
                    y1={yOf(tick.value)}
                    y2={yOf(tick.value)}
                    className={tick.value === 0 ? "viz-baseline" : "viz-grid"}
                  />
                  <text
                    x={MARGIN.left - 8}
                    y={yOf(tick.value) + 3}
                    className="viz-tick"
                    textAnchor="end"
                  >
                    {tick.label}
                  </text>
                </g>
              ))}
              {geometry.paths.map((d) => (
                <path key={d} d={d} className="viz-line-path" fill="none" />
              ))}
              {geometry.dots.map((point) => (
                <circle
                  key={`${point.x},${point.y}`}
                  className="viz-line-dot"
                  cx={point.x}
                  cy={point.y}
                  r="2.5"
                />
              ))}
              {economics.days.map(
                (day, i) =>
                  i % labelEvery === 0 && (
                    <text
                      key={day.date}
                      x={geometry.xs[i]}
                      y={baseline + 16}
                      className="viz-tick"
                      textAnchor="middle"
                    >
                      {day.date.slice(5)}
                    </text>
                  ),
              )}
            </svg>
          </div>
        </div>
      )}
      <details className="viz-table">
        <summary>View as table</summary>
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
                <td>
                  <Money value={day.cost} currency={economics.currency} />
                </td>
                <td>{day.quantity ?? "—"}</td>
                <td>
                  <Money value={day.unitCost} currency={economics.currency} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}
