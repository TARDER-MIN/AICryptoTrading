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

		HTFStructureLookback:   4,
		HTFPivotStrength:       2,
		HTFZoneATRMultiple:     0.75,
		CHOCHPivotStrength:     2,
		TargetBarrierBufferATR: 0.10,

		MinHTFSweepATR:            0.05,
		MinHTFReclaimATR:          0.10,
		MinDisplacementBodyPct:    0.6,
		MaxOpposingWickPct:        0.25,
		MinDisplacementATR:        0.8,
		MinDisplacementVolume:     1.2,
		DisplacementATRLookback:   14,
		DisplacementVolLookback:   20,
		CHOCHLookback:             5,
		MaxBarsForRetest:          6,
		OrderBlockLookback:        5,
		RequireRejection:          true,
		EstimatedRoundTripCostPct: 0.14,
		MinTargetCostMultiple:     5,
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

// bullishFixture creates one fully closed UTC day and enough completed H4
// context, then an H1 PDL sweep/reclaim followed by M5 execution. The larger
// history is intentional: ordinary internal H1 levels are no longer valid
// HTF locations.
func bullishFixture(start time.Time) []models.Candle {
	return bullishFixtureAtUTCOffset(start, 0)
}

func bullishFixtureAtUTCOffset(start time.Time, sweepHour int) []models.Candle {
	sweepOffset := 24 + sweepHour
	c := flatM5(start, sweepOffset+1)
	c[sweepOffset*12+4].Low = 98.5 // PDL=99.5; H1 closes back at 100
	executionOffset := time.Duration(sweepOffset+1) * time.Hour
	c = append(c,
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 0*time.Minute), Open: 100, High: 100.4, Low: 99.5, Close: 100, Volume: 10},
		// Confirmed pivot high: two lower highs exist on both sides before displacement.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 5*time.Minute), Open: 100, High: 101.0, Low: 99.7, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 10*time.Minute), Open: 100, High: 100.4, Low: 99.8, Close: 100.1, Volume: 10},
		// Last bearish M5 candle before displacement: OB body [100.0, 100.2].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 15*time.Minute), Open: 100.2, High: 100.3, Low: 99.9, Close: 100.0, Volume: 10},
		// Bullish displacement closes above the confirmed M5 pivot (CHOCH).
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 20*time.Minute), Open: 100.1, High: 101.8, Low: 100.0, Close: 101.6, Volume: 30},
		// Bullish FVG [100.3, 100.8].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 25*time.Minute), Open: 101.1, High: 101.5, Low: 100.8, Close: 101.2, Volume: 20},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 30*time.Minute), Open: 101.2, High: 101.3, Low: 100.9, Close: 101.0, Volume: 10},
		// First FVG retest, closing back above it with a long lower rejection wick.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 35*time.Minute), Open: 100.6, High: 101.1, Low: 100.2, Close: 100.9, Volume: 20},
	)
	return c
}

func bearishFixture(start time.Time) []models.Candle {
	sweepOffset := 24
	c := flatM5(start, sweepOffset+1)
	c[sweepOffset*12+4].High = 101.5 // PDH=100.5; H1 closes back at 100
	executionOffset := time.Duration(sweepOffset+1) * time.Hour
	c = append(c,
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 0*time.Minute), Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10},
		// Confirmed pivot low: two higher lows exist on both sides before displacement.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 5*time.Minute), Open: 100, High: 100.4, Low: 99.0, Close: 100, Volume: 10},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 10*time.Minute), Open: 100, High: 100.3, Low: 99.5, Close: 99.9, Volume: 10},
		// Last bullish M5 candle before displacement: OB body [99.8, 100.0].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 15*time.Minute), Open: 99.8, High: 100.1, Low: 99.7, Close: 100.0, Volume: 10},
		// Bearish displacement closes below the confirmed M5 pivot (CHOCH).
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 20*time.Minute), Open: 99.9, High: 100.0, Low: 98.2, Close: 98.4, Volume: 30},
		// Bearish FVG [99.2, 99.7].
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 25*time.Minute), Open: 98.8, High: 99.2, Low: 98.5, Close: 98.8, Volume: 20},
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 30*time.Minute), Open: 98.8, High: 99.1, Low: 98.6, Close: 98.9, Volume: 10},
		// First FVG retest, closing back below it with a long upper rejection wick.
		models.Candle{Symbol: "BTC-USDT", Ts: start.Add(executionOffset + 35*time.Minute), Open: 99.3, High: 99.8, Low: 98.9, Close: 99.1, Volume: 20},
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
	if sig.CHOCHLevel != 101.0 || sig.SweepPrice != 98.5 {
		t.Errorf("CHOCH/sweep = %.4f/%.4f, want 101.0/98.5", sig.CHOCHLevel, sig.SweepPrice)
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
	if !strings.Contains(sig.Reason, "PDL") {
		t.Errorf("reason does not identify previous-day low liquidity: %q", sig.Reason)
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
	if !strings.Contains(sig.Reason, "PDH") || !strings.Contains(sig.Reason, "定向做空") {
		t.Errorf("upper-liquidity sweep was not explained as SELL bias: %q", sig.Reason)
	}
}

func TestHTF3Plus1DoesNotSeePartialH1Sweep(t *testing.T) {
	candles := bullishFixture(htfBase)
	partial := candles[:24*12+6] // hour 24 has swept, but its H1 candle has not closed
	biases := PrepareHTFBiases(partial, htfTestParams())
	if biases[len(biases)-1].Valid {
		t.Fatalf("partial H1 candle leaked a future bias: %#v", biases[len(biases)-1])
	}
}

func TestHTF3Plus1RequiresH1Sweep(t *testing.T) {
	candles := bullishFixture(htfBase)
	candles[24*12+4].Low = 99.5
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD without H1 sweep, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1BreakoutWithoutReclaimDoesNotReverse(t *testing.T) {
	candles := bearishFixture(htfBase)
	// The H1 bar trades above PDH but finishes above it. That is a breakout,
	// not an upper-liquidity sweep/reclaim, so it must not create SELL bias.
	lastInSweepHour := 24*12 + 11
	candles[lastInSweepHour].Close = 101.0
	candles[lastInSweepHour].High = 101.2
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD after unreclaimed upside breakout, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresMeaningfulHTFSweepPenetration(t *testing.T) {
	candles := bullishFixture(htfBase)
	// Prior H1 ATR is 1.0 and the fixed minimum is 0.05 ATR. A 0.04 poke
	// through PDL is noise, not a qualifying external-liquidity raid.
	candles[24*12+4].Low = 99.46
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD for undersized H1 sweep, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresMeaningfulHTFReclaim(t *testing.T) {
	candles := bullishFixture(htfBase)
	// The sweep is deep enough, but the H1 close is only 0.05 ATR above PDL;
	// the fixed reclaim threshold is 0.10 ATR.
	lastInSweepHour := 24*12 + 11
	candles[lastInSweepHour].Close = 99.55
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD for shallow H1 reclaim, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1DoesNotRelabelRecoveryAfterAcceptedBreakoutAsSweep(t *testing.T) {
	candles := bullishFixtureAtUTCOffset(htfBase, 1)
	// Price was already accepted below PDL on the preceding H1 close. The
	// following recovery above PDL is not a new one-candle raid/reclaim.
	previousHourClose := 25*12 - 1
	candles[previousHourClose].Close = 99.0
	candles[previousHourClose].Low = 98.9
	candles[25*12].Open = 99.0
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD after prior acceptance below PDL, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1IgnoresInternalH1Liquidity(t *testing.T) {
	candles := bearishFixture(htfBase)
	// A higher level inside both the previous day and current H4 context
	// makes the normal 101.5 poke merely internal liquidity.
	candles[20*12].High = 102.0
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD for internal H1 sweep away from HTF external liquidity, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RejectsAmbiguousTwoSidedSweep(t *testing.T) {
	candles := bullishFixture(htfBase)
	candles[24*12+4].High = 101.5
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD when one H1 bar sweeps PDH and PDL, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1UsesCompletedH4ExternalLiquidityWhenPreviousDayUnavailable(t *testing.T) {
	start := time.Date(2026, time.January, 1, 4, 0, 0, 0, time.UTC)
	candles := bullishFixture(start)
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalBuy || !strings.Contains(sig.Reason, "H4外部低點") {
		t.Fatalf("expected H4 external-low BUY fallback, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresM5CHOCH(t *testing.T) {
	candles := bullishFixture(htfBase)
	displacement := 25*12 + 4
	candles[displacement] = models.Candle{
		Symbol: "BTC-USDT", Ts: candles[displacement].Ts,
		Open: 99.7, High: 100.7, Low: 99.65, Close: 100.4, Volume: 30,
	}
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD without close through M5 structure, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresATRDisplacement(t *testing.T) {
	candles := bullishFixture(htfBase)
	params := htfTestParams()
	params.MinDisplacementATR = 3.0
	sig := DecideSilverBullet(candles, nil, params, candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD when M5 displacement body is too small relative to ATR, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RequiresRelativeVolumeExpansion(t *testing.T) {
	candles := bullishFixture(htfBase)
	candles[25*12+4].Volume = 10
	sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD without M5 relative-volume expansion, got %s (%s)", sig.Action, sig.Reason)
	}
}

func TestHTF3Plus1RejectsTargetTooSmallForCosts(t *testing.T) {
	candles := bullishFixture(htfBase)
	params := htfTestParams()
	params.MinTargetCostMultiple = 20
	sig := DecideSilverBullet(candles, nil, params, candles[len(candles)-1].Ts)
	if sig.Action != models.SignalHold {
		t.Fatalf("expected HOLD when target distance is too small relative to costs, got %s (%s)", sig.Action, sig.Reason)
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
	candles := flatM5(htfBase, 25)
	candles[24*12+4].Low = 98.5
	// A multi-hour data gap must age out the H1 bias. Counting only the
	// compressed list of available H1 buckets would incorrectly keep it live.
	candles = append(candles, models.Candle{
		Symbol: "BTC-USDT", Ts: htfBase.Add(30 * time.Hour),
		Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10,
	})
	biases := PrepareHTFBiases(candles, htfTestParams())
	if biases[len(biases)-1].Valid {
		t.Fatalf("stale H1 bias survived a multi-hour data gap: %#v", biases[len(biases)-1])
	}
}

func TestHTF3Plus1NoSessionGate(t *testing.T) {
	for _, hour := range []int{0, 7, 13, 19} {
		candles := bullishFixtureAtUTCOffset(htfBase, hour)
		sig := DecideSilverBullet(candles, nil, htfTestParams(), candles[len(candles)-1].Ts)
		if sig.Action != models.SignalBuy {
			t.Errorf("sweep hour %d UTC: expected BUY without session gating, got %s (%s)", hour, sig.Action, sig.Reason)
		}
	}
}


func TestHTFContextRejectsShortAgainstBullishH4Structure(t *testing.T) {
	start := htfBase
	values := []struct {
		high, low, close float64
	}{
		{100, 96, 98}, {102, 97, 100}, {105, 98, 103},
		{103, 99, 101}, {104, 100, 102}, {106, 101, 104}, {107, 102, 106},
	}
	bars := make([]h1Bar, 0, len(values))
	for i, value := range values {
		barStart := start.Add(time.Duration(i) * 4 * time.Hour)
		bars = append(bars, h1Bar{
			Start: barStart, End: barStart.Add(4 * time.Hour),
			Open: value.close - 0.5, High: value.high, Low: value.low, Close: value.close,
		})
	}
	params := htfTestParams()
	params.HTFStructureLookback = len(bars)
	sweep := h1Bar{Low: 105, High: 108, Open: 106, Close: 106.5}
	context := buildHTFContext(bars, sweep, models.SignalSell, params)
	if context.Valid {
		t.Fatalf("bullish H4 structure incorrectly allowed a SELL context: %#v", context)
	}
	if got := classifyH4Trend(bars, 2); got != "多頭結構" {
		t.Fatalf("H4 trend = %q, want bullish structure", got)
	}
}

func TestHTFContextRejectsMidRangeSweepWithoutZone(t *testing.T) {
	var bars []h1Bar
	for i := 0; i < 12; i++ {
		price := 100.0 + float64(i)
		start := htfBase.Add(time.Duration(i) * 4 * time.Hour)
		bars = append(bars, h1Bar{
			Start: start, End: start.Add(4 * time.Hour),
			Open: price, High: price + 1, Low: price - 1, Close: price,
		})
	}
	params := htfTestParams()
	params.HTFStructureLookback = 12
	sweep := h1Bar{Open: 106, High: 106.5, Low: 105, Close: 105.5}
	if context := buildHTFContext(bars, sweep, models.SignalBuy, params); context.Valid {
		t.Fatalf("mid-range sweep without fresh H4 zone was accepted: %#v", context)
	}
}

func TestM5CHOCHRequiresConfirmedPivot(t *testing.T) {
	var candles []models.Candle
	highs := []float64{100, 102, 105, 103, 104, 106}
	for i, high := range highs {
		candles = append(candles, models.Candle{
			Ts: htfBase.Add(time.Duration(i) * 5 * time.Minute),
			Open: high - 1, High: high, Low: high - 2, Close: high - 0.5, Volume: 10,
		})
	}
	candles[5].Close = 104.5
	if level, ok := confirmsCHOCH(candles, 5, 5, 2, true); ok || level != 105 {
		t.Fatalf("close below confirmed pivot returned level %.2f ok=%v, want 105/false", level, ok)
	}
	candles[5].Close = 105.5
	if level, ok := confirmsCHOCH(candles, 5, 5, 2, true); !ok || level != 105 {
		t.Fatalf("close above confirmed pivot returned level %.2f ok=%v, want 105/true", level, ok)
	}
}

func TestTargetPathMustClearOpposingStructure(t *testing.T) {
	params := htfTestParams()
	bias := HTFBias{
		Action: models.SignalBuy, SweepATR: 2,
		TargetBarrier: 105, TargetBarrierName: "H1確認擺動高點",
	}
	if targetPathClear(100, 104.9, bias, params) {
		t.Fatal("target crossing the required structure buffer was accepted")
	}
	if !targetPathClear(100, 104.5, bias, params) {
		t.Fatal("target with clean room before opposing structure was rejected")
	}
}
