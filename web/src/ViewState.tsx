// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import type { ReactNode } from "react";
import { WarningIcon } from "./icons";

// The skeleton is purely visual; announcing the load state is the job of each
// view's persistent ViewStatus node (a live region mounted with content is
// unreliably announced, and the skeleton's unmount was never announced at all).
export function LoadingSkeleton() {
  return (
    <div className="skeleton-card" aria-hidden="true">
      <div className="skeleton-line" />
      <div className="skeleton-chart" />
    </div>
  );
}

// ViewStatus is a persistent (always-mounted) polite live region. Swap its
// text between a loading message and a completion message — live regions
// announce additions/changes, NOT removals, so an empty string would make
// the completion silent.
export function ViewStatus({ message }: { message: string }) {
  return (
    <p className="sr-only" role="status">
      {message}
    </p>
  );
}

export function ErrorState({
  children,
  onRetry,
}: {
  children: ReactNode;
  onRetry?: () => void;
}) {
  return (
    <div className="state-card" role="alert" aria-live="assertive">
      <div className="state-content">
        <WarningIcon className="state-icon" size={28} />
        <p className="state-title">Unable to load this view</p>
        <p className="state-message">{children}</p>
        {onRetry && (
          <button type="button" className="state-retry" onClick={onRetry}>
            Retry
          </button>
        )}
      </div>
    </div>
  );
}

export function StatCard({
  label,
  value,
  subtitle,
}: {
  label: string;
  value: ReactNode;
  subtitle?: ReactNode;
}) {
  return (
    <article className="stat-card">
      <p className="stat-label">{label}</p>
      <p className="stat-value">{value}</p>
      {subtitle && <p className="stat-subtitle">{subtitle}</p>}
    </article>
  );
}
