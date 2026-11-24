-- A provider request's cache writes take one split by lifetime, and the first
-- batch that holds them decides it. A row from the telemetry path names its
-- request, so the store finds a stored write of another lifetime by the row id
-- that write carries. A row sent to POST /v1/rows names no request: its row id
-- is a digest of the request and the class, and nothing in it leads back to
-- the request. So a request whose writes were stored from telemetry at one
-- lifetime, then sent as rows at another, was counted twice.
--
-- request_write_ids holds, for each request a stored write row named, the row
-- id its writes take at every lifetime, and the run the row named, empty when
-- it named none. A write row that names no request is left out when its own
-- id is here for its own run. The table holds digests and run ids, never a
-- request id.
--
-- Rows stored before this migration kept their request in no column, so no id
-- is recorded for them, and a later write of theirs is matched as before.
--
-- The table only grows. The application role may read and insert, and holds
-- no grant to update or delete a row.

CREATE TABLE request_write_ids (
    tenant_id       uuid NOT NULL REFERENCES tenants (id),
    row_id          text NOT NULL CHECK (char_length(row_id) = 64),
    workflow_run_id text NOT NULL,
    PRIMARY KEY (tenant_id, row_id)
);

ALTER TABLE request_write_ids ENABLE ROW LEVEL SECURITY;
ALTER TABLE request_write_ids FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON request_write_ids
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON request_write_ids TO costroid_app;
