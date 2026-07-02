-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Incremental sync state (decision D16, slice 3): one row per
-- (connector, source_identity) holding the change-detection tuple of the
-- source's partition-level manifest as listed by S3 — key, ETag,
-- LastModified, and size. A period whose listed tuple equals the stored
-- one is skipped without any GetObject call. LastModified is the
-- load-bearing change signal (S3 ETags are not content digests under
-- SSE-KMS or multipart upload); the key, ETag, and size corroborate.
-- The tuple is upserted after EVERY successful sync outcome, so stores
-- predating this migration converge to zero-GET syncs after one pass.
CREATE TABLE sync_state (
    connector              VARCHAR NOT NULL,
    source_identity        VARCHAR NOT NULL,
    manifest_key           VARCHAR NOT NULL,
    manifest_etag          VARCHAR NOT NULL,
    manifest_last_modified TIMESTAMP NOT NULL,
    manifest_size          BIGINT NOT NULL,
    updated_at             TIMESTAMP NOT NULL,
    PRIMARY KEY (connector, source_identity)
);
