-- What the gateway counted on a minted key, in USD, as the closing job attested
-- it once the run was over. The figure is the gateway's own count, so it can
-- stand behind a budget where the token counts on the rows cannot. Each figure
-- names a key in minted_credentials, and the closing job's CI token named that
-- key's repository, run and attempt. A key keeps the largest figure attested
-- for it, because a gateway's count only grows.

CREATE TABLE gateway_spend (
    tenant_id   uuid NOT NULL,
    key_sha256  text NOT NULL,
    usd         numeric(38, 12) NOT NULL CHECK (usd >= 0),
    attested_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (tenant_id, key_sha256),
    FOREIGN KEY (tenant_id, key_sha256) REFERENCES minted_credentials (tenant_id, key_sha256)
);

ALTER TABLE gateway_spend ENABLE ROW LEVEL SECURITY;
ALTER TABLE gateway_spend FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON gateway_spend
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON gateway_spend TO costroid_app;
GRANT UPDATE (usd, attested_at) ON gateway_spend TO costroid_app;
