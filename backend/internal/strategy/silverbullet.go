// Package strategy's Silver Bullet detector implements the ICT (Inner
// Circle Trader) "Silver Bullet" setup, upgraded to a fuller "2026-era"
// rule set beyond the original bare sweep+FVG pattern:
//
//  1. Liquidity sweep: price wicks beyond a recent swing high/low then
//     closes back inside (a stop-hunt) - unchanged from the original design.
//  2. Displacement-quality Fair Value Gap: the 3-candle imbalance following
//     the sweep must be formed by a genuine "displacement" candle (large
//     body, small opposing wick) - a weak/indecisive candle creating the
//     gap is treated as noise, not a real institutional move.
//  3. Optimal Trade Entry (OTE): rather than entering at the FVG-confirming
//     candle's close, the setup waits (up to MaxBarsForOTE bars) for price
//     to retrace into the 62%-79% Fibonacci zone of the sweep-to-displacement
//     leg before triggering - a tighter, better risk:reward entry than an
//     immediate market fill.
//  4. Unicorn Model confluence: the OTE zone must overlap a nearby Breaker
//     Block (the last opposite-colored candle before the sweep, i.e. the
//     order block the sweep broke) - requiring multiple independent ICT
//     concepts to agree on the same price region, not just one.
//  5. SMT (Smart Money Technique) divergence: a correlated anchor symbol
//     (see AnchorSymbolFor) must NOT confirm the same extreme - e.g. for a
//     bullish reversal, the anchor should hold above its own recent swing
//     low while this symbol sweeps below its own, indicating the sweep is
//     an isolated stop-hunt rather than broad-market weakness.
//
// This replaced the project's original MA-cross/RSI/VWAP/MACD rule
// (rule_strategy.go, removed), then the bare sweep+FVG-only Silver Bullet
// detector, as the local rule that gates the AI confirmation step - see
// internal/autotrader/watcher.go.
//
// Unlike ICT's original equities/forex-session-based definition, this
// detector does NOT gate on a time-of-day window (e.g. the classic NY
// 10:00-11:00/14:00-15:00 "Silver Bullet" hours) - by explicit user
// decision, since BingX perpetuals trade 24/7 and there's no equivalent
// session structure to key off. A qualifying setup is evaluated whenever it
// occurs, at any hour.
package strategy

import (
	"fmt"
	"math"
	"time"

	"cryptotrading/internal/models"
)

// AnchorSymbols are the fixed SMT-divergence reference symbols. They are
// kept streaming/candle-cached at all times regardless of the user's
// trading watchlist (see cmd/server/streams.go), since the daily AI
// watchlist selection (internal/watchlistai) can freely drop BTC/ETH from
// the tradable set while SMT confirmation still needs their live structure.
var AnchorSymbols = []string{"BTC-USDT", "ETH-USDT"}

// AnchorSymbolFor returns the correlated reference symbol used for SMT
// divergence confirmation: BTCUSDT for everything except BTCUSDT itself,
// which uses ETHUSDT instead.
func AnchorSymbolFor(symbol string) string {
	if symbol == "BTC-USDT" {
		return "ETH-USDT"
	}
	return "BTC-USDT"
}

// SBParams are the tunable Silver Bullet parameters. The first five are
// grid-searched by internal/backtest/optimize.go; the rest are fixed
// structural rules from the ICT 2026 upgrade (displacement/OTE/breaker/SMT)
// deliberately left OUT of the grid search - they encode well-established
// ICT thresholds rather than free parameters to curve-fit, which is the
// point of adding them. See internal/strategy/params.go for how the
// grid-searched set is persisted/loaded.
type SBParams struct {
	SwingLookback   int     `json:"swing_lookback"`     // bars back to establish the "liquidity" swing high/low
	MinFVGSizePct   float64 `json:"min_fvg_size_pct"`   // minimum gap size as % of price, filters noise
	MaxBarsForSweep int     `json:"max_bars_for_sweep"` // how many bars before the FVG to search for a qualifying sweep
	StopBufferPct   float64 `json:"stop_buffer_pct"`    // stop placed this % beyond the sweep extreme
	RiskRewardRatio float64 `json:"risk_reward_ratio"`  // take-profit = entry + RR * (entry - stop)

	// --- ICT 2026 upgrade (fixed, not grid-searched) ---
	MinDisplacementBodyPct   float64 `json:"min_displacement_body_pct"`  // displacement candle body/range must be >= this
	MaxOpposingWickPct       float64 `json:"max_opposing_wick_pct"`      // displacement candle's opposing-side wick/range must be <= this
	OTEMinRetrace            float64 `json:"ote_min_retrace"`            // shallow bound of the OTE Fibonacci zone (e.g. 0.62)
	OTEMaxRetrace            float64 `json:"ote_max_retrace"`            // deep bound of the OTE Fibonacci zone (e.g. 0.79)
	MaxBarsForOTE            int     `json:"max_bars_for_ote"`           // bars to wait after the FVG for price to retrace into the OTE zone before the setup expires
	BreakerLookback          int     `json:"breaker_lookback"`           // bars back from the sweep to search for the order-block/breaker candle
	RequireBreakerConfluence bool    `json:"require_breaker_confluence"` // Unicorn Model: OTE zone must overlap a breaker block
	RequireSMTDivergence     bool    `json:"require_smt_divergence"`     // anchor symbol must NOT confirm the same sweep extreme
}

// DefaultSBParams is the seed configuration before any backtest-driven
// tuning. The ICT-2026 thresholds are well-established community values
// (62%-79% OTE, a strong-bodied/small-wick displacement candle) rather than
// anything backtest-fitted.
func DefaultSBParams() SBParams {
	return SBParams{
		SwingLookback:   20,
		MinFVGSizePct:   0.1,
		MaxBarsForSweep: 3,
		StopBufferPct:   0.1,
		RiskRewardRatio: 1.5,

		MinDisplacementBodyPct:   0.6,
		MaxOpposingWickPct:       0.25,
		OTEMinRetrace:            0.62,
		OTEMaxRetrace:            0.79,
		MaxBarsForOTE:            8,
		BreakerLookback:          10,
		RequireBreakerConfluence: true,
		RequireSMTDivergence:     true,
	}
}

// SBSignal is the outcome of one DecideSilverBullet evaluation.
type SBSignal struct {
	Action models.SignalAction
	Reason string

	// SweepTs/FVGTs/FVGLow/FVGHigh are only meaningful when Action is
	// BUY/SELL. FVGTs (the bar timestamp where the FVG confirmed) is the
	// dedup key callers use to detect "is this a genuinely new setup" - see
	// internal/autotrader.Trader.lastSignaledFVG.
	SweepTs         time.Time
	SweepPrice      float64
	FVGTs           time.Time
	FVGLow, FVGHigh float64

	// DisplacementBodyPct is the FVG's displacement candle body/range ratio
	// that qualified it as a genuine impulse move rather than noise.
	DisplacementBodyPct float64

	// OTELow/OTEHigh is the 62%-79% Fibonacci retracement zone of the
	// sweep-to-displacement leg that Entry was filled within.
	OTELow, OTEHigh float64

	// BreakerLow/BreakerHigh is the nearby order-block/breaker candle's body
	// range, when one was found (zero if none was found nearby - this is
	// populated for display even when RequireBreakerConfluence is off).
	BreakerLow, BreakerHigh float64

	// SMTAnchorSymbol/SMTConfirmed describe the cross-symbol divergence
	// check: did the anchor symbol fail to confirm the same sweep extreme.
	SMTAnchorSymbol string
	SMTConfirmed    bool

	Entry, StopLoss, TakeProfit float64
}

// DecideSilverBullet evaluates candles (this symbol) against anchorCandles
// (see AnchorSymbolFor - may be nil/empty, in which case SMT confirmation
// fails closed whenever RequireSMTDivergence is set) for a qualifying
// setup. Pure function, no lookahead: only ever reads candles[:len(candles)]
// and anchorCandles[:len(anchorCandles)], as if `asOf` were "right now".
// `asOf` is kept as a parameter for forward compatibility (backtest replay
// passes the historical bar's own timestamp) though unused directly here -
// candles[len(candles)-1].Ts already serves that role.
func DecideSilverBullet(candles []models.Candle, anchorCandles []models.Candle, params SBParams, asOf time.Time) SBSignal {
	_ = asOf
	n := len(candles) - 1
	minNeeded := params.SwingLookback + params.MaxBarsForSweep + 2
	if n < minNeeded || params.SwingLookback < 1 {
		return SBSignal{Action: models.SignalHold, Reason: "資料不足"}
	}

	if sig, ok := tryDirection(candles, anchorCandles, params, n, true); ok {
		return sig
	}
	if sig, ok := tryDirection(candles, anchorCandles, params, n, false); ok {
		return sig
	}
	return SBSignal{Action: models.SignalHold, Reason: "無符合條件的sweep+位移FVG+OTE組合"}
}

// tryDirection scans backward from the current bar n for the most recent
// FVG (within MaxBarsForOTE bars) that has a qualifying preceding sweep and
// displacement candle, an OTE entry that has just now (bar n, first touch,
// not yet invalidated) been reached, and - if required - a breaker-block
// confluence and SMT divergence confirmation. bullish=true looks for a
// downside-sweep/long setup; false looks for an upside-sweep/short setup.
func tryDirection(candles, anchorCandles []models.Candle, params SBParams, n int, bullish bool) (SBSignal, bool) {
	lo := n - params.MaxBarsForOTE
	minLo := params.SwingLookback + params.MaxBarsForSweep
	if lo < minLo {
		lo = minLo
	}

	for k := n; k >= lo; k-- {
		if k < 2 {
			break
		}
		c0, c1, c2 := candles[k-2], candles[k-1], candles[k]

		var gapLow, gapHigh float64
		if bullish {
			if !(c0.High < c2.Low) {
				continue
			}
			gapLow, gapHigh = c0.High, c2.Low
		} else {
			if !(c0.Low > c2.High) {
				continue
			}
			gapLow, gapHigh = c2.High, c0.Low
		}
		if !fvgSizeOK(gapLow, gapHigh, c2.Close, params.MinFVGSizePct) {
			continue
		}

		sweepIdx, sweepPrice, found := findRecentSweep(candles, k-2, params, bullish)
		if !found {
			continue
		}

		bodyPct, dispOK := displacementQuality(c1, bullish, params)
		if !dispOK {
			continue
		}

		legExtreme := c1.High
		if !bullish {
			legExtreme = c1.Low
		}
		if bullish && legExtreme <= sweepPrice {
			continue
		}
		if !bullish && legExtreme >= sweepPrice {
			continue
		}
		oteLow, oteHigh, oteEntry := oteZone(sweepPrice, legExtreme, params, bullish)

		var stop float64
		if bullish {
			stop = sweepPrice * (1 - params.StopBufferPct/100)
		} else {
			stop = sweepPrice * (1 + params.StopBufferPct/100)
		}

		var breakerLow, breakerHigh float64
		if bLow, bHigh, bFound := findBreakerBlock(candles, sweepIdx, params, bullish); bFound {
			breakerLow, breakerHigh = bLow, bHigh
			if params.RequireBreakerConfluence && !rangesOverlap(bLow, bHigh, oteLow, oteHigh) {
				continue
			}
		} else if params.RequireBreakerConfluence {
			continue
		}

		if !oteEntryTriggered(candles, k, n, stop, oteLow, oteHigh, bullish) {
			continue
		}

		var risk float64
		if bullish {
			risk = oteEntry - stop
		} else {
			risk = stop - oteEntry
		}
		if risk <= 0 {
			continue
		}
		var target float64
		if bullish {
			target = oteEntry + params.RiskRewardRatio*risk
		} else {
			target = oteEntry - params.RiskRewardRatio*risk
		}

		sig := SBSignal{
			SweepTs: candles[sweepIdx].Ts, SweepPrice: sweepPrice,
			FVGTs: c2.Ts, FVGLow: gapLow, FVGHigh: gapHigh,
			DisplacementBodyPct: bodyPct,
			OTELow:              oteLow, OTEHigh: oteHigh,
			BreakerLow: breakerLow, BreakerHigh: breakerHigh,
			Entry: oteEntry, StopLoss: stop, TakeProfit: target,
		}

		if params.RequireSMTDivergence {
			confirmed, note := checkSMT(candles, anchorCandles, sweepIdx, candles[n].Ts, params, bullish)
			sig.SMTConfirmed = confirmed
			if len(anchorCandles) > 0 {
				sig.SMTAnchorSymbol = anchorCandles[0].Symbol
			}
			if !confirmed {
				return SBSignal{Action: models.SignalHold, Reason: "OTE進場條件成立但SMT背離未確認：" + note}, true
			}
		}

		dirWord, action := "做多", models.SignalBuy
		if !bullish {
			dirWord, action = "做空", models.SignalSell
		}
		sig.Action = action
		reason := fmt.Sprintf("掃%s(%.6g)+位移FVG(實體%.0f%%,%.6g~%.6g)+OTE進場(%.6g)",
			sweepSideLabel(bullish), sweepPrice, bodyPct*100, gapLow, gapHigh, oteEntry)
		if params.RequireBreakerConfluence {
			reason += fmt.Sprintf("+Breaker重疊(%.6g~%.6g)", breakerLow, breakerHigh)
		}
		if params.RequireSMTDivergence {
			reason += fmt.Sprintf("+SMT背離確認(%s)", sig.SMTAnchorSymbol)
		}
		reason += "，符合ICT2026 Silver Bullet" + dirWord + "條件"
		sig.Reason = reason
		return sig, true
	}
	return SBSignal{}, false
}

func sweepSideLabel(bullish bool) string {
	if bullish {
		return "底"
	}
	return "頂"
}

func fvgSizeOK(gapLow, gapHigh, price, minPct float64) bool {
	if price <= 0 {
		return false
	}
	return (gapHigh-gapLow)/price*100 >= minPct
}

// displacementQuality checks whether c is a genuine "displacement" candle -
// large body relative to its range, with a small opposing-side wick -
// qualifying the FVG it forms as an institutional impulse move rather than
// noise. Returns the body/range ratio for display even when it fails.
func displacementQuality(c models.Candle, bullish bool, params SBParams) (bodyPct float64, ok bool) {
	rng := c.High - c.Low
	if rng <= 0 {
		return 0, false
	}
	if bullish {
		if c.Close <= c.Open {
			return 0, false
		}
		bodyPct = (c.Close - c.Open) / rng
		opposingWickPct := (math.Min(c.Open, c.Close) - c.Low) / rng
		return bodyPct, bodyPct >= params.MinDisplacementBodyPct && opposingWickPct <= params.MaxOpposingWickPct
	}
	if c.Close >= c.Open {
		return 0, false
	}
	bodyPct = (c.Open - c.Close) / rng
	opposingWickPct := (c.High - math.Max(c.Open, c.Close)) / rng
	return bodyPct, bodyPct >= params.MinDisplacementBodyPct && opposingWickPct <= params.MaxOpposingWickPct
}

// oteZone computes the 62%-79% Fibonacci retracement zone of the leg from
// sweepPrice (the manipulation extreme, "A") to legExtreme (the extreme of
// the displacement candle, "B"), plus the mid-zone (~70.5%) entry level -
// the classic ICT "Optimal Trade Entry" reference point.
func oteZone(sweepPrice, legExtreme float64, params SBParams, bullish bool) (low, high, entry float64) {
	mid := (params.OTEMinRetrace + params.OTEMaxRetrace) / 2
	if bullish {
		rangeAB := legExtreme - sweepPrice
		high = legExtreme - params.OTEMinRetrace*rangeAB
		low = legExtreme - params.OTEMaxRetrace*rangeAB
		entry = legExtreme - mid*rangeAB
		return
	}
	rangeAB := sweepPrice - legExtreme
	low = legExtreme + params.OTEMinRetrace*rangeAB
	high = legExtreme + params.OTEMaxRetrace*rangeAB
	entry = legExtreme + mid*rangeAB
	return
}

// oteEntryTriggered reports whether bar n is the FIRST bar (since fvgIdx)
// whose range touches the OTE zone, with no intervening bar having closed
// beyond the invalidation stop. This is what turns "a qualifying setup
// exists somewhere in recent history" into "enter right now" - the setup
// must not have already been actionable on an earlier bar (that call would
// have fired then) nor already invalidated.
func oteEntryTriggered(candles []models.Candle, fvgIdx, n int, stop, oteLow, oteHigh float64, bullish bool) bool {
	for j := fvgIdx + 1; j < n; j++ {
		c := candles[j]
		if bullish {
			if c.Close < stop || c.Low <= oteHigh {
				return false
			}
		} else {
			if c.Close > stop || c.High >= oteLow {
				return false
			}
		}
	}
	c := candles[n]
	if bullish {
		return c.Close >= stop && c.Low <= oteHigh
	}
	return c.Close <= stop && c.High >= oteLow
}

// findBreakerBlock scans backward from just before sweepIdx for the most
// recent opposite-colored candle - the classic ICT "order block" - whose
// body range becomes the Breaker Block once the sweep takes it out. bullish
// setups look for the last bearish (down-close) candle; bearish setups look
// for the last bullish (up-close) candle.
func findBreakerBlock(candles []models.Candle, sweepIdx int, params SBParams, bullish bool) (low, high float64, ok bool) {
	earliest := max(sweepIdx-params.BreakerLookback, 0)
	for j := sweepIdx - 1; j >= earliest; j-- {
		c := candles[j]
		if bullish && c.Close < c.Open {
			return math.Min(c.Open, c.Close), math.Max(c.Open, c.Close), true
		}
		if !bullish && c.Close > c.Open {
			return math.Min(c.Open, c.Close), math.Max(c.Open, c.Close), true
		}
	}
	return 0, 0, false
}

func rangesOverlap(aLow, aHigh, bLow, bHigh float64) bool {
	return aLow <= bHigh && bLow <= aHigh
}

// checkSMT compares this symbol's swept extreme against the anchor symbol's
// own structure over the same window: for a bullish setup, divergence is
// confirmed when the anchor did NOT make an equally deep new low (it held
// above its own prior swing low) while this symbol did - suggesting the
// sweep is an isolated stop-hunt on this symbol rather than broad-market
// weakness. Symmetric for bearish (new highs). Fails closed (not confirmed)
// when anchor data is missing or too short to compare.
func checkSMT(candles, anchorCandles []models.Candle, sweepIdx int, nowTs time.Time, params SBParams, bullish bool) (bool, string) {
	if len(anchorCandles) < params.SwingLookback+2 {
		return false, "錨定幣種資料不足"
	}
	sweepTs := candles[sweepIdx].Ts
	aSweep := nearestIndexAtOrBefore(anchorCandles, sweepTs)
	aNow := nearestIndexAtOrBefore(anchorCandles, nowTs)
	if aSweep < params.SwingLookback || aNow < aSweep {
		return false, "錨定幣種資料不足以比對結構"
	}

	if bullish {
		priorLow := anchorCandles[aSweep-params.SwingLookback].Low
		for _, c := range anchorCandles[aSweep-params.SwingLookback : aSweep] {
			if c.Low < priorLow {
				priorLow = c.Low
			}
		}
		sinceLow := anchorCandles[aSweep].Low
		for _, c := range anchorCandles[aSweep : aNow+1] {
			if c.Low < sinceLow {
				sinceLow = c.Low
			}
		}
		if sinceLow > priorLow {
			return true, fmt.Sprintf("錨定幣種未同步破底(前低%.6g，期間低點%.6g)", priorLow, sinceLow)
		}
		return false, fmt.Sprintf("錨定幣種同步破底(前低%.6g，期間低點%.6g)，無背離", priorLow, sinceLow)
	}

	priorHigh := anchorCandles[aSweep-params.SwingLookback].High
	for _, c := range anchorCandles[aSweep-params.SwingLookback : aSweep] {
		if c.High > priorHigh {
			priorHigh = c.High
		}
	}
	sinceHigh := anchorCandles[aSweep].High
	for _, c := range anchorCandles[aSweep : aNow+1] {
		if c.High > sinceHigh {
			sinceHigh = c.High
		}
	}
	if sinceHigh < priorHigh {
		return true, fmt.Sprintf("錨定幣種未同步創高(前高%.6g，期間高點%.6g)", priorHigh, sinceHigh)
	}
	return false, fmt.Sprintf("錨定幣種同步創高(前高%.6g，期間高點%.6g)，無背離", priorHigh, sinceHigh)
}

func nearestIndexAtOrBefore(candles []models.Candle, ts time.Time) int {
	idx := -1
	for i, c := range candles {
		if c.Ts.After(ts) {
			break
		}
		idx = i
	}
	return idx
}

// findRecentSweep scans backward from index `before` (the FVG's first
// candle, inclusive) through params.MaxBarsForSweep bars for a bar that
// swept beyond the swing high/low established over the SwingLookback bars
// immediately preceding it, then closed back inside (a rejection). Returns
// the most recent (highest-index) qualifying bar. wantLowSweep=true looks
// for a downside sweep (bullish setup); false looks for an upside sweep
// (bearish setup).
func findRecentSweep(candles []models.Candle, before int, params SBParams, wantLowSweep bool) (idx int, extremePrice float64, ok bool) {
	earliest := max(before-params.MaxBarsForSweep+1, params.SwingLookback)
	for j := before; j >= earliest; j-- {
		lookback := candles[j-params.SwingLookback : j]
		if wantLowSweep {
			swingLow := lookback[0].Low
			for _, c := range lookback {
				if c.Low < swingLow {
					swingLow = c.Low
				}
			}
			if candles[j].Low < swingLow && candles[j].Close > swingLow {
				return j, candles[j].Low, true
			}
		} else {
			swingHigh := lookback[0].High
			for _, c := range lookback {
				if c.High > swingHigh {
					swingHigh = c.High
				}
			}
			if candles[j].High > swingHigh && candles[j].Close < swingHigh {
				return j, candles[j].High, true
			}
		}
	}
	return 0, 0, false
}
