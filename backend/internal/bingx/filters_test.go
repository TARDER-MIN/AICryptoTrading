package bingx

import "testing"

// seed loads a fixed filter table directly (bypassing the network) so
// MaxQtyForCap can be tested against the real numbers gathered during
// planning: BTCUSDT/ETHUSDT should be infeasible at a $10 cap, SOL/XRP/ADA/
// DOGE/TRX should be feasible.
func seed(fc *FilterCache, filters map[string]SymbolFilters) {
	fc.mu.Lock()
	defer fc.mu.Unlock()
	fc.byID = filters
}

func TestMaxQtyForCap(t *testing.T) {
	fc := NewFilterCache(nil)
	seed(fc, map[string]SymbolFilters{
		"BTCUSDT":  {Symbol: "BTCUSDT", StepSize: 0.001, MinQty: 0.001, MinNotionalUSD: 50, QuantityPrecision: 3},
		"ETHUSDT":  {Symbol: "ETHUSDT", StepSize: 0.001, MinQty: 0.001, MinNotionalUSD: 20, QuantityPrecision: 3},
		"SOLUSDT":  {Symbol: "SOLUSDT", StepSize: 0.01, MinQty: 0.01, MinNotionalUSD: 5, QuantityPrecision: 2},
		"XRPUSDT":  {Symbol: "XRPUSDT", StepSize: 0.1, MinQty: 0.1, MinNotionalUSD: 5, QuantityPrecision: 1},
		"ADAUSDT":  {Symbol: "ADAUSDT", StepSize: 1, MinQty: 1, MinNotionalUSD: 5, QuantityPrecision: 0},
		"DOGEUSDT": {Symbol: "DOGEUSDT", StepSize: 1, MinQty: 1, MinNotionalUSD: 5, QuantityPrecision: 0},
	})

	cases := []struct {
		symbol       string
		price        float64
		capUSD       float64
		wantFeasible bool
		maxNotional  float64 // upper bound the resulting notional must not exceed
	}{
		{"BTCUSDT", 79323, 10, false, 0}, // MIN_NOTIONAL $50 alone exceeds $10 cap
		{"ETHUSDT", 2503, 10, false, 0},  // MIN_NOTIONAL $20 alone exceeds $10 cap
		{"SOLUSDT", 104.05, 10, true, 10},
		{"XRPUSDT", 1.4277, 10, true, 10},
		{"ADAUSDT", 0.2195, 10, true, 10},
		{"DOGEUSDT", 0.0906, 10, true, 10},
	}

	for _, c := range cases {
		t.Run(c.symbol, func(t *testing.T) {
			res := fc.MaxQtyForCap(c.symbol, c.price, c.capUSD)
			if res.Feasible != c.wantFeasible {
				t.Fatalf("MaxQtyForCap(%s, %.4f, %.2f) feasible=%v reason=%q, want feasible=%v",
					c.symbol, c.price, c.capUSD, res.Feasible, res.Reason, c.wantFeasible)
			}
			if c.wantFeasible {
				if res.Qty <= 0 {
					t.Fatalf("%s: expected positive qty, got %v", c.symbol, res.Qty)
				}
				if res.NotionalUSD > c.maxNotional+1e-9 {
					t.Fatalf("%s: notional %.4f exceeds cap %.4f", c.symbol, res.NotionalUSD, c.maxNotional)
				}
			}
		})
	}
}

func TestMaxQtyForCapNeverExceedsCapEvenAtOddPrices(t *testing.T) {
	fc := NewFilterCache(nil)
	seed(fc, map[string]SymbolFilters{
		"TESTUSDT": {Symbol: "TESTUSDT", StepSize: 0.001, MinQty: 0.001, MinNotionalUSD: 5, QuantityPrecision: 3},
	})
	// A price where cap/price doesn't land on a clean step boundary -
	// rounding must go DOWN, never up past the cap.
	res := fc.MaxQtyForCap("TESTUSDT", 3.33333, 10)
	if !res.Feasible {
		t.Fatalf("expected feasible, got reason=%q", res.Reason)
	}
	if res.NotionalUSD > 10 {
		t.Fatalf("notional %.6f exceeds $10 cap", res.NotionalUSD)
	}
}

func TestMaxQtyForCapUnknownSymbol(t *testing.T) {
	fc := NewFilterCache(nil)
	res := fc.MaxQtyForCap("NOSUCHUSDT", 100, 10)
	if res.Feasible {
		t.Fatal("expected infeasible for a symbol with no cached filters")
	}
}

// TestRoundPrice_MatchesProductionFailure reproduces the exact live incident
// that motivated RoundPrice's existence: a 5-decimal OTE-computed stop/take-
// profit price (2.43622 / 2.41621) sent unrounded as an algo order's
// triggerPrice for NEARUSDT (tickSize 0.001, pricePrecision 3) got rejected
// by Binance with code=-1111 "Precision is over the maximum defined for
// this asset", leaving a real short position with no protective orders at
// all. RoundPrice must collapse both to a valid 3-decimal, tick-aligned price.
func TestRoundPrice_MatchesProductionFailure(t *testing.T) {
	fc := NewFilterCache(nil)
	seed(fc, map[string]SymbolFilters{
		"NEARUSDT": {Symbol: "NEARUSDT", TickSize: 0.001, PricePrecision: 3},
	})

	cases := []struct {
		name  string
		price float64
		want  float64
	}{
		{"stop_loss", 2.43622, 2.436},
		{"take_profit", 2.41621, 2.416},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := fc.RoundPrice("NEARUSDT", c.price)
			if got != c.want {
				t.Fatalf("RoundPrice(NEARUSDT, %v) = %v, want %v", c.price, got, c.want)
			}
		})
	}
}

func TestRoundPrice_RoundsToNearestTickNotFloor(t *testing.T) {
	fc := NewFilterCache(nil)
	seed(fc, map[string]SymbolFilters{
		"TESTUSDT": {Symbol: "TESTUSDT", TickSize: 0.01, PricePrecision: 2},
	})
	// 1.2349 is closer to 1.23 than 1.24 - nearest-tick, not floor-to-tick
	// (which would give the same answer here) or ceil-to-tick (1.24, wrong).
	if got := fc.RoundPrice("TESTUSDT", 1.2349); got != 1.23 {
		t.Fatalf("RoundPrice = %v, want 1.23", got)
	}
	// 1.2351 is closer to 1.24 - must round up, not floor down to 1.23.
	if got := fc.RoundPrice("TESTUSDT", 1.2351); got != 1.24 {
		t.Fatalf("RoundPrice = %v, want 1.24 (floor would wrongly give 1.23)", got)
	}
}

func TestRoundPrice_NoFilterDataFailsOpen(t *testing.T) {
	fc := NewFilterCache(nil)
	if got := fc.RoundPrice("NOSUCHUSDT", 2.43622); got != 2.43622 {
		t.Fatalf("RoundPrice with no cached filters should pass the price through unchanged, got %v", got)
	}
}
