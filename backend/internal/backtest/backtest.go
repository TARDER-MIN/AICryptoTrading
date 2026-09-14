// Package backtest replays historical candles through
// strategy.DecideSilverBullet to simulate trades and compute performance
// metrics - the engine internal/backtest/optimize.go's grid search uses to
// pick (and validate) HTF 3+1 parameters. There is no other backtest
// path in this project; it exists solely to support parameter tuning, not
// as a general-purpose research tool.
package backtest

import (
	"math"
	"sort"
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

// CostModel holds the explicit execution-cost assumptions used by the
// optimizer. Values are percentage points per side (for example 0.05 means
// 0.05%, not 5%). Live automatic entries and protective exits are market
// orders, so both legs are conservatively treated as taker executions.
// Historical funding is only deducted when the backtest is supplied actual
// settlement-by-settlement rates from BingX. FundingIncluded must remain false
// if that history could not be fetched completely for the tested universe.
type CostModel struct {
	TakerFeePctPerSide          float64 `json:"taker_fee_pct_per_side"`
	EstimatedSlippagePctPerSide float64 `json:"estimated_slippage_pct_per_side"`
	FundingIncluded             bool    `json:"funding_included"`
	FundingSource               string  `json:"funding_source"`
	FeeSource                   string  `json:"fee_source"`
}

// FundingEvent is one historical perpetual-futures funding settlement. Rate
// is a decimal fraction (0.0001 means 0.01%); MarkPrice is the settlement mark
// used to express the charge against the trade's entry notional.
type FundingEvent struct {
	Ts        time.Time
	Rate      float64
	MarkPrice float64
}

func (m CostModel) normalized() CostModel {
	if math.IsNaN(m.TakerFeePctPerSide) || math.IsInf(m.TakerFeePctPerSide, 0) || m.TakerFeePctPerSide < 0 {
		m.TakerFeePctPerSide = 0
	}
	if math.IsNaN(m.EstimatedSlippagePctPerSide) || math.IsInf(m.EstimatedSlippagePctPerSide, 0) || m.EstimatedSlippagePctPerSide < 0 {
		m.EstimatedSlippagePctPerSide = 0
	}
	return m
}

type Trade struct {
	Symbol              string
	EntryIdx, ExitIdx   int
	EntryTs, ExitTs     time.Time
	Side                models.SignalAction // BUY or SELL
	Entry, Stop, Target float64
	ExitPrice           float64
	ExitReason          string // "stop" | "target" | "end_of_data"
	GrossPnLPct         float64
	FeeCostPct          float64
	SlippageCostPct     float64
	FundingCostPct      float64
	PnLPct              float64 // net of every modeled cost above
}

type Result struct {
	Trades       []Trade `json:"-"` // omitted from API responses (large); metrics below are what's shown
	TotalTrades  int     `json:"total_trades"`
	WinCount     int     `json:"win_count"`
	WinRate      float64 `json:"win_rate"`
	TargetExits  int     `json:"target_exits"`
	StopExits    int     `json:"stop_exits"`
	EndDataExits int     `json:"end_of_data_exits"`
	// Return/drawdown values are cumulative price-return percentage points
	// on one unit of notional. They are not leveraged account-equity returns.
	GrossReturnPct              float64 `json:"gross_return_pct"`
	FeeCostPct                  float64 `json:"fee_cost_pct"`
	SlippageCostPct             float64 `json:"slippage_cost_pct"`
	FundingCostPct              float64 `json:"funding_cost_pct"`
	TotalTradingCostPct         float64 `json:"total_trading_cost_pct"`
	TotalReturnPct              float64 `json:"total_return_pct"` // net return
	Sharpe                      float64 `json:"sharpe"`           // unannualized mean/stdev of per-trade PnLPct - a relative ranking measure, not a rigorous annualized Sharpe
	MaxDrawdownPct              float64 `json:"max_drawdown_pct"`
	ProfitFactor                float64 `json:"profit_factor"`
	TakerFeePctPerSide          float64 `json:"taker_fee_pct_per_side"`
	EstimatedSlippagePctPerSide float64 `json:"estimated_slippage_pct_per_side"`
	FundingIncluded             bool    `json:"funding_included"`
	FundingSource               string  `json:"funding_source"`
	FeeSource                   string  `json:"fee_source"`
}

// Run walks candles once, evaluating the HTF 3+1 strategy at every bar
// (using only the precomputed H1 bias available at that M5 close - no
// lookahead) and simulating one trade at a time (no pyramiding - a new
// signal while a simulated position is open is ignored, mirroring the live
// autotrader's "already_holding_direction" guard). anchorCandles is retained
// only for call-site compatibility with older releases; HTF 3+1 does not use
// SMT as a hard direction gate.
// Returns every trade with EntryIdx/ExitIdx into the candle slice, so
// callers can slice metrics by time range (see Metrics) without re-running
// the simulation.
func Run(candles []models.Candle, anchorCandles []models.Candle, params strategy.SBParams, costs CostModel, funding []FundingEvent) []Trade {
	_ = anchorCandles
	costs = costs.normalized()
	params = strategy.NormalizeParams(params)
	var trades []Trade
	var open *Trade
	lastFVGTs := time.Time{}
	htfBiases := strategy.PrepareHTFBiases(candles, params)

	for i := range candles {
		if open != nil {
			c := candles[i]
			hitStop, hitTarget := false, false
			if open.Side == models.SignalBuy {
				hitStop = c.Low <= open.Stop
				hitTarget = c.High >= open.Target
			} else {
				hitStop = c.High >= open.Stop
				hitTarget = c.Low <= open.Target
			}
			// Conservative tie-break: if a single bar's range touches both
			// stop and target, assume the worse outcome (stop) hit first.
			switch {
			case hitStop:
				closeTrade(open, i, c.Ts, open.Stop, "stop", costs, funding)
				trades = append(trades, *open)
				open = nil
			case hitTarget:
				closeTrade(open, i, c.Ts, open.Target, "target", costs, funding)
				trades = append(trades, *open)
				open = nil
			}
			continue // a bar that opens a position can't also evaluate a new signal
		}

		if i < 2 {
			continue
		}
		sig := strategy.DecideSilverBulletWithBias(candles[:i+1], params, htfBiases[i], candles[i].Ts)
		if sig.Action == models.SignalHold {
			continue
		}
		if !sig.FVGTs.After(lastFVGTs) {
			continue // same setup already acted on (or an older one) - dedup
		}
		lastFVGTs = sig.FVGTs
		open = &Trade{
			EntryIdx: i, EntryTs: candles[i].Ts, Side: sig.Action,
			Entry: sig.Entry, Stop: sig.StopLoss, Target: sig.TakeProfit,
		}
	}

	if open != nil && len(candles) > 0 {
		last := candles[len(candles)-1]
		closeTrade(open, len(candles)-1, last.Ts, last.Close, "end_of_data", costs, funding)
		trades = append(trades, *open)
	}

	return trades
}

func closeTrade(t *Trade, idx int, ts time.Time, exitPrice float64, reason string, costs CostModel, funding []FundingEvent) {
	t.ExitIdx, t.ExitTs, t.ExitPrice, t.ExitReason = idx, ts, exitPrice, reason
	if t.Entry <= 0 || exitPrice <= 0 {
		return
	}
	if t.Side == models.SignalBuy {
		t.GrossPnLPct = (exitPrice - t.Entry) / t.Entry * 100
	} else {
		t.GrossPnLPct = (t.Entry - exitPrice) / t.Entry * 100
	}

	// With fixed quantity, exit notional differs slightly from entry
	// notional. Express both legs' costs as percentage points of entry
	// notional so the result stays directly comparable with GrossPnLPct.
	exitNotionalRatio := exitPrice / t.Entry
	t.FeeCostPct = costs.TakerFeePctPerSide * (1 + exitNotionalRatio)
	t.SlippageCostPct = costs.EstimatedSlippagePctPerSide * (1 + exitNotionalRatio)
	if costs.FundingIncluded {
		t.FundingCostPct = fundingCostPct(*t, funding)
	}
	t.PnLPct = t.GrossPnLPct - t.FeeCostPct - t.SlippageCostPct - t.FundingCostPct
}

// fundingCostPct returns a signed cost in percentage points of entry
// notional. Positive values are paid by the position and negative values are
// funding income. At a positive rate longs pay and shorts receive; at a
// negative rate the direction reverses. A settlement exactly at EntryTs is
// excluded because the simulated entry occurs at that candle's close, while
// a settlement at ExitTs is included conservatively.
func fundingCostPct(t Trade, events []FundingEvent) float64 {
	if t.Entry <= 0 || t.ExitTs.Before(t.EntryTs) {
		return 0
	}
	direction := 1.0
	if t.Side == models.SignalSell {
		direction = -1
	}
	var total float64
	for _, event := range events {
		if !event.Ts.After(t.EntryTs) || event.Ts.After(t.ExitTs) {
			continue
		}
		notionalRatio := 1.0
		if event.MarkPrice > 0 {
			notionalRatio = event.MarkPrice / t.Entry
		}
		total += direction * event.Rate * 100 * notionalRatio
	}
	return total
}

// Metrics aggregates the subset of trades with EntryTs in [from, to) into a
// Result. A zero from/to means "unbounded" on that side - this lets callers
// pool trades from multiple symbols (each with its own candle-index space,
// but a shared absolute time axis) and slice by a single global split
// timestamp, which per-symbol integer indices couldn't do consistently.
func Metrics(trades []Trade, from, to time.Time, costs CostModel) Result {
	costs = costs.normalized()
	var subset []Trade
	for _, t := range trades {
		if !from.IsZero() && t.EntryTs.Before(from) {
			continue
		}
		if !to.IsZero() && !t.EntryTs.Before(to) {
			continue
		}
		// Exclude a position that exits in a later split. Otherwise its PnL
		// would let validation/test prices leak backward into parameter
		// selection even though its entry belongs to the earlier interval.
		if !to.IsZero() && !t.ExitTs.Before(to) {
			continue
		}
		subset = append(subset, t)
	}
	sort.SliceStable(subset, func(i, j int) bool { return subset[i].EntryTs.Before(subset[j].EntryTs) })

	res := Result{
		Trades: subset, TotalTrades: len(subset),
		TakerFeePctPerSide:          costs.TakerFeePctPerSide,
		EstimatedSlippagePctPerSide: costs.EstimatedSlippagePctPerSide,
		FundingIncluded:             costs.FundingIncluded,
		FundingSource:               costs.FundingSource,
		FeeSource:                   costs.FeeSource,
	}
	if len(subset) == 0 {
		return res
	}

	var sumPnL, grossWin, grossLoss float64
	for _, t := range subset {
		res.GrossReturnPct += t.GrossPnLPct
		res.FeeCostPct += t.FeeCostPct
		res.SlippageCostPct += t.SlippageCostPct
		res.FundingCostPct += t.FundingCostPct
		sumPnL += t.PnLPct
		if t.PnLPct > 0 {
			res.WinCount++
			grossWin += t.PnLPct
		} else {
			grossLoss += -t.PnLPct
		}
		switch t.ExitReason {
		case "target":
			res.TargetExits++
		case "stop":
			res.StopExits++
		case "end_of_data":
			res.EndDataExits++
		}
	}
	res.TotalTradingCostPct = res.FeeCostPct + res.SlippageCostPct + res.FundingCostPct
	res.WinRate = float64(res.WinCount) / float64(len(subset)) * 100
	res.TotalReturnPct = sumPnL
	if grossLoss > 0 {
		res.ProfitFactor = grossWin / grossLoss
	} else if grossWin > 0 {
		// Keep API responses valid JSON. encoding/json rejects +Inf; a
		// finite cap communicates "no observed losses" without breaking
		// the dashboard when a small sample contains only winners.
		res.ProfitFactor = 999.0
	}

	mean := sumPnL / float64(len(subset))
	var variance float64
	for _, t := range subset {
		d := t.PnLPct - mean
		variance += d * d
	}
	variance /= float64(len(subset))
	stdev := math.Sqrt(variance)
	if stdev > 0 {
		res.Sharpe = mean / stdev
	}

	var equity, peak, maxDD float64
	for _, t := range subset {
		equity += t.PnLPct
		if equity > peak {
			peak = equity
		}
		if dd := peak - equity; dd > maxDD {
			maxDD = dd
		}
	}
	res.MaxDrawdownPct = maxDD

	return res
}
