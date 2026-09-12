package backtest

import (
	"time"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

// Candidate is one grid-search point plus its train/selection-validation
// performance, kept for the "top candidates" transparency report. Test is
// intentionally omitted from JSON and never participates in ranking.
type Candidate struct {
	Params     strategy.SBParams `json:"params"`
	Train      Result            `json:"train"`
	Validation Result            `json:"validation"`
	test       Result
}

type OptimizeReport struct {
	Best                 strategy.SBParams `json:"best_params"`
	Train                Result            `json:"train_metrics"`
	Validation           Result            `json:"validation_metrics"`
	Test                 Result            `json:"test_metrics"`
	CostModel            CostModel         `json:"cost_model"`
	TopCandidates        []Candidate       `json:"top_candidates"`
	CandidatesEvaluated  int               `json:"candidates_evaluated"`
	CandidatesPassed     int               `json:"candidates_passed"`
	TrainEnd             time.Time         `json:"train_end"`
	TestStart            time.Time         `json:"test_start"`
	HistoryDays          int               `json:"history_days,omitempty"`
	HistoryAvailableDays float64           `json:"history_available_days,omitempty"`
	SymbolsTested        []string          `json:"symbols_tested,omitempty"`
	HistoryStart         time.Time         `json:"history_start,omitempty"`
	HistoryEnd           time.Time         `json:"history_end,omitempty"`
	CandlesTested        int               `json:"candles_tested,omitempty"`
}

// minValidationTrades/minValidationSharpe are the overfitting guard: a
// parameter set is only eligible for selection if it produced enough
// selection-validation trades and was profitable in the per-trade Sharpe
// sense. This middle segment is intentionally used for model selection; the
// later test segment is the only interval the selection process never sees.
const (
	minValidationTrades = 10
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
// timestamp into training, selection-validation, and final-test Results.
// Candidates are ranked only by selection-validation Sharpe among those
// clearing the minimum trade-count/Sharpe bar. The final test interval is
// reported only after the winner is fixed; it is never used to rank or
// filter candidates.
func Optimize(candlesBySymbol map[string][]models.Candle, fundingBySymbol map[string][]FundingEvent, tradableSymbols []string, trainFrac, validationFrac float64, costs CostModel) OptimizeReport {
	minTs, maxTs := timeRange(candlesBySymbol, tradableSymbols)
	if minTs.IsZero() || maxTs.IsZero() || !maxTs.After(minTs) {
		return OptimizeReport{Best: strategy.DefaultSBParams(), CostModel: costs.normalized()}
	}
	if trainFrac <= 0 || validationFrac <= 0 || trainFrac+validationFrac >= 1 {
		trainFrac, validationFrac = 0.6, 0.2
	}
	trainEnd := minTs.Add(time.Duration(float64(maxTs.Sub(minTs)) * trainFrac))
	testStart := minTs.Add(time.Duration(float64(maxTs.Sub(minTs)) * (trainFrac + validationFrac)))
	costs = costs.normalized()

	var candidates []Candidate
	for _, p := range paramGrid() {
		var allTrades []Trade
		for _, symbol := range tradableSymbols {
			candles, ok := candlesBySymbol[symbol]
			if !ok {
				continue
			}
			anchorCandles := candlesBySymbol[strategy.AnchorSymbolFor(symbol)]
			allTrades = append(allTrades, Run(candles, anchorCandles, p, costs, fundingBySymbol[symbol])...)
		}
		train := Metrics(allTrades, time.Time{}, trainEnd, costs)
		valid := Metrics(allTrades, trainEnd, testStart, costs)
		test := Metrics(allTrades, testStart, time.Time{}, costs)
		candidates = append(candidates, Candidate{Params: p, Train: train, Validation: valid, test: test})
	}

	report := OptimizeReport{
		CostModel: costs, TrainEnd: trainEnd, TestStart: testStart,
		CandidatesEvaluated: len(candidates),
	}

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
		report.Test = best.test
	} else {
		report.Best = strategy.DefaultSBParams()
	}

	topN := min(len(pool), 5)
	report.TopCandidates = pool[:topN]

	return report
}

func timeRange(candlesBySymbol map[string][]models.Candle, symbols []string) (min, max time.Time) {
	for _, symbol := range symbols {
		candles := candlesBySymbol[symbol]
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
