-- The document is written once and read whole. Nothing queries inside it, so
-- jsonb's binary parse has no reader to pay for, and json keeps each row as it
-- was written. lz4 compresses it for less than pglz. docs/serve.md carries the
-- measurement behind both.

ALTER TABLE focus_rows ALTER COLUMN document TYPE json USING document::json;
ALTER TABLE focus_rows ALTER COLUMN document SET COMPRESSION lz4;
