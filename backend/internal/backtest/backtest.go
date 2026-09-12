// Package backtest replays historical candles through
// strategy.DecideSilverBullet to simulate trades and compute performance
// metrics - the engine internal/backtest/optimize.go's grid search uses to
// pick (and validate) Silver Bullet parameters. There is no other backtest
// path in this project; it exists solely to support parameter tuning, not
// as a general-purpose research tool.
package backtest

import (
	"math"
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

type Trade struct {
	EntryIdx, ExitIdx   int
	EntryTs, ExitTs     time.Time
	Side                models.SignalAction // BUY or SELL
	Entry, Stop, Target float64
	ExitPrice           float64
	ExitReason          string // "stop" | "target" | "end_of_data"
	PnLPct              float64
}

type Result struct {
	Trades         []Trade `json:"-"` // omitted from API responses (large); metrics below are what's shown
	TotalTrades    int     `json:"total_trades"`
	WinCount       int     `json:"win_count"`
	WinRate        float64 `json:"win_rate"`
	TotalReturnPct float64 `json:"total_return_pct"`
	Sharpe         float64 `json:"sharpe"` // unannualized mean/stdev of per-trade PnLPct - a relative ranking measure, not a rigorous annualized Sharpe
	MaxDrawdownPct float64 `json:"max_drawdown_pct"`
	ProfitFactor   float64 `json:"profit_factor"`
}

// Run walks candles once, evaluating strategy.DecideSilverBullet at every
// bar (using only candles up to and including that bar - no lookahead) and
// simulating one trade at a time (no pyramiding within the backtest - a new
// signal while a simulated position is open is ignored, mirroring the live
// autotrader's "already_holding_direction" guard). anchorCandles is the SMT
// reference symbol's full series (see strategy.AnchorSymbolFor) - pass nil
// if unavailable, which fails SMT confirmation closed rather than panicking.
// Returns every trade with EntryIdx/ExitIdx into the candle slice, so
// callers can slice metrics by time range (see Metrics) without re-running
// the simulation.
func Run(candles []models.Candle, anchorCandles []models.Candle, params strategy.SBParams) []Trade {
	var trades []Trade
	var open *Trade
	lastFVGTs := time.Time{}

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
				closeTrade(open, i, c.Ts, open.Stop, "stop")
				trades = append(trades, *open)
				open = nil
			case hitTarget:
				closeTrade(open, i, c.Ts, open.Target, "target")
				trades = append(trades, *open)
				open = nil
			}
			continue // a bar that opens a position can't also evaluate a new signal
		}

		if i < 2 {
			continue
		}
		sig := strategy.DecideSilverBullet(candles[:i+1], anchorCandles, params, candles[i].Ts)
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
		closeTrade(open, len(candles)-1, last.Ts, last.Close, "end_of_data")
		trades = append(trades, *open)
	}

	return trades
}

func closeTrade(t *Trade, idx int, ts time.Time, exitPrice float64, reason string) {
	t.ExitIdx, t.ExitTs, t.ExitPrice, t.ExitReason = idx, ts, exitPrice, reason
	if t.Side == models.SignalBuy {
		t.PnLPct = (exitPrice - t.Entry) / t.Entry * 100
	} else {
		t.PnLPct = (t.Entry - exitPrice) / t.Entry * 100
	}
}

// Metrics aggregates the subset of trades with EntryTs in [from, to) into a
// Result. A zero from/to means "unbounded" on that side - this lets callers
// pool trades from multiple symbols (each with its own candle-index space,
// but a shared absolute time axis) and slice by a single global split
// timestamp, which per-symbol integer indices couldn't do consistently.
func Metrics(trades []Trade, from, to time.Time) Result {
	var subset []Trade
	for _, t := range trades {
		if !from.IsZero() && t.EntryTs.Before(from) {
			continue
		}
		if !to.IsZero() && !t.EntryTs.Before(to) {
			continue
		}
		subset = append(subset, t)
	}

	res := Result{Trades: subset, TotalTrades: len(subset)}
	if len(subset) == 0 {
		return res
	}

	var sumPnL, grossWin, grossLoss float64
	for _, t := range subset {
		sumPnL += t.PnLPct
		if t.PnLPct > 0 {
			res.WinCount++
			grossWin += t.PnLPct
		} else {
			grossLoss += -t.PnLPct
		}
	}
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
