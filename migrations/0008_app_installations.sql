-- The GitHub App's installations: the account each belongs to, whether it is
-- suspended, and the repositories it reaches. The tables hold ids and names.
-- They have no column for a token, a key or a secret: an installation token is
-- minted when it is needed and is never stored.
--
-- The deployment owns these tables, as it owns the App, so they have no tenant
-- column. Row security is on and forced like everywhere else, with a policy
-- that says so.

CREATE TABLE app_installations (
    installation_id bigint PRIMARY KEY CHECK (installation_id > 0),
    account         text NOT NULL CHECK (account ~ '^[A-Za-z0-9][A-Za-z0-9-]{0,38}$'),
    suspended       boolean NOT NULL DEFAULT false
);

CREATE TABLE app_installation_repositories (
    installation_id bigint NOT NULL REFERENCES app_installations (installation_id) ON DELETE CASCADE,
    repository_id   bigint NOT NULL CHECK (repository_id > 0),
    full_name       text NOT NULL CHECK (full_name ~ '^[A-Za-z0-9_.-]{1,100}/[A-Za-z0-9_.-]{1,100}$'),
    PRIMARY KEY (installation_id, repository_id)
);

ALTER TABLE app_installations ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_installations FORCE ROW LEVEL SECURITY;
CREATE POLICY deployment_wide ON app_installations
    USING (true)
    WITH CHECK (true);

ALTER TABLE app_installation_repositories ENABLE ROW LEVEL SECURITY;
ALTER TABLE app_installation_repositories FORCE ROW LEVEL SECURITY;
CREATE POLICY deployment_wide ON app_installation_repositories
    USING (true)
    WITH CHECK (true);

GRANT SELECT, INSERT, DELETE ON app_installations TO costroid_app;
GRANT UPDATE (account, suspended) ON app_installations TO costroid_app;
GRANT SELECT, INSERT, DELETE ON app_installation_repositories TO costroid_app;
GRANT UPDATE (full_name) ON app_installation_repositories TO costroid_app;
