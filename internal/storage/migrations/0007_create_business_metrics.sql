-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

CREATE TABLE business_metrics (
    x_tenant_id  VARCHAR NOT NULL,
    source_label VARCHAR NOT NULL,
    metric_day   TIMESTAMP NOT NULL,
    metric_name  VARCHAR NOT NULL,
    quantity     DECIMAL(38, 18) NOT NULL
);
