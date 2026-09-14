-- The location-first HTF 3+1 model changes signal eligibility, M5 CHOCH
-- semantics and target-path validation. Reset active metrics/parameters so an
-- older optimizer result can never be treated as evidence for this strategy.
-- Historical reports remain stored only as stale same-sample baselines.
UPDATE strategy_params
SET params = '{
        "swing_lookback": 6,
        "min_fvg_size_pct": 0.1,
        "max_bars_for_sweep": 4,
        "stop_buffer_pct": 0.1,
        "risk_reward_ratio": 1.5,
        "htf_structure_lookback": 12,
        "htf_pivot_strength": 2,
        "htf_zone_atr_multiple": 0.75,
        "choch_pivot_strength": 2,
        "target_barrier_buffer_atr": 0.1,
        "min_htf_sweep_atr": 0.05,
        "min_htf_reclaim_atr": 0.1,
        "min_displacement_body_pct": 0.6,
        "max_opposing_wick_pct": 0.25,
        "min_displacement_atr": 0.8,
        "min_displacement_volume": 1.2,
        "displacement_atr_lookback": 14,
        "displacement_vol_lookback": 20,
        "choch_lookback": 12,
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

-- Every strategy migration is fail-closed for real order execution.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now()
WHERE id = 1;
