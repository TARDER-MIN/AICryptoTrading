-- Holds the currently-active Silver Bullet strategy parameters (replaces
-- the old hardcoded MA-cross rule) plus the backtest metrics that justified
-- them, so the dashboard can show what's live and why. Single-row table,
-- same pattern as settings.
CREATE TABLE strategy_params (
    id                 INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    params             JSONB NOT NULL,
    train_metrics      JSONB,
    validation_metrics JSONB,
    optimized_at       TIMESTAMPTZ,
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);

INSERT INTO strategy_params (id, params) VALUES (1, '{
    "swing_lookback": 20,
    "min_fvg_size_pct": 0.1,
    "max_bars_for_sweep": 3,
    "stop_buffer_pct": 0.1,
    "risk_reward_ratio": 1.5,
    "sessions": [
        {"start_hour": 10, "start_minute": 0, "end_hour": 11, "end_minute": 0},
        {"start_hour": 14, "start_minute": 0, "end_hour": 15, "end_minute": 0}
    ]
}'::jsonb);
