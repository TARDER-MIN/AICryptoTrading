package strategy

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadParams reads the currently-active Silver Bullet parameters from the
// single-row strategy_params table (migration 0005), falling back to
// DefaultSBParams if the row is somehow missing or empty.
func LoadParams(ctx context.Context, pool *pgxpool.Pool) (SBParams, error) {
	var raw []byte
	err := pool.QueryRow(ctx, `SELECT params FROM strategy_params WHERE id = 1`).Scan(&raw)
	if err != nil {
		return DefaultSBParams(), err
	}
	var p SBParams
	if err := json.Unmarshal(raw, &p); err != nil {
		return DefaultSBParams(), err
	}
	backfillICT2026Defaults(&p)
	// Risk/reward is a fixed strategy rule, not a tunable database value.
	p.RiskRewardRatio = 1.5
	return p, nil
}

// backfillICT2026Defaults fills in the ICT-2026 fields (displacement/OTE/
// breaker/SMT) with their default values when loading a params row saved
// before that upgrade existed - their JSON zero-values (0, false) would
// otherwise silently neuter or disable those checks instead of applying the
// intended defaults.
func backfillICT2026Defaults(p *SBParams) {
	d := DefaultSBParams()
	// A pre-upgrade row's JSON simply has none of these keys, so every one
	// of them unmarshals to the Go zero value - check before overwriting any
	// of them so this also detects "legacy row" for the two bools below,
	// which have no other way to distinguish "key absent" from "explicitly
	// false" once unmarshaled.
	legacy := p.OTEMinRetrace == 0 && p.OTEMaxRetrace == 0 && p.MaxBarsForOTE == 0 && p.BreakerLookback == 0

	if p.MinDisplacementBodyPct == 0 {
		p.MinDisplacementBodyPct = d.MinDisplacementBodyPct
	}
	if p.MaxOpposingWickPct == 0 {
		p.MaxOpposingWickPct = d.MaxOpposingWickPct
	}
	if p.OTEMinRetrace == 0 {
		p.OTEMinRetrace = d.OTEMinRetrace
	}
	if p.OTEMaxRetrace == 0 {
		p.OTEMaxRetrace = d.OTEMaxRetrace
	}
	if p.MaxBarsForOTE == 0 {
		p.MaxBarsForOTE = d.MaxBarsForOTE
	}
	if p.BreakerLookback == 0 {
		p.BreakerLookback = d.BreakerLookback
	}
	// RequireBreakerConfluence/RequireSMTDivergence default to true - always
	// the intended value for a legacy row (one saved before these keys
	// existed at all), since "explicitly false" never occurred before this
	// upgrade shipped.
	if legacy {
		p.RequireBreakerConfluence = true
		p.RequireSMTDivergence = true
	}
}

// SaveParams persists a new active parameter set plus the backtest metrics
// that justified it (see internal/backtest.Result, passed through as
// already-marshaled JSON by the caller to avoid an import cycle between
// strategy and backtest).
func SaveParams(ctx context.Context, pool *pgxpool.Pool, params SBParams, trainMetrics, validationMetrics, testMetrics, lastReport []byte) error {
	// Keep optimized and externally supplied parameter sets on the fixed
	// 1:1.5 risk/reward rule as well.
	params.RiskRewardRatio = 1.5
	paramsJSON, err := json.Marshal(params)
	if err != nil {
		return err
	}
	_, err = pool.Exec(ctx, `
		UPDATE strategy_params
		SET params = $1, train_metrics = $2, validation_metrics = $3, test_metrics = $4,
		    last_report = $5, optimized_at = now(), updated_at = now()
		WHERE id = 1
	`, paramsJSON, trainMetrics, validationMetrics, testMetrics, lastReport)
	return err
}
