-- The override record is of a rule at one base branch of a repository. A
-- branch's costroid.yml sets its own limits and its own policy.auto_demote,
-- so a failure on a pull request into one branch must neither count toward
-- another branch's rate nor re-arm its rule.
--
-- failing_verdicts gains the base branch the host stated for the pull
-- request, and its window index reads it. Adding a NOT NULL column with no
-- default is safe here: migration 0009 made the table and no release wrote to
-- it, so it holds no row. A row there would stop this migration, and nothing
-- would be guessed for it.
--
-- rule_demotions holds where each rule's demotion stands at a base branch, as
-- serve's verdicts moved it: the limit the window counts failures under, when
-- the window last started empty, whether policy.auto_demote was false, and,
-- once a verdict's record crossed the threshold, when and on what counts.
--
-- A row is a whole state. The latest re-arm stands, and beside it the row of
-- the verdict that started its term, when one did. One row per re-arm and per
-- term: a second verdict that would start the same term writes nothing, so the
-- term's start is recorded once and the state does not flap.
--
-- The table only grows. The application role may read and insert, and holds no
-- grant to update, delete or truncate a row. A term row must carry a record
-- that demotes: at least 10 failing pull requests, approvals from at least 2
-- accounts, and more than one in five of them approved.
--
-- A base branch is a name of letters, digits and ._/+- that starts with a
-- letter or a digit, at most 255 characters, with no "..", as the service
-- asks the host about one.

ALTER TABLE failing_verdicts ADD COLUMN base_branch text NOT NULL
    CHECK (base_branch ~ '^[A-Za-z0-9][A-Za-z0-9._/+-]{0,254}$' AND position('..' IN base_branch) = 0);

DROP INDEX failing_verdicts_window;
CREATE INDEX failing_verdicts_window ON failing_verdicts (tenant_id, repo, base_branch, rule, rule_limit, failed_at)
    INCLUDE (pr, head_sha);

CREATE TABLE rule_demotions (
    tenant_id   uuid NOT NULL REFERENCES tenants (id),
    repo        text NOT NULL CHECK (repo ~ '^[a-z0-9.-]+(/[a-z0-9._~%-]+){1,8}$'),
    base_branch text NOT NULL CHECK (base_branch ~ '^[A-Za-z0-9][A-Za-z0-9._/+-]{0,254}$' AND position('..' IN base_branch) = 0),
    rule        text NOT NULL CHECK (rule ~ '^[a-z][a-z0-9_]{0,62}$'),
    rule_limit  text NOT NULL CHECK (rule_limit ~ '^(0|[1-9][0-9]{0,37})(\.[0-9]{0,37}[1-9])?$'),
    rearmed_at  timestamptz NOT NULL,
    off         boolean NOT NULL,
    demoted_at  timestamptz,
    failing     integer,
    approved    integer,
    approvers   integer,
    CONSTRAINT a_term_has_its_record CHECK (
        (demoted_at IS NULL AND failing IS NULL AND approved IS NULL AND approvers IS NULL)
        OR (demoted_at IS NOT NULL AND NOT off AND demoted_at >= rearmed_at
            AND failing BETWEEN 10 AND 20 AND approved <= failing AND approved * 5 > failing AND approvers >= 2)
    )
);

CREATE UNIQUE INDEX rule_demotions_once ON rule_demotions (tenant_id, repo, base_branch, rule, rearmed_at, (demoted_at IS NULL));

ALTER TABLE rule_demotions ENABLE ROW LEVEL SECURITY;
ALTER TABLE rule_demotions FORCE ROW LEVEL SECURITY;
CREATE POLICY tenant_isolation ON rule_demotions
    USING (tenant_id = costroid_tenant())
    WITH CHECK (tenant_id = costroid_tenant());

GRANT SELECT, INSERT ON rule_demotions TO costroid_app;
