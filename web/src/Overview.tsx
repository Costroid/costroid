// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useRef, useState } from "react";
import type { components } from "./api/schema";
import {
  getAnomalies,
  getBusinessMetrics,
  getCostsSummary,
  getInsights,
  getUnitEconomicsDaily,
} from "./api";
import { EmptyIcon } from "./icons";
import { Money } from "./money";
import type { Range } from "./range";
import { readUrlState, writeUrlState } from "./urlstate";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";
import {
  compareDecimalMagnitude,
  serviceColor,
  sparklineGeometry,
} from "./viz";

type CostsSummary = components["schemas"]["CostsSummary"];
type Anomaly = components["schemas"]["Anomaly"];
type UnitEconomics = components["schemas"]["UnitEconomics"];
type Insight = components["schemas"]["Insight"];
type InsightLink = components["schemas"]["InsightLink"];

type FetchParams = {
  start: string;
  end: string;
  currency: string;
  provider: string;
};

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

type InsightsState =
  | { status: "loading" }
  | { status: "error"; message: string; params: FetchParams }
  | {
      status: "ready";
      insights: Insight[];
      currency: string;
      params: FetchParams;
    };

export default function Overview({
  range = { start: "", end: "" },
  onNavigate,
}: {
  range?: Range;
  onNavigate?: (link: InsightLink) => void;
}) {
  const { start, end } = range;
  const [currency, setCurrency] = useState<string>(
    () => readUrlState().currency ?? "",
  );
  const [provider, setProvider] = useState<string>(
    () => readUrlState().provider ?? "",
  );
  const [summaryState, setSummaryState] = useState<SummaryState>({
    status: "loading",
  });
  const [anomalyState, setAnomalyState] = useState<AnomalyState>({
    status: "loading",
  });
  const [unitState, setUnitState] = useState<UnitState>({ status: "loading" });
  const [insightsState, setInsightsState] = useState<InsightsState>({
    status: "loading",
  });
  // One token re-runs all four fetch effects; every error card shares it.
  const [retryToken, setRetryToken] = useState(0);
  const retry = () => setRetryToken((t) => t + 1);
  // Once an insight link starts navigation, skip the filter write so a queued
  // currency/provider reconciliation cannot clobber the link's hash fields.
  const suppressFilterWrite = useRef(false);

  useEffect(() => {
    if (suppressFilterWrite.current) return;
    writeUrlState({ currency, provider });
  }, [currency, provider]);

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
            groupBy: provider ? "service" : "provider",
            ...(currency ? { currency } : {}),
            ...(provider ? { provider } : {}),
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
        // All providers is a valid display state. Reconcile against the
        // unscoped selector LIST, never the echoed requested provider, and
        // still commit this ready body while the snapped request starts.
        const nextProvider =
          provider !== "" && !summary.providers.includes(provider)
            ? ""
            : provider;
        if (nextProvider !== provider) {
          setProvider(nextProvider);
        }
        setSummaryState({
          status: "ready",
          summary,
          params: { start, end, currency, provider },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setSummaryState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency, provider },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, provider, retryToken]);

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
            ...(provider ? { provider } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "ready",
          anomalies: body.anomalies ?? [],
          params: { start, end, currency, provider },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setAnomalyState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency, provider },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, provider, retryToken]);

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
            params: { start, end, currency, provider },
          });
          return;
        }
        const economics = await getUnitEconomicsDaily(
          {
            metric: first.name,
            start,
            end,
            ...(currency ? { currency } : {}),
            ...(provider ? { provider } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setUnitState({
          status: "ready",
          economics,
          params: { start, end, currency, provider },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setUnitState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency, provider },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, provider, retryToken]);

  // Effect 4: insights digest → full-width narrative panel.
  // The request is provider-independent (no provider query param). provider is
  // still in deps and params so a filter switch stays coherent with caption and
  // staleness (same loading UX as the other cards; body is unchanged).
  useEffect(() => {
    setInsightsState({ status: "loading" });
    const controller = new AbortController();
    async function load() {
      try {
        const body = await getInsights(
          {
            start,
            end,
            ...(currency ? { currency } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) return;
        setInsightsState({
          status: "ready",
          insights: body.insights ?? [],
          currency: body.currency,
          params: { start, end, currency, provider },
        });
      } catch (err) {
        if (controller.signal.aborted) return;
        setInsightsState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, currency, provider },
        });
      }
    }
    void load();
    return () => controller.abort();
  }, [start, end, currency, provider, retryToken]);

  // Synchronous staleness: held terminal data for different params → loading.
  // This includes errors so a range/currency/provider change never flashes an
  // old error for one frame before the passive effects set loading states.
  const summary: SummaryState =
    (summaryState.status === "ready" || summaryState.status === "error") &&
    (summaryState.params.start !== start ||
      summaryState.params.end !== end ||
      summaryState.params.currency !== currency ||
      summaryState.params.provider !== provider)
      ? { status: "loading" }
      : summaryState;
  const anomalies: AnomalyState =
    (anomalyState.status === "ready" || anomalyState.status === "error") &&
    (anomalyState.params.start !== start ||
      anomalyState.params.end !== end ||
      anomalyState.params.currency !== currency ||
      anomalyState.params.provider !== provider)
      ? { status: "loading" }
      : anomalyState;
  const unit: UnitState =
    (unitState.status === "ready" ||
      unitState.status === "empty" ||
      unitState.status === "error") &&
    (unitState.params.start !== start ||
      unitState.params.end !== end ||
      unitState.params.currency !== currency ||
      unitState.params.provider !== provider)
      ? { status: "loading" }
      : unitState;
  const insights: InsightsState =
    (insightsState.status === "ready" || insightsState.status === "error") &&
    (insightsState.params.start !== start ||
      insightsState.params.end !== end ||
      insightsState.params.currency !== currency ||
      insightsState.params.provider !== provider)
      ? { status: "loading" }
      : insightsState;
  const filtered =
    summary.status === "ready" && summary.summary.provider !== "";

  function handleInsightNavigate(link: InsightLink) {
    // Arm before the callback so a reconciling setCurrency/setProvider that
    // flushes in the same commit cannot rewrite the hash the link just set.
    suppressFilterWrite.current = true;
    onNavigate?.(link);
  }

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
        {summary.status === "ready" && summary.summary.providers.length > 1 && (
          <div
            className="cost-group-control"
            role="group"
            aria-label="Provider"
          >
            <span>Provider</span>
            <button
              type="button"
              aria-pressed={provider === ""}
              onClick={() => setProvider("")}
            >
              All providers
            </button>
            {summary.summary.providers.map((name) => (
              <button
                key={name}
                type="button"
                aria-pressed={provider === name}
                onClick={() => setProvider(name)}
              >
                {name}
              </button>
            ))}
          </div>
        )}
      </div>

      <ViewStatus
        message={
          summary.status === "loading" ||
          anomalies.status === "loading" ||
          unit.status === "loading" ||
          insights.status === "loading"
            ? "Loading overview…"
            : summary.status === "error" ||
                anomalies.status === "error" ||
                unit.status === "error" ||
                insights.status === "error"
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
            <ProviderSplitCard summary={summary.summary} filtered={filtered} />
            <MoversCard summary={summary.summary} filtered={filtered} />
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

        <div className="overview-card-slot overview-insights-slot">
          {insights.status === "loading" && <LoadingSkeleton />}
          {insights.status === "error" && (
            <ErrorState onRetry={retry}>
              Failed to load insights: {insights.message}
            </ErrorState>
          )}
          {insights.status === "ready" && (
            <InsightsCard
              insights={insights.insights}
              currency={insights.currency}
              start={start}
              end={end}
              provider={provider}
              onNavigate={onNavigate ? handleInsightNavigate : undefined}
            />
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

function ProviderSplitCard({
  summary,
  filtered,
}: {
  summary: CostsSummary;
  filtered: boolean;
}) {
  // Widths are Number() geometry only (D40); displayed money stays strings.
  const widths = summary.keys.map((k) => Math.max(0, Number(k.total)));
  const sum = widths.reduce((a, b) => a + b, 0) || 1;

  return (
    <article className="overview-card" aria-labelledby="overview-split">
      <h3 id="overview-split" className="overview-card-title">
        {filtered ? "Spend by service" : "Spend by provider"}
      </h3>
      {summary.keys.length === 0 ? (
        <p className="overview-muted">No cost in this range.</p>
      ) : (
        <>
          <div
            className="overview-split-bar"
            role="img"
            aria-label={
              filtered ? "Service spend split" : "Provider spend split"
            }
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

function MoversCard({
  summary,
  filtered,
}: {
  summary: CostsSummary;
  filtered: boolean;
}) {
  // NORMAL when ≥1 key carries delta (equivalently previousTotal present at top).
  const hasDelta = summary.keys.some((k) => k.delta !== undefined);
  if (!hasDelta) {
    return (
      <article className="overview-card" aria-labelledby="overview-movers">
        <h3 id="overview-movers" className="overview-card-title">
          {filtered ? "Largest services" : "Largest providers"}
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
        <span>{filtered ? "Service" : "Provider"}</span>
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

function InsightsCard({
  insights,
  currency,
  start,
  end,
  provider,
  onNavigate,
}: {
  insights: Insight[];
  currency: string;
  start: string;
  end: string;
  provider: string;
  onNavigate?: (link: InsightLink) => void;
}) {
  const missingBound = start === "" || end === "";
  const hasComparisonType = insights.some(
    (item) => item.type === "top-mover" || item.type === "unit-cost-drift",
  );
  const showRangeHint = missingBound && !hasComparisonType;

  return (
    <article
      className="overview-card overview-insights-panel"
      aria-labelledby="overview-insights"
    >
      <h3 id="overview-insights" className="overview-card-title">
        Insights
      </h3>
      {provider !== "" && (
        <p className="overview-muted">This digest covers all providers.</p>
      )}
      {insights.length === 0 ? (
        <p className="overview-muted">No insights for this range.</p>
      ) : (
        <ul className="overview-insights-list">
          {insights.map((insight, index) => (
            <li
              key={`${insight.type}:${insight.key ?? ""}:${index}`}
              className="overview-insight"
            >
              <div className="overview-insight-header">
                <h4 className="overview-insight-title">{insight.title}</h4>
                <span className="overview-insight-magnitude">
                  <Money value={insight.magnitude} currency={currency} />
                </span>
              </div>
              <p className="overview-insight-body">{insight.body}</p>
              {insight.evidence.length > 0 && (
                <dl className="overview-insight-evidence">
                  {insight.evidence.map((row) => (
                    <div
                      key={row.name}
                      className="overview-insight-evidence-row"
                    >
                      <dt>{row.name}</dt>
                      <dd>{row.value}</dd>
                    </div>
                  ))}
                </dl>
              )}
              {onNavigate && (
                <button
                  type="button"
                  className="overview-insight-link"
                  aria-label={`View details for ${insight.title}`}
                  onClick={() => onNavigate(insight.link)}
                >
                  View details
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
      {showRangeHint && (
        <p className="overview-muted">
          Choose a start and end date to include period comparisons.
        </p>
      )}
    </article>
  );
}
