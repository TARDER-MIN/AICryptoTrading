package backtest

import (
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

// Candidate is one grid-search point plus its train/validation performance,
// kept for the "top candidates" transparency report.
type Candidate struct {
	Params     strategy.SBParams `json:"params"`
	Train      Result            `json:"train"`
	Validation Result            `json:"validation"`
}

type OptimizeReport struct {
	Best                strategy.SBParams `json:"best_params"`
	Train               Result            `json:"train_metrics"`
	Validation          Result            `json:"validation_metrics"`
	TopCandidates       []Candidate       `json:"top_candidates"`
	CandidatesEvaluated int               `json:"candidates_evaluated"`
	CandidatesPassed    int               `json:"candidates_passed"`
	SplitTime           time.Time         `json:"split_time"`
	HistoryDays         int               `json:"history_days,omitempty"`
	SymbolsTested       []string          `json:"symbols_tested,omitempty"`
	HistoryStart        time.Time         `json:"history_start,omitempty"`
	HistoryEnd          time.Time         `json:"history_end,omitempty"`
	CandlesTested       int               `json:"candles_tested,omitempty"`
}

// minValidationTrades/minValidationSharpe are the overfitting guard: a
// parameter set is only eligible for selection if it produced enough
// validation-period trades to be statistically meaningful and was
// profitable (in the Sharpe sense) on data the grid search never "saw" as
// an optimization target - the actual defense against picking a set that
// merely curve-fits the training window.
const (
	minValidationTrades = 5
	minValidationSharpe = 0.0
)

// Optimize grid-searches strategy.SBParams over historical candles pooled
// across every symbol in tradableSymbols (all fetched over the same
// absolute date range - see internal/bingx.KlinesRange - so a single
// global split timestamp at trainFrac of that range applies uniformly).
// candlesBySymbol may additionally contain entries for symbols NOT in
// tradableSymbols (namely strategy.AnchorSymbols, BTC/ETH, kept fresh for
// SMT divergence lookups even when the AI daily watchlist selection drops
// them from the tradable set) - those are only ever read as SMT reference
// data via strategy.AnchorSymbolFor, never simulated as a tradeable symbol
// themselves. The HTTP optimizer supplies an independent liquidity-ranked
// crypto universe here; changing it does not alter the live watchlist. Every
// candidate is simulated with backtest.Run per tradable
// symbol; trades are pooled and split by EntryTs relative to the split
// timestamp into train/validation Results. Candidates are ranked by
// VALIDATION Sharpe (never training Sharpe) among those clearing the
// minimum trade-count/Sharpe bar - this is what makes the result
// meaningfully out-of-sample rather than just the best-fitting curve on
// data the search directly optimized against.
func Optimize(candlesBySymbol map[string][]models.Candle, tradableSymbols []string, trainFrac float64) OptimizeReport {
	minTs, maxTs := timeRange(candlesBySymbol)
	splitTs := minTs.Add(time.Duration(float64(maxTs.Sub(minTs)) * trainFrac))

	var candidates []Candidate
	for _, p := range paramGrid() {
		var allTrades []Trade
		for _, symbol := range tradableSymbols {
			candles, ok := candlesBySymbol[symbol]
			if !ok {
				continue
			}
			anchorCandles := candlesBySymbol[strategy.AnchorSymbolFor(symbol)]
			allTrades = append(allTrades, Run(candles, anchorCandles, p)...)
		}
		train := Metrics(allTrades, time.Time{}, splitTs)
		valid := Metrics(allTrades, splitTs, time.Time{})
		candidates = append(candidates, Candidate{Params: p, Train: train, Validation: valid})
	}

	report := OptimizeReport{SplitTime: splitTs, CandidatesEvaluated: len(candidates)}

	var passing []Candidate
	for _, c := range candidates {
		if c.Validation.TotalTrades >= minValidationTrades && c.Validation.Sharpe > minValidationSharpe {
			passing = append(passing, c)
		}
	}
	report.CandidatesPassed = len(passing)

	pool := passing
	if len(pool) == 0 {
		// Nothing cleared the validation bar - fall back to ranking all
		// candidates by validation Sharpe anyway so the caller still gets a
		// usable (if flagged-as-weak) report rather than an error; the
		// small validation trade count is visible in the response either way.
		pool = candidates
	}
	sortByValidationSharpeDesc(pool)

	if len(pool) > 0 {
		best := pool[0]
		report.Best = best.Params
		report.Train = best.Train
		report.Validation = best.Validation
	} else {
		report.Best = strategy.DefaultSBParams()
	}

	topN := min(len(pool), 5)
	report.TopCandidates = pool[:topN]

	return report
}

func timeRange(candlesBySymbol map[string][]models.Candle) (min, max time.Time) {
	for _, candles := range candlesBySymbol {
		if len(candles) == 0 {
			continue
		}
		first, last := candles[0].Ts, candles[len(candles)-1].Ts
		if min.IsZero() || first.Before(min) {
			min = first
		}
		if last.After(max) {
			max = last
		}
	}
	return min, max
}

func sortByValidationSharpeDesc(c []Candidate) {
	for i := 1; i < len(c); i++ {
		for j := i; j > 0 && c[j].Validation.Sharpe > c[j-1].Validation.Sharpe; j-- {
			c[j], c[j-1] = c[j-1], c[j]
		}
	}
}

// paramGrid enumerates a deliberately small combination space so a full
// optimize run (candidates x symbols x candles) stays fast in pure Go. Only
// the original five dimensions are grid-searched; the ICT-2026 displacement/
// OTE/breaker/SMT fields are held fixed at their DefaultSBParams() values on
// every candidate (see strategy.SBParams doc comment) - they're
// well-established structural thresholds, not free parameters to curve-fit,
// and adding them to the grid would multiply the search space for no
// overfitting-safety benefit.
func paramGrid() []strategy.SBParams {
	base := strategy.DefaultSBParams()
	var grid []strategy.SBParams
	for _, swing := range []int{10, 15, 20} {
		for _, fvgPct := range []float64{0.05, 0.1, 0.2} {
			for _, sweepBars := range []int{2, 3, 5} {
				for _, stopBuf := range []float64{0.05, 0.1} {
					// Keep the strategy's risk/reward fixed at 1:1.5.
					for _, rr := range []float64{1.5} {
						p := base
						p.SwingLookback = swing
						p.MinFVGSizePct = fvgPct
						p.MaxBarsForSweep = sweepBars
						p.StopBufferPct = stopBuf
						p.RiskRewardRatio = rr
						grid = append(grid, p)
					}
				}
			}
		}
	}
	return grid
}
