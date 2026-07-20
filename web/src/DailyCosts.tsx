// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getAnomalies, getCostsDaily, type CostGroupBy } from "./api";
import { dailyCostsCsvFilename, dailyCostsToCsv, downloadCsv } from "./csv";
import { EmptyIcon } from "./icons";
import { formatMoney, Money } from "./money";
import type { Range } from "./range";
import { readUrlState, writeUrlState } from "./urlstate";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";
import {
  HEIGHT,
  MARGIN,
  MAX_BAR_WIDTH,
  SEGMENT_GAP,
  WIDTH,
  capLabelPositions,
  segmentPath,
  serviceColor,
  yTicks,
} from "./viz";

type DailyCosts = components["schemas"]["DailyCosts"];
type Anomaly = components["schemas"]["Anomaly"];

// FetchParams identifies the request a held "ready" result was fetched FOR, so a
// render can detect synchronously that the current props no longer match it.
type FetchParams = {
  start: string;
  end: string;
  groupBy: CostGroupBy;
  tagKey: string;
  currency: string;
  provider: string;
};
type CostFetchParams = FetchParams;

type CostsState =
  | { status: "loading" }
  | { status: "error"; message: string; params: CostFetchParams }
  | { status: "ready"; costs: DailyCosts; params: CostFetchParams };

// AnomalyState is fetched independently of the chart: a failure never blocks the
// chart, only suppresses the overlay (with a small non-blocking notice). Its
// params let a render ignore flags fetched for a stale grouping/range.
type AnomalyState =
  | { status: "loading"; params: FetchParams }
  | { status: "error"; message: string; params: FetchParams }
  | { status: "ready"; flags: Anomaly[]; params: FetchParams };

const GROUP_BY_OPTIONS: { id: CostGroupBy; label: string }[] = [
  { id: "service", label: "Service" },
  { id: "provider", label: "Provider" },
  { id: "allocation", label: "Allocation" },
  { id: "subaccount", label: "Subaccount" },
  { id: "region", label: "Region" },
  { id: "tag", label: "Tag" },
];

// groupLabelOf maps a grouping id to the lowercase heading/aria noun.
function groupLabelOf(groupBy: CostGroupBy): string {
  return (
    GROUP_BY_OPTIONS.find((o) => o.id === groupBy)?.label.toLowerCase() ??
    groupBy
  );
}

export default function DailyCosts({
  range = { start: "", end: "" },
}: {
  range?: Range;
}) {
  const [state, setState] = useState<CostsState>({ status: "loading" });
  const [retryToken, setRetryToken] = useState(0);
  const displayedCurrency =
    state.status === "ready" ? state.costs.currency : null;
  const displayedProvider =
    state.status === "ready" ? state.costs.provider : null;
  const [groupBy, setGroupBy] = useState<CostGroupBy>(
    () => readUrlState().groupBy ?? "service",
  );
  const [tagKey, setTagKey] = useState<string>(
    () => readUrlState().tagKey ?? "",
  );
  const [currency, setCurrency] = useState<string>(
    () => readUrlState().currency ?? "",
  );
  const [provider, setProvider] = useState<string>(
    () => readUrlState().provider ?? "",
  );
  const [anomalyState, setAnomalyState] = useState<AnomalyState>({
    status: "loading",
    params: {
      start: range.start,
      end: range.end,
      groupBy: "service",
      tagKey: "",
      currency: "",
      provider: "",
    },
  });
  const { start, end } = range;

  useEffect(() => {
    writeUrlState({ groupBy, tagKey, currency, provider });
  }, [groupBy, tagKey, currency, provider]);

  useEffect(() => {
    setState({ status: "loading" });
    const controller = new AbortController();

    async function load() {
      try {
        const costs = await getCostsDaily(
          {
            start,
            end,
            groupBy,
            ...(groupBy === "tag" ? { tagKey } : {}),
            ...(currency ? { currency } : {}),
            ...(provider ? { provider } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        // A selected currency no longer present in the range (e.g. the date
        // range narrowed to a window without it) snaps to the first in-range
        // currency — never the server's ECHOED request currency, which would
        // leave the stale selection filtering to an empty series while the
        // selector may be hidden (single-currency range), stranding the user.
        // An empty range snaps to "" (the server default).
        const nextCurrency =
          currency !== "" && !costs.currencies.includes(currency)
            ? (costs.currencies[0] ?? "")
            : currency;
        if (nextCurrency !== currency) {
          setCurrency(nextCurrency);
        }
        // Unlike currency, all providers is a valid display state, so a
        // narrowed range that drops the selection safely lands on All.
        const nextProvider =
          provider !== "" && !costs.providers.includes(provider)
            ? ""
            : provider;
        if (nextProvider !== provider) {
          setProvider(nextProvider);
        }
        if (groupBy === "tag") {
          if (costs.tagKeys.length === 0) {
            setGroupBy("service");
            setTagKey("");
          } else if (!costs.tagKeys.includes(tagKey)) {
            setTagKey(costs.tagKeys[0]);
          }
        }
        setState({
          status: "ready",
          costs,
          params: { start, end, groupBy, tagKey, currency, provider },
        });
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params: { start, end, groupBy, tagKey, currency, provider },
        });
      }
    }

    void load();
    return () => controller.abort();
  }, [start, end, groupBy, tagKey, currency, provider, retryToken]);

  // The anomaly overlay is fetched with the SAME range + groupBy + resolved
  // currency as the chart, but independently: a failure here must never break
  // the chart (it only drops the overlay and shows a small notice). No stale
  // markers — the held flags carry the params they were fetched for.
  useEffect(() => {
    if (displayedCurrency === null || displayedProvider === null) {
      return;
    }

    const params = {
      start,
      end,
      groupBy,
      tagKey,
      currency: displayedCurrency,
      provider: displayedProvider,
    };
    setAnomalyState({ status: "loading", params });
    const controller = new AbortController();

    async function loadAnomalies() {
      try {
        const body = await getAnomalies(
          {
            start: params.start,
            end: params.end,
            groupBy: params.groupBy,
            ...(params.groupBy === "tag" ? { tagKey: params.tagKey } : {}),
            currency: params.currency,
            ...(params.provider ? { provider: params.provider } : {}),
          },
          controller.signal,
        );
        if (controller.signal.aborted) {
          return;
        }
        setAnomalyState({
          status: "ready",
          flags: body.anomalies ?? [],
          params,
        });
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        setAnomalyState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
          params,
        });
      }
    }

    void loadAnomalies();
    return () => controller.abort();
  }, [start, end, groupBy, tagKey, displayedCurrency, displayedProvider]);

  const groupLabel =
    groupBy === "tag" ? `tag ${tagKey}` : groupLabelOf(groupBy);

  // Only use flags fetched for the CURRENT grouping/range (never a stale set).
  const anomalyMatches =
    anomalyState.params.start === start &&
    anomalyState.params.end === end &&
    anomalyState.params.groupBy === groupBy &&
    anomalyState.params.tagKey === tagKey &&
    anomalyState.params.currency === displayedCurrency &&
    anomalyState.params.provider === displayedProvider;
  const anomalyFlags =
    anomalyState.status === "ready" && anomalyMatches ? anomalyState.flags : [];
  const anomalyNotice =
    anomalyState.status === "error" && anomalyMatches
      ? anomalyState.message
      : null;

  // Derive staleness SYNCHRONOUSLY during render: when the held "ready" data was
  // fetched for different params than the current props (a grouping switch or a
  // range change), show the loading state THIS frame instead of rendering the new
  // heading beside the stale chart. The effect's own setState({status:"loading"})
  // runs only AFTER this mismatch frame would otherwise commit, so deriving it
  // here — not via effect timing — is what eliminates the [new heading + old
  // chart] frame.
  const view: CostsState =
    (state.status === "ready" || state.status === "error") &&
    (state.params.start !== start ||
      state.params.end !== end ||
      state.params.groupBy !== groupBy ||
      state.params.tagKey !== tagKey ||
      state.params.currency !== currency ||
      state.params.provider !== provider)
      ? { status: "loading" }
      : state;

  return (
    <section aria-labelledby="costs-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Cost overview</p>
          <h2 id="costs-title">Daily cost by {groupLabel}</h2>
        </div>
        <div
          className="cost-group-control"
          role="group"
          aria-label="Group costs by"
        >
          <span>Group by</span>
          {GROUP_BY_OPTIONS.filter(
            (option) =>
              option.id !== "tag" ||
              (view.status === "ready" && view.costs.tagKeys.length > 0),
          ).map((option) => (
            <button
              key={option.id}
              type="button"
              aria-pressed={groupBy === option.id}
              onClick={() => {
                if (option.id === "tag" && view.status === "ready") {
                  setTagKey(
                    view.costs.tagKeys.includes(tagKey)
                      ? tagKey
                      : view.costs.tagKeys[0],
                  );
                }
                setGroupBy(option.id);
              }}
            >
              {option.label}
            </button>
          ))}
        </div>
        {groupBy === "tag" &&
          view.status === "ready" &&
          view.costs.tagKeys.length > 0 && (
            <label className="cost-group-control">
              <span>Tag key</span>
              <select
                aria-label="Tag key"
                value={tagKey}
                onChange={(event) => setTagKey(event.target.value)}
              >
                {view.costs.tagKeys.map((key) => (
                  <option key={key} value={key}>
                    {key}
                  </option>
                ))}
              </select>
            </label>
          )}
        {view.status === "ready" && view.costs.currencies.length > 1 && (
          <div
            className="cost-group-control"
            role="group"
            aria-label="Currency"
          >
            <span>Currency</span>
            {view.costs.currencies.map((code) => (
              <button
                key={code}
                type="button"
                aria-pressed={(currency || view.costs.currency) === code}
                onClick={() => setCurrency(code)}
              >
                {code}
              </button>
            ))}
          </div>
        )}
        {view.status === "ready" && view.costs.providers.length > 1 && (
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
            {view.costs.providers.map((name) => (
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
          view.status === "loading"
            ? "Loading daily costs…"
            : view.status === "ready"
              ? "Daily costs loaded"
              : ""
        }
      />
      {view.status === "loading" && <LoadingSkeleton />}
      {view.status === "error" && (
        <ErrorState onRetry={() => setRetryToken((t) => t + 1)}>
          Failed to load daily costs: {view.message}
        </ErrorState>
      )}
      {view.status === "ready" &&
        (view.costs.days.length === 0 ? (
          <EmptyState />
        ) : (
          <>
            {anomalyNotice && (
              <p className="viz-anomaly-notice" role="status">
                Anomaly overlay unavailable: {anomalyNotice}
              </p>
            )}
            <Chart
              costs={view.costs}
              groupBy={groupBy}
              tagKey={tagKey}
              anomalies={anomalyFlags}
            />
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
        <p className="state-title">No cost data yet</p>
        <p className="state-message">
          Ingest an AWS FOCUS export, then reload this page:
        </p>
        <pre>
          <code>
            costroid ingest --connector aws-focus --path &lt;export.csv.gz&gt;
          </code>
        </pre>
        <p>
          Stop <code>costroid serve</code> while ingesting; the embedded store
          allows a single process at a time.
        </p>
      </div>
    </div>
  );
}

function Chart({
  costs,
  groupBy,
  tagKey,
  anomalies,
}: {
  costs: DailyCosts;
  groupBy: CostGroupBy;
  tagKey: string;
  anomalies: Anomaly[];
}) {
  const [activeDay, setActiveDay] = useState<number | null>(null);
  // WCAG 1.4.13: Escape dismisses the tooltip whether it was opened by hover
  // or by keyboard focus, so listen at the document while it is showing.
  useEffect(() => {
    if (activeDay === null) {
      return;
    }
    const onKeyDown = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setActiveDay(null);
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => document.removeEventListener("keydown", onKeyDown);
  }, [activeDay]);
  const groupLabel =
    groupBy === "tag" ? `tag ${tagKey}` : groupLabelOf(groupBy);
  const groups = [
    ...new Set(costs.days.flatMap((d) => d.services.map((s) => s.key))),
  ].sort();

  // Flags keyed by calendar day (a day can carry both a total flag and one or
  // more key flags). The API dates match the day.date strings verbatim.
  const flagsByDate = new Map<string, Anomaly[]>();
  for (const flag of anomalies) {
    flagsByDate.set(flag.date, [...(flagsByDate.get(flag.date) ?? []), flag]);
  }

  // The stacked bars render positive costs only: FOCUS Credit/Adjustment
  // rows can be negative, and a diverging below-baseline geometry is a
  // later slice. The y-scale therefore spans each day's positive-segment
  // sum — the rendered stack height. Net day totals (which can be lower)
  // appear only as the cap labels and the grand total, labeled as net.
  const positiveServices = (day: DailyCosts["days"][number]) =>
    day.services.filter((s) => Number(s.cost) > 0);
  const dayPositiveSums = costs.days.map((d) =>
    positiveServices(d).reduce((sum, s) => sum + Number(s.cost), 0),
  );
  const ticks = yTicks(Math.max(...dayPositiveSums));
  const top = ticks[ticks.length - 1].value || 1;

  const plotWidth = WIDTH - MARGIN.left - MARGIN.right;
  const plotHeight = HEIGHT - MARGIN.top - MARGIN.bottom;
  const baseline = MARGIN.top + plotHeight;
  const band = plotWidth / costs.days.length;
  const barWidth = Math.min(MAX_BAR_WIDTH, band * 0.6);
  const yOf = (value: number) => baseline - (value / top) * plotHeight;

  // Long ranges: label every k-th day so at most ~12 date labels render.
  const labelEvery = Math.max(1, Math.ceil(costs.days.length / 12));

  // Cap labels render at display precision (exact net total in the SVG
  // title); positions are sized from the SAME formatted strings so the
  // edge clamp + collision thinning match what is drawn.
  const capLabels = costs.days.map((d) => formatMoney(d.total));
  const capPositions = capLabelPositions(capLabels);

  const tooltipDay = activeDay === null ? null : costs.days[activeDay];
  const tooltipLeft =
    activeDay === null
      ? 50
      : ((MARGIN.left + activeDay * band + band / 2) / WIDTH) * 100;

  return (
    <div>
      <div className="stat-grid">
        <StatCard
          label="Period total (net)"
          value={<Money value={costs.total} currency={costs.currency} />}
          subtitle={costs.currency}
        />
      </div>
      <div className="viz-panel">
        <div className="chart-wrapper">
          {/* role="group", not "img": an img would declare its focusable,
              labeled hit-target rects presentational. */}
          <svg
            viewBox={`0 0 ${WIDTH} ${HEIGHT}`}
            role="group"
            aria-label={`Stacked daily cost by ${groupLabel}`}
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
                  aria-hidden="true"
                  textAnchor="end"
                >
                  {tick.label}
                </text>
              </g>
            ))}
            {costs.days.map((day, i) => {
              const x = MARGIN.left + i * band + (band - barWidth) / 2;
              const positive = positiveServices(day);
              let cursor = baseline;
              return (
                <g key={day.date} className="viz-day">
                  {positive.map((svc, j) => {
                    const height = (Number(svc.cost) / top) * plotHeight;
                    const isTop = j === positive.length - 1;
                    const segmentBottom = cursor;
                    cursor -= height;
                    const gap = isTop ? 0 : SEGMENT_GAP;
                    const drawnHeight = Math.max(height - gap, 1);
                    // When height - gap drops below the 1px floor, drawnHeight is
                    // clamped UP to 1; anchor that sliver to its bin bottom so its
                    // bottom edge stays at segmentBottom (never protruding below the
                    // zero baseline for the bottom segment). The unclamped branch
                    // keeps its exact prior expression, so its output — which can
                    // differ from segmentBottom - drawnHeight in the last float ulp —
                    // stays byte-identical.
                    const y =
                      height - gap < 1
                        ? segmentBottom - drawnHeight
                        : segmentBottom - height + gap;
                    return (
                      <path
                        key={svc.key}
                        className="viz-segment"
                        d={segmentPath(x, y, barWidth, drawnHeight, isTop)}
                        fill={serviceColor(svc.key)}
                      >
                        <title>{`${svc.key}: ${svc.cost} ${costs.currency} (${day.date})`}</title>
                      </path>
                    );
                  })}
                  {capPositions[i] !== null && (
                    <text
                      x={capPositions[i]!}
                      y={cursor - 6}
                      className="viz-cap"
                      textAnchor="middle"
                    >
                      <title>{`Net day total: ${day.total} ${costs.currency}`}</title>
                      {capLabels[i]}
                    </text>
                  )}
                  {flagsByDate.has(day.date) && (
                    <AnomalyMarker
                      cx={x + barWidth / 2}
                      capTop={cursor}
                      barWidth={barWidth}
                      flags={flagsByDate.get(day.date)!}
                    />
                  )}
                  {i % labelEvery === 0 && (
                    <text
                      x={x + barWidth / 2}
                      y={baseline + 16}
                      className="viz-tick"
                      aria-hidden="true"
                      textAnchor="middle"
                    >
                      {day.date.slice(5)}
                    </text>
                  )}
                  <rect
                    className="viz-hit-target"
                    x={MARGIN.left + i * band}
                    y={MARGIN.top}
                    width={band}
                    height={plotHeight}
                    tabIndex={0}
                    aria-label={`${day.date} cost details`}
                    aria-describedby={
                      activeDay === i ? "costs-tooltip" : undefined
                    }
                    onPointerEnter={() => setActiveDay(i)}
                    onPointerLeave={() => setActiveDay(null)}
                    onFocus={() => setActiveDay(i)}
                    onBlur={() => setActiveDay(null)}
                  />
                </g>
              );
            })}
          </svg>
          {tooltipDay && (
            <div
              className="chart-tooltip"
              id="costs-tooltip"
              role="tooltip"
              style={{ left: `${tooltipLeft}%`, top: "52%" }}
            >
              <strong>{tooltipDay.date}</strong>
              {tooltipDay.services.map((service) => (
                <span className="chart-tooltip-row" key={service.key}>
                  <span>{service.key}</span>
                  <span>
                    {formatMoney(service.cost)} {costs.currency}
                  </span>
                </span>
              ))}
              <span className="chart-tooltip-row">
                <span>Total (net)</span>
                <span>
                  {formatMoney(tooltipDay.total)} {costs.currency}
                </span>
              </span>
            </div>
          )}
        </div>
        <ul className="viz-legend">
          {groups.map((name) => (
            <li key={name}>
              <span
                className="viz-swatch"
                style={{ background: serviceColor(name) }}
              />
              {name}
            </li>
          ))}
        </ul>
      </div>
      <div className="viz-table-actions">
        <button
          type="button"
          onClick={() =>
            downloadCsv(
              dailyCostsCsvFilename(costs, groupBy, tagKey),
              dailyCostsToCsv(costs),
            )
          }
        >
          Download CSV
        </button>
      </div>
      <details className="viz-table">
        <summary>View as table</summary>
        <table>
          <thead>
            <tr>
              <th scope="col">Date</th>
              {groups.map((name) => (
                <th scope="col" key={name}>
                  {name}
                </th>
              ))}
              <th scope="col">Total (net)</th>
            </tr>
          </thead>
          <tbody>
            {costs.days.map((day) => (
              <tr key={day.date}>
                <th scope="row">
                  {day.date}
                  {flagsByDate.get(day.date)?.map((flag) => (
                    <span
                      key={`${flag.scope}:${flag.key ?? ""}`}
                      className={`viz-anomaly-badge viz-anomaly-${flag.direction}`}
                      data-date={day.date}
                    >
                      {anomalyLabel(flag)} {flag.direction}
                    </span>
                  ))}
                </th>
                {groups.map((name) => (
                  <td key={name}>
                    <Money
                      value={day.services.find((s) => s.key === name)?.cost}
                      currency={costs.currency}
                    />
                  </td>
                ))}
                <td>
                  <Money value={day.total} currency={costs.currency} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </details>
    </div>
  );
}

// anomalyLabel names the scope of one flag for the tooltip and the table badge:
// "total" for the summed series, or the grouping key for a key flag.
function anomalyLabel(flag: Anomaly): string {
  return flag.scope === "total" ? "total" : (flag.key ?? "key");
}

// markerDirection is the day's overall direction — the total flag's if present,
// otherwise the first flag's — used for the glyph orientation and coloring.
function markerDirection(flags: Anomaly[]): "increase" | "decrease" {
  const chosen = flags.find((f) => f.scope === "total") ?? flags[0];
  return chosen.direction === "decrease" ? "decrease" : "increase";
}

// AnomalyMarker draws a small direction-aware glyph for one flagged day (an
// up-triangle for a spike, a down-triangle for a dip), carrying a data-date and a
// verbatim <title> of the API's decimal statistics. capTop is the top y of the
// day's positive stack. Placement is deterministic and never collides with the
// net-total cap label: above the cap when there is room, otherwise just to the
// right of the cap for a near-full-height bar (both stay inside the viewport).
function AnomalyMarker({
  cx,
  capTop,
  barWidth,
  flags,
}: {
  cx: number;
  capTop: number;
  barWidth: number;
  flags: Anomaly[];
}) {
  const direction = markerDirection(flags);
  const roomAbove = capTop >= 30;
  const gx = roomAbove
    ? cx
    : Math.max(MARGIN.left + 5, Math.min(WIDTH - MARGIN.right - 5, cx));
  // Near-full-height markers sit just inside the bar, away from the cap label;
  // clamping x keeps the final day's triangle inside the viewBox.
  const gy = roomAbove
    ? capTop - 22
    : capTop + Math.min(9, Math.max(5, barWidth / 2));
  const glyph =
    direction === "decrease"
      ? `M${gx},${gy + 5} L${gx - 4},${gy - 3} L${gx + 4},${gy - 3} Z`
      : `M${gx},${gy - 5} L${gx - 4},${gy + 3} L${gx + 4},${gy + 3} Z`;
  const title = flags
    .map(
      (f) =>
        `${anomalyLabel(f)} ${f.direction}: observed ${f.observed}, median ${f.median}, threshold ${f.threshold}`,
    )
    .join("\n");
  return (
    <path
      className={`viz-anomaly viz-anomaly-${direction}`}
      data-date={flags[0].date}
      data-direction={direction}
      d={glyph}
    >
      <title>{title}</title>
    </path>
  );
}
