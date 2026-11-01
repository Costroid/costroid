-- The ledger. The migration role owns every object here. The role the service
-- runs as, costroid_app, must already exist. It owns nothing and is granted
-- only what the service does.

CREATE FUNCTION costroid_tenant() RETURNS uuid
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('costroid.tenant_id', true), '')::uuid $$;

CREATE FUNCTION costroid_lookup() RETURNS text
    LANGUAGE sql STABLE PARALLEL SAFE
    AS $$ SELECT NULLIF(current_setting('costroid.lookup', true), '') $$;

CREATE TABLE tenants (
    id         uuid PRIMARY KEY,
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

-- An SCM owner, such as github.com:acme, belongs to one tenant.
CREATE TABLE tenant_bindings (
    owner_key  text PRIMARY KEY,
    tenant_id  uuid NOT NULL REFERENCES tenants (id),
    created_at timestamptz NOT NULL DEFAULT now()
);

-- A key authenticates a sender with no CI token. Its rows stay unverified.
CREATE TABLE ingest_keys (
    key_sha256 text PRIMARY KEY CHECK (key_sha256 ~ '^[0-9a-f]{64}$'),
    tenant_id  uuid NOT NULL REFERENCES tenants (id),
    created_at timestamptz NOT NULL DEFAULT now(),
    revoked_at timestamptz
);

CREATE TABLE focus_rows (
    tenant_id           uuid NOT NULL REFERENCES tenants (id),
    row_id              text NOT NULL CHECK (char_length(row_id) = 64),
    billing_currency    text NOT NULL,
    service_category    text NOT NULL,
    sku_meter           text NOT NULL,
    consumed_unit       text NOT NULL,
    consumed_quantity   numeric(38, 12) NOT NULL CHECK (consumed_quantity >= 0),
    list_cost           numeric(38, 12) NOT NULL CHECK (list_cost >= 0),
    effective_cost      numeric(38, 12) NOT NULL CHECK (effective_cost >= 0),
    charge_period_start timestamptz NOT NULL,
    charge_period_end   timestamptz NOT NULL,
    cost_basis          text NOT NULL,
    cost_source         text NOT NULL,
    attribution_grade   text NOT NULL CHECK (attribution_grade IN ('exact', 'windowed', 'unattributed')),
    enforcement_grade   text NOT NULL,
    run_identity        text NOT NULL CHECK (run_identity IN ('token_verified', 'unverified')),
    agent               text,
    model               text,
    repo                text,
    pr                  text,
    commit_sha          text,
    workflow_run_id     text,
    run_attempt         text,
    actor               text,
    event               text,
    catalog_version     text,
    document            jsonb NOT NULL,
    ingested_at         timestamptz NOT NULL DEFAULT now(),
    budget_eligible     boolean GENERATED ALWAYS AS
        (run_identity = 'token_verified' AND attribution_grade <> 'unattributed') STORED,
    PRIMARY KEY (tenant_id, row_id),
    CHECK (charge_period_end > charge_period_start),
    CONSTRAINT unverified_is_unattributed
        CHECK (run_identity = 'token_verified' OR attribution_grade = 'unattributed')
);

CREATE INDEX focus_rows_pull_request ON focus_rows (tenant_id, repo, pr);

-- One counter per field name per day. A value never reaches this table.
CREATE TABLE ingest_drops (
    tenant_id uuid NOT NULL REFERENCES tenants (id),
    day       date NOT NULL,
    field     text NOT NULL,
    count     bigint NOT NULL CHECK (count > 0),
    PRIMARY KEY (tenant_id, day, field)
);

ALTER TABLE tenants ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenants FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenants
    USING (id = costroid_tenant())
    WITH CHECK (id = costroid_tenant());

ALTER TABLE tenant_bindings ENABLE ROW LEVEL SECURITY;
ALTER TABLE tenant_bindings FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON tenant_bindings
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());
-- Resolving a tenant happens before any tenant is set, so a transaction may
-- read the one row it names exactly. Nothing can list the table this way.
CREATE POLICY point_lookup ON tenant_bindings FOR SELECT
    USING (owner_key = costroid_lookup());

ALTER TABLE ingest_keys ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingest_keys FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ingest_keys
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());
CREATE POLICY point_lookup ON ingest_keys FOR SELECT
    USING (key_sha256 = costroid_lookup());

ALTER TABLE focus_rows ENABLE ROW LEVEL SECURITY;
ALTER TABLE focus_rows FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON focus_rows
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

ALTER TABLE ingest_drops ENABLE ROW LEVEL SECURITY;
ALTER TABLE ingest_drops FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON ingest_drops
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT USAGE ON SCHEMA public TO costroid_app;
GRANT EXECUTE ON FUNCTION costroid_tenant(), costroid_lookup() TO costroid_app;
GRANT SELECT ON tenants, tenant_bindings, ingest_keys TO costroid_app;
GRANT SELECT, INSERT ON focus_rows TO costroid_app;
GRANT SELECT, INSERT, UPDATE ON ingest_drops TO costroid_app;
