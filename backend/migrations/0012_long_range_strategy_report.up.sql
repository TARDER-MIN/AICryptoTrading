-- Keep the optional one-year proxy study separate from the most recent exact
-- BingX report. The proxy report is research-only and must never overwrite
-- live parameters or the evidence attached to those parameters.
ALTER TABLE strategy_params
    ADD COLUMN last_long_report JSONB;

-- Any research migration is fail-closed for real order execution.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now();
