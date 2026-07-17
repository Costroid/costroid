-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

CREATE TABLE sync_runs (
    source_name       VARCHAR NOT NULL,
    connector         VARCHAR NOT NULL,
    tenant_id         VARCHAR NOT NULL,
    started_at        TIMESTAMP NOT NULL,
    finished_at       TIMESTAMP NOT NULL,
    outcome           VARCHAR NOT NULL CHECK (outcome IN ('success', 'partial', 'error')),
    error             VARCHAR NOT NULL,
    periods_processed BIGINT NOT NULL,
    periods_skipped   BIGINT NOT NULL,
    records_ingested  BIGINT NOT NULL
);
