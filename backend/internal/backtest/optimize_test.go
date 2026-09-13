package backtest

import (
	"math"
	"testing"
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
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

func TestFinalTestDiagnosticsBreaksDownAndSeparatesEndOfData(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	trades := []Trade{
		{Symbol: "BTC-USDT", Side: models.SignalBuy, EntryTs: base, ExitTs: base.Add(time.Minute), ExitReason: "target", GrossPnLPct: 2, PnLPct: 2},
		{Symbol: "BTC-USDT", Side: models.SignalSell, EntryTs: base.Add(time.Hour), ExitTs: base.Add(time.Hour + time.Minute), ExitReason: "stop", GrossPnLPct: -1, PnLPct: -1},
		{Symbol: "ETH-USDT", Side: models.SignalBuy, EntryTs: base.Add(24 * time.Hour), ExitTs: base.Add(25 * time.Hour), ExitReason: "end_of_data", GrossPnLPct: -0.5, PnLPct: -0.5},
	}

	got := buildFinalTestDiagnostics(trades, CostModel{})
	if got.CompletedOnly.TotalTrades != 2 || got.CompletedOnly.EndDataExits != 0 {
		t.Fatalf("completed-only metrics = %d trades / %d end exits, want 2/0", got.CompletedOnly.TotalTrades, got.CompletedOnly.EndDataExits)
	}
	if math.Abs(got.CompletedOnly.TotalReturnPct-1) > 1e-9 {
		t.Errorf("completed-only net = %.2f, want 1.00", got.CompletedOnly.TotalReturnPct)
	}
	if len(got.BySymbol) != 2 || got.BySymbol[0].Label != "ETH-USDT" || got.BySymbol[0].Metrics.EndDataExits != 1 {
		t.Fatalf("symbol diagnostics = %#v, want ETH loss/end-of-data first", got.BySymbol)
	}
	if len(got.BySide) != 2 || len(got.ByDay) != 2 {
		t.Fatalf("diagnostic group sizes side/day = %d/%d, want 2/2", len(got.BySide), len(got.ByDay))
	}
}

func TestWalkForwardSelectsEachFoldWithoutFutureLeakage(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	maxTs := base.Add(50 * time.Hour)
	paramsA := strategy.DefaultSBParams()
	paramsA.SwingLookback = 10
	paramsB := strategy.DefaultSBParams()
	paramsB.SwingLookback = 20

	makeBlock := func(block int, positive bool) []Trade {
		trades := make([]Trade, 0, 10)
		for i := 0; i < 10; i++ {
			pnl := 1.0 + float64(i%2)
			reason := "target"
			if !positive {
				pnl = -pnl
				reason = "stop"
			}
			entry := base.Add(time.Duration(block)*10*time.Hour + time.Duration(i)*30*time.Minute)
			trades = append(trades, Trade{
				Symbol: "BTC-USDT", Side: models.SignalBuy, EntryTs: entry,
				ExitTs: entry.Add(time.Minute), ExitReason: reason, GrossPnLPct: pnl, PnLPct: pnl,
			})
		}
		return trades
	}

	// Candidate A is the only valid choice using block 1. Candidate B has
	// enormous future performance, which must not influence fold 1's pick.
	var tradesA, tradesB []Trade
	for block := 0; block < 5; block++ {
		tradesA = append(tradesA, makeBlock(block, block == 0)...)
		tradesB = append(tradesB, makeBlock(block, block != 0)...)
	}
	candidates := []Candidate{
		{Params: paramsA, allTrades: tradesA},
		{Params: paramsB, allTrades: tradesB},
	}

	got := buildWalkForwardReport(candidates, base, maxTs, CostModel{})
	if got.TotalFolds != 4 || len(got.Folds) != 4 {
		t.Fatalf("walk-forward folds = %d/%d, want 4/4", got.TotalFolds, len(got.Folds))
	}
	if got.Folds[0].SelectedParams.SwingLookback != 10 {
		t.Fatalf("fold 1 selected lookback %d, want 10 from past-only performance", got.Folds[0].SelectedParams.SwingLookback)
	}
	if got.Folds[0].TestMetrics.TotalReturnPct >= 0 || got.Folds[0].Passed {
		t.Fatalf("fold 1 test = %.2f passed=%v, want an honest losing future block", got.Folds[0].TestMetrics.TotalReturnPct, got.Folds[0].Passed)
	}
	if got.AggregateMetrics.TotalTrades != 40 {
		t.Errorf("aggregate walk-forward trades = %d, want 40", got.AggregateMetrics.TotalTrades)
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
