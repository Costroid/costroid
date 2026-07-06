-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Cost-orphaned AI usage metrics: the count/categorical quantities the AI-vendor
-- connectors already fetch but that carry no money on the vendor's cost report,
-- so they are never fabricated into FOCUS cost_records (Anthropic priority/flex-
-- tier tokens and web-search request counts; standard/batch usage keys no cost
-- row referenced; OpenAI recognized-but-unpriced line items). This table lives
-- OUTSIDE cost_records and feeds only the usage-metrics query surface — it never
-- contributes to any BilledCost or token total (DailyCostsByService and
-- DailyTokensByService read FROM cost_records).
--
-- Cardinal Rule (decision D7): every column is count or categorical metadata —
-- tenant, connector identity, the UTC usage day, service/model name, service
-- tier, a metric name, a unit, and an exact quantity. There is NO prompt or
-- response content and NO content-derived field. metric_name and unit are a
-- frozen Costroid vocabulary; line-item / model names are opaque billing
-- descriptors, never parsed for content.
--
-- Frozen metric vocabulary (Costroid convention — rules USG-n):
--   USG-1  token orphans (Anthropic priority/flex-tier tokens; standard/batch
--          usage keys no cost row referenced)  unit "Tokens",   metric_name = the token_type
--   USG-2  Anthropic web-search request counts  unit "Requests", metric_name "web_search_requests"
--   USG-3  OpenAI recognized-but-unpriced rows  unit "Unknown",  metric_name = the line_item verbatim
--          (a deliberate non-assertion: a unit is never guessed as "Tokens")
--
-- service_name and service_tier are NOT NULL and the writer binds "" for a
-- vendor with no tier concept (OpenAI) — NEVER SQL NULL. This is a deliberate DB
-- guard: DailyUsageMetrics scans service_tier as a plain Go string and GROUPs BY
-- it, so a stored NULL would become a NULL group and error at scan
-- ("converting NULL to string is unsupported") on exactly the tier-less rows.
--
-- quantity is DECIMAL(38,18) — the same scale as cost_records (decision D25) —
-- so a >2^53 token count stays exact (never float64); the writer binds it
-- through a scale-bound CAST so a value is never silently rounded.
--
-- The pair (connector, source_identity) is the per-month replace key: re-syncing
-- a corrected month DELETEs its prior usage rows and re-INSERTs, so a restatement
-- wholly supersedes (decision D26a) and an unchanged re-sync is idempotent.
CREATE TABLE usage_metrics (
    x_tenant_id         VARCHAR NOT NULL,
    connector           VARCHAR NOT NULL,
    source_identity     VARCHAR NOT NULL,
    charge_period_start TIMESTAMP NOT NULL,
    service_name        VARCHAR NOT NULL,
    service_tier        VARCHAR NOT NULL,
    metric_name         VARCHAR NOT NULL,
    unit                VARCHAR NOT NULL,
    quantity            DECIMAL(38, 18) NOT NULL
);
