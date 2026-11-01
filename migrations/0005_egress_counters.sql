-- The egress report: one counter per UTC day, host and outcome. It has no column
-- for a path, a query, a header or a body.
--
-- The deployment owns this table, so it has no tenant column. Row security is on
-- and forced like everywhere else, with a policy that says so.

CREATE TABLE egress_counters (
    day     date NOT NULL,
    host    text NOT NULL CHECK (char_length(host) BETWEEN 1 AND 253),
    outcome text NOT NULL CHECK (outcome IN ('completed', 'failed', 'refused')),
    count   bigint NOT NULL CHECK (count > 0),
    PRIMARY KEY (day, host, outcome)
);

ALTER TABLE egress_counters ENABLE ROW LEVEL SECURITY;
ALTER TABLE egress_counters FORCE ROW LEVEL SECURITY;
CREATE POLICY deployment_wide ON egress_counters
    USING (true)
    WITH CHECK (true);

GRANT SELECT, INSERT, UPDATE ON egress_counters TO costroid_app;
