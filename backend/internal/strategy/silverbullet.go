// Package strategy implements the user's HTF 3+1 execution model for
// crypto perpetuals:
//
//  1. A fully-closed H1 candle sweeps a prior H1 swing high/low and closes
//     back inside. That determines the only allowed trade direction.
//  2. After the H1 sweep is confirmed, M5 must print a genuine CHOCH
//     (Change of Character / structure shift) with displacement.
//  3. The displacement must leave a fresh M5 FVG; the last opposite candle
//     before displacement is also tracked as the M5 order block.
//  4. Entry occurs only on the first retest of the fresh FVG or order block
//     when that retest candle rejects the zone. A first touch without a
//     qualifying rejection consumes that zone permanently.
//
// There is deliberately no time-of-day gate. Risk/reward is fixed at 1:1.5.
// H1 candles are derived from M5 data using only completed H1 buckets, so
// both live evaluation and historical replay are free of higher-timeframe
// lookahead.
package strategy

import (
	"fmt"
	"math"
	"sort"
	"time"

	"cryptotrading/internal/models"
)

const fixedRiskRewardRatio = 1.5

// AnchorSymbols and AnchorSymbolFor remain for source compatibility with the
// older SMT implementation. HTF 3+1 no longer uses cross-symbol SMT as a hard
// gate; direction now comes from the traded symbol's own closed H1 structure.
var AnchorSymbols = []string{"BTC-USDT", "ETH-USDT"}

func AnchorSymbolFor(symbol string) string {
	if symbol == "BTC-USDT" {
		return "ETH-USDT"
	}
	return "BTC-USDT"
}

// SBParams contains the small parameter set used by the optimizer. The first
// five JSON keys are retained so existing saved rows upgrade safely, but their
// meanings now match HTF 3+1 rather than the retired M5-only Silver Bullet:
// SwingLookback and MaxBarsForSweep are measured in closed H1 candles.
type SBParams struct {
	SwingLookback   int     `json:"swing_lookback"`     // prior H1 candles used to define liquidity
	MinFVGSizePct   float64 `json:"min_fvg_size_pct"`   // minimum M5 FVG size as percentage of price
	MaxBarsForSweep int     `json:"max_bars_for_sweep"` // H1 candles for which the latest sweep bias remains valid
	StopBufferPct   float64 `json:"stop_buffer_pct"`    // stop beyond the selected M5 FVG/OB edge
	RiskRewardRatio float64 `json:"risk_reward_ratio"`  // fixed to 1.5 when loaded/saved

	MinDisplacementBodyPct float64 `json:"min_displacement_body_pct"` // M5 displacement body/range threshold
	MaxOpposingWickPct     float64 `json:"max_opposing_wick_pct"`     // maximum opposing wick/range
	CHOCHLookback          int     `json:"choch_lookback"`            // M5 bars whose high/low must be closed through
	MaxBarsForRetest       int     `json:"max_bars_for_retest"`       // M5 bars allowed for the first retest
	OrderBlockLookback     int     `json:"order_block_lookback"`      // M5 bars searched for the last opposite candle
	RequireRejection       bool    `json:"require_rejection"`         // first retest must close back out of the zone
}

func DefaultSBParams() SBParams {
	return SBParams{
		SwingLookback:   12,
		MinFVGSizePct:   0.05,
		MaxBarsForSweep: 4,
		StopBufferPct:   0.1,
		RiskRewardRatio: 1.5,

		MinDisplacementBodyPct: 0.6,
		MaxOpposingWickPct:     0.25,
		CHOCHLookback:          5,
		MaxBarsForRetest:       6,
		OrderBlockLookback:     10,
		RequireRejection:       true,
	}
}

// RequiredM5History is the minimum live candle window needed to build the H1
// liquidity lookback and still retain enough M5 bars for the execution leg.
func RequiredM5History(params SBParams) int {
	h1Bars := max(2, params.SwingLookback) + 1
	return h1Bars*12 + max(1, params.MaxBarsForRetest) + max(2, params.CHOCHLookback) + 3
}

type HTFBias struct {
	Action      models.SignalAction
	SweepTs     time.Time
	SweepPrice  float64
	ConfirmedAt time.Time
	Valid       bool
}

type SBSignal struct {
	Action models.SignalAction
	Reason string

	SweepTs         time.Time
	SweepPrice      float64
	FVGTs           time.Time
	FVGLow, FVGHigh float64

	DisplacementBodyPct float64
	CHOCHTs             time.Time
	CHOCHLevel          float64
	EntryZoneType       string

	// Kept under the legacy Breaker JSON/database field names so existing
	// installations need no destructive migration. They now contain the M5
	// order-block body zone, not a Breaker Block.
	BreakerLow, BreakerHigh float64

	// Legacy fields retained for older API consumers. HTF 3+1 does not use
	// OTE or SMT as hard gates and therefore leaves these zero-valued.
	OTELow, OTEHigh float64
	SMTAnchorSymbol string
	SMTConfirmed    bool

	Entry, StopLoss, TakeProfit float64
}

type h1Bar struct {
	Start, End time.Time
	Open       float64
	High       float64
	Low        float64
	Close      float64
	Volume     float64
}

type h1Sweep struct {
	Action    models.SignalAction
	Ts        time.Time
	Price     float64
	Confirmed time.Time
	H1Index   int
}

// PrepareHTFBiases precomputes the no-lookahead H1 direction available at
// every M5 bar. Backtests call this once per parameter set; the live path calls
// DecideSilverBullet, which uses the same function and reads only the latest
// element. Returning a slice aligned with candles makes the no-lookahead rule
// explicit and testable.
func PrepareHTFBiases(candles []models.Candle, params SBParams) []HTFBias {
	out := make([]HTFBias, len(candles))
	if len(candles) == 0 || params.SwingLookback < 2 || params.MaxBarsForSweep < 1 {
		return out
	}
	interval := inferCandleInterval(candles)
	h1 := aggregateCompleteH1(candles, interval)
	if len(h1) <= params.SwingLookback {
		return out
	}

	sweeps := detectH1Sweeps(h1, params.SwingLookback)
	if len(sweeps) == 0 {
		return out
	}

	closedH1 := -1
	nextSweep := 0
	var latest h1Sweep
	hasLatest := false
	for i, candle := range candles {
		closedThrough := candle.Ts.Add(interval)
		for closedH1+1 < len(h1) && !h1[closedH1+1].End.After(closedThrough) {
			closedH1++
		}
		for nextSweep < len(sweeps) && sweeps[nextSweep].H1Index <= closedH1 {
			latest = sweeps[nextSweep]
			hasLatest = true
			nextSweep++
		}
		biasAge := closedThrough.Sub(latest.Confirmed)
		if !hasLatest || closedH1 < latest.H1Index || biasAge < 0 || biasAge >= time.Duration(params.MaxBarsForSweep)*time.Hour {
			continue
		}
		out[i] = HTFBias{
			Action: latest.Action, SweepTs: latest.Ts, SweepPrice: latest.Price,
			ConfirmedAt: latest.Confirmed, Valid: true,
		}
	}
	return out
}

// DecideSilverBullet keeps the established function name so the surrounding
// app can upgrade without a risky, all-at-once API rewrite. anchorCandles is
// intentionally ignored: H1 structure on the traded symbol is now the hard
// direction gate.
func DecideSilverBullet(candles []models.Candle, anchorCandles []models.Candle, params SBParams, asOf time.Time) SBSignal {
	_ = anchorCandles
	_ = asOf
	if len(candles) == 0 {
		return SBSignal{Action: models.SignalHold, Reason: "資料不足"}
	}
	biases := PrepareHTFBiases(candles, params)
	return DecideSilverBulletWithBias(candles, params, biases[len(biases)-1], candles[len(candles)-1].Ts)
}

// DecideSilverBulletWithBias evaluates the M5 execution leg using a bias
// already proven to be available at the current bar. It is exported for the
// backtest engine so H1 aggregation is not repeated on every M5 candle.
func DecideSilverBulletWithBias(candles []models.Candle, params SBParams, bias HTFBias, asOf time.Time) SBSignal {
	_ = asOf
	if !bias.Valid || (bias.Action != models.SignalBuy && bias.Action != models.SignalSell) {
		return SBSignal{Action: models.SignalHold, Reason: "尚無已收線H1流動性掃掠方向"}
	}
	n := len(candles) - 1
	minBars := params.CHOCHLookback + 4
	if n < minBars {
		return SBSignal{Action: models.SignalHold, Reason: "M5資料不足"}
	}
	bullish := bias.Action == models.SignalBuy
	lo := n - params.MaxBarsForRetest
	if lo < params.CHOCHLookback+2 {
		lo = params.CHOCHLookback + 2
	}

	for fvgIdx := n - 1; fvgIdx >= lo; fvgIdx-- {
		if fvgIdx < 2 {
			break
		}
		c0, displacement, c2 := candles[fvgIdx-2], candles[fvgIdx-1], candles[fvgIdx]
		if displacement.Ts.Before(bias.ConfirmedAt) {
			continue
		}

		gapLow, gapHigh, gapOK := fairValueGap(c0, c2, bullish)
		if !gapOK || !fvgSizeOK(gapLow, gapHigh, c2.Close, params.MinFVGSizePct) {
			continue
		}
		bodyPct, displacementOK := displacementQuality(displacement, bullish, params)
		if !displacementOK {
			continue
		}
		chochLevel, chochOK := confirmsCHOCH(candles, fvgIdx-1, params.CHOCHLookback, bullish)
		if !chochOK {
			continue
		}

		obLow, obHigh, obFound := findOrderBlock(candles, fvgIdx-1, params.OrderBlockLookback, bullish)
		zones := []entryZone{{low: gapLow, high: gapHigh, kind: "FVG", formedAt: fvgIdx}}
		if obFound {
			// The order block exists as soon as displacement closes. Therefore
			// c2 is already a possible first retest of the OB, whereas the FVG
			// itself does not exist until c2 closes.
			zones = append(zones, entryZone{low: obLow, high: obHigh, kind: "OB", formedAt: fvgIdx - 1})
		}
		for _, zone := range zones {
			if !firstRetestRejects(candles, n, zone, bullish, params.RequireRejection) {
				continue
			}
			entry := candles[n].Close
			stop := zone.low * (1 - params.StopBufferPct/100)
			if !bullish {
				stop = zone.high * (1 + params.StopBufferPct/100)
			}
			risk := entry - stop
			if !bullish {
				risk = stop - entry
			}
			if risk <= 0 {
				continue
			}
			target := entry + fixedRiskRewardRatio*risk
			if !bullish {
				target = entry - fixedRiskRewardRatio*risk
			}

			direction := "做多"
			sweepSide := "前低"
			if !bullish {
				direction, sweepSide = "做空", "前高"
			}
			return SBSignal{
				Action: bias.Action,
				Reason: fmt.Sprintf(
					"H1掃%s後收回定向%s＋M5 CHOCH突破%.6g＋位移FVG(實體%.0f%%)＋Fresh %s首次回踩拒絕，RR固定1:1.5",
					sweepSide, direction, chochLevel, bodyPct*100, zone.kind,
				),
				SweepTs: bias.SweepTs, SweepPrice: bias.SweepPrice,
				FVGTs: c2.Ts, FVGLow: gapLow, FVGHigh: gapHigh,
				DisplacementBodyPct: bodyPct,
				CHOCHTs:             displacement.Ts, CHOCHLevel: chochLevel,
				EntryZoneType: zone.kind,
				BreakerLow:    obLow, BreakerHigh: obHigh,
				Entry: entry, StopLoss: stop, TakeProfit: target,
			}
		}
	}
	return SBSignal{Action: models.SignalHold, Reason: "H1方向成立，但尚無M5 CHOCH＋位移FVG／OB首次回踩拒絕"}
}

type entryZone struct {
	low, high float64
	kind      string
	formedAt  int
}

func fairValueGap(c0, c2 models.Candle, bullish bool) (low, high float64, ok bool) {
	if bullish && c0.High < c2.Low {
		return c0.High, c2.Low, true
	}
	if !bullish && c0.Low > c2.High {
		return c2.High, c0.Low, true
	}
	return 0, 0, false
}

func fvgSizeOK(low, high, price, minPct float64) bool {
	return price > 0 && high > low && (high-low)/price*100 >= minPct
}

func displacementQuality(c models.Candle, bullish bool, params SBParams) (float64, bool) {
	rng := c.High - c.Low
	if rng <= 0 {
		return 0, false
	}
	if bullish {
		if c.Close <= c.Open {
			return 0, false
		}
		body := (c.Close - c.Open) / rng
		opposingWick := (math.Min(c.Open, c.Close) - c.Low) / rng
		return body, body >= params.MinDisplacementBodyPct && opposingWick <= params.MaxOpposingWickPct
	}
	if c.Close >= c.Open {
		return 0, false
	}
	body := (c.Open - c.Close) / rng
	opposingWick := (c.High - math.Max(c.Open, c.Close)) / rng
	return body, body >= params.MinDisplacementBodyPct && opposingWick <= params.MaxOpposingWickPct
}

func confirmsCHOCH(candles []models.Candle, displacementIdx, lookback int, bullish bool) (float64, bool) {
	if lookback < 2 || displacementIdx < lookback {
		return 0, false
	}
	window := candles[displacementIdx-lookback : displacementIdx]
	if bullish {
		level := window[0].High
		for _, c := range window[1:] {
			level = math.Max(level, c.High)
		}
		return level, candles[displacementIdx].Close > level
	}
	level := window[0].Low
	for _, c := range window[1:] {
		level = math.Min(level, c.Low)
	}
	return level, candles[displacementIdx].Close < level
}

// findOrderBlock returns the body of the last opposite-coloured M5 candle
// before displacement. It is an entry zone, not an additional hard
// confluence requirement: a fresh FVG or a fresh OB may trigger the setup.
func findOrderBlock(candles []models.Candle, displacementIdx, lookback int, bullish bool) (float64, float64, bool) {
	earliest := max(0, displacementIdx-lookback)
	for i := displacementIdx - 1; i >= earliest; i-- {
		c := candles[i]
		if bullish && c.Close < c.Open {
			return math.Min(c.Open, c.Close), math.Max(c.Open, c.Close), true
		}
		if !bullish && c.Close > c.Open {
			return math.Min(c.Open, c.Close), math.Max(c.Open, c.Close), true
		}
	}
	return 0, 0, false
}

func firstRetestRejects(candles []models.Candle, currentIdx int, zone entryZone, bullish, requireRejection bool) bool {
	if currentIdx <= zone.formedAt || currentIdx >= len(candles) {
		return false
	}
	for i := zone.formedAt + 1; i < currentIdx; i++ {
		if touchesZone(candles[i], zone) {
			return false // first touch already happened: zone is consumed
		}
	}
	current := candles[currentIdx]
	if !touchesZone(current, zone) {
		return false
	}
	if !requireRejection {
		return true
	}
	previous := candles[currentIdx-1]
	rng := current.High - current.Low
	if rng <= 0 {
		return false
	}
	body := math.Abs(current.Close - current.Open)
	if bullish {
		closesOut := current.Close > zone.high
		lowerWick := math.Min(current.Open, current.Close) - current.Low
		wickReject := current.Close > current.Open && lowerWick >= math.Max(body*0.5, rng*0.2)
		engulf := previous.Close < previous.Open && current.Close >= previous.Open && current.Open <= previous.Close
		return closesOut && (wickReject || engulf)
	}
	closesOut := current.Close < zone.low
	upperWick := current.High - math.Max(current.Open, current.Close)
	wickReject := current.Close < current.Open && upperWick >= math.Max(body*0.5, rng*0.2)
	engulf := previous.Close > previous.Open && current.Close <= previous.Open && current.Open >= previous.Close
	return closesOut && (wickReject || engulf)
}

func touchesZone(c models.Candle, zone entryZone) bool {
	return c.Low <= zone.high && c.High >= zone.low
}

func inferCandleInterval(candles []models.Candle) time.Duration {
	var diffs []time.Duration
	start := max(1, len(candles)-30)
	for i := start; i < len(candles); i++ {
		d := candles[i].Ts.Sub(candles[i-1].Ts)
		if d > 0 && d <= time.Hour {
			diffs = append(diffs, d)
		}
	}
	if len(diffs) == 0 {
		return 5 * time.Minute
	}
	sort.Slice(diffs, func(i, j int) bool { return diffs[i] < diffs[j] })
	return diffs[len(diffs)/2]
}

func aggregateCompleteH1(candles []models.Candle, interval time.Duration) []h1Bar {
	if len(candles) == 0 || interval <= 0 || interval > time.Hour {
		return nil
	}
	type bucket struct {
		bar         h1Bar
		first, last time.Time
		count       int
	}
	var buckets []bucket
	for _, c := range candles {
		start := c.Ts.UTC().Truncate(time.Hour)
		if len(buckets) == 0 || !buckets[len(buckets)-1].bar.Start.Equal(start) {
			buckets = append(buckets, bucket{
				bar:   h1Bar{Start: start, End: start.Add(time.Hour), Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume},
				first: c.Ts, last: c.Ts, count: 1,
			})
			continue
		}
		b := &buckets[len(buckets)-1]
		b.bar.High = math.Max(b.bar.High, c.High)
		b.bar.Low = math.Min(b.bar.Low, c.Low)
		b.bar.Close = c.Close
		b.bar.Volume += c.Volume
		b.last = c.Ts
		b.count++
	}

	expected := int(time.Hour / interval)
	var out []h1Bar
	for _, b := range buckets {
		complete := b.count >= expected && b.first.Equal(b.bar.Start) && !b.last.Add(interval).Before(b.bar.End)
		if complete {
			out = append(out, b.bar)
		}
	}
	return out
}

func detectH1Sweeps(bars []h1Bar, lookback int) []h1Sweep {
	var out []h1Sweep
	for i := lookback; i < len(bars); i++ {
		priorLow, priorHigh := bars[i-lookback].Low, bars[i-lookback].High
		for _, b := range bars[i-lookback+1 : i] {
			priorLow = math.Min(priorLow, b.Low)
			priorHigh = math.Max(priorHigh, b.High)
		}
		bull := bars[i].Low < priorLow && bars[i].Close > priorLow
		bear := bars[i].High > priorHigh && bars[i].Close < priorHigh
		if bull == bear { // neither, or an ambiguous outside bar sweeping both
			continue
		}
		action, price := models.SignalBuy, bars[i].Low
		if bear {
			action, price = models.SignalSell, bars[i].High
		}
		out = append(out, h1Sweep{Action: action, Ts: bars[i].Start, Price: price, Confirmed: bars[i].End, H1Index: i})
	}
	return out
}
