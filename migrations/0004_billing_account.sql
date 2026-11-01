-- BillingAccountId as the sender stated it. It is a claim, and nothing resolves
-- a tenant from it. Every row stored before this column reads unknown, because
-- the service wrote that value whatever the sender stated, so the default is
-- also the true value of those rows.

ALTER TABLE focus_rows ADD COLUMN billing_account_id text NOT NULL DEFAULT 'unknown'
    CONSTRAINT billing_account_id_length CHECK (char_length(billing_account_id) <= 512);
