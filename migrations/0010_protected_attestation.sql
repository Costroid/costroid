-- A key earns a row exact only when a run that a pull request cannot edit
-- attested it: a run that workflow_run started on a protected branch, whose CI
-- token states ref_protected as the JSON string "true". The service checks the
-- token before it writes here, and writes nothing else here.
--
-- credential_attestations holds those attestations. Each names the run the key
-- was minted for, the head commit GitHub's event named for that run, and the
-- attesting run, whose own token arrived with it. One attesting run and attempt
-- attests one key, and a key is attested once.
--
-- credential_spend holds what the gateway counted on an attested key, as the
-- decimal string a protected run sent once the run it serves had ended. A key
-- holds one figure, and one attesting run and attempt writes one. A figure is
-- never replaced, so the larger of two is never kept.
--
-- Both tables only grow. The application role may read and insert, and holds no
-- grant to update or delete a row.
--
-- minted_credentials and gateway_spend held attestations that a job in the
-- gateway's environment sent, on the strength of that environment's rules,
-- which no token states. The application role loses every grant on them, so no
-- reading of the service rests on one again. Rows graded before this migration
-- keep their grade.

CREATE TABLE credential_attestations (
    tenant_id             uuid NOT NULL REFERENCES tenants (id),
    key_sha256            text NOT NULL CHECK (key_sha256 ~ '^[0-9a-f]{64}$'),
    repo                  text NOT NULL,
    workflow_run_id       text NOT NULL,
    run_attempt           text NOT NULL,
    head_sha              text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    attesting_run_id      text NOT NULL,
    attesting_run_attempt text NOT NULL,
    attesting_ref         text NOT NULL CHECK (attesting_ref LIKE 'refs/heads/%'),
    attested_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key_sha256),
    CONSTRAINT one_attestation_per_attesting_run
        UNIQUE (tenant_id, repo, attesting_run_id, attesting_run_attempt),
    CONSTRAINT attests_another_run CHECK (attesting_run_id <> workflow_run_id)
);

CREATE TABLE credential_spend (
    tenant_id             uuid NOT NULL,
    key_sha256            text NOT NULL,
    spend                 text NOT NULL CHECK (spend ~ '^(0|[1-9][0-9]{0,25})(\.[0-9]{1,12})?$'),
    attesting_repo        text NOT NULL,
    attesting_run_id      text NOT NULL,
    attesting_run_attempt text NOT NULL,
    attested_at           timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key_sha256),
    CONSTRAINT one_spend_per_attesting_run
        UNIQUE (tenant_id, attesting_repo, attesting_run_id, attesting_run_attempt),
    FOREIGN KEY (tenant_id, key_sha256) REFERENCES credential_attestations (tenant_id, key_sha256)
);

ALTER TABLE credential_attestations ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_attestations FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credential_attestations
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

ALTER TABLE credential_spend ENABLE ROW LEVEL SECURITY;
ALTER TABLE credential_spend FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON credential_spend
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON credential_attestations, credential_spend TO costroid_app;

REVOKE ALL ON minted_credentials, gateway_spend FROM costroid_app;
