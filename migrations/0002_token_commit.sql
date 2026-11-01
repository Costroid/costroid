-- costroid/commit_sha means the head commit, and a token does not always state
-- one: on GitHub's pull_request event its sha is a merge commit. The token's
-- commit gets its own column, and head_basis says where commit_sha came from.
-- A row with head_basis = 'stated' is a sender's claim. An SCM check of stated
-- heads reads those rows.

ALTER TABLE focus_rows ADD COLUMN token_commit_sha text;
ALTER TABLE focus_rows ADD COLUMN head_basis text;

ALTER TABLE focus_rows ADD CONSTRAINT head_basis_known
    CHECK (head_basis IN ('token', 'stated'));
ALTER TABLE focus_rows ADD CONSTRAINT head_from_token_is_the_token_commit
    CHECK (head_basis <> 'token' OR commit_sha = token_commit_sha);
ALTER TABLE focus_rows ADD CONSTRAINT unverified_has_no_token_commit
    CHECK (run_identity = 'token_verified' OR token_commit_sha IS NULL);
