package httpapi

import (
	"encoding/json"
	"log"
	"net/http"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/backtest"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/models"
	"cryptotrading/internal/settings"
	"cryptotrading/internal/strategy"
)

const (
	optimizeHistoryDays        = 180
	optimizeUniverseSize       = 20
	optimizeTrainFraction      = 0.6
	optimizeValidationFraction = 0.2
)

// BingX also exposes stocks, forex, indices and commodities through the
// perpetual-contract endpoints. Those instruments use the prefixes below and
// must never enter this crypto-only strategy's optimization universe.
var bingxNonCryptoPrefixes = []string{"NCCO", "NCFX", "NCSI", "NCSK"}

// selectOptimizeSymbols builds an independent backtest universe from the most
// liquid live BingX crypto perpetuals. It deliberately does not read or change
// the user's live watchlist: tuning sample size and live-trading selection are
// separate concerns.
func selectOptimizeSymbols(contracts []bingx.ExchangeInfoSymbol, tickers []bingx.Ticker24hr, limit int) []string {
	tradableCrypto := make(map[string]bool, len(contracts))
	for _, contract := range contracts {
		symbol := strings.ToUpper(contract.Symbol)
		if contract.Status != "TRADING" || contract.ContractType != "PERPETUAL" || contract.QuoteAsset != "USDT" || isBingXNonCrypto(symbol) {
			continue
		}
		tradableCrypto[symbol] = true
	}

	type rankedSymbol struct {
		symbol      string
		quoteVolume float64
	}
	ranked := make([]rankedSymbol, 0, len(tickers))
	seen := make(map[string]bool, len(tickers))
	for _, ticker := range tickers {
		symbol := strings.ToUpper(ticker.Symbol)
		quoteVolume := ticker.QuoteVolume.Float()
		if !tradableCrypto[symbol] || seen[symbol] || ticker.LastPrice.Float() <= 0 || quoteVolume <= 0 {
			continue
		}
		seen[symbol] = true
		ranked = append(ranked, rankedSymbol{symbol: symbol, quoteVolume: quoteVolume})
	}

	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].quoteVolume == ranked[j].quoteVolume {
			return ranked[i].symbol < ranked[j].symbol
		}
		return ranked[i].quoteVolume > ranked[j].quoteVolume
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}

	symbols := make([]string, len(ranked))
	for i, item := range ranked {
		symbols[i] = item.symbol
	}
	return symbols
}

func isBingXNonCrypto(symbol string) bool {
	for _, prefix := range bingxNonCryptoPrefixes {
		if strings.HasPrefix(symbol, prefix) {
			return true
		}
	}
	return false
}

// fundingHistoryCovers checks that the returned settlement sequence reaches
// both ends of the candle sample. A one-day tolerance is deliberately wider
// than BingX's normal/dynamic settlement intervals while still detecting an
// endpoint that returned only a recent partial history.
func fundingHistoryCovers(candles []models.Candle, rates []bingx.FundingRateEvent) bool {
	if len(candles) == 0 || len(rates) == 0 {
		return false
	}
	const maxCoverageGap = 24 * time.Hour
	return !rates[0].Time.After(candles[0].Ts.Add(maxCoverageGap)) &&
		!rates[len(rates)-1].Time.Before(candles[len(candles)-1].Ts.Add(-maxCoverageGap))
}

func registerStrategyRoutes(g *gin.RouterGroup, d Deps) {
	g.GET("/strategy/params", func(c *gin.Context) {
		params, err := strategy.LoadParams(c.Request.Context(), d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		var trainMetrics, validationMetrics, testMetrics, lastReport json.RawMessage
		var optimizedAt *time.Time
		_ = d.Pool.QueryRow(c.Request.Context(), `
			SELECT train_metrics, validation_metrics, test_metrics, last_report, optimized_at
			FROM strategy_params WHERE id = 1
		`).Scan(&trainMetrics, &validationMetrics, &testMetrics, &lastReport, &optimizedAt)

		c.JSON(http.StatusOK, gin.H{
			"params": params, "train_metrics": trainMetrics, "validation_metrics": validationMetrics,
			"test_metrics": testMetrics, "last_report": lastReport, "optimized_at": optimizedAt,
		})
	})

	// POST /strategy/optimize pulls deep history for an independent pool of
	// the 20 most-liquid BingX crypto perpetuals, grid-searches Silver Bullet
	// parameters against it. Robust selection runs only inside the first 80%
	// development window; the final 20% and direction checks are fail-closed
	// deployment gates. A rejected run saves its report without changing the
	// active parameters. A passing run saves/reloads the winner, while this
	// research action always keeps automatic order execution paused.
	g.POST("/strategy/optimize", func(c *gin.Context) {
		ctx := c.Request.Context()
		paused := false
		if _, err := settings.Update(ctx, d.Pool, settings.UpdateInput{AutotradeEnabled: &paused}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pause auto-trading before optimization: " + err.Error()})
			return
		}

		contracts, err := d.BingX.ExchangeInfo(ctx)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "list BingX contracts: " + err.Error()})
			return
		}
		tickers, err := d.BingX.Ticker24hrAll(ctx)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": "list BingX tickers: " + err.Error()})
			return
		}
		symbols := selectOptimizeSymbols(contracts, tickers, optimizeUniverseSize)
		if len(symbols) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no live crypto perpetuals available for backtesting"})
			return
		}

		end := time.Now()
		start := end.AddDate(0, 0, -optimizeHistoryDays)

		// Always fetch the SMT anchor symbols (strategy.AnchorSymbols) too,
		// even if either one falls outside the current top-20 tradable set -
		// Optimize needs their history to backtest SMT
		// divergence confirmation the same way live trading uses it.
		fetchSet := append([]string{}, symbols...)
		for _, a := range strategy.AnchorSymbols {
			if !slices.Contains(fetchSet, a) {
				fetchSet = append(fetchSet, a)
			}
		}

		candlesBySymbol := make(map[string][]models.Candle, len(fetchSet))
		for _, symbol := range fetchSet {
			candles, err := d.BingX.KlinesRange(ctx, symbol, d.Cfg.KlineInterval, start, end)
			if err != nil {
				if slices.Contains(strategy.AnchorSymbols, symbol) {
					c.JSON(http.StatusBadGateway, gin.H{"error": "fetch required SMT anchor history for " + symbol + ": " + err.Error()})
					return
				}
				log.Printf("strategy optimize: skipping %s after history fetch failed: %v", symbol, err)
				continue
			}
			if len(candles) == 0 {
				if slices.Contains(strategy.AnchorSymbols, symbol) {
					c.JSON(http.StatusBadGateway, gin.H{"error": "no historical data returned for required SMT anchor " + symbol})
					return
				}
				log.Printf("strategy optimize: skipping %s because no historical data was returned", symbol)
				continue
			}
			candlesBySymbol[symbol] = candles
		}

		availableSymbols := make([]string, 0, len(symbols))
		for _, symbol := range symbols {
			if len(candlesBySymbol[symbol]) > 0 {
				availableSymbols = append(availableSymbols, symbol)
			}
		}
		if len(availableSymbols) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no historical data returned for any optimization symbol"})
			return
		}

		// Fetch settlement-by-settlement funding for the same perpetual
		// contracts and date range. Never mix partial coverage into the
		// report: if any symbol fails, omit funding for every symbol and say
		// so through CostModel.FundingIncluded instead of presenting unlike
		// cost bases as comparable results. The endpoint is rate-limited to
		// 1 request/s per IP, hence the deliberate spacing.
		fundingBySymbol := make(map[string][]backtest.FundingEvent, len(availableSymbols))
		fundingComplete := true
		for i, symbol := range availableSymbols {
			if i > 0 {
				select {
				case <-ctx.Done():
					return
				case <-time.After(1100 * time.Millisecond):
				}
			}
			candles := candlesBySymbol[symbol]
			rates, err := d.BingX.FundingRatesRange(ctx, symbol, candles[0].Ts, candles[len(candles)-1].Ts)
			if err != nil || !fundingHistoryCovers(candles, rates) {
				if err != nil {
					log.Printf("strategy optimize: historical funding unavailable for %s; omitting funding from the entire report: %v", symbol, err)
				} else {
					log.Printf("strategy optimize: incomplete historical funding coverage for %s; omitting funding from the entire report", symbol)
				}
				fundingComplete = false
				fundingBySymbol = nil
				break
			}
			converted := make([]backtest.FundingEvent, 0, len(rates))
			for _, rate := range rates {
				converted = append(converted, backtest.FundingEvent{
					Ts: rate.Time, Rate: rate.Rate, MarkPrice: rate.MarkPrice,
				})
			}
			fundingBySymbol[symbol] = converted
		}

		takerFeePct := d.Cfg.BacktestTakerFeePct
		feeSource := "env_fallback"
		if commission, err := d.BingX.UserCommissionRate(ctx); err != nil {
			log.Printf("strategy optimize: account-specific commission unavailable; using configured %.4f%% taker fee per side: %v", takerFeePct, err)
		} else {
			takerFeePct = commission.TakerCommissionRate * 100
			feeSource = "bingx_account"
		}

		costs := backtest.CostModel{
			TakerFeePctPerSide:          takerFeePct,
			EstimatedSlippagePctPerSide: d.Cfg.BacktestSlippagePct,
			FundingIncluded:             fundingComplete,
			FundingSource:               "bingx_history",
			FeeSource:                   feeSource,
		}
		if !fundingComplete {
			costs.FundingSource = "unavailable"
		}
		report := backtest.Optimize(
			candlesBySymbol, fundingBySymbol, availableSymbols,
			optimizeTrainFraction, optimizeValidationFraction, costs,
		)
		report.HistoryDays = optimizeHistoryDays
		report.SymbolsTested = availableSymbols
		for _, symbol := range availableSymbols {
			candles := candlesBySymbol[symbol]
			report.CandlesTested += len(candles)
			if report.HistoryStart.IsZero() || candles[0].Ts.Before(report.HistoryStart) {
				report.HistoryStart = candles[0].Ts
			}
			last := candles[len(candles)-1].Ts
			if report.HistoryEnd.IsZero() || last.After(report.HistoryEnd) {
				report.HistoryEnd = last
			}
		}
		if report.HistoryEnd.After(report.HistoryStart) {
			report.HistoryAvailableDays = report.HistoryEnd.Sub(report.HistoryStart).Hours() / 24
		}

		trainJSON, _ := json.Marshal(report.Train)
		validJSON, _ := json.Marshal(report.Validation)
		testJSON, _ := json.Marshal(report.Test)
		reportJSON, _ := json.Marshal(report)
		var saveErr error
		if report.ParamsApplied {
			saveErr = strategy.SaveParams(ctx, d.Pool, report.Best, trainJSON, validJSON, testJSON, reportJSON)
		} else {
			saveErr = strategy.SaveOptimizationReport(ctx, d.Pool, reportJSON)
		}
		if saveErr != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": saveErr.Error()})
			return
		}
		if report.ParamsApplied {
			d.Trader.SetSBParams(report.Best)
		}

		c.JSON(http.StatusOK, report)
	})
}
