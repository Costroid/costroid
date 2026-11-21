-- The override record: the failing verdicts serve answered on a pull request,
-- one row per rule that failed, and the approvals serve verified on one. A
-- rule's override rate is read from the two together.
--
-- Both tables only grow. The application role may read and insert, and holds
-- no grant to update, delete or truncate a row, so a record cannot be edited
-- into a rate it never had.
--
-- A failing verdict keeps the limit the rule failed under, because the record
-- is of that limit. The limit is a decimal string in its shortest form, with
-- no trailing zero, so 5 and 5.00 are one limit and are compared as text, which
-- row level security lets an index answer.

CREATE TABLE failing_verdicts (
    tenant_id  uuid NOT NULL REFERENCES tenants (id),
    repo       text NOT NULL CHECK (repo ~ '^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$'),
    pr         text NOT NULL CHECK (pr ~ '^[1-9][0-9]{0,19}$'),
    head_sha   text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    rule       text NOT NULL CHECK (rule ~ '^[a-z][a-z0-9_]{0,62}$'),
    rule_limit text NOT NULL CHECK (rule_limit ~ '^(0|[1-9][0-9]{0,37})(\.[0-9]{0,37}[1-9])?$'),
    failed_at  timestamptz NOT NULL
);

CREATE TABLE pull_request_approvals (
    tenant_id   uuid NOT NULL REFERENCES tenants (id),
    repo        text NOT NULL CHECK (repo ~ '^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$'),
    pr          text NOT NULL CHECK (pr ~ '^[1-9][0-9]{0,19}$'),
    head_sha    text NOT NULL CHECK (head_sha ~ '^[0-9a-f]{40}([0-9a-f]{24})?$'),
    approver    text NOT NULL CHECK (approver ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
    approved_at timestamptz NOT NULL
);

-- The query reads one repository's failures of one rule under one limit over
-- a span of time, then the approvals of the pull requests it found. The first
-- index carries the pull request and the head, so the window is read from the
-- index alone.
CREATE INDEX failing_verdicts_window ON failing_verdicts (tenant_id, repo, rule, rule_limit, failed_at)
    INCLUDE (pr, head_sha);
CREATE INDEX pull_request_approvals_head ON pull_request_approvals (tenant_id, repo, pr, head_sha);

ALTER TABLE failing_verdicts ENABLE ROW LEVEL SECURITY;
ALTER TABLE failing_verdicts FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON failing_verdicts
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

ALTER TABLE pull_request_approvals ENABLE ROW LEVEL SECURITY;
ALTER TABLE pull_request_approvals FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON pull_request_approvals
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON failing_verdicts, pull_request_approvals TO costroid_app;
