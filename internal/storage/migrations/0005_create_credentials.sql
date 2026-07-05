-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Encrypted credential store (decisions D17, D32, slice 5): one row per
-- named credential slot. Secrets are AES-256-GCM ciphertext with a fresh
-- random 96-bit nonce per write and the credential NAME bound as the GCM
-- additional authenticated data, so a ciphertext moved to another name
-- fails to decrypt. The 256-bit key lives in a separate key file OUTSIDE
-- the data directory (so a database backup alone exposes nothing); this
-- table holds only the nonce and ciphertext, never the key or any
-- plaintext. Names and timestamps are the only non-secret metadata.
CREATE TABLE credentials (
    name       TEXT PRIMARY KEY,
    nonce      BLOB NOT NULL,
    ciphertext BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL,
    updated_at TIMESTAMP NOT NULL
);
