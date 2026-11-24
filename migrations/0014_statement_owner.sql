-- A row id was kept once per tenant, and the first statement of it stood
-- whole, whoever sent it. A row id is the sender's word, so any sender of the
-- tenant that stated another run's row ids first kept that run's own rows off
-- its pull request, and the run's later statement was a silent duplicate.
--
-- A statement now has an owner. A statement a CI token verified is owned by
-- the token's repository and run. Every statement the ledger did not verify
-- has one owner, shared. A row id is kept once per owner: a run's statement
-- of an id never meets another run's at the unique index, so no sender
-- reaches a run's own row. The attempt is not part of the owner, so a re-run
-- of failed jobs that sends attempt 1's rows under attempt 2's token restates
-- them, as before.
--
-- Every stored row satisfies the new index, because the old key was narrower.
-- No stored row is changed.

CREATE UNIQUE INDEX focus_rows_statement ON focus_rows (
    tenant_id,
    row_id,
    (CASE WHEN run_identity = 'token_verified' THEN coalesce(repo, '') END),
    (CASE WHEN run_identity = 'token_verified' THEN coalesce(workflow_run_id, '') END)
) NULLS NOT DISTINCT;

ALTER TABLE focus_rows DROP CONSTRAINT focus_rows_pkey;

-- request_write_ids recorded the ids a request's cache writes take with the
-- run a record claimed, and a write naming no request was left out when its
-- id was recorded for its own run. Under an ingest key that run is only a
-- claim, so an unverified record kept out a run's own write. An id is now
-- recorded per owner, as a row is: verified is true with the token's
-- repository and run, and false for the shared owner, whose repository and
-- run are empty. request_key is the id the request's five-minute write takes,
-- the same for each of its writes, so a reader can tell two lifetimes of one
-- request's writes from two requests. Ids recorded before this migration hold
-- none of the three, and are matched by run as before.

ALTER TABLE request_write_ids ADD COLUMN verified boolean;
ALTER TABLE request_write_ids ADD COLUMN repo text;
ALTER TABLE request_write_ids ADD COLUMN request_key text
    CONSTRAINT request_key_is_a_row_id CHECK (char_length(request_key) = 64);
ALTER TABLE request_write_ids ADD CONSTRAINT an_owner_is_whole
    CHECK ((verified IS NULL AND repo IS NULL AND request_key IS NULL)
        OR (verified IS NOT NULL AND repo IS NOT NULL AND request_key IS NOT NULL));

CREATE UNIQUE INDEX request_write_ids_statement ON request_write_ids (
    tenant_id,
    row_id,
    verified,
    (CASE WHEN verified THEN repo END),
    (CASE WHEN verified THEN workflow_run_id END)
) NULLS NOT DISTINCT;

ALTER TABLE request_write_ids DROP CONSTRAINT request_write_ids_pkey;

CREATE INDEX request_write_ids_request ON request_write_ids (tenant_id, request_key);

-- ledger_rows holds the rows a reader of a figure reads. A statement the ledger
-- verified stands on its own pull request, beside any other run's statement of
-- the same id. An unverified statement stands only where no verified statement
-- holds its id, nor, for a cache write, another lifetime's id of the same
-- request's writes. contested says another run also stated the row's id, and
-- the ledger verified both. outranked says a row the ledger did not verify
-- stated the same charge, by its id or by another lifetime of the request's
-- cache writes, and gave way to this one. An unverified statement that stands
-- is neither: nothing verified holds its charge.
--
-- The view reads as the role that queries it, so row level security holds it
-- to that role's tenant whoever owns it. A pull request's unverified rows are
-- read from an index of their own, so a pull request whose rows a run verified
-- pays nothing for them.

CREATE INDEX focus_rows_unverified_pull_request ON focus_rows (tenant_id, repo, pr)
    WHERE run_identity <> 'token_verified';

CREATE VIEW ledger_rows WITH (security_invoker = true) AS
SELECT f.*,
    (SELECT count(*) FROM focus_rows o
        WHERE o.tenant_id = f.tenant_id AND o.row_id = f.row_id AND o.run_identity = 'token_verified') > 1 AS contested,
    EXISTS (
        SELECT FROM focus_rows u
        WHERE u.tenant_id = f.tenant_id AND u.row_id = f.row_id AND u.run_identity <> 'token_verified')
    OR (f.sku_meter LIKE 'cache\_write\_%' AND EXISTS (
        SELECT FROM request_write_ids w
        JOIN request_write_ids s ON s.tenant_id = w.tenant_id AND s.request_key = w.request_key
        JOIN focus_rows u ON u.tenant_id = s.tenant_id AND u.row_id = s.row_id AND u.run_identity <> 'token_verified'
        WHERE w.tenant_id = f.tenant_id AND w.row_id = f.row_id)) AS outranked
FROM focus_rows f
WHERE f.run_identity = 'token_verified'
UNION ALL
SELECT f.*,
    false AS contested,
    false AS outranked
FROM focus_rows f
WHERE f.run_identity <> 'token_verified'
    AND NOT EXISTS (
        SELECT FROM focus_rows v
        WHERE v.tenant_id = f.tenant_id AND v.row_id = f.row_id AND v.run_identity = 'token_verified')
    AND NOT EXISTS (
        SELECT FROM request_write_ids w
        JOIN request_write_ids s ON s.tenant_id = w.tenant_id AND s.request_key = w.request_key
        JOIN focus_rows v ON v.tenant_id = s.tenant_id AND v.row_id = s.row_id AND v.run_identity = 'token_verified'
        WHERE w.tenant_id = f.tenant_id AND w.row_id = f.row_id);

GRANT SELECT ON ledger_rows TO costroid_app;
