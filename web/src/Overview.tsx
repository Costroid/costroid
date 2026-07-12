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
import type { Range } from "./range";
import { ErrorState, LoadingSkeleton, StatCard } from "./ViewState";
import { compareDecimalMagnitude, serviceColor, sparklinePoints } from "./viz";

type CostsSummary = components["schemas"]["CostsSummary"];
type Anomaly = components["schemas"]["Anomaly"];
type UnitEconomics = components["schemas"]["UnitEconomics"];

type FetchParams = { start: string; end: string };

type SummaryState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; summary: CostsSummary; params: FetchParams };

type AnomalyState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; anomalies: Anomaly[]; params: FetchParams };

type UnitState =
  | { status: "loading" }
  | { status: "error"; message: string }
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
  const [summaryState, setSummaryState] = useState<SummaryState>({
    status: "loading",
  });
  const [anomalyState, setAnomalyState] = useState<AnomalyState>({
    status: "loading",
  });
  const [unitState, setUnitState] = useState<UnitState>({ status: "loading" });

  // Effect 1: costs summary → cards 1–3 (degrade together).
  useEffect(() => {
    setSummaryState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const summary = await getCostsSummary(
          { start, end, groupBy: "provider" },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setSummaryState({
          status: "ready",
          summary,
          params: { start, end },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setSummaryState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end]);

  // Effect 2: anomalies by service → card 4.
  useEffect(() => {
    setAnomalyState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const body = await getAnomalies(
          { start, end, groupBy: "service" },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "ready",
          anomalies: body.anomalies ?? [],
          params: { start, end },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end]);

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
          setUnitState({ status: "empty", params: { start, end } });
          return;
        }
        const economics = await getUnitEconomicsDaily(
          { metric: first.name, start, end },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setUnitState({
          status: "ready",
          economics,
          params: { start, end },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setUnitState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end]);

  // Synchronous staleness: held ready data for different params → loading.
  const summary: SummaryState =
    summaryState.status === "ready" &&
    (summaryState.params.start !== start || summaryState.params.end !== end)
      ? { status: "loading" }
      : summaryState;
  const anomalies: AnomalyState =
    anomalyState.status === "ready" &&
    (anomalyState.params.start !== start || anomalyState.params.end !== end)
      ? { status: "loading" }
      : anomalyState;
  const unit: UnitState =
    (unitState.status === "ready" || unitState.status === "empty") &&
    (unitState.params.start !== start || unitState.params.end !== end)
      ? { status: "loading" }
      : unitState;

  return (
    <section className="overview" aria-labelledby="overview-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Executive landing</p>
          <h2 id="overview-title">Overview</h2>
        </div>
      </div>

      <div className="overview-grid">
        {/* Cards 1–3: summary chain */}
        {summary.status === "loading" && (
          <div className="overview-summary-block">
            <LoadingSkeleton label="Loading cost summary…" />
          </div>
        )}
        {summary.status === "error" && (
          <div className="overview-summary-block">
            <ErrorState>
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

        {/* Card 4: anomalies */}
        {anomalies.status === "loading" && (
          <LoadingSkeleton label="Loading anomalies…" />
        )}
        {anomalies.status === "error" && (
          <ErrorState>Failed to load anomalies: {anomalies.message}</ErrorState>
        )}
        {anomalies.status === "ready" && (
          <AnomalyCountCard anomalies={anomalies.anomalies} />
        )}

        {/* Card 5: unit cost */}
        {unit.status === "loading" && (
          <LoadingSkeleton label="Loading unit economics…" />
        )}
        {unit.status === "error" && (
          <ErrorState>Failed to load unit cost: {unit.message}</ErrorState>
        )}
        {unit.status === "empty" && <UnitEmptyState />}
        {unit.status === "ready" && <UnitCostCard economics={unit.economics} />}
      </div>
    </section>
  );
}

function PeriodTotalCard({ summary }: { summary: CostsSummary }) {
  return (
    <article className="overview-card" aria-labelledby="overview-period-total">
      <h3 id="overview-period-total" className="overview-card-title">
        Period total
      </h3>
      <StatCard
        label="Period total"
        value={summary.total}
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
                <span className="overview-key-total">{k.total}</span>
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
          No preceding window to compare — ranking by period total.
        </p>
        <ul className="overview-key-list">
          {summary.keys.map((k) => (
            <li key={k.key}>
              <span className="overview-key-name">{k.key}</span>
              <span className="overview-key-total">{k.total}</span>
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
      <ul className="overview-key-list">
        {ranked.map((k) => (
          <li key={k.key}>
            <span className="overview-key-name">{k.key}</span>
            <span className="overview-key-delta">{k.delta}</span>
            <span className="overview-key-total">{k.total}</span>
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
      ? "by service — no spikes or dips in this range"
      : `by service — ${increases} increase${increases === 1 ? "" : "s"}, ${decreases} decrease${decreases === 1 ? "" : "s"}`;

  return (
    <article className="overview-card" aria-labelledby="overview-anomalies">
      <h3 id="overview-anomalies" className="overview-card-title">
        Anomalies
      </h3>
      <StatCard
        label="Flagged days"
        value={count}
        subtitle={count === 0 ? <span>All clear — {subtitle}</span> : subtitle}
      />
    </article>
  );
}

function UnitCostCard({ economics }: { economics: UnitEconomics }) {
  // Geometry only: Number() for sparkline y-scale of unitCost strings.
  const values = economics.days.map((d) =>
    d.unitCost === undefined || d.unitCost === null ? null : Number(d.unitCost),
  );
  const segments = sparklinePoints(values, 200, 40);
  const pathD = segments
    .map((seg) =>
      seg
        .map(
          (p, i) => `${i === 0 ? "M" : "L"}${p.x.toFixed(2)},${p.y.toFixed(2)}`,
        )
        .join(" "),
    )
    .join(" ");

  return (
    <article className="overview-card" aria-labelledby="overview-unit">
      <h3 id="overview-unit" className="overview-card-title">
        Unit cost
      </h3>
      <StatCard
        label="Period unit cost"
        value={economics.period.unitCost ?? "—"}
        subtitle={`${economics.currency} / ${economics.metric}`}
      />
      {pathD && (
        <svg
          className="overview-sparkline"
          viewBox="0 0 200 40"
          role="img"
          aria-label={`Daily unit cost sparkline for ${economics.metric}`}
        >
          <path d={pathD} className="overview-sparkline-path" fill="none" />
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
