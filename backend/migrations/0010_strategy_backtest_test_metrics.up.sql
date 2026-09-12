-- Preserve the untouched final-test metrics separately from the
-- selection-validation metrics used to choose the parameter set.
ALTER TABLE strategy_params
    ADD COLUMN test_metrics JSONB;

-- This upgrade is specifically a research/backtest release. Keep the live
-- order path disarmed across the update; the user may explicitly re-enable
-- it later from the dashboard after reviewing the untouched test result.
ALTER TABLE settings
    ALTER COLUMN autotrade_enabled SET DEFAULT false;
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now();
