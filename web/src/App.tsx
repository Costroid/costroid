// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

import { useEffect, useState } from "react";
import type { components } from "./api/schema";

type Meta = components["schemas"]["Meta"];

type MetaState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; meta: Meta };

export default function App() {
  const [state, setState] = useState<MetaState>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();

    async function load() {
      try {
        const res = await fetch("/api/v1/meta", { signal: controller.signal });
        if (!res.ok) {
          throw new Error(`GET /api/v1/meta returned ${res.status}`);
        }
        const meta = (await res.json()) as Meta;
        setState({ status: "ready", meta });
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
  }, []);

  return (
    <main>
      <h1>Costroid</h1>
      {state.status === "loading" && <p>Loading instance metadata…</p>}
      {state.status === "error" && (
        <p role="alert">Failed to load instance metadata: {state.message}</p>
      )}
      {state.status === "ready" && (
        <dl>
          <dt>Name</dt>
          <dd>{state.meta.name}</dd>
          <dt>Version</dt>
          <dd>{state.meta.version}</dd>
          <dt>FOCUS version</dt>
          <dd>{state.meta.focusVersion}</dd>
        </dl>
      )}
    </main>
  );
}
