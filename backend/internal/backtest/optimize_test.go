package backtest

import (
	"math"
	"testing"
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

func TestRobustSelectionRewardsRepeatedDevelopmentPerformanceWithoutFinalLeakage(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	developmentEnd := base.Add(40 * time.Hour)
	paramsStable := strategy.DefaultSBParams()
	paramsStable.SwingLookback = 10
	paramsUnstable := strategy.DefaultSBParams()
	paramsUnstable.SwingLookback = 20

	makeTrades := func(block int, pnl func(int) float64) []Trade {
		trades := make([]Trade, 0, 10)
		for i := 0; i < 10; i++ {
			entry := base.Add(time.Duration(block)*10*time.Hour + time.Duration(i)*45*time.Minute)
			value := pnl(i)
			reason := "target"
			if value < 0 {
				reason = "stop"
			}
			side := models.SignalBuy
			if i%2 == 1 {
				side = models.SignalSell
			}
			trades = append(trades, Trade{
				Symbol: "BTC-USDT", Side: side, EntryTs: entry, ExitTs: entry.Add(time.Minute),
				ExitReason: reason, GrossPnLPct: value, PnLPct: value,
			})
		}
		return trades
	}

	var stable, unstable []Trade
	for block := 1; block <= 3; block++ {
		stable = append(stable, makeTrades(block, func(i int) float64 {
			return 1 + float64((i/2)%2)
		})...)
		unstable = append(unstable, makeTrades(block, func(i int) float64 {
			if block == 1 {
				return 10 + float64((i/2)%2)
			}
			return -1 - float64((i/2)%2)
		})...)
	}
	// Future trades begin at developmentEnd and must never rescue the
	// unstable candidate during selection.
	unstable = append(unstable, makeTrades(4, func(i int) float64 {
		return 100 + float64((i/2)%2)
	})...)

	ranked, passed := rankRobustCandidates([]Candidate{
		{Params: paramsUnstable, allTrades: unstable},
		{Params: paramsStable, allTrades: stable},
	}, base, developmentEnd, CostModel{})
	if passed != 1 || len(ranked) != 2 {
		t.Fatalf("robust candidates = %d/%d, want 1/2", passed, len(ranked))
	}
	if !ranked[0].eligible || ranked[0].candidate.Params.SwingLookback != 10 {
		t.Fatalf("winner = lookback %d eligible=%v, want stable lookback 10", ranked[0].candidate.Params.SwingLookback, ranked[0].eligible)
	}
}

func TestRobustSelectionRejectsOneSidedCandidate(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	params := strategy.DefaultSBParams()
	var trades []Trade
	for block := 1; block <= 3; block++ {
		for i := 0; i < 10; i++ {
			entry := base.Add(time.Duration(block)*10*time.Hour + time.Duration(i)*45*time.Minute)
			side, pnl, reason := models.SignalBuy, -0.5-float64((i/2)%2)/10, "stop"
			if i%2 == 1 {
				side, pnl, reason = models.SignalSell, 2+float64((i/2)%2), "target"
			}
			trades = append(trades, Trade{
				Symbol: "BTC-USDT", Side: side, EntryTs: entry, ExitTs: entry.Add(time.Minute),
				ExitReason: reason, GrossPnLPct: pnl, PnLPct: pnl,
			})
		}
	}

	ranked, passed := rankRobustCandidates(
		[]Candidate{{Params: params, allTrades: trades}},
		base, base.Add(40*time.Hour), CostModel{},
	)
	if passed != 0 || len(ranked) != 1 || ranked[0].eligible {
		t.Fatalf("one-sided candidate passed=%d eligible=%v, want rejected", passed, ranked[0].eligible)
	}
	if ranked[0].robustSides != 1 {
		t.Fatalf("robust sides = %d, want only SELL", ranked[0].robustSides)
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

func TestDeploymentGateBlocksUnprofitableFinalBuySide(t *testing.T) {
	base := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	var trades []Trade
	for i := 0; i < 20; i++ {
		side, pnl, reason := models.SignalBuy, -0.5-float64((i/2)%2)/10, "stop"
		if i%2 == 1 {
			side, pnl, reason = models.SignalSell, 2+float64((i/2)%2), "target"
		}
		entry := base.Add(time.Duration(i) * time.Hour)
		trades = append(trades, Trade{
			Symbol: "BTC-USDT", Side: side, EntryTs: entry, ExitTs: entry.Add(time.Minute),
			ExitReason: reason, GrossPnLPct: pnl, PnLPct: pnl,
		})
	}
	testMetrics := Metrics(trades, time.Time{}, time.Time{}, CostModel{})
	report := OptimizeReport{
		CandidatesPassed: 1,
		RobustSelection: RobustSelectionReport{UsedFallback: false},
		WalkForward: WalkForwardReport{
			TotalFolds: 4, PassedFolds: 3,
			AggregateMetrics: Result{
				TotalTrades: 100, TotalReturnPct: 10, ProfitFactor: 1.5,
				Sharpe: 0.2, MaxDrawdownPct: 5,
			},
		Test:             testMetrics,
		FinalDiagnostics: buildFinalTestDiagnostics(trades, CostModel{}),
	}

	blockers := deploymentBlockers(report)
	if !containsString(blockers, "final_buy_unprofitable") {
		t.Fatalf("blockers = %v, want final_buy_unprofitable", blockers)
	}
	if containsString(blockers, "final_sell_unprofitable") || containsString(blockers, "final_sell_insufficient") {
		t.Fatalf("blockers = %v, profitable SELL side should pass", blockers)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
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
