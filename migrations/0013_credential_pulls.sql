-- A pull request's gateway figure was summed over the keys its rows name. A
-- run whose rows never reached the pull request, left out by the pull
-- request's own workflow or lost by a failed ingest job, took its key's count
-- with it, and a budget could pass on the rest.
--
-- credential_pulls holds the pull requests an attesting run named for a key:
-- the ones GitHub's workflow_run event lists for the run the key was minted
-- for. They are written once, with the attestation, by the same run of a
-- protected branch, so the run's own rows never decide them. A pull request's
-- figure adds up the count of every key attested for it, whether a row of the
-- key arrived or not, and a key attested for it that holds no count yet keeps
-- the figure from standing behind its rows.
--
-- An attestation stored before this migration names no pull request, and its
-- key counts on a pull request through the rows that name it, as before.
--
-- The table only grows. The application role may read and insert, and holds
-- no grant to update or delete a row.

CREATE TABLE credential_pulls (
    tenant_id  uuid NOT NULL,
    key_sha256 text NOT NULL,
    pr         text NOT NULL CHECK (pr ~ '^[1-9][0-9]{0,19}$'),
    PRIMARY KEY (tenant_id, key_sha256, pr),
    FOREIGN KEY (tenant_id, key_sha256) REFERENCES credential_attestations (tenant_id, key_sha256)
);

CREATE INDEX credential_pulls_pull_request ON credential_pulls (tenant_id, pr);

ALTER TABLE credential_pulls ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_pulls FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credential_pulls
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON credential_pulls TO costroid_app;
