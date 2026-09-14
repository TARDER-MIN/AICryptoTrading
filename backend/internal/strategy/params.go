package strategy

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LoadParams reads the currently-active HTF 3+1 parameters from the
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
	return NormalizeParams(p), nil
}

// backfillHTF3Plus1Defaults upgrades parameter JSON saved by the retired
// M5-only strategy. Unknown legacy OTE/Breaker/SMT keys are ignored by JSON;
// the new H1/CHOCH/retest fields receive safe defaults here.
func backfillHTF3Plus1Defaults(p *SBParams) {
	d := DefaultSBParams()
	if p.SwingLookback < 2 {
		p.SwingLookback = d.SwingLookback
	}
	if p.MinFVGSizePct <= 0 {
		p.MinFVGSizePct = d.MinFVGSizePct
	}
	if p.MaxBarsForSweep < 1 {
		p.MaxBarsForSweep = d.MaxBarsForSweep
	}
	if p.StopBufferPct < 0 {
		p.StopBufferPct = d.StopBufferPct
	}
	if p.MinDisplacementBodyPct <= 0 {
		p.MinDisplacementBodyPct = d.MinDisplacementBodyPct
	}
	if p.MaxOpposingWickPct <= 0 {
		p.MaxOpposingWickPct = d.MaxOpposingWickPct
	}
	if p.MinHTFSweepATR <= 0 {
		p.MinHTFSweepATR = d.MinHTFSweepATR
	}
	if p.MinHTFReclaimATR <= 0 {
		p.MinHTFReclaimATR = d.MinHTFReclaimATR
	}
	if p.MinDisplacementATR <= 0 {
		p.MinDisplacementATR = d.MinDisplacementATR
	}
	if p.MinDisplacementVolume <= 0 {
		p.MinDisplacementVolume = d.MinDisplacementVolume
	}
	if p.DisplacementATRLookback < 2 {
		p.DisplacementATRLookback = d.DisplacementATRLookback
	}
	if p.DisplacementVolLookback < 2 {
		p.DisplacementVolLookback = d.DisplacementVolLookback
	}
	if p.CHOCHLookback < 2 {
		p.CHOCHLookback = d.CHOCHLookback
	}
	if p.MaxBarsForRetest < 1 {
		p.MaxBarsForRetest = d.MaxBarsForRetest
	}
	legacy := p.OrderBlockLookback == 0
	if legacy {
		p.OrderBlockLookback = d.OrderBlockLookback
		p.RequireRejection = true
	}
	if p.EstimatedRoundTripCostPct <= 0 {
		p.EstimatedRoundTripCostPct = d.EstimatedRoundTripCostPct
	}
	if p.MinTargetCostMultiple <= 0 {
		p.MinTargetCostMultiple = d.MinTargetCostMultiple
	}
}

// SaveParams persists a new active parameter set plus the backtest metrics
// that justified it (see internal/backtest.Result, passed through as
// already-marshaled JSON by the caller to avoid an import cycle between
// strategy and backtest).
func SaveParams(ctx context.Context, pool *pgxpool.Pool, params SBParams, trainMetrics, validationMetrics, testMetrics, lastReport []byte) error {
	// Keep optimized and externally supplied parameter sets on the fixed
	// 1:1.5 risk/reward rule as well.
	params = NormalizeParams(params)
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

// SaveOptimizationReport records a rejected research run without replacing
// the currently-active parameters or the metrics that justified them. This is
// the fail-closed path used when walk-forward/final deployment gates do not
// pass; the dashboard can still explain the result after a refresh.
func SaveOptimizationReport(ctx context.Context, pool *pgxpool.Pool, lastReport []byte) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategy_params
		SET last_report = $1, updated_at = now()
		WHERE id = 1
	`, lastReport)
	return err
}

// SaveLongOptimizationReport stores the optional one-year proxy study in its
// own column. It intentionally cannot update params, optimized_at, or the
// train/validation/test metrics that justified the active live parameters.
func SaveLongOptimizationReport(ctx context.Context, pool *pgxpool.Pool, lastReport []byte) error {
	_, err := pool.Exec(ctx, `
		UPDATE strategy_params
		SET last_long_report = $1, updated_at = now()
		WHERE id = 1
	`, lastReport)
	return err
}
