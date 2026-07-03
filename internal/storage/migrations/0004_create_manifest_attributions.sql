-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Manifest-attribution cache (decision D16, slice 4): one row per
-- manifest blob a connector has ever fetched, mapping the blob's listed
-- change-detection tuple (ETag, LastModified, size) to its permanent
-- attribution (billing period, run submission time). Azure Cost
-- Management manifest blobs are immutable once written — a refresh
-- writes a new run folder — so a listed manifest whose tuple matches a
-- cached row needs NO fetch to know which billing period and run it
-- describes. This is what keeps an unchanged re-sync at ZERO Get Blob
-- calls even in CreateNewReport mode, where superseded runs' manifests
-- remain listed forever. Rows for manifests that disappear from the
-- listing (OverwritePreviousReport replaces the run folder) go stale
-- harmlessly; they are never consulted for unlisted blobs.
CREATE TABLE manifest_attributions (
    connector      VARCHAR NOT NULL,
    manifest_key   VARCHAR NOT NULL,
    etag           VARCHAR NOT NULL,
    last_modified  TIMESTAMP NOT NULL,
    size_bytes     BIGINT NOT NULL,
    billing_period VARCHAR NOT NULL,
    submitted_time TIMESTAMP NOT NULL,
    -- The export the manifest belongs to: two different exports
    -- delivering under one prefix would silently replace each other's
    -- data, so discovery errors the affected periods instead.
    export_name    VARCHAR NOT NULL,
    updated_at     TIMESTAMP NOT NULL,
    PRIMARY KEY (connector, manifest_key)
);
