---
title: Introduction
description: Meet Costroid, the self-hostable, FOCUS-native cost platform.
---

<!-- SPDX-License-Identifier: Apache-2.0 -->
<!-- Copyright 2026 The Costroid Authors -->

Costroid is an open-source, self-hostable, [FOCUS](https://focus.finops.org/)-native cost platform. It ingests cost and usage data from cloud providers, SaaS sources, AI/LLM vendors, and generic FOCUS or CSV exports. Costroid normalizes that data into one FOCUS-conformant model for allocation, unit economics, anomaly detection, and a dashboard, entirely on your own infrastructure.

**FOCUS** is the FinOps Open Cost and Usage Specification, an open standard from the FinOps Foundation for representing cloud, SaaS, and AI cost and usage in one schema.

## Why Costroid

- **One schema for everything.** Cloud, SaaS, and AI/token spend are normalized to FOCUS.
- **Self-hostable and data-sovereign.** Costroid runs on your infrastructure with no mandatory external calls.
- **Open.** The source is transparent and auditable, with no vendor lock-in.

## Architecture

Costroid is a Go backend with a TypeScript/React dashboard embedded in the same self-contained binary.

```text
Sources ──▶ Ingestion ──▶ FOCUS engine ──▶ Storage ──▶ API ──▶ Dashboard (web)
(cloud/     (per-source   (normalize +     (DuckDB       │
 SaaS/AI)    connectors)   validate)        default)      │
                                                          └──▶ allocation · unit economics · anomaly detection
```

- **Backend:** Go handles ingestion, FOCUS normalization and validation, storage, analytics, and the API.
- **Dashboard:** the embedded React application consumes that API; it has no separate source of truth.
- **Storage:** DuckDB and Parquet provide the zero-ops embedded default.

## Next steps

Follow [Getting started](/getting-started/) to run the demo, verify a release, ingest your first export, or build from source. For a network deployment, read [Security & deployment](/security/) before changing the default bind or authentication mode.

Costroid is licensed under the [Apache License 2.0](https://github.com/Costroid/costroid/blob/main/LICENSE).
