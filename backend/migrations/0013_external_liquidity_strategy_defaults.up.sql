-- The external-liquidity strategy changes both the meaning of swing_lookback
-- (completed H4 bars) and the fixed signal-quality gates. Do not carry an old
-- optimized parameter row or its headline metrics into the new live rules.
-- Historical reports remain available as comparison samples; their missing
-- strategy_version marks them as stale in the dashboard.
UPDATE strategy_params
SET params = '{
        "swing_lookback": 6,
        "min_fvg_size_pct": 0.1,
        "max_bars_for_sweep": 4,
        "stop_buffer_pct": 0.1,
        "risk_reward_ratio": 1.5,
        "min_htf_sweep_atr": 0.05,
        "min_htf_reclaim_atr": 0.1,
        "min_displacement_body_pct": 0.6,
        "max_opposing_wick_pct": 0.25,
        "min_displacement_atr": 0.8,
        "min_displacement_volume": 1.2,
        "displacement_atr_lookback": 14,
        "displacement_vol_lookback": 20,
        "choch_lookback": 5,
        "max_bars_for_retest": 6,
        "order_block_lookback": 10,
        "require_rejection": true,
        "estimated_round_trip_cost_pct": 0.14,
        "min_target_cost_multiple": 5
    }'::jsonb,
    train_metrics = NULL,
    validation_metrics = NULL,
    test_metrics = NULL,
    optimized_at = NULL,
    updated_at = now()
WHERE id = 1;

-- A strategy migration is always fail-closed for real order execution.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now()
WHERE id = 1;
