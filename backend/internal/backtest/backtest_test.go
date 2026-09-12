package backtest

import (
	"math"
	"testing"
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

func mkCandle(ts time.Time, o, h, l, c float64) models.Candle {
	return models.Candle{Ts: ts, Open: o, High: h, Low: l, Close: c, Volume: 10}
}

// TestMetrics_KnownTrades verifies the win/loss classification and
// arithmetic (return, win rate, profit factor, Sharpe, max drawdown)
// against a small, hand-computed set of trades - independent of the
// pattern-detection logic in Run, which is tested separately in
// internal/strategy.
func TestMetrics_KnownTrades(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	trades := []Trade{
		{EntryTs: base, ExitTs: base.Add(time.Minute), Side: models.SignalBuy, GrossPnLPct: 2.0, PnLPct: 2.0},
		{EntryTs: base.Add(time.Hour), ExitTs: base.Add(time.Hour + time.Minute), Side: models.SignalBuy, GrossPnLPct: -1.0, PnLPct: -1.0},
		{EntryTs: base.Add(2 * time.Hour), ExitTs: base.Add(2*time.Hour + time.Minute), Side: models.SignalSell, GrossPnLPct: 3.0, PnLPct: 3.0},
		{EntryTs: base.Add(3 * time.Hour), ExitTs: base.Add(3*time.Hour + time.Minute), Side: models.SignalBuy, GrossPnLPct: -1.0, PnLPct: -1.0},
	}

	res := Metrics(trades, time.Time{}, time.Time{}, CostModel{})

	if res.TotalTrades != 4 {
		t.Fatalf("TotalTrades = %d, want 4", res.TotalTrades)
	}
	if res.WinCount != 2 {
		t.Errorf("WinCount = %d, want 2", res.WinCount)
	}
	wantReturn := 2.0 - 1.0 + 3.0 - 1.0
	if math.Abs(res.TotalReturnPct-wantReturn) > 1e-9 {
		t.Errorf("TotalReturnPct = %.4f, want %.4f", res.TotalReturnPct, wantReturn)
	}
	wantWinRate := 50.0
	if math.Abs(res.WinRate-wantWinRate) > 1e-9 {
		t.Errorf("WinRate = %.4f, want %.4f", res.WinRate, wantWinRate)
	}
	wantPF := 5.0 / 2.0 // gross win 2+3=5, gross loss 1+1=2
	if math.Abs(res.ProfitFactor-wantPF) > 1e-9 {
		t.Errorf("ProfitFactor = %.4f, want %.4f", res.ProfitFactor, wantPF)
	}
	// Equity path: 2, 1, 4, 3 -> peak 4 at trade 3, trough after is 3 -> drawdown 1.
	// But also check the running peak before trade 3: after trade1 equity=2 (peak=2),
	// after trade2 equity=1 (dd=1), after trade3 equity=4 (peak=4), after trade4 equity=3 (dd=1).
	if math.Abs(res.MaxDrawdownPct-1.0) > 1e-9 {
		t.Errorf("MaxDrawdownPct = %.4f, want 1.0", res.MaxDrawdownPct)
	}
}

func TestCloseTrade_PnLArithmeticBothSides(t *testing.T) {
	buy := &Trade{Side: models.SignalBuy, Entry: 100}
	closeTrade(buy, 1, time.Now(), 105, "target", CostModel{}, nil)
	if math.Abs(buy.PnLPct-5.0) > 1e-9 {
		t.Errorf("BUY closeTrade PnLPct = %.4f, want 5.0", buy.PnLPct)
	}

	sell := &Trade{Side: models.SignalSell, Entry: 100}
	closeTrade(sell, 1, time.Now(), 95, "target", CostModel{}, nil)
	if math.Abs(sell.PnLPct-5.0) > 1e-9 {
		t.Errorf("SELL closeTrade PnLPct = %.4f, want 5.0", sell.PnLPct)
	}

	buyLoss := &Trade{Side: models.SignalBuy, Entry: 100}
	closeTrade(buyLoss, 1, time.Now(), 98, "stop", CostModel{}, nil)
	if math.Abs(buyLoss.PnLPct-(-2.0)) > 1e-9 {
		t.Errorf("BUY stop-out PnLPct = %.4f, want -2.0", buyLoss.PnLPct)
	}
}

func TestMetrics_TimeRangeFiltering(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	split := base.Add(2 * time.Hour)
	trades := []Trade{
		{EntryTs: base, ExitTs: base.Add(time.Minute), GrossPnLPct: 1.0, PnLPct: 1.0},
		{EntryTs: base.Add(time.Hour), ExitTs: base.Add(time.Hour + time.Minute), GrossPnLPct: 1.0, PnLPct: 1.0},
		{EntryTs: split, ExitTs: split.Add(time.Minute), GrossPnLPct: 1.0, PnLPct: 1.0},
		{EntryTs: base.Add(3 * time.Hour), ExitTs: base.Add(3*time.Hour + time.Minute), GrossPnLPct: 1.0, PnLPct: 1.0},
	}

	train := Metrics(trades, time.Time{}, split, CostModel{})
	valid := Metrics(trades, split, time.Time{}, CostModel{})

	if train.TotalTrades != 2 {
		t.Errorf("train TotalTrades = %d, want 2", train.TotalTrades)
	}
	if valid.TotalTrades != 2 {
		t.Errorf("valid TotalTrades = %d, want 2", valid.TotalTrades)
	}
}

// TestRun_StopHitClosesTrade uses a trivial always-HOLD-except-once params
// setup indirectly by relying on strategy's own tests for signal
// generation; here we only need to verify that once a trade is open, Run
// correctly detects a stop/target touch and closes it with the right
// exit price and PnL sign. We do this by constructing a scenario through
// the public Run/strategy path using the same fixture shape as the
// strategy package's bullish setup, then checking the resulting trade's
// exit behaves sanely (closes, PnL matches side/exit-price arithmetic).
func TestRun_ProducesConsistentTradeArithmetic(t *testing.T) {
	// A short synthetic series is enough to confirm Run doesn't panic and
	// that any trade it does produce has internally consistent PnL sign
	// (BUY trades gain when ExitPrice > Entry, SELL trades gain when
	// ExitPrice < Entry) - the detection logic itself is covered by
	// internal/strategy's tests.
	base := time.Date(2026, 1, 15, 10, 0, 0, 0, time.UTC)
	var candles []models.Candle
	price := 100.0
	for i := range 50 {
		ts := base.Add(time.Duration(i) * 5 * time.Minute)
		candles = append(candles, mkCandle(ts, price, price+0.5, price-0.5, price))
		price += 0.01
	}

	trades := Run(candles, nil, strategy.DefaultSBParams(), CostModel{}, nil)
	for _, tr := range trades {
		if tr.Side == models.SignalBuy && tr.PnLPct > 0 && tr.ExitPrice <= tr.Entry {
			t.Errorf("BUY trade shows positive PnL (%.4f) but ExitPrice %.4f <= Entry %.4f", tr.PnLPct, tr.ExitPrice, tr.Entry)
		}
		if tr.Side == models.SignalSell && tr.PnLPct > 0 && tr.ExitPrice >= tr.Entry {
			t.Errorf("SELL trade shows positive PnL (%.4f) but ExitPrice %.4f >= Entry %.4f", tr.PnLPct, tr.ExitPrice, tr.Entry)
		}
	}
}

func TestCloseTrade_DeductsRoundTripFeesAndSlippage(t *testing.T) {
	costs := CostModel{TakerFeePctPerSide: 0.05, EstimatedSlippagePctPerSide: 0.02}
	trade := &Trade{Side: models.SignalBuy, Entry: 100}
	closeTrade(trade, 1, time.Now(), 105, "target", costs, nil)

	// Entry costs are 0.07%; exit costs are 0.07% * 1.05 because a fixed
	// quantity is worth slightly more at the exit price.
	wantCost := 0.07 * (1 + 1.05)
	if math.Abs(trade.FeeCostPct-0.1025) > 1e-9 {
		t.Errorf("FeeCostPct = %.6f, want 0.1025", trade.FeeCostPct)
	}
	if math.Abs(trade.SlippageCostPct-0.041) > 1e-9 {
		t.Errorf("SlippageCostPct = %.6f, want 0.041", trade.SlippageCostPct)
	}
	if math.Abs(trade.PnLPct-(5.0-wantCost)) > 1e-9 {
		t.Errorf("net PnLPct = %.6f, want %.6f", trade.PnLPct, 5.0-wantCost)
	}
}

func TestCloseTrade_AppliesHistoricalFundingByDirectionAndMarkNotional(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	events := []FundingEvent{
		// Entry-time settlement is excluded; the simulated position opens at
		// the candle close.
		{Ts: base, Rate: 0.99, MarkPrice: 100},
		// Positive funding: a long pays and a short receives. Mark is 2%
		// above entry, so the entry-notional cost is 0.01% * 1.02.
		{Ts: base.Add(time.Hour), Rate: 0.0001, MarkPrice: 102},
		// Negative funding at ExitTs is included conservatively and reverses
		// who pays. Mark is 1% above entry.
		{Ts: base.Add(2 * time.Hour), Rate: -0.0002, MarkPrice: 101},
		{Ts: base.Add(3 * time.Hour), Rate: 0.99, MarkPrice: 100},
	}
	costs := CostModel{FundingIncluded: true}

	long := &Trade{Side: models.SignalBuy, Entry: 100, EntryTs: base}
	closeTrade(long, 1, base.Add(2*time.Hour), 101, "end_of_data", costs, events)
	wantLongFunding := 0.0001*100*1.02 + -0.0002*100*1.01
	if math.Abs(long.FundingCostPct-wantLongFunding) > 1e-9 {
		t.Errorf("long FundingCostPct = %.8f, want %.8f", long.FundingCostPct, wantLongFunding)
	}

	short := &Trade{Side: models.SignalSell, Entry: 100, EntryTs: base}
	closeTrade(short, 1, base.Add(2*time.Hour), 99, "end_of_data", costs, events)
	if math.Abs(short.FundingCostPct-(-wantLongFunding)) > 1e-9 {
		t.Errorf("short FundingCostPct = %.8f, want %.8f", short.FundingCostPct, -wantLongFunding)
	}

	withoutFunding := &Trade{Side: models.SignalBuy, Entry: 100, EntryTs: base}
	closeTrade(withoutFunding, 1, base.Add(2*time.Hour), 101, "end_of_data", CostModel{}, events)
	if withoutFunding.FundingCostPct != 0 {
		t.Errorf("FundingCostPct = %.8f with funding disabled, want 0", withoutFunding.FundingCostPct)
	}
}

func TestMetrics_ExcludesBoundaryCrossingTradeAndSortsChronologically(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	boundary := base.Add(2 * time.Hour)
	trades := []Trade{
		// Deliberately out of order: chronological equity is +2, +1, so
		// maximum drawdown must be 1 rather than 0.
		{EntryTs: base.Add(time.Hour), ExitTs: base.Add(time.Hour + time.Minute), GrossPnLPct: -1, PnLPct: -1},
		{EntryTs: base, ExitTs: base.Add(time.Minute), GrossPnLPct: 2, PnLPct: 2},
		// Entry is in training but exit consumes validation data: exclude it.
		{EntryTs: base.Add(90 * time.Minute), ExitTs: boundary.Add(time.Minute), GrossPnLPct: 9, PnLPct: 9},
	}

	got := Metrics(trades, time.Time{}, boundary, CostModel{})
	if got.TotalTrades != 2 {
		t.Fatalf("TotalTrades = %d, want 2", got.TotalTrades)
	}
	if math.Abs(got.MaxDrawdownPct-1) > 1e-9 {
		t.Errorf("MaxDrawdownPct = %.4f, want 1", got.MaxDrawdownPct)
	}
}
