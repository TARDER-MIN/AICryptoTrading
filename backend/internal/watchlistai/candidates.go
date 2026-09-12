// Package watchlistai is the crypto counterpart to the sibling
// TWSEDailyTrading project's own watchlistai package: fetch the tradable
// universe, mechanically pre-filter/rank by liquidity and $-cap
// feasibility, then hand a shortlist to Claude to pick the daily watchlist
// (see refresh.go). The AI-selected list becomes the watchlist itself, not
// a separate parallel list - matching that project's convention.
package watchlistai

import (
	"context"
	"sort"
	"time"

	"cryptotrading/internal/backtest"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

const (
	// DefaultCandidatePool bounds how many liquidity-ranked, cap-feasible
	// symbols get shown to Claude - large enough for a real choice, small
	// enough to keep the prompt (and cost) reasonable.
	DefaultCandidatePool = 50
	DefaultPickCount     = 10

	// SignalScanLookbackDays is how much recent history CountRecentSignals
	// replays per candidate. Short on purpose (vs. the 180 days
	// internal/httpapi.strategy_handlers.go uses for parameter tuning) -
	// this only needs to rank candidates by "does the CURRENT live ruleset
	// actually fire on this symbol lately", not produce a statistically
	// rigorous count, and keeping it short bounds the daily refresh's REST
	// call volume (poolSize+2 anchor fetches, each paginated).
	SignalScanLookbackDays = 14
)

type Candidate struct {
	Symbol         string  `json:"symbol"`
	Price          float64 `json:"price"`
	ChangePct24h   float64 `json:"change_pct_24h"`
	QuoteVolume24h float64 `json:"quote_volume_24h"`
	FundingRate    float64 `json:"funding_rate"`
	// RecentSignalCount is how many Silver Bullet trades the CURRENTLY
	// ACTIVE ICT-2026 rules (all five gates: sweep, displacement FVG, OTE,
	// Breaker confluence, SMT divergence) would have produced for this
	// symbol over the last SignalScanLookbackDays - filled in by
	// AnnotateSignalFrequency, zero until then. This is the primary
	// selection signal for "which symbols actually produce signals under
	// this strategy", as opposed to guessing from surface stats like 24h
	// change%.
	RecentSignalCount int `json:"recent_signal_count"`
}

// FetchCandidates pulls the full USDT-margined perpetual universe (live
// exchangeInfo + 24h ticker + funding rate, all unauthenticated bulk
// calls), keeps only symbols that are actually tradable under the current
// margin*leverage setting (bingx.FilterCache.MaxQtyForCap -
// recommending something the bot can't actually auto-trade would defeat
// the point), ranks by 24h quote volume (the standard liquidity signal),
// and returns the top poolSize.
func FetchCandidates(ctx context.Context, bclient *bingx.Client, filters *bingx.FilterCache, capUSD float64, poolSize int) ([]Candidate, error) {
	symbols, err := bclient.ExchangeInfo(ctx)
	if err != nil {
		return nil, err
	}
	tickers, err := bclient.Ticker24hrAll(ctx)
	if err != nil {
		return nil, err
	}
	fundingBySymbol := map[string]float64{}
	if premiums, err := bclient.PremiumIndexAll(ctx); err == nil {
		for _, p := range premiums {
			fundingBySymbol[p.Symbol] = p.LastFundingRate.Float()
		}
	}
	// A funding-rate fetch failure isn't fatal to the whole selection -
	// candidates just show FundingRate=0 and the AI prompt is told that's
	// "not yet known" rather than blocking the run over a secondary signal.

	tradingUSDT := make(map[string]bool, len(symbols))
	for _, s := range symbols {
		if s.Status == "TRADING" && s.ContractType == "PERPETUAL" && s.QuoteAsset == "USDT" {
			tradingUSDT[s.Symbol] = true
		}
	}

	var candidates []Candidate
	for _, t := range tickers {
		if !tradingUSDT[t.Symbol] {
			continue
		}
		price := t.LastPrice.Float()
		if price <= 0 {
			continue
		}
		sizing := filters.MaxQtyForCap(t.Symbol, price, capUSD)
		if !sizing.Feasible {
			continue
		}
		candidates = append(candidates, Candidate{
			Symbol: t.Symbol, Price: price,
			ChangePct24h: t.PriceChangePercent.Float(), QuoteVolume24h: t.QuoteVolume.Float(),
			FundingRate: fundingBySymbol[t.Symbol],
		})
	}

	sort.Slice(candidates, func(i, j int) bool { return candidates[i].QuoteVolume24h > candidates[j].QuoteVolume24h })
	if len(candidates) > poolSize {
		candidates = candidates[:poolSize]
	}
	return candidates, nil
}

// AnnotateSignalFrequency fills in each candidate's RecentSignalCount by
// replaying the currently-active Silver Bullet rules (params) against the
// last SignalScanLookbackDays of that symbol's history - the same
// backtest.Run engine internal/httpapi/strategy_handlers.go uses for
// parameter tuning, here used to answer "how often does THIS symbol
// actually produce a signal under the rules as they stand today". Fetches
// the two strategy.AnchorSymbols once and reuses them for every candidate's
// SMT check (see strategy.AnchorSymbolFor) - without this, every non-anchor
// candidate would fail SMT confirmation for lack of reference data and show
// a count of zero regardless of how signal-prone it actually is.
//
// A per-candidate fetch/backtest failure only zeroes that one candidate's
// count (logged by the caller, if it cares) rather than aborting the whole
// scan - one bad symbol shouldn't block ranking the other 40-some.
func AnnotateSignalFrequency(ctx context.Context, bclient *bingx.Client, candidates []Candidate, params strategy.SBParams, interval string) []Candidate {
	end := time.Now()
	start := end.AddDate(0, 0, -SignalScanLookbackDays)

	anchorCandles := make(map[string][]models.Candle, len(strategy.AnchorSymbols))
	for _, a := range strategy.AnchorSymbols {
		if c, err := bclient.KlinesRange(ctx, a, interval, start, end); err == nil {
			anchorCandles[a] = c
		}
	}

	out := make([]Candidate, len(candidates))
	for i, c := range candidates {
		out[i] = c
		candles, err := bclient.KlinesRange(ctx, c.Symbol, interval, start, end)
		if err != nil || len(candles) == 0 {
			continue
		}
		anchor := anchorCandles[strategy.AnchorSymbolFor(c.Symbol)]
		trades := backtest.Run(candles, anchor, params, backtest.CostModel{}, nil)
		out[i].RecentSignalCount = len(trades)
	}
	return out
}
