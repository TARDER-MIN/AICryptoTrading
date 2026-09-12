ALTER TABLE strategy_params
    DROP COLUMN test_metrics;

ALTER TABLE settings
    ALTER COLUMN autotrade_enabled SET DEFAULT true;
