-- SPDX-License-Identifier: Apache-2.0
-- Copyright 2026 The Costroid Authors

-- Widen all money/quantity columns from DECIMAL(38,12) to DECIMAL(38,18)
-- (decision D25): FOCUS permits providers to emit decimal64 and wider
-- values, and at scale 12 a single conformant 16-fractional-digit row
-- aborted an entire export. Scale 18 covers observed provider precision
-- while keeping 20 integer digits; values exceeding scale 18 are still
-- rejected at ingest, never rounded. Existing values have at most 12
-- fractional digits, so this widening is lossless.
ALTER TABLE cost_records ALTER COLUMN billed_cost SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN effective_cost SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN list_cost SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN contracted_cost SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN pricing_quantity SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN list_unit_price SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN contracted_unit_price SET DATA TYPE DECIMAL(38, 18);
ALTER TABLE cost_records ALTER COLUMN consumed_quantity SET DATA TYPE DECIMAL(38, 18);
