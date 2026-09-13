package strategy

import (
	"math"
	"strings"
	"testing"
	"time"

	"cryptotrading/internal/models"
)

func htfTestParams() SBParams {
	return SBParams{
		SwingLookback:   4,
		MinFVGSizePct:   0.05,
		MaxBarsForSweep: 3,
		StopBufferPct:   0.1,
		RiskRewardRatio: 1.5,

		MinDisplacementBodyPct: 0.6,
		MaxOpposingWickPct:     0.25,
		CHOCHLookback:          3,
		MaxBarsForRetest:       6,
		OrderBlockLookback:     5,
		RequireRejection:       true,
	}
}

func flatM5(start time.Time, hours int) []models.Candle {
	result := make([]models.Candle, 0, hours*12)
	for i := 0; i < hours*12; i++ {
		result = append(result, models.Candle{
			Symbol: "BTC-USDT", Ts: start.Add(time.Duration(i) * 5 * time.Minute),
			Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10,
		})
	}
	return result
}

// bullishFixture creates four prior H1 ranges, a fifth fully-closed H1 bar
// that sweeps the prior low and reclaims it, then a M5 CHOCH/displacement/FVG
// and a first-retest rejection inside the still-open next H1 bucket.
func bullishFixture(start time.Time) []models.Candle {
	c := flatM5(start, 5)
	c[4*12+4].Low = 98.5 // H1 downside liquidity sweep; H1 closes back at 100
	c = append(c,
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 0*time.Minute), Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 5*time.Minute), Open: 100, High: 100.5, Low: 99.7, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 10*time.Minute), Open: 100, High: 100.5, Low: 99.8, Close: 100.1, Volume: 10},
		// Last bearish M5 candle before displacement: OB body [100.0, 100.2].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 15*time.Minute), Open: 100.2, High: 100.3, Low: 99.9, Close: 100.0, Volume: 10},
		// Bullish displacement closes above the previous three M5 highs (CHOCH).
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 20*time.Minute), Open: 100.1, High: 101.8, Low: 100.0, Close: 101.6, Volume: 30},
		// Bullish FVG [100.3, 100.8].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 25*time.Minute), Open: 101.1, High: 101.5, Low: 100.8, Close: 101.2, Volume: 20},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 30*time.Minute), Open: 101.2, High: 101.3, Low: 100.9, Close: 101.0, Volume: 10},
		// First FVG retest, closing back above it with a long lower rejection wick.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 35*time.Minute), Open: 100.6, High: 101.1, Low: 100.2, Close: 100.9, Volume: 20},
	)
	return c
}

func bearishFixture(start time.Time) []models.Candle {
	c := flatM5(start, 5)
	c[4*12+4].High = 101.5 // H1 upside liquidity sweep; H1 closes back at 100
	c = append(c,
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 0*time.Minute), Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 5*time.Minute), Open: 100, High: 100.4, Low: 99.5, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 10*time.Minute), Open: 100, High: 100.3, Low: 99.5, Close: 99.9, Volume: 10},
		// Last bullish M5 candle before displacement: OB body [99.8, 100.0].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 15*time.Minute), Open: 99.8, High: 100.1, Low: 99.7, Close: 100.0, Volume: 10},
		// Bearish displacement closes below the previous three M5 lows (CHOCH).
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 20*time.Minute), Open: 99.9, High: 100.0, Low: 98.2, Close: 98.4, Volume: 30},
		// Bearish FVG [99.2, 99.7].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 25*time.Minute), Open: 98.8, High: 99.2, Low: 98.5, Close: 98.8, Volume: 20},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 30*time.Minute), Open: 98.8, High: 99.1, Low: 98.6, Close: 98.9, Volume: 10},
		// First FVG retest, closing back below it with a long upper rejection wick.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(5*time.Hour + 35*time.Minute), Open: 99.3, High: 99.8, Low: 98.9, Close: 99.1, Volume: 20},
	)
	return c
}

var htfBase = time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)

func TestHTF3Plus1BullishSetup(t *testing.T) {
	candles := bullishFixture(htfBase)
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalBuy {
		t.Fatalf("expected BUY, got %s (%s)", sig.Action, sig.Reason)
	}
	if sig.EntryZoneType != "FVG" || sig.FVGLow != 100.3 || sig.FVGHigh != 100.8 {
		t.Fatalf("zone = %s [%.4f, %.4f], want FVG [100.3, 100.8]", sig.EntryZoneType, sig.FVGLow, sig.FVGHigh)
	}
	if sig.CHOCHLevel != 100.5 || sig.SweepPrice != 98.5 {
		t.Errorf("CHOCH/sweep = %.4f/%.4f, want 100.5/98.5", sig.CHOCHLevel, sig.SweepPrice)
	}
	if !(sig.StopLoss < sig.Entry && sig.Entry < sig.TakeProfit) {
		t.Fatalf("invalid BUY prices stop %.5f entry %.5f target %.5f", sig.StopLoss, sig.Entry, sig.TakeProfit)
	}
	rr := (sig.TakeProfit - sig.Entry) / (sig.Entry - sig.StopLoss)
	if math.Abs(rr-1.5) > 1e-9 {
		t.Errorf("risk/reward = %.8f, want 1.5", rr)
	}
	if !strings.Contains(sig.Reason, "H1") || !strings.Contains(sig.Reason, "CHOCH") || !strings.Contains(sig.Reason, "首次回踩拒絕") {
		t.Errorf("reason does not explain HTF 3+1: %q", sig.Reason)
	}
}

func TestHTF3Plus1RiskRewardCannotBeOverridden(t *testing.T) {
	candles := bullishFixture(htfBase)
	params := htfTestParams()
	params.RiskRewardRatio = 9
	sig := DecideSilverBullet(candles, nil, params, candles[len(candles)-1].Ts)
	if sig.Action != models.SignalBuy {
		t.Fatalf("expected BUY, got %s (%s)", sig.Action, sig.Reason)
	}
	rr := (sig.TakeProfit - sig.Entry) / (sig.Entry - sig.StopLoss)
	if math.Abs(rr-1.5) > 1e-9 {
		t.Errorf("risk/reward = %.8f after override attempt, want fixed 1.5", rr)
	}
}

func TestHTF3Plus1BearishSetup(t *testing.T) {
	candles := bearishFixture(htfBase)
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalSell {
		t.Fatalf("expected SELL, got %s (%s)", sig.Action, sig.Reason)
	}
	if sig.EntryZoneType != "FVG" || sig.FVGLow != 99.2 || sig.FVGHigh != 99.7 {
		t.Fatalf("zone = %s [%.4f, %.4f], want FVG [99.2, 99.7]", sig.EntryZoneType, sig.FVGLow, sig.FVGHigh)
	}
	if !(sig.TakeProfit < sig.Entry && sig.Entry < sig.StopLoss) {
		t.Fatalf("invalid SELL prices target %.5f entry %.5f stop %.5f", sig.TakeProfit, sig.Entry, sig.StopLoss)
	}
}

func TestHTF3Plus1DoesNotSeePartialH1Sweep(t *testing.T) {
	candles := bullishFixture(htfBase)
	partial := candles[:4*12+6] // hour 4 has swept, but its H1 candle has not closed
	biases := PrepareHTFBiases(partial, htfTestParams())
	if biases[len(biases)-1].Valid {
		t.Fatalf("partial H1 candle leaked a future bias: %#v", biases[len(biases)-1])
	}
}

func TestHTF3Plus1RequiresH1Sweep(t *testing.T) {
	candles := bullishFixture(htfBase)
	candles[4*12+4].Low = 99.5
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD without H1 sweep, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresM5CHOCH(t *testing.T) {
	candles := bullishFixture(htfBase)
	displacement := 5*12 + 4
	candles[displacement] = models.Candle{
		Symbol: "BTC-USDT", Ts: candles[displacement].Ts,
		Open: 99.7, High: 100.7, Low: 99.65, Close: 100.4, Volume: 30,
	}
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD without close through M5 structure, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1FirstTouchConsumesBothZones(t *testing.T) {
	candles := bullishFixture(htfBase)
	// The bar before the current rejection reaches through both the FVG and
	// OB without a qualifying close/rejection. Neither zone may be reused.
	firstTouch := len(candles) - 2
	candles[firstTouch].Low = 99.9
	candles[firstTouch].Close = 100.1
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD after first touch consumed the zones, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresRejectionAtFirstRetest(t *testing.T) {
	candles := bullishFixture(htfBase)
	last := len(candles) - 1
	candles[last] = models.Candle{
		Symbol: "BTC-USDT", Ts: candles[last].Ts,
		Open: 100.9, High: 101.0, Low: 100.5, Close: 100.6, Volume: 20,
	}
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[last].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD when first retest does not reject, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1HonorsRetestWindow(t *testing.T) {
	candles := bullishFixture(htfBase)
	params := htfTestParams()
	params.MaxBarsForRetest = 1
	sig := DecideSilverBullet(candles, nil, params, candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD when the first retest is outside the configured M5 window, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1BiasExpiresAcrossMissingH1Data(t *testing.T) {
	candles := flatM5(htfBase, 5)
	candles[4*12+4].Low = 98.5
	// A multi-hour data gap must age out the H1 bias. Counting only the
	// compressed list of available H1 buckets would incorrectly keep it live.
	candles = append(candles, models.Candle{
		Symbol: "BTC-USDT", Ts: htfBase.Add(10 * time.Hour),
		Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10,
	})
	biases := PrepareHTFBiases(candles, htfTestParams())
	if biases[len(biases)-1].Valid {
		t.Fatalf("stale H1 bias survived a multi-hour data gap: %#v", biases[len(biases)-1])
	}
}

func TestHTF3Plus1NoSessionGate(t *testing.T) {
	for _, hour := range []int{0, 7, 13, 19} {
		start := time.Date(2026, time.January, 2, hour, 0, 0, 0, time.UTC)
		candles := bullishFixture(start)
		sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
		if sig.Action != models.SignalBuy {
			t.Errorf("start hour %d: expected BUY without session gating, got %s (%s)", hour, sig.Action, sig.Reason)
		}
	}
}
