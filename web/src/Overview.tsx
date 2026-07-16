// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import {
  getAnomalies,
  getBusinessMetrics,
  getCostsSummary,
  getUnitEconomicsDaily,
} from "./api";
import { EmptyIcon } from "./icons";
import { Money } from "./money";
import type { Range } from "./range";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";
import {
  compareDecimalMagnitude,
  serviceColor,
  sparklineGeometry,
} from "./viz";

type CostsSummary = components["schemas"]["CostsSummary"];
type Anomaly = components["schemas"]["Anomaly"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

type FetchParams = { start: string; end: string; currency: string };

type SummaryState =
  | { status: "loading" }
  | { status: "error"; message: string; params: FetchParams }
  | { status: "ready"; summary: CostsSummary; params: FetchParams };

type AnomalyState =
  | { status: "loading" }
  | { status: "error"; message: string; params: FetchParams }
  | { status: "ready"; anomalies: Anomaly[]; params: FetchParams };

type UnitState =
  | { status: "loading" }
  | { status: "error"; message: string; params: FetchParams }
  | { status: "empty"; params: FetchParams }
  | {
      status: "ready";
      economics: UnitEconomics;
      params: FetchParams;
    };

export default function Overview({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const { start, end } = range;
  const [currency, setCurrency] = useState<string>("");
  const [summaryState, setSummaryState] = useState<SummaryState>({
    status: "loading",
  });
  const [anomalyState, setAnomalyState] = useState<AnomalyState>({
    status: "loading",
  });
  const [unitState, setUnitState] = useState<UnitState>({ status: "loading" });
  // One token re-runs all three fetch effects; every error card shares it.
  const [retryToken, setRetryToken] = useState(0);
  const retry = () => setRetryToken((t) => t + 1);

  // Effect 1: costs summary → cards 1–3 (degrade together).
  useEffect(() => {
    setSummaryState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const summary = await getCostsSummary(
          {
            start,
            end,
            groupBy: "provider",
            ...(currency ? { currency } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        // The server echoes an explicitly requested but absent currency. Recover
        // from a range change by checking the in-range LIST, never that echo.
        const nextCurrency =
          currency !== "" && !summary.currencies.includes(currency)
            ? (summary.currencies[0] ?? "")
            : currency;
        if (nextCurrency !== currency) {
          setCurrency(nextCurrency);
        }
        setSummaryState({
          status: "ready",
          summary,
          params: { start, end, currency },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setSummaryState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, retryToken]);

  // Effect 2: anomalies by service → card 4.
  useEffect(() => {
    setAnomalyState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const body = await getAnomalies(
          {
            start,
            end,
            groupBy: "service",
            ...(currency ? { currency } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "ready",
          anomalies: body.anomalies ?? [],
          params: { start, end, currency },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, retryToken]);

  // Effect 3: business metrics → unit economics (chained) → card 5.
  useEffect(() => {
    setUnitState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const metrics = await getBusinessMetrics(controller.signal);
        if (controller.signal.aborted) return;
        // Server orders metric names ASC — metrics[0] is deterministic.
        const first = metrics.metrics[0];
        if (!first) {
          setUnitState({
            status: "empty",
            params: { start, end, currency },
          });
          return;
        }
        const economics = await getUnitEconomicsDaily(
          {
            metric: first.name,
            start,
            end,
            ...(currency ? { currency } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setUnitState({
          status: "ready",
          economics,
          params: { start, end, currency },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setUnitState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, retryToken]);

  // Synchronous staleness: held terminal data for different params → loading.
  // This includes errors so a range/currency change never flashes an old error
  // for one frame before the passive effects set their loading states.
  const summary: SummaryState =
    (summaryState.status === "ready" || summaryState.status === "error") &&
    (summaryState.params.start !== start ||
      summaryState.params.end !== end ||
      summaryState.params.currency !== currency)
      ? { status: "loading" }
      : summaryState;
  const anomalies: AnomalyState =
    (anomalyState.status === "ready" || anomalyState.status === "error") &&
    (anomalyState.params.start !== start ||
      anomalyState.params.end !== end ||
      anomalyState.params.currency !== currency)
      ? { status: "loading" }
      : anomalyState;
  const unit: UnitState =
    (unitState.status === "ready" ||
      unitState.status === "empty" ||
      unitState.status === "error") &&
    (unitState.params.start !== start ||
      unitState.params.end !== end ||
      unitState.params.currency !== currency)
      ? { status: "loading" }
      : unitState;

  return (
    <section className="overview" aria-labelledby="overview-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Executive landing</p>
          <h2 id="overview-title">Overview</h2>
        </div>
        {summary.status === "ready" &&
          summary.summary.currencies.length > 1 && (
            <div
              className="cost-group-control"
              role="group"
              aria-label="Currency"
            >
              <span>Currency</span>
              {summary.summary.currencies.map((code) => (
                <button
                  key={code}
                  type="button"
                  aria-pressed={(currency || summary.summary.currency) === code}
                  onClick={() => setCurrency(code)}
                >
                  {code}
                </button>
              ))}
            </div>
          )}
      </div>

      <ViewStatus
        message={
          summary.status === "loading" ||
          anomalies.status === "loading" ||
          unit.status === "loading"
            ? "Loading overview…"
            : summary.status === "error" ||
                anomalies.status === "error" ||
                unit.status === "error"
              ? ""
              : "Overview loaded"
        }
      />
      <div className="overview-grid">
        {/* Cards 1–3: summary chain */}
        {summary.status === "loading" && (
          <div className="overview-summary-block">
            <LoadingSkeleton />
          </div>
        )}
        {summary.status === "error" && (
          <div className="overview-summary-block">
            <ErrorState onRetry={retry}>
              Failed to load cost summary: {summary.message}
            </ErrorState>
          </div>
        )}
        {summary.status === "ready" && (
          <>
            <PeriodTotalCard summary={summary.summary} />
            <ProviderSplitCard summary={summary.summary} />
            <MoversCard summary={summary.summary} />
          </>
        )}

        <div className="overview-card-slot overview-anomaly-slot">
          {/* Card 4: anomalies */}
          {anomalies.status === "loading" && <LoadingSkeleton />}
          {anomalies.status === "error" && (
            <ErrorState onRetry={retry}>
              Failed to load anomalies: {anomalies.message}
            </ErrorState>
          )}
          {anomalies.status === "ready" && (
            <AnomalyCountCard anomalies={anomalies.anomalies} />
          )}
        </div>

        <div className="overview-card-slot overview-unit-slot">
          {/* Card 5: unit cost */}
          {unit.status === "loading" && <LoadingSkeleton />}
          {unit.status === "error" && (
            <ErrorState onRetry={retry}>
              Failed to load unit cost: {unit.message}
            </ErrorState>
          )}
          {unit.status === "empty" && <UnitEmptyState />}
          {unit.status === "ready" && (
            <UnitCostCard economics={unit.economics} />
          )}
        </div>
      </div>
    </section>
  );
}

function PeriodTotalCard({ summary }: { summary: CostsSummary }) {
  return (
    <article
      className="overview-card overview-hero"
      aria-labelledby="overview-period-total"
    >
      <h3 id="overview-period-total" className="overview-card-title">
        Period total
      </h3>
      <StatCard
        label="Period total"
        value={<Money value={summary.total} currency={summary.currency} />}
        subtitle={summary.currency}
      />
    </article>
  );
}

function ProviderSplitCard({ summary }: { summary: CostsSummary }) {
  // Widths are Number() geometry only (D40); displayed money stays strings.
  const widths = summary.keys.map((k) => Math.max(0, Number(k.total)));
  const sum = widths.reduce((a, b) => a + b, 0) || 1;

  return (
    <article className="overview-card" aria-labelledby="overview-split">
      <h3 id="overview-split" className="overview-card-title">
        Spend by provider
      </h3>
      {summary.keys.length === 0 ? (
        <p className="overview-muted">No cost in this range.</p>
      ) : (
        <>
          <div
            className="overview-split-bar"
            role="img"
            aria-label="Provider spend split"
          >
            {summary.keys.map((k, i) => (
              <span
                key={k.key}
                className="overview-split-segment"
                style={{
                  width: `${(widths[i] / sum) * 100}%`,
                  background: serviceColor(k.key),
                }}
                title={`${k.key}: ${k.total} ${summary.currency}`}
              />
            ))}
          </div>
          <ul className="overview-key-list">
            {summary.keys.map((k) => (
              <li key={k.key}>
                <span
                  className="viz-swatch"
                  style={{ background: serviceColor(k.key) }}
                />
                <span className="overview-key-name">{k.key}</span>
                <span className="overview-key-total">
                  <Money value={k.total} currency={summary.currency} />
                </span>
              </li>
            ))}
          </ul>
        </>
      )}
    </article>
  );
}

function MoversCard({ summary }: { summary: CostsSummary }) {
  // NORMAL when ≥1 key carries delta (equivalently previousTotal present at top).
  const hasDelta = summary.keys.some((k) => k.delta !== undefined);
  if (!hasDelta) {
    return (
      <article className="overview-card" aria-labelledby="overview-movers">
        <h3 id="overview-movers" className="overview-card-title">
          Largest providers
        </h3>
        <p className="overview-muted">
          No preceding window to compare; ranking by period total.
        </p>
        <ul className="overview-key-list">
          {summary.keys.map((k) => (
            <li key={k.key}>
              <span className="overview-key-name">{k.key}</span>
              <span className="overview-key-total">
                <Money value={k.total} currency={summary.currency} />
              </span>
            </li>
          ))}
        </ul>
      </article>
    );
  }

  // Rank by |delta| desc via magnitude comparator (not Number()).
  const ranked = [...summary.keys].sort((a, b) => {
    const cmp = compareDecimalMagnitude(b.delta ?? "0", a.delta ?? "0");
    if (cmp !== 0) return cmp;
    return a.key.localeCompare(b.key);
  });

  const windowLabel =
    summary.previousStart && summary.previousEnd
      ? `${summary.previousStart} → ${summary.previousEnd}`
      : null;

  return (
    <article className="overview-card" aria-labelledby="overview-movers">
      <h3 id="overview-movers" className="overview-card-title">
        Top movers
      </h3>
      {windowLabel && <p className="overview-muted">vs {windowLabel}</p>}
      <div className="overview-movers-header">
        <span>Provider</span>
        <span>Change</span>
        <span>Total</span>
      </div>
      <ul className="overview-key-list overview-movers-list">
        {ranked.map((k) => (
          <li key={k.key}>
            <span className="overview-key-name">{k.key}</span>
            <span className="overview-key-delta">
              <Money value={k.delta} currency={summary.currency} signed />
            </span>
            <span className="overview-key-total">
              <Money value={k.total} currency={summary.currency} />
            </span>
          </li>
        ))}
      </ul>
    </article>
  );
}

function AnomalyCountCard({ anomalies }: { anomalies: Anomaly[] }) {
  const count = anomalies.length;
  const increases = anomalies.filter((a) => a.direction === "increase").length;
  const decreases = anomalies.filter((a) => a.direction === "decrease").length;
  const subtitle =
    count === 0
      ? "by service: no spikes or dips in this range"
      : `by service: ${increases} increase${increases === 1 ? "" : "s"}, ${decreases} decrease${decreases === 1 ? "" : "s"}`;

  return (
    <article className="overview-card" aria-labelledby="overview-anomalies">
      <h3 id="overview-anomalies" className="overview-card-title">
        Anomalies
      </h3>
      <StatCard
        label="Flagged days"
        value={count}
        subtitle={count === 0 ? <span>All clear; {subtitle}</span> : subtitle}
      />
    </article>
  );
}

function UnitCostCard({ economics }: { economics: UnitEconomics }) {
  // Geometry only: Number() for sparkline y-scale of unitCost strings.
  const values = economics.days.map((d) =>
    d.unitCost === undefined || d.unitCost === null ? null : Number(d.unitCost),
  );
  const geometry = sparklineGeometry(values, 200, 40);
  const hasGeometry = geometry.paths.length > 0 || geometry.dots.length > 0;

  return (
    <article className="overview-card" aria-labelledby="overview-unit">
      <h3 id="overview-unit" className="overview-card-title">
        Unit cost
      </h3>
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
      {hasGeometry && (
        <svg
          className="overview-sparkline"
          viewBox="0 0 200 40"
          role="img"
          aria-label={`Daily unit cost sparkline for ${economics.metric}`}
        >
          {geometry.paths.map((d) => (
            <path
              key={d}
              d={d}
              className="overview-sparkline-path"
              fill="none"
            />
          ))}
          {geometry.dots.map((point) => (
            <circle
              key={`${point.x},${point.y}`}
              className="overview-sparkline-dot"
              cx={point.x}
              cy={point.y}
              r="1.5"
            />
          ))}
        </svg>
      )}
    </article>
  );
}

function UnitEmptyState() {
  return (
    <div className="viz-empty overview-card">
      <div className="state-content">
        <EmptyIcon className="state-icon" size={30} />
        <p className="state-title">No business metrics yet</p>
        <p className="state-message">
          Import a strict CSV, then reload this page:
        </p>
        <pre>
          <code>costroid metrics import --path &lt;file.csv&gt;</code>
        </pre>
      </div>
    </div>
  );
}
