// Package strategy implements the user's location-first HTF 3+1 execution
// model for crypto perpetuals:
//
//  1. Completed H4 candles define market structure, premium/discount and
//     fresh SNR/FVG/OB context. Longs are forbidden in confirmed bearish H4
//     structure; shorts are forbidden in confirmed bullish H4 structure.
//  2. A fully closed H1 candle must raid external liquidity and reclaim it
//     inside the correct H4 location: upper liquidity -> SELL, lower -> BUY.
//  3. M5 must close through a confirmed pivot (CHOCH) with ATR/volume
//     displacement. The same displacement leg must create a fresh FVG; its
//     last opposite candle is the only eligible order block.
//  4. Entry occurs only on the first rejecting retest. A non-rejecting first
//     touch consumes the zone.
//  5. The fixed 1:1.5 target must clear costs and must not cross the nearest
//     opposing H4/H1 structure.
//
// There is deliberately no time-of-day gate; reports split results into UTC
// entry blocks instead. Every HTF input uses completed buckets only, keeping
// live evaluation and historical replay free of lookahead.
package strategy

import (
	"fmt"
	"math"
	"sort"
	"time"

	"cryptotrading/internal/models"
)

const (
	fixedRiskRewardRatio = 1.5
	// StrategyVersion is persisted with each research report so the dashboard
	// can distinguish an old baseline from results produced by these rules.
	StrategyVersion = "ict2022_sweep_mss_fvg_v4"
)

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
// meanings now match HTF 3+1 rather than the retired M5-only Silver Bullet.
// SwingLookback is measured in completed H4 candles; MaxBarsForSweep is the
// number of hours for which a confirmed H1 sweep bias remains usable.
type SBParams struct {
	SwingLookback   int     `json:"swing_lookback"`     // completed H4 candles used to define external liquidity
	MinFVGSizePct   float64 `json:"min_fvg_size_pct"`   // minimum M5 FVG size as percentage of price
	MaxBarsForSweep int     `json:"max_bars_for_sweep"` // hours for which the latest H1 sweep bias remains valid
	StopBufferPct   float64 `json:"stop_buffer_pct"`    // stop beyond the selected M5 FVG/OB edge
	RiskRewardRatio float64 `json:"risk_reward_ratio"`  // fixed to 1.5 when loaded/saved

	HTFStructureLookback   int     `json:"htf_structure_lookback"`    // completed H4 bars used for range/structure context
	HTFPivotStrength       int     `json:"htf_pivot_strength"`        // completed H4 bars required on each side of a pivot
	HTFZoneATRMultiple     float64 `json:"htf_zone_atr_multiple"`     // maximum distance from H4 SNR edge, in H4 ATR
	CHOCHPivotStrength     int     `json:"choch_pivot_strength"`      // completed M5 bars required on each side of a pivot
	TargetBarrierBufferATR float64 `json:"target_barrier_buffer_atr"` // clearance kept before opposing H4/H1 structure
	RelaxHTFContext        bool    `json:"relax_htf_context"`         // V4 research: H4 trend/location is context, not an entry veto

	MinHTFSweepATR            float64 `json:"min_htf_sweep_atr"`             // wick penetration beyond liquidity, in H1 ATR
	MinHTFReclaimATR          float64 `json:"min_htf_reclaim_atr"`           // close back inside liquidity, in H1 ATR
	MinDisplacementBodyPct    float64 `json:"min_displacement_body_pct"`     // M5 displacement body/range threshold
	MaxOpposingWickPct        float64 `json:"max_opposing_wick_pct"`         // maximum opposing wick/range
	MinDisplacementATR        float64 `json:"min_displacement_atr"`          // M5 body size in ATR units
	MinDisplacementVolume     float64 `json:"min_displacement_volume"`       // M5 volume / prior average volume
	DisplacementATRLookback   int     `json:"displacement_atr_lookback"`     // completed M5 bars used for ATR
	DisplacementVolLookback   int     `json:"displacement_vol_lookback"`     // completed M5 bars used for volume average
	CHOCHLookback             int     `json:"choch_lookback"`                // M5 bars whose high/low must be closed through
	MaxBarsForRetest          int     `json:"max_bars_for_retest"`           // M5 bars allowed for the first retest
	OrderBlockLookback        int     `json:"order_block_lookback"`          // M5 bars searched for the last opposite candle
	RequireRejection          bool    `json:"require_rejection"`             // first retest must close back out of the zone
	EstimatedRoundTripCostPct float64 `json:"estimated_round_trip_cost_pct"` // fee+slippage estimate for both sides
	MinTargetCostMultiple     float64 `json:"min_target_cost_multiple"`      // target distance / round-trip cost floor
}

func DefaultSBParams() SBParams {
	return SBParams{
		SwingLookback:   6,
		MinFVGSizePct:   0.05,
		MaxBarsForSweep: 6,
		StopBufferPct:   0.1,
		RiskRewardRatio: 1.5,

		HTFStructureLookback:   12,
		HTFPivotStrength:       1,
		HTFZoneATRMultiple:     1.25,
		CHOCHPivotStrength:     1,
		TargetBarrierBufferATR: 0.05,
		RelaxHTFContext:        true,

		MinHTFSweepATR:            0.03,
		MinHTFReclaimATR:          0.03,
		MinDisplacementBodyPct:    0.50,
		MaxOpposingWickPct:        0.35,
		MinDisplacementATR:        0.50,
		MinDisplacementVolume:     1.00,
		DisplacementATRLookback:   14,
		DisplacementVolLookback:   20,
		CHOCHLookback:             16,
		MaxBarsForRetest:          12,
		OrderBlockLookback:        10,
		RequireRejection:          true,
		EstimatedRoundTripCostPct: 0.14,
		MinTargetCostMultiple:     3,
	}
}

// NormalizeParams makes old JSON rows and direct test/caller structs obey
// the current fixed-rule defaults. It also closes the old escape hatch where
// an externally supplied value could alter the strategy's 1:1.5 target.
func NormalizeParams(params SBParams) SBParams {
	backfillHTF3Plus1Defaults(&params)
	// V4 keeps the core ICT sequence mandatory while allowing research
	// thresholds to be loosened without restoring the old restrictive defaults.
	params.HTFStructureLookback = max(params.HTFStructureLookback, 4)
	params.HTFPivotStrength = max(params.HTFPivotStrength, 1)
	params.HTFZoneATRMultiple = math.Max(params.HTFZoneATRMultiple, 0.25)
	params.CHOCHPivotStrength = max(params.CHOCHPivotStrength, 1)
	params.CHOCHLookback = max(params.CHOCHLookback, params.CHOCHPivotStrength*2+1)
	params.TargetBarrierBufferATR = math.Max(params.TargetBarrierBufferATR, 0)
	params.MinHTFSweepATR = math.Max(params.MinHTFSweepATR, 0.01)
	params.MinHTFReclaimATR = math.Max(params.MinHTFReclaimATR, 0.01)
	params.MinDisplacementBodyPct = math.Max(params.MinDisplacementBodyPct, 0.35)
	params.MaxOpposingWickPct = math.Min(math.Max(params.MaxOpposingWickPct, 0.20), 0.60)
	params.MinDisplacementATR = math.Max(params.MinDisplacementATR, 0.35)
	params.MinDisplacementVolume = math.Max(params.MinDisplacementVolume, 0.80)
	params.DisplacementATRLookback = max(params.DisplacementATRLookback, 5)
	params.DisplacementVolLookback = max(params.DisplacementVolLookback, 5)
	params.RequireRejection = true
	params.MinTargetCostMultiple = math.Max(params.MinTargetCostMultiple, 2)
	params.RiskRewardRatio = fixedRiskRewardRatio
	return params
}

// RequiredM5History is the minimum live candle window needed to build the H1
// liquidity lookback and still retain enough M5 bars for the execution leg.
func RequiredM5History(params SBParams) int {
	params = NormalizeParams(params)
	// Need enough history for the previous UTC day and the configured number
	// of fully closed H4 bars, plus the M5 execution window.
	h4Bars := max(max(2, params.SwingLookback), params.HTFStructureLookback)
	h1Bars := max(24, h4Bars*4) + 2
	m5Execution := max(max(params.DisplacementATRLookback, params.DisplacementVolLookback), params.CHOCHLookback)
	return h1Bars*12 + max(1, params.MaxBarsForRetest) + max(2, m5Execution) + 3
}

type HTFBias struct {
	Action      models.SignalAction
	SweepTs     time.Time
	SweepPrice  float64
	Location    string
	ConfirmedAt time.Time
	Valid       bool

	HTFTrend          string
	HTFContext        string
	RangeLow          float64
	RangeHigh         float64
	Equilibrium       float64
	SweepATR          float64
	TargetBarrier     float64
	TargetBarrierName string
}

type SBSignal struct {
	Action models.SignalAction
	Reason string

	SweepTs         time.Time
	SweepPrice      float64
	FVGTs           time.Time
	FVGLow, FVGHigh float64

	DisplacementBodyPct float64
	DisplacementATR     float64
	DisplacementVolume  float64
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
	Location  string
	Confirmed time.Time
	H1Index   int

	HTFTrend          string
	HTFContext        string
	RangeLow          float64
	RangeHigh         float64
	Equilibrium       float64
	SweepATR          float64
	TargetBarrier     float64
	TargetBarrierName string
}

// PrepareHTFBiases precomputes the no-lookahead H1 direction available at
// every M5 bar. Backtests call this once per parameter set; the live path calls
// DecideSilverBullet, which uses the same function and reads only the latest
// element. Returning a slice aligned with candles makes the no-lookahead rule
// explicit and testable.
func PrepareHTFBiases(candles []models.Candle, params SBParams) []HTFBias {
	out := make([]HTFBias, len(candles))
	params = NormalizeParams(params)
	if len(candles) == 0 || params.SwingLookback < 2 || params.MaxBarsForSweep < 1 {
		return out
	}
	interval := inferCandleInterval(candles)
	h1 := aggregateCompleteH1(candles, interval)
	if len(h1) <= params.SwingLookback {
		return out
	}

	sweeps := detectH1Sweeps(h1, params)
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
			Location: latest.Location, ConfirmedAt: latest.Confirmed, Valid: true,
			HTFTrend: latest.HTFTrend, HTFContext: latest.HTFContext,
			RangeLow: latest.RangeLow, RangeHigh: latest.RangeHigh, Equilibrium: latest.Equilibrium,
			SweepATR: latest.SweepATR, TargetBarrier: latest.TargetBarrier,
			TargetBarrierName: latest.TargetBarrierName,
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
	params = NormalizeParams(params)
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
		bodyPct, atrMultiple, volumeMultiple, displacementOK := displacementQuality(candles, fvgIdx-1, bullish, params)
		if !displacementOK {
			continue
		}
		chochLevel, chochOK := confirmsCHOCH(candles, fvgIdx-1, params.CHOCHLookback, params.CHOCHPivotStrength, bullish)
		if !chochOK {
			continue
		}

		obLow, obHigh, obFound := findOrderBlock(candles, fvgIdx-1, params.OrderBlockLookback, bullish, bias.ConfirmedAt)
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
			targetDistancePct := math.Abs(target-entry) / entry * 100
			minTargetDistancePct := params.EstimatedRoundTripCostPct * params.MinTargetCostMultiple
			if targetDistancePct < minTargetDistancePct {
				continue
			}
			if !targetPathClear(entry, target, bias, params) {
				continue
			}

			direction := "做多"
			sweepSide := "前低"
			if !bullish {
				direction, sweepSide = "做空", "前高"
			}
			barrier := "前方無未突破HTF結構"
			if bias.TargetBarrier > 0 {
				barrier = fmt.Sprintf("%s %.6g", bias.TargetBarrierName, bias.TargetBarrier)
			}
			return SBSignal{
				Action: bias.Action,
				Reason: fmt.Sprintf(
					"H4%s／%s(區間%.6g-%.6g)＋H1在%s掃%s後收回定向%s＋M5確認擺動CHOCH突破%.6g＋ATR %.1fx／量能 %.1fx同位移FVG(實體%.0f%%)＋Fresh %s首次回踩拒絕＋1:1.5路徑未先撞%s＋目標距離%.2f%%≥成本%.2f%%×%.0f",
					bias.HTFTrend, bias.HTFContext, bias.RangeLow, bias.RangeHigh,
					bias.Location, sweepSide, direction, chochLevel, atrMultiple, volumeMultiple,
					bodyPct*100, zone.kind, barrier, targetDistancePct,
					params.EstimatedRoundTripCostPct, params.MinTargetCostMultiple,
				),
				SweepTs: bias.SweepTs, SweepPrice: bias.SweepPrice,
				FVGTs: c2.Ts, FVGLow: gapLow, FVGHigh: gapHigh,
				DisplacementBodyPct: bodyPct,
				DisplacementATR:     atrMultiple,
				DisplacementVolume:  volumeMultiple,
				CHOCHTs:             displacement.Ts, CHOCHLevel: chochLevel,
				EntryZoneType: zone.kind,
				BreakerLow:    obLow, BreakerHigh: obHigh,
				Entry: entry, StopLoss: stop, TakeProfit: target,
			}
		}
	}
	return SBSignal{Action: models.SignalHold, Reason: "H4位置與H1掃掠方向成立，但尚無確認擺動CHOCH＋同位移Fresh FVG／OB首次回踩拒絕，或1:1.5目標路徑受阻"}
}

func targetPathClear(entry, target float64, bias HTFBias, params SBParams) bool {
	if entry <= 0 || target <= 0 {
		return false
	}
	if bias.TargetBarrier <= 0 || bias.SweepATR <= 0 {
		return true
	}
	buffer := bias.SweepATR * params.TargetBarrierBufferATR
	if bias.Action == models.SignalBuy {
		if bias.TargetBarrier <= entry+buffer {
			return true // the displacement/retest already closed beyond that structure
		}
		return target+buffer < bias.TargetBarrier
	}
	if bias.TargetBarrier >= entry-buffer {
		return true
	}
	return target-buffer > bias.TargetBarrier
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

func displacementQuality(candles []models.Candle, idx int, bullish bool, params SBParams) (bodyFraction, atrMultiple, volumeMultiple float64, ok bool) {
	if idx < 0 || idx >= len(candles) {
		return 0, 0, 0, false
	}
	c := candles[idx]
	rng := c.High - c.Low
	if rng <= 0 {
		return 0, 0, 0, false
	}
	absBody := math.Abs(c.Close - c.Open)
	atr := candleATRBefore(candles, idx, params.DisplacementATRLookback)
	averageVolume := averageVolumeBefore(candles, idx, params.DisplacementVolLookback)
	if atr <= 0 || averageVolume <= 0 {
		return 0, 0, 0, false
	}
	atrMultiple = absBody / atr
	volumeMultiple = c.Volume / averageVolume
	if atrMultiple < params.MinDisplacementATR || volumeMultiple < params.MinDisplacementVolume {
		return 0, atrMultiple, volumeMultiple, false
	}
	if bullish {
		if c.Close <= c.Open {
			return 0, atrMultiple, volumeMultiple, false
		}
		body := (c.Close - c.Open) / rng
		opposingWick := (math.Min(c.Open, c.Close) - c.Low) / rng
		return body, atrMultiple, volumeMultiple, body >= params.MinDisplacementBodyPct && opposingWick <= params.MaxOpposingWickPct
	}
	if c.Close >= c.Open {
		return 0, atrMultiple, volumeMultiple, false
	}
	body := (c.Open - c.Close) / rng
	opposingWick := (c.High - math.Max(c.Open, c.Close)) / rng
	return body, atrMultiple, volumeMultiple, body >= params.MinDisplacementBodyPct && opposingWick <= params.MaxOpposingWickPct
}

func candleATRBefore(candles []models.Candle, idx, lookback int) float64 {
	if lookback < 2 || idx < lookback || idx > len(candles) {
		return 0
	}
	start := idx - lookback
	var total float64
	for i := start; i < idx; i++ {
		tr := candles[i].High - candles[i].Low
		if i > 0 {
			previousClose := candles[i-1].Close
			tr = math.Max(tr, math.Abs(candles[i].High-previousClose))
			tr = math.Max(tr, math.Abs(candles[i].Low-previousClose))
		}
		if tr <= 0 {
			return 0
		}
		total += tr
	}
	return total / float64(lookback)
}

func averageVolumeBefore(candles []models.Candle, idx, lookback int) float64 {
	if lookback < 2 || idx < lookback || idx > len(candles) {
		return 0
	}
	var total float64
	for _, candle := range candles[idx-lookback : idx] {
		if candle.Volume < 0 || math.IsNaN(candle.Volume) || math.IsInf(candle.Volume, 0) {
			return 0
		}
		total += candle.Volume
	}
	if total <= 0 {
		return 0
	}
	return total / float64(lookback)
}

func confirmsCHOCH(candles []models.Candle, displacementIdx, lookback, strength int, bullish bool) (float64, bool) {
	if strength < 1 || lookback < strength*2+1 || displacementIdx < lookback {
		return 0, false
	}
	start := displacementIdx - lookback
	end := displacementIdx // displacement itself cannot confirm its own pivot
	if bullish {
		for i := end - strength - 1; i >= start+strength; i-- {
			if confirmedM5Pivot(candles, i, strength, true) {
				return candles[i].High, candles[displacementIdx].Close > candles[i].High
			}
		}
		return 0, false
	}
	for i := end - strength - 1; i >= start+strength; i-- {
		if confirmedM5Pivot(candles, i, strength, false) {
			return candles[i].Low, candles[displacementIdx].Close < candles[i].Low
		}
	}
	return 0, false
}

func confirmedM5Pivot(candles []models.Candle, idx, strength int, high bool) bool {
	if idx-strength < 0 || idx+strength >= len(candles) {
		return false
	}
	value := candles[idx].Low
	if high {
		value = candles[idx].High
	}
	for offset := 1; offset <= strength; offset++ {
		if high {
			if value <= candles[idx-offset].High || value <= candles[idx+offset].High {
				return false
			}
		} else if value >= candles[idx-offset].Low || value >= candles[idx+offset].Low {
			return false
		}
	}
	return true
}

// findOrderBlock returns the body of the last opposite-coloured M5 candle
// before displacement. It is an entry zone, not an additional hard
// confluence requirement: a fresh FVG or a fresh OB may trigger the setup.
func findOrderBlock(candles []models.Candle, displacementIdx, lookback int, bullish bool, notBefore time.Time) (float64, float64, bool) {
	earliest := max(0, displacementIdx-lookback)
	for i := displacementIdx - 1; i >= earliest; i-- {
		c := candles[i]
		if c.Ts.Before(notBefore) {
			break
		}
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

func aggregateCompleteH4(h1 []h1Bar) []h1Bar {
	if len(h1) == 0 {
		return nil
	}
	type bucket struct {
		bar         h1Bar
		first, last time.Time
		count       int
	}
	var buckets []bucket
	for _, c := range h1 {
		start := c.Start.UTC().Truncate(4 * time.Hour)
		if len(buckets) == 0 || !buckets[len(buckets)-1].bar.Start.Equal(start) {
			buckets = append(buckets, bucket{
				bar:   h1Bar{Start: start, End: start.Add(4 * time.Hour), Open: c.Open, High: c.High, Low: c.Low, Close: c.Close, Volume: c.Volume},
				first: c.Start, last: c.End, count: 1,
			})
			continue
		}
		b := &buckets[len(buckets)-1]
		b.bar.High = math.Max(b.bar.High, c.High)
		b.bar.Low = math.Min(b.bar.Low, c.Low)
		b.bar.Close = c.Close
		b.bar.Volume += c.Volume
		b.last = c.End
		b.count++
	}

	var out []h1Bar
	for _, b := range buckets {
		if b.count == 4 && b.first.Equal(b.bar.Start) && !b.last.Before(b.bar.End) {
			out = append(out, b.bar)
		}
	}
	return out
}

type dayLevels struct {
	low, high   float64
	first, last time.Time
	count       int
}

func completeUTCDayLevels(h1 []h1Bar) map[time.Time]dayLevels {
	days := make(map[time.Time]dayLevels)
	for _, bar := range h1 {
		day := bar.Start.UTC().Truncate(24 * time.Hour)
		level, ok := days[day]
		if !ok {
			level = dayLevels{low: bar.Low, high: bar.High, first: bar.Start, last: bar.End}
		} else {
			level.low = math.Min(level.low, bar.Low)
			level.high = math.Max(level.high, bar.High)
			if bar.Start.Before(level.first) {
				level.first = bar.Start
			}
			if bar.End.After(level.last) {
				level.last = bar.End
			}
		}
		level.count++
		days[day] = level
	}
	for day, level := range days {
		if level.count != 24 || !level.first.Equal(day) || level.last.Before(day.Add(24*time.Hour)) {
			delete(days, day)
		}
	}
	return days
}

func h1ATRBefore(bars []h1Bar, idx, lookback int) float64 {
	if lookback < 2 || idx < lookback || idx > len(bars) {
		return 0
	}
	var total float64
	for i := idx - lookback; i < idx; i++ {
		tr := bars[i].High - bars[i].Low
		if i > 0 {
			previousClose := bars[i-1].Close
			tr = math.Max(tr, math.Abs(bars[i].High-previousClose))
			tr = math.Max(tr, math.Abs(bars[i].Low-previousClose))
		}
		if tr <= 0 {
			return 0
		}
		total += tr
	}
	return total / float64(lookback)
}

type sweepCandidate struct {
	action   models.SignalAction
	level    float64
	location string
}

func qualifiesHTFSweep(bar h1Bar, previousClose float64, candidate sweepCandidate, atr float64, params SBParams) bool {
	if atr <= 0 || candidate.level <= 0 {
		return false
	}
	minimumSweep := atr * params.MinHTFSweepATR
	minimumReclaim := atr * params.MinHTFReclaimATR
	if candidate.action == models.SignalBuy {
		// The bar must approach from above the level, raid below it, and
		// finish back above. A later recovery after price was already
		// accepted below the level is not relabelled as a fresh sweep.
		return previousClose >= candidate.level && bar.Open >= candidate.level &&
			bar.Low <= candidate.level-minimumSweep && bar.Close >= candidate.level+minimumReclaim
	}
	return previousClose <= candidate.level && bar.Open <= candidate.level &&
		bar.High >= candidate.level+minimumSweep && bar.Close <= candidate.level-minimumReclaim
}

type htfContext struct {
	Valid       bool
	Trend       string
	Zone        string
	RangeLow    float64
	RangeHigh   float64
	Equilibrium float64
	ATR         float64
}

type barPivot struct {
	Index int
	Value float64
}

func averageBarATR(bars []h1Bar) float64 {
	if len(bars) < 2 {
		return 0
	}
	start := max(1, len(bars)-14)
	var total float64
	var count int
	for i := start; i < len(bars); i++ {
		tr := bars[i].High - bars[i].Low
		tr = math.Max(tr, math.Abs(bars[i].High-bars[i-1].Close))
		tr = math.Max(tr, math.Abs(bars[i].Low-bars[i-1].Close))
		if tr <= 0 {
			continue
		}
		total += tr
		count++
	}
	if count == 0 {
		return 0
	}
	return total / float64(count)
}

func confirmedBarPivots(bars []h1Bar, strength int, high bool) []barPivot {
	if strength < 1 || len(bars) < strength*2+1 {
		return nil
	}
	var out []barPivot
	for i := strength; i+strength < len(bars); i++ {
		value := bars[i].Low
		if high {
			value = bars[i].High
		}
		ok := true
		for offset := 1; offset <= strength; offset++ {
			if high {
				if value <= bars[i-offset].High || value <= bars[i+offset].High {
					ok = false
					break
				}
			} else if value >= bars[i-offset].Low || value >= bars[i+offset].Low {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, barPivot{Index: i, Value: value})
		}
	}
	return out
}

func classifyH4Trend(bars []h1Bar, strength int) string {
	highs := confirmedBarPivots(bars, strength, true)
	lows := confirmedBarPivots(bars, strength, false)
	lastClose := bars[len(bars)-1].Close
	if len(highs) > 0 && lastClose > highs[len(highs)-1].Value {
		return "多頭結構"
	}
	if len(lows) > 0 && lastClose < lows[len(lows)-1].Value {
		return "空頭結構"
	}
	if len(highs) >= 2 && len(lows) >= 2 {
		higherHigh := highs[len(highs)-1].Value > highs[len(highs)-2].Value
		higherLow := lows[len(lows)-1].Value > lows[len(lows)-2].Value
		lowerHigh := highs[len(highs)-1].Value < highs[len(highs)-2].Value
		lowerLow := lows[len(lows)-1].Value < lows[len(lows)-2].Value
		if higherHigh && higherLow {
			return "多頭結構"
		}
		if lowerHigh && lowerLow {
			return "空頭結構"
		}
	}
	return "盤整結構"
}

func zoneStillFresh(bars []h1Bar, formedAt int, low, high float64) bool {
	for i := formedAt + 1; i < len(bars); i++ {
		if bars[i].Low <= high && bars[i].High >= low {
			return false
		}
	}
	return true
}

func matchingFreshH4Zone(bars []h1Bar, sweep h1Bar, action models.SignalAction, atr float64) string {
	for i := len(bars) - 1; i >= 2; i-- {
		if action == models.SignalBuy && bars[i-2].High < bars[i].Low {
			low, high := bars[i-2].High, bars[i].Low
			if zoneStillFresh(bars, i, low, high) && sweep.Low <= high && sweep.High >= low {
				return "H4多頭FVG"
			}
		}
		if action == models.SignalSell && bars[i-2].Low > bars[i].High {
			low, high := bars[i].High, bars[i-2].Low
			if zoneStillFresh(bars, i, low, high) && sweep.Low <= high && sweep.High >= low {
				return "H4空頭FVG"
			}
		}
	}
	for i := len(bars) - 1; i >= 1; i-- {
		base, impulse := bars[i-1], bars[i]
		impulseBody := math.Abs(impulse.Close - impulse.Open)
		if impulseBody < atr*0.8 {
			continue
		}
		low, high := math.Min(base.Open, base.Close), math.Max(base.Open, base.Close)
		if !zoneStillFresh(bars, i, low, high) || sweep.Low > high || sweep.High < low {
			continue
		}
		if action == models.SignalBuy && base.Close < base.Open && impulse.Close > impulse.Open && impulse.Close > base.High {
			return "H4多頭OB"
		}
		if action == models.SignalSell && base.Close > base.Open && impulse.Close < impulse.Open && impulse.Close < base.Low {
			return "H4空頭OB"
		}
	}
	return ""
}

func buildHTFContext(closedH4 []h1Bar, sweep h1Bar, action models.SignalAction, params SBParams) htfContext {
	lookback := max(params.HTFStructureLookback, params.SwingLookback)
	if len(closedH4) < lookback {
		return htfContext{}
	}
	window := closedH4[len(closedH4)-lookback:]
	low, high := window[0].Low, window[0].High
	for _, bar := range window[1:] {
		low = math.Min(low, bar.Low)
		high = math.Max(high, bar.High)
	}
	atr := averageBarATR(window)
	if atr <= 0 || high <= low {
		return htfContext{}
	}
	equilibrium := (low + high) / 2
	trend := classifyH4Trend(window, params.HTFPivotStrength)
	if !params.RelaxHTFContext {
		if action == models.SignalBuy && trend == "空頭結構" {
			return htfContext{}
		}
		if action == models.SignalSell && trend == "多頭結構" {
			return htfContext{}
		}
	}

	zone := ""
	edgeDistance := atr * params.HTFZoneATRMultiple
	if action == models.SignalBuy {
		if !params.RelaxHTFContext && sweep.Low > equilibrium {
			return htfContext{}
		}
		if sweep.Low <= low+edgeDistance {
			zone = "H4折價SNR"
		} else if params.RelaxHTFContext && sweep.Low <= equilibrium {
			zone = "H4折價區"
		}
	} else {
		if !params.RelaxHTFContext && sweep.High < equilibrium {
			return htfContext{}
		}
		if sweep.High >= high-edgeDistance {
			zone = "H4溢價SNR"
		} else if params.RelaxHTFContext && sweep.High >= equilibrium {
			zone = "H4溢價區"
		}
	}
	if zone == "" {
		zone = matchingFreshH4Zone(window, sweep, action, atr)
	}
	if zone == "" && params.RelaxHTFContext {
		zone = "H4背景觀察"
	}
	if zone == "" {
		return htfContext{}
	}
	return htfContext{
		Valid: true, Trend: trend, Zone: zone,
		RangeLow: low, RangeHigh: high, Equilibrium: equilibrium, ATR: atr,
	}
}

func nearestH1StructureBarrier(bars []h1Bar, action models.SignalAction, current float64) (float64, string) {
	if len(bars) < 5 {
		return 0, ""
	}
	start := max(0, len(bars)-24)
	window := bars[start:]
	pivots := confirmedBarPivots(window, 2, action == models.SignalBuy)
	var best float64
	for _, pivot := range pivots {
		level := pivot.Value
		if action == models.SignalBuy {
			if level <= current || (best > 0 && level >= best) {
				continue
			}
		} else if level >= current || (best > 0 && level <= best) {
			continue
		}
		best = level
	}
	if best == 0 {
		return 0, ""
	}
	if action == models.SignalBuy {
		return best, "H1確認擺動高點"
	}
	return best, "H1確認擺動低點"
}

// detectH1Sweeps accepts a reversal only when the external-liquidity raid is
// also in valid H4 structure/location. Ordinary internal H1 highs/lows remain
// ineligible as sweep sources. Every H4/H1 input is fully closed before the
// sweep candle begins, so neither live use nor replay can see future context.
func detectH1Sweeps(bars []h1Bar, params SBParams) []h1Sweep {
	h4 := aggregateCompleteH4(bars)
	days := completeUTCDayLevels(bars)
	liquidityLookback := max(2, params.SwingLookback)
	var out []h1Sweep
	h4Closed := 0

	for i, bar := range bars {
		if i == 0 {
			continue
		}
		for h4Closed < len(h4) && !h4[h4Closed].End.After(bar.Start) {
			h4Closed++
		}
		atr := h1ATRBefore(bars, i, 14)
		if atr <= 0 {
			continue
		}

		var lower, upper []sweepCandidate
		previousDay := bar.Start.UTC().Truncate(24 * time.Hour).Add(-24 * time.Hour)
		previousDayLevels, hasPreviousDay := days[previousDay]
		if hasPreviousDay {
			lower = append(lower, sweepCandidate{action: models.SignalBuy, level: previousDayLevels.low, location: "PDL前日低點"})
			upper = append(upper, sweepCandidate{action: models.SignalSell, level: previousDayLevels.high, location: "PDH前日高點"})
		}
		if h4Closed >= liquidityLookback {
			window := h4[h4Closed-liquidityLookback : h4Closed]
			low, high := window[0].Low, window[0].High
			for _, h4Bar := range window[1:] {
				low = math.Min(low, h4Bar.Low)
				high = math.Max(high, h4Bar.High)
			}
			lower = append(lower, sweepCandidate{action: models.SignalBuy, level: low, location: "H4外部低點"})
			upper = append(upper, sweepCandidate{action: models.SignalSell, level: high, location: "H4外部高點"})
		}

		var bull, bear *sweepCandidate
		for j := range lower {
			if qualifiesHTFSweep(bar, bars[i-1].Close, lower[j], atr, params) {
				candidate := lower[j]
				bull = &candidate
				break
			}
		}
		for j := range upper {
			if qualifiesHTFSweep(bar, bars[i-1].Close, upper[j], atr, params) {
				candidate := upper[j]
				bear = &candidate
				break
			}
		}
		if (bull == nil) == (bear == nil) {
			continue
		}

		action, price, location := models.SignalBuy, bar.Low, ""
		if bull != nil {
			location = bull.location
		} else {
			action, price, location = models.SignalSell, bar.High, bear.location
		}
		context := buildHTFContext(h4[:h4Closed], bar, action, params)
		if !context.Valid {
			continue
		}

		var barrier float64
		var barrierName string
		considerBarrier := func(level float64, name string) {
			if action == models.SignalBuy {
				if level > bar.Close && (barrier == 0 || level < barrier) {
					barrier, barrierName = level, name
				}
				return
			}
			if level < bar.Close && (barrier == 0 || level > barrier) {
				barrier, barrierName = level, name
			}
		}
		if action == models.SignalBuy {
			considerBarrier(context.RangeHigh, "H4區間高點")
			if hasPreviousDay {
				considerBarrier(previousDayLevels.high, "PDH前日高點")
			}
		} else {
			considerBarrier(context.RangeLow, "H4區間低點")
			if hasPreviousDay {
				considerBarrier(previousDayLevels.low, "PDL前日低點")
			}
		}
		if level, name := nearestH1StructureBarrier(bars[:i], action, bar.Close); level > 0 {
			considerBarrier(level, name)
		}
		if barrier <= 0 && !params.RelaxHTFContext {
			continue
		}

		out = append(out, h1Sweep{
			Action: action, Ts: bar.Start, Price: price,
			Location: location, Confirmed: bar.End, H1Index: i,
			HTFTrend: context.Trend, HTFContext: context.Zone,
			RangeLow: context.RangeLow, RangeHigh: context.RangeHigh, Equilibrium: context.Equilibrium,
			SweepATR: atr, TargetBarrier: barrier, TargetBarrierName: barrierName,
		})
	}
	return out
}
