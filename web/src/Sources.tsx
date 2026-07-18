// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";
import { getSyncStatus } from "./api";
import { EmptyIcon } from "./icons";
import { ErrorState, LoadingSkeleton, StatCard, ViewStatus } from "./ViewState";

type SyncStatusResponse = components["schemas"]["SyncStatusResponse"];
type SyncSourceStatus = components["schemas"]["SyncSourceStatus"];

type SourcesState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; data: SyncStatusResponse };

export function formatUTCDateTime(value: string): string {
  const timestamp = new Date(value);
  if (Number.isNaN(timestamp.getTime())) {
    return "—";
  }
  const year = timestamp.getUTCFullYear();
  const month = String(timestamp.getUTCMonth() + 1).padStart(2, "0");
  const day = String(timestamp.getUTCDate()).padStart(2, "0");
  const hour = String(timestamp.getUTCHours()).padStart(2, "0");
  const minute = String(timestamp.getUTCMinutes()).padStart(2, "0");
  return `${year}-${month}-${day} ${hour}:${minute} UTC`;
}

export default function Sources() {
  const [state, setState] = useState<SourcesState>({ status: "loading" });
  const [retryToken, setRetryToken] = useState(0);

  useEffect(() => {
    setState({ status: "loading" });
    const controller = new AbortController();

    async function load() {
      try {
        const data = await getSyncStatus(controller.signal);
        if (controller.signal.aborted) {
          return;
        }
        setState({ status: "ready", data });
      } catch (err) {
        if (controller.signal.aborted) {
          return;
        }
        setState({
          status: "error",
          message: err instanceof Error ? err.message : String(err),
        });
      }
    }

    void load();
    return () => controller.abort();
  }, [retryToken]);

  return (
    <section className="sources" aria-labelledby="sources-title">
      <div className="view-heading">
        <div>
          <p className="view-kicker">Ingestion</p>
          <h2 id="sources-title">Scheduled ingestion</h2>
        </div>
      </div>
      <ViewStatus
        message={
          state.status === "loading"
            ? "Loading scheduled ingestion status…"
            : state.status === "ready"
              ? "Scheduled ingestion status loaded"
              : ""
        }
      />
      {state.status === "loading" && <LoadingSkeleton />}
      {state.status === "error" && (
        <ErrorState onRetry={() => setRetryToken((token) => token + 1)}>
          Failed to load scheduled ingestion status: {state.message}
        </ErrorState>
      )}
      {state.status === "ready" && <SourcesReady data={state.data} />}
    </section>
  );
}

function SourcesReady({ data }: { data: SyncStatusResponse }) {
  const failing = data.sources.filter(
    (source) => source.lastRun?.outcome === "error",
  ).length;

  return (
    <>
      <div className="sources-summary">
        <StatCard label="Scheduler" value={data.enabled ? "On" : "Off"} />
        <StatCard label="Sources" value={data.sources.length} />
        <StatCard label="Failing" value={failing} />
      </div>
      {!data.enabled && (
        <p className="sources-disabled">
          Scheduled ingestion is off for this instance.
        </p>
      )}
      {data.sources.length === 0 ? (
        <SourcesEmpty />
      ) : (
        <SourcesTable sources={data.sources} />
      )}
    </>
  );
}

function SourcesEmpty() {
  return (
    <div className="viz-empty">
      <div className="state-content">
        <EmptyIcon className="state-icon" size={30} />
        <p className="state-title">No sync sources yet</p>
        <p className="state-message">
          Enable scheduling with <code>costroid serve --sync</code> and a{" "}
          <code>sources.json</code> file.
        </p>
      </div>
    </div>
  );
}

function SourcesTable({ sources }: { sources: SyncSourceStatus[] }) {
  return (
    <div className="sources-table">
      <table>
        <thead>
          <tr>
            <th scope="col">Source</th>
            <th scope="col">Connector</th>
            <th scope="col">Last run</th>
            <th scope="col">Outcome</th>
            <th scope="col">Records</th>
            <th scope="col">Next run</th>
          </tr>
        </thead>
        <tbody>
          {sources.map((source) => (
            <SourceRows
              key={`${source.name}\0${source.tenant}`}
              source={source}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function SourceRows({ source }: { source: SyncSourceStatus }) {
  const lastRun = source.lastRun;
  const showTenant = source.tenant !== "" && source.tenant !== "default";

  return (
    <>
      <tr>
        <th scope="row">
          {source.name}
          {showTenant && <span className="source-tenant">{source.tenant}</span>}
        </th>
        <td>{source.connector}</td>
        <td>{lastRun ? formatUTCDateTime(lastRun.finishedAt) : "—"}</td>
        <td>
          {lastRun ? (
            <span className={`outcome-badge outcome-${lastRun.outcome}`}>
              {lastRun.outcome}
            </span>
          ) : (
            "—"
          )}
        </td>
        <td className="sources-records">
          {lastRun ? lastRun.recordsIngested : "—"}
        </td>
        <td>{source.nextRunAt ? formatUTCDateTime(source.nextRunAt) : "—"}</td>
      </tr>
      {lastRun?.outcome === "error" &&
        (lastRun.error || source.lastSuccessAt) && (
          <tr className="source-detail-row">
            <td colSpan={6}>
              {lastRun.error && (
                <span className="source-error">{lastRun.error}</span>
              )}
              {source.lastSuccessAt && (
                <span>
                  Last success: {formatUTCDateTime(source.lastSuccessAt)}
                </span>
              )}
            </td>
          </tr>
        )}
    </>
  );
}
