-- Persist the complete latest research report so the final-test breakdown
-- and walk-forward diagnostics survive a browser refresh or restart.
ALTER TABLE strategy_params
    ADD COLUMN last_report JSONB;

-- This release expands research diagnostics only. Keep live order execution
-- explicitly disarmed while the new out-of-sample evidence is reviewed.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now();
