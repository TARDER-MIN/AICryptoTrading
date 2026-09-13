package backtest

import (
	"sort"
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
	allTrades  []Trade
}

// Breakdown is a cost-aware slice of the untouched final-test trades. Label
// is a symbol, direction, or UTC entry date depending on which report field
// contains it.
type Breakdown struct {
	Label   string `json:"label"`
	Metrics Result `json:"metrics"`
}

// FinalTestDiagnostics exposes where an aggregate final-test result came
// from. CompletedOnly removes positions marked to market solely because the
// BingX history ended, which makes any end-of-sample distortion visible
// without silently changing the headline result.
type FinalTestDiagnostics struct {
	CompletedOnly Result      `json:"completed_only_metrics"`
	BySymbol      []Breakdown `json:"by_symbol"`
	BySide        []Breakdown `json:"by_side"`
	ByDay         []Breakdown `json:"by_day"`
}

// WalkForwardFold selects a parameter set using only data strictly before
// TestStart, then scores it on the next chronological block. The selected
// parameters may differ by fold; no test block can influence its own
// selection.
type WalkForwardFold struct {
	Index             int               `json:"index"`
	SelectionStart    time.Time         `json:"selection_start"`
	SelectionEnd      time.Time         `json:"selection_end"`
	TestStart         time.Time         `json:"test_start"`
	TestEnd           time.Time         `json:"test_end"`
	SelectedParams    strategy.SBParams `json:"selected_params"`
	CandidatesPassed  int               `json:"candidates_passed"`
	UsedFallback      bool              `json:"used_fallback"`
	SelectionMetrics  Result            `json:"selection_metrics"`
	TestMetrics       Result            `json:"test_metrics"`
	Passed            bool              `json:"passed"`
}

type WalkForwardReport struct {
	Folds            []WalkForwardFold `json:"folds"`
	AggregateMetrics Result            `json:"aggregate_metrics"`
	PassedFolds      int               `json:"passed_folds"`
	TotalFolds       int               `json:"total_folds"`
}

// RobustSelectionFold is one chronological validation slice for a single,
// fixed candidate. All folds end before the untouched final 20%, so they may
// safely participate in ranking without leaking final-test prices.
type RobustSelectionFold struct {
	Index       int       `json:"index"`
	TestStart   time.Time `json:"test_start"`
	TestEnd     time.Time `json:"test_end"`
	TestMetrics Result    `json:"test_metrics"`
	Passed      bool      `json:"passed"`
}

// RobustSelectionReport explains why the proposed parameter set won (or why
// no set was safe enough to apply). Unlike the diagnostic WalkForward report,
// these folds score the same candidate repeatedly across the development
// sample; this rewards temporal stability instead of one exceptional slice.
type RobustSelectionReport struct {
	SelectedParams   strategy.SBParams     `json:"selected_params"`
	Folds            []RobustSelectionFold `json:"folds"`
	AggregateMetrics Result                `json:"aggregate_metrics"`
	BySide           []Breakdown           `json:"by_side"`
	PassedFolds      int                   `json:"passed_folds"`
	TotalFolds       int                   `json:"total_folds"`
	CandidatesPassed int                   `json:"candidates_passed"`
	UsedFallback     bool                  `json:"used_fallback"`
}

type OptimizeReport struct {
	Best                 strategy.SBParams    `json:"best_params"`
	Train                Result               `json:"train_metrics"`
	Validation           Result               `json:"validation_metrics"`
	Test                 Result               `json:"test_metrics"`
	CostModel            CostModel            `json:"cost_model"`
	TopCandidates        []Candidate          `json:"top_candidates"`
	CandidatesEvaluated  int                  `json:"candidates_evaluated"`
	CandidatesPassed     int                  `json:"candidates_passed"`
	TrainEnd             time.Time            `json:"train_end"`
	TestStart            time.Time            `json:"test_start"`
	HistoryDays          int                  `json:"history_days,omitempty"`
	HistoryAvailableDays float64              `json:"history_available_days,omitempty"`
	SymbolsTested        []string             `json:"symbols_tested,omitempty"`
	HistoryStart         time.Time            `json:"history_start,omitempty"`
	HistoryEnd           time.Time            `json:"history_end,omitempty"`
	CandlesTested        int                  `json:"candles_tested,omitempty"`
	FinalDiagnostics     FinalTestDiagnostics `json:"final_test_diagnostics"`
	RobustSelection      RobustSelectionReport `json:"robust_selection"`
	WalkForward          WalkForwardReport    `json:"walk_forward"`
	ParamsApplied        bool                 `json:"params_applied"`
	ApplyBlockers        []string             `json:"apply_blockers"`
}

// These thresholds are deliberately fixed in code rather than tuned against
// the same sample. They control the development folds, direction checks, and
// the independent deployment gate described below.
const (
	minValidationTrades = 10
	minValidationSharpe = 0.0

	robustSelectionBlocks    = 4
	minRobustPassedFolds     = 2
	minRobustTotalTrades     = 30
	minRobustSideTrades      = 10
	minRobustProfitFactor    = 1.20
	maxRobustDrawdownPct     = 10.0
	minDeploymentPassedFolds = 3
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
// candidate is simulated with backtest.Run per tradable symbol; trades are
// pooled and split by EntryTs relative to the global timestamps. Candidate
// ranking uses repeated chronological folds entirely inside the first 80%
// development sample and requires profitable evidence from both BUY and SELL
// trades. The final 20% is opened only after the proposed winner is fixed and
// acts as a deployment gate, never as a ranking input. Failing any gate keeps
// the report visible but prevents the proposed parameters from being applied.
func Optimize(candlesBySymbol map[string][]models.Candle, fundingBySymbol map[string][]FundingEvent, tradableSymbols []string, trainFrac, validationFrac float64, costs CostModel) OptimizeReport {
	minTs, maxTs := timeRange(candlesBySymbol, tradableSymbols)
	if minTs.IsZero() || maxTs.IsZero() || !maxTs.After(minTs) {
		return OptimizeReport{
			Best: strategy.DefaultSBParams(), CostModel: costs.normalized(),
			ApplyBlockers: []string{"insufficient_history"},
		}
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
			symbolTrades := Run(candles, anchorCandles, p, costs, fundingBySymbol[symbol])
			for i := range symbolTrades {
				symbolTrades[i].Symbol = symbol
			}
			allTrades = append(allTrades, symbolTrades...)
		}
		train := Metrics(allTrades, time.Time{}, trainEnd, costs)
		valid := Metrics(allTrades, trainEnd, testStart, costs)
		test := Metrics(allTrades, testStart, time.Time{}, costs)
		candidates = append(candidates, Candidate{
			Params: p, Train: train, Validation: valid, test: test, allTrades: allTrades,
		})
	}

	report := OptimizeReport{
		CostModel: costs, TrainEnd: trainEnd, TestStart: testStart,
		CandidatesEvaluated: len(candidates),
	}

	ranked, robustCandidatesPassed := rankRobustCandidates(candidates, minTs, testStart, costs)
	report.CandidatesPassed = robustCandidatesPassed

	if len(ranked) > 0 {
		best := ranked[0]
		report.Best = best.candidate.Params
		report.Train = best.candidate.Train
		report.Validation = best.candidate.Validation
		report.Test = best.candidate.test
		report.RobustSelection = best.report
		report.RobustSelection.CandidatesPassed = robustCandidatesPassed
		report.RobustSelection.UsedFallback = !best.eligible

		topN := min(len(ranked), 5)
		for _, item := range ranked[:topN] {
			report.TopCandidates = append(report.TopCandidates, item.candidate)
		}
	} else {
		report.Best = strategy.DefaultSBParams()
		report.RobustSelection.UsedFallback = true
	}
	report.FinalDiagnostics = buildFinalTestDiagnostics(report.Test.Trades, costs)
	report.WalkForward = buildWalkForwardReport(candidates, minTs, maxTs, costs)
	report.ApplyBlockers = deploymentBlockers(report)
	report.ParamsApplied = len(report.ApplyBlockers) == 0

	return report
}

type robustCandidateScore struct {
	candidate       Candidate
	report          RobustSelectionReport
	eligible        bool
	robustSides     int
	worstFoldReturn float64
}

// rankRobustCandidates scores every fixed parameter set on three consecutive
// validation folds within the first 80% development window. The untouched
// final 20% is not passed to this function and therefore cannot influence the
// winner. A fallback candidate is still returned for diagnosis, but its
// eligible flag remains false so the HTTP layer cannot apply it.
func rankRobustCandidates(candidates []Candidate, minTs, developmentEnd time.Time, costs CostModel) ([]robustCandidateScore, int) {
	scores := make([]robustCandidateScore, 0, len(candidates))
	passed := 0
	for _, candidate := range candidates {
		score := scoreRobustCandidate(candidate, minTs, developmentEnd, costs)
		if score.eligible {
			passed++
		}
		scores = append(scores, score)
	}

	sort.SliceStable(scores, func(i, j int) bool {
		left, right := scores[i], scores[j]
		if left.eligible != right.eligible {
			return left.eligible
		}
		if left.report.PassedFolds != right.report.PassedFolds {
			return left.report.PassedFolds > right.report.PassedFolds
		}
		if left.robustSides != right.robustSides {
			return left.robustSides > right.robustSides
		}
		if left.worstFoldReturn != right.worstFoldReturn {
			return left.worstFoldReturn > right.worstFoldReturn
		}
		if left.report.AggregateMetrics.ProfitFactor != right.report.AggregateMetrics.ProfitFactor {
			return left.report.AggregateMetrics.ProfitFactor > right.report.AggregateMetrics.ProfitFactor
		}
		if left.report.AggregateMetrics.Sharpe != right.report.AggregateMetrics.Sharpe {
			return left.report.AggregateMetrics.Sharpe > right.report.AggregateMetrics.Sharpe
		}
		if left.report.AggregateMetrics.TotalReturnPct != right.report.AggregateMetrics.TotalReturnPct {
			return left.report.AggregateMetrics.TotalReturnPct > right.report.AggregateMetrics.TotalReturnPct
		}
		return left.report.AggregateMetrics.MaxDrawdownPct < right.report.AggregateMetrics.MaxDrawdownPct
	})
	return scores, passed
}

func scoreRobustCandidate(candidate Candidate, minTs, developmentEnd time.Time, costs CostModel) robustCandidateScore {
	report := RobustSelectionReport{SelectedParams: candidate.Params}
	if minTs.IsZero() || !developmentEnd.After(minTs) {
		return robustCandidateScore{candidate: candidate, report: report}
	}

	span := developmentEnd.Sub(minTs)
	var aggregateTrades []Trade
	for foldIndex := 1; foldIndex < robustSelectionBlocks; foldIndex++ {
		testStart := minTs.Add(time.Duration(float64(span) * float64(foldIndex) / robustSelectionBlocks))
		testEnd := minTs.Add(time.Duration(float64(span) * float64(foldIndex+1) / robustSelectionBlocks))
		if foldIndex == robustSelectionBlocks-1 {
			testEnd = developmentEnd
		}
		metrics := Metrics(candidate.allTrades, testStart, testEnd, costs)
		foldPassed := passesSingleFold(metrics)
		if foldPassed {
			report.PassedFolds++
		}
		report.Folds = append(report.Folds, RobustSelectionFold{
			Index: foldIndex, TestStart: testStart, TestEnd: testEnd,
			TestMetrics: metrics, Passed: foldPassed,
		})
		aggregateTrades = append(aggregateTrades, metrics.Trades...)
	}
	report.TotalFolds = len(report.Folds)
	report.AggregateMetrics = Metrics(aggregateTrades, time.Time{}, time.Time{}, costs)
	report.BySide = breakdownBySide(aggregateTrades, costs)

	robustSides := 0
	for _, side := range []string{string(models.SignalBuy), string(models.SignalSell)} {
		metrics, ok := breakdownMetrics(report.BySide, side)
		if ok && passesDirectionGate(metrics) {
			robustSides++
		}
	}
	worstFoldReturn := 0.0
	if len(report.Folds) > 0 {
		worstFoldReturn = report.Folds[0].TestMetrics.TotalReturnPct
		for _, fold := range report.Folds[1:] {
			if fold.TestMetrics.TotalReturnPct < worstFoldReturn {
				worstFoldReturn = fold.TestMetrics.TotalReturnPct
			}
		}
	}

	aggregate := report.AggregateMetrics
	eligible := report.PassedFolds >= minRobustPassedFolds &&
		aggregate.TotalTrades >= minRobustTotalTrades &&
		aggregate.TotalReturnPct > 0 && aggregate.ProfitFactor >= minRobustProfitFactor &&
		aggregate.Sharpe > 0 && aggregate.MaxDrawdownPct <= maxRobustDrawdownPct &&
		robustSides == 2

	return robustCandidateScore{
		candidate: candidate, report: report, eligible: eligible,
		robustSides: robustSides, worstFoldReturn: worstFoldReturn,
	}
}

func passesSingleFold(metrics Result) bool {
	return metrics.TotalTrades >= minValidationTrades && metrics.TotalReturnPct > 0 &&
		metrics.ProfitFactor > 1 && metrics.Sharpe > minValidationSharpe
}

func passesDirectionGate(metrics Result) bool {
	return metrics.TotalTrades >= minRobustSideTrades && metrics.TotalReturnPct > 0 &&
		metrics.ProfitFactor > 1 && metrics.Sharpe > 0
}

func breakdownBySide(trades []Trade, costs CostModel) []Breakdown {
	groups := make(map[string][]Trade)
	for _, trade := range trades {
		groups[string(trade.Side)] = append(groups[string(trade.Side)], trade)
	}
	out := groupedBreakdowns(groups, costs)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Label < out[j].Label })
	return out
}

func breakdownMetrics(rows []Breakdown, label string) (Result, bool) {
	for _, row := range rows {
		if row.Label == label {
			return row.Metrics, true
		}
	}
	return Result{}, false
}

// deploymentBlockers is deliberately stricter than candidate ranking. The
// proposed parameters must survive development-fold stability, the adaptive
// four-fold walk-forward process, and the untouched final test after costs.
// BUY and SELL must each have enough completed final trades and independent
// positive expectancy; otherwise the report is saved without changing live
// strategy parameters.
func deploymentBlockers(report OptimizeReport) []string {
	var blockers []string
	if report.RobustSelection.UsedFallback || report.CandidatesPassed == 0 {
		blockers = append(blockers, "no_robust_candidate")
	}

	walkForward := report.WalkForward
	if walkForward.TotalFolds == 0 || walkForward.PassedFolds < minDeploymentPassedFolds {
		blockers = append(blockers, "walk_forward_pass_rate")
	}
	if walkForward.AggregateMetrics.TotalTrades < minRobustTotalTrades {
		blockers = append(blockers, "walk_forward_trades")
	}
	if walkForward.AggregateMetrics.TotalReturnPct <= 0 {
		blockers = append(blockers, "walk_forward_net")
	}
	if walkForward.AggregateMetrics.ProfitFactor < minRobustProfitFactor {
		blockers = append(blockers, "walk_forward_profit_factor")
	}
	if walkForward.AggregateMetrics.Sharpe <= 0 {
		blockers = append(blockers, "walk_forward_sharpe")
	}
	if walkForward.AggregateMetrics.MaxDrawdownPct > maxRobustDrawdownPct {
		blockers = append(blockers, "walk_forward_drawdown")
	}

	final := report.FinalDiagnostics.CompletedOnly
	if final.TotalTrades < minValidationTrades {
		blockers = append(blockers, "final_trades")
	}
	if final.TotalReturnPct <= 0 {
		blockers = append(blockers, "final_net")
	}
	if final.ProfitFactor <= 1 {
		blockers = append(blockers, "final_profit_factor")
	}
	if final.Sharpe <= 0 {
		blockers = append(blockers, "final_sharpe")
	}
	if final.MaxDrawdownPct > maxRobustDrawdownPct {
		blockers = append(blockers, "final_drawdown")
	}

	completed := make([]Trade, 0, len(report.Test.Trades))
	for _, trade := range report.Test.Trades {
		if trade.ExitReason != "end_of_data" {
			completed = append(completed, trade)
		}
	}
	finalSides := breakdownBySide(completed, report.CostModel)
	for _, side := range []struct {
		label string
		name  string
	}{
		{label: string(models.SignalBuy), name: "buy"},
		{label: string(models.SignalSell), name: "sell"},
	} {
		metrics, ok := breakdownMetrics(finalSides, side.label)
		if !ok || metrics.TotalTrades < minRobustSideTrades {
			blockers = append(blockers, "final_"+side.name+"_insufficient")
		} else if !passesDirectionGate(metrics) {
			blockers = append(blockers, "final_"+side.name+"_unprofitable")
		}
	}
	return blockers
}

func buildFinalTestDiagnostics(trades []Trade, costs CostModel) FinalTestDiagnostics {
	completed := make([]Trade, 0, len(trades))
	bySymbol := make(map[string][]Trade)
	bySide := make(map[string][]Trade)
	byDay := make(map[string][]Trade)
	for _, trade := range trades {
		if trade.ExitReason != "end_of_data" {
			completed = append(completed, trade)
		}
		bySymbol[trade.Symbol] = append(bySymbol[trade.Symbol], trade)
		bySide[string(trade.Side)] = append(bySide[string(trade.Side)], trade)
		byDay[trade.EntryTs.UTC().Format("2006-01-02")] = append(byDay[trade.EntryTs.UTC().Format("2006-01-02")], trade)
	}

	diagnostics := FinalTestDiagnostics{
		CompletedOnly: Metrics(completed, time.Time{}, time.Time{}, costs),
		BySymbol:      groupedBreakdowns(bySymbol, costs),
		BySide:        groupedBreakdowns(bySide, costs),
		ByDay:         groupedBreakdowns(byDay, costs),
	}
	// Symbols are intentionally worst-to-best so the largest sources of
	// final-test loss are visible without manual sorting in the dashboard.
	sort.SliceStable(diagnostics.BySymbol, func(i, j int) bool {
		return diagnostics.BySymbol[i].Metrics.TotalReturnPct < diagnostics.BySymbol[j].Metrics.TotalReturnPct
	})
	sort.SliceStable(diagnostics.BySide, func(i, j int) bool {
		return diagnostics.BySide[i].Label < diagnostics.BySide[j].Label
	})
	sort.SliceStable(diagnostics.ByDay, func(i, j int) bool {
		return diagnostics.ByDay[i].Label < diagnostics.ByDay[j].Label
	})
	return diagnostics
}

func groupedBreakdowns(groups map[string][]Trade, costs CostModel) []Breakdown {
	out := make([]Breakdown, 0, len(groups))
	for label, trades := range groups {
		if label == "" {
			label = "UNKNOWN"
		}
		out = append(out, Breakdown{Label: label, Metrics: Metrics(trades, time.Time{}, time.Time{}, costs)})
	}
	return out
}

// buildWalkForwardReport performs four expanding-window walk-forward folds
// over five equal chronological blocks. Fold 1 selects on block 1 and tests
// block 2; fold 4 selects on blocks 1-4 and tests the untouched fifth block.
// It does not select the main winner, but its aggregate result is one of the
// independent deployment gates that must pass before that winner is applied.
func buildWalkForwardReport(candidates []Candidate, minTs, maxTs time.Time, costs CostModel) WalkForwardReport {
	const blocks = 5
	var report WalkForwardReport
	if len(candidates) == 0 || minTs.IsZero() || !maxTs.After(minTs) {
		return report
	}

	span := maxTs.Sub(minTs)
	var aggregateTrades []Trade
	for foldIndex := 1; foldIndex < blocks; foldIndex++ {
		selectionEnd := minTs.Add(time.Duration(float64(span) * float64(foldIndex) / blocks))
		testEnd := minTs.Add(time.Duration(float64(span) * float64(foldIndex+1) / blocks))
		testTo := testEnd
		if foldIndex == blocks-1 {
			testTo = time.Time{}
		}

		selected, selectionMetrics, candidatesPassed, usedFallback := selectWalkForwardCandidate(
			candidates, minTs, selectionEnd, costs,
		)
		testMetrics := Metrics(selected.allTrades, selectionEnd, testTo, costs)
		passed := !usedFallback && passesSingleFold(testMetrics)
		if passed {
			report.PassedFolds++
		}
		report.Folds = append(report.Folds, WalkForwardFold{
			Index: foldIndex, SelectionStart: minTs, SelectionEnd: selectionEnd,
			TestStart: selectionEnd, TestEnd: testEnd, SelectedParams: selected.Params,
			CandidatesPassed: candidatesPassed, UsedFallback: usedFallback,
			SelectionMetrics: selectionMetrics, TestMetrics: testMetrics, Passed: passed,
		})
		aggregateTrades = append(aggregateTrades, testMetrics.Trades...)
	}
	report.TotalFolds = len(report.Folds)
	report.AggregateMetrics = Metrics(aggregateTrades, time.Time{}, time.Time{}, costs)
	return report
}

type scoredCandidate struct {
	candidate Candidate
	metrics   Result
}

func selectWalkForwardCandidate(candidates []Candidate, from, to time.Time, costs CostModel) (Candidate, Result, int, bool) {
	all := make([]scoredCandidate, 0, len(candidates))
	passing := make([]scoredCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		metrics := Metrics(candidate.allTrades, from, to, costs)
		scored := scoredCandidate{candidate: candidate, metrics: metrics}
		all = append(all, scored)
		if metrics.TotalTrades >= minValidationTrades && metrics.Sharpe > minValidationSharpe {
			passing = append(passing, scored)
		}
	}

	pool := passing
	usedFallback := false
	if len(pool) == 0 {
		pool = all
		usedFallback = true
	}
	sort.SliceStable(pool, func(i, j int) bool {
		if pool[i].metrics.Sharpe == pool[j].metrics.Sharpe {
			return pool[i].metrics.TotalReturnPct > pool[j].metrics.TotalReturnPct
		}
		return pool[i].metrics.Sharpe > pool[j].metrics.Sharpe
	})
	if len(pool) == 0 {
		return Candidate{}, Result{}, len(passing), usedFallback
	}
	return pool[0].candidate, pool[0].metrics, len(passing), usedFallback
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
