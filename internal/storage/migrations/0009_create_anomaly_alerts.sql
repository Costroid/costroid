-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

CREATE TABLE anomaly_alerts (
    tenant_id    VARCHAR NOT NULL,
    scope        VARCHAR NOT NULL CHECK (scope IN ('total', 'key')),
    series_key   VARCHAR NOT NULL,
    currency     VARCHAR NOT NULL,
    anomaly_date DATE NOT NULL,
    direction    VARCHAR NOT NULL CHECK (direction IN ('increase', 'decrease')),
    alerted_at   TIMESTAMP NOT NULL,
    PRIMARY KEY (tenant_id, scope, series_key, currency, anomaly_date, direction)
);
