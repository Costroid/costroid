// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

export type Range = { start: string; end: string };

export function rangeQuery(start: string, end: string): string {
  const params = new URLSearchParams();
  if (start !== "") {
    params.append("start", start);
  }
  if (end !== "") {
    params.append("end", end);
  }
  const query = params.toString();
  return query === "" ? "" : `?${query}`;
}
