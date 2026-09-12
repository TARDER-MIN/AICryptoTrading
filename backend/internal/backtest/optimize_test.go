package backtest

import (
	"testing"
	"time"

	"cryptotrading/internal/models"
)

func TestSortByValidationSharpeDoesNotUseFinalTest(t *testing.T) {
	candidates := []Candidate{
		{Validation: Result{Sharpe: 0.4}, test: Result{Sharpe: 99}},
		{Validation: Result{Sharpe: 0.8}, test: Result{Sharpe: -99}},
	}

	sortByValidationSharpeDesc(candidates)
	if candidates[0].Validation.Sharpe != 0.8 {
		t.Fatalf("winner validation Sharpe = %.2f, want 0.8", candidates[0].Validation.Sharpe)
	}
	if candidates[0].test.Sharpe != -99 {
		t.Fatal("final-test Sharpe influenced candidate ordering")
	}
}

func TestTimeRangeUsesTradableUniverseNotAnchorOnlySeries(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	candles := map[string][]models.Candle{
		"ALT-USDT": {{Ts: base.Add(24 * time.Hour)}, {Ts: base.Add(48 * time.Hour)}},
		// An extra SMT anchor may have a wider range, but it must not move
		// the train/validation/test boundaries of the tradable sample.
		"BTC-USDT": {{Ts: base}, {Ts: base.Add(72 * time.Hour)}},
	}

	minTs, maxTs := timeRange(candles, []string{"ALT-USDT"})
	if !minTs.Equal(base.Add(24*time.Hour)) || !maxTs.Equal(base.Add(48*time.Hour)) {
		t.Fatalf("timeRange = %s..%s, want tradable ALT range", minTs, maxTs)
	}
}
