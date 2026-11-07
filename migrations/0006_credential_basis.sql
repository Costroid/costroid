-- SubAccountId is a sender's claim. A row reads exact only when its credential
-- is a gateway key the minting job attested for the run the row's CI token
-- names. minted_credentials holds those attestations. Each one arrived under a
-- CI token whose job ran in the gateway's environment, and named that token's
-- repository, run and attempt.

CREATE TABLE minted_credentials (
    tenant_id       uuid NOT NULL REFERENCES tenants (id),
    key_sha256      text NOT NULL CHECK (key_sha256 ~ '^[0-9a-f]{64}$'),
    repo            text NOT NULL,
    workflow_run_id text NOT NULL,
    run_attempt     text NOT NULL,
    environment     text NOT NULL,
    attested_at     timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key_sha256)
);

ALTER TABLE minted_credentials ENABLE ROW LEVEL SECURITY;
ALTER TABLE minted_credentials FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON minted_credentials
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON minted_credentials TO costroid_app;

-- credential_basis says whether a stored row's credential was minted for its
-- run or only stated, and is empty when the row names none. The rule binds
-- every row written from here on. Rows stored before it are not regraded, so
-- the constraint is not validated against them.

ALTER TABLE focus_rows ADD COLUMN credential_basis text
    CONSTRAINT credential_basis_known CHECK (credential_basis IN ('minted', 'stated'));

ALTER TABLE focus_rows ADD CONSTRAINT exact_needs_a_minted_credential
    CHECK (attribution_grade <> 'exact' OR credential_basis IS NOT DISTINCT FROM 'minted') NOT VALID;
