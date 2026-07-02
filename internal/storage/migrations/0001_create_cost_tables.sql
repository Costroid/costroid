-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Ingest batches: one row per (connector, source identity) replace key
-- (see the ingest package for the pinned idempotency semantics).
CREATE TABLE ingest_batches (
    connector       VARCHAR NOT NULL,
    source_identity VARCHAR NOT NULL,
    content_hash    VARCHAR NOT NULL,
    tenant_id       VARCHAR NOT NULL,
    record_count    BIGINT NOT NULL,
    ingested_at     TIMESTAMP NOT NULL,
    PRIMARY KEY (connector, source_identity)
);

-- Normalized FOCUS 1.4 cost records (decisions D3, D4, D15). Column
-- names are the snake_case FOCUS 1.4 column IDs; x_tenant_id is the
-- FOCUS custom column carrying the tenant. Monetary and quantity columns
-- are DECIMAL — never floats — so no precision is lost. Timestamps are
-- stored as UTC.
CREATE TABLE cost_records (
    x_tenant_id           VARCHAR NOT NULL,
    batch_connector       VARCHAR NOT NULL,
    batch_source_identity VARCHAR NOT NULL,

    billing_account_id    VARCHAR NOT NULL,
    billing_account_name  VARCHAR,
    billing_account_type  VARCHAR,
    sub_account_id        VARCHAR,
    sub_account_name      VARCHAR,
    sub_account_type      VARCHAR,

    service_provider_name VARCHAR NOT NULL,
    host_provider_name    VARCHAR,
    invoice_issuer_name   VARCHAR NOT NULL,

    billing_currency      VARCHAR NOT NULL,
    billing_period_start  TIMESTAMP NOT NULL,
    billing_period_end    TIMESTAMP NOT NULL,
    invoice_id            VARCHAR,

    charge_category       VARCHAR NOT NULL,
    charge_class          VARCHAR,
    charge_description    VARCHAR,
    charge_frequency      VARCHAR,
    charge_period_start   TIMESTAMP NOT NULL,
    charge_period_end     TIMESTAMP NOT NULL,

    billed_cost           DECIMAL(38, 12) NOT NULL,
    effective_cost        DECIMAL(38, 12) NOT NULL,
    list_cost             DECIMAL(38, 12) NOT NULL,
    contracted_cost       DECIMAL(38, 12) NOT NULL,

    pricing_category      VARCHAR,
    pricing_quantity      DECIMAL(38, 12),
    pricing_unit          VARCHAR,
    list_unit_price       DECIMAL(38, 12),
    contracted_unit_price DECIMAL(38, 12),

    consumed_quantity     DECIMAL(38, 12),
    consumed_unit         VARCHAR,

    service_name          VARCHAR NOT NULL,
    service_category      VARCHAR NOT NULL,
    service_subcategory   VARCHAR,

    sku_id                VARCHAR,
    sku_price_id          VARCHAR,
    sku_meter             VARCHAR,

    resource_id           VARCHAR,
    resource_name         VARCHAR,
    resource_type         VARCHAR,

    region_id             VARCHAR,
    region_name           VARCHAR,
    availability_zone     VARCHAR,

    -- FOCUS Tags key->value map, serialized as JSON text.
    tags                  VARCHAR
);
