package httpapi

import (
	"encoding/json"
	"log"
	"math"
	"net/http"
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
	longResearchYears          = 1
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

		var trainMetrics, validationMetrics, testMetrics, lastReport, lastLongReport json.RawMessage
		var optimizedAt *time.Time
		if err := d.Pool.QueryRow(c.Request.Context(), `
			SELECT COALESCE(train_metrics, 'null'::jsonb),
			       COALESCE(validation_metrics, 'null'::jsonb),
			       COALESCE(test_metrics, 'null'::jsonb),
			       COALESCE(last_report, 'null'::jsonb),
			       COALESCE(last_long_report, 'null'::jsonb),
			       optimized_at
			FROM strategy_params WHERE id = 1
		`).Scan(&trainMetrics, &validationMetrics, &testMetrics, &lastReport, &lastLongReport, &optimizedAt); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"params": params, "train_metrics": trainMetrics, "validation_metrics": validationMetrics,
			"test_metrics": testMetrics, "last_report": lastReport,
			"last_long_report": lastLongReport, "optimized_at": optimizedAt,
		})
	})

	// POST /strategy/optimize reuses the exact symbols and date range from the
	// previous report when available, making the new HTF 3+1 result directly
	// comparable with the user's saved baseline. A clean installation instead
	// selects the current 20 most-liquid BingX crypto perpetuals and requests
	// up to 180 days. Robust selection runs only inside the first 80%
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
		if d.Cfg.KlineInterval != "5m" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "HTF 3+1 research requires KLINE_INTERVAL=5m"})
			return
		}

		var previousRaw json.RawMessage
		var previous backtest.OptimizeReport
		if err := d.Pool.QueryRow(ctx, `
			SELECT COALESCE(last_report, 'null'::jsonb)
			FROM strategy_params WHERE id = 1
		`).Scan(&previousRaw); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(previousRaw) > 0 {
			_ = json.Unmarshal(previousRaw, &previous)
		}

		symbols := append([]string{}, previous.SymbolsTested...)
		start, end := previous.HistoryStart, previous.HistoryEnd
		reusedPreviousSample := len(symbols) > 0 && !start.IsZero() && end.After(start) && !end.After(time.Now().Add(time.Hour))
		requestedHistoryDays := optimizeHistoryDays
		if reusedPreviousSample {
			requestedHistoryDays = int(math.Ceil(end.Sub(start).Hours() / 24))
		} else {
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
			symbols = selectOptimizeSymbols(contracts, tickers, optimizeUniverseSize)
			end = time.Now().UTC()
			start = end.AddDate(0, 0, -optimizeHistoryDays)
		}
		if len(symbols) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no live crypto perpetuals available for backtesting"})
			return
		}

		fetchSet := append([]string{}, symbols...)

		candlesBySymbol := make(map[string][]models.Candle, len(fetchSet))
		unavailableSymbols := make([]string, 0)
		for _, symbol := range fetchSet {
			candles, err := d.BingX.KlinesRange(ctx, symbol, d.Cfg.KlineInterval, start, end)
			if err != nil {
				log.Printf("strategy optimize: skipping %s after history fetch failed: %v", symbol, err)
				unavailableSymbols = append(unavailableSymbols, symbol)
				continue
			}
			if len(candles) == 0 {
				log.Printf("strategy optimize: skipping %s because no historical data was returned", symbol)
				unavailableSymbols = append(unavailableSymbols, symbol)
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
		report.ReportKind = "recent_bingx"
		report.DataSource = "bingx_usdt_perpetual"
		report.ReusedPreviousSample = reusedPreviousSample
		report.HistoryDays = requestedHistoryDays
		report.SymbolsTested = availableSymbols
		report.SymbolsUnavailable = unavailableSymbols
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

	// POST /strategy/optimize-long repeats the same HTF 3+1 research on the
	// exact symbol universe and end date from the latest BingX run, but asks
	// for one full calendar year of public Binance USDT-perpetual M5 history.
	// BingX currently exposes a much shorter M5 range, so this is an explicitly
	// labelled cross-venue proxy study. It is saved separately and can never
	// change live parameters, even when every robustness gate passes.
	g.POST("/strategy/optimize-long", func(c *gin.Context) {
		ctx := c.Request.Context()
		paused := false
		if _, err := settings.Update(ctx, d.Pool, settings.UpdateInput{AutotradeEnabled: &paused}); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "pause auto-trading before long-range research: " + err.Error()})
			return
		}
		if d.Cfg.KlineInterval != "5m" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "HTF 3+1 long-range research requires KLINE_INTERVAL=5m"})
			return
		}
		if d.Research == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "long-range research data client is unavailable"})
			return
		}

		// Reuse the exact symbols and endpoint date from the immediately
		// comparable BingX report whenever it exists. A clean install may run
		// this button first, in which case the current top-20 universe is used.
		var previousRaw json.RawMessage
		var previous backtest.OptimizeReport
		if err := d.Pool.QueryRow(ctx, `
			SELECT COALESCE(last_report, 'null'::jsonb)
			FROM strategy_params WHERE id = 1
		`).Scan(&previousRaw); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if len(previousRaw) > 0 {
			_ = json.Unmarshal(previousRaw, &previous)
		}
		symbols := append([]string{}, previous.SymbolsTested...)
		end := previous.HistoryEnd
		if len(symbols) == 0 {
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
			symbols = selectOptimizeSymbols(contracts, tickers, optimizeUniverseSize)
		}
		if len(symbols) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no crypto perpetual symbols available for long-range research"})
			return
		}
		if end.IsZero() || end.After(time.Now().Add(time.Hour)) {
			end = time.Now().UTC()
		}
		start := end.AddDate(-longResearchYears, 0, 0)

		candlesBySymbol := make(map[string][]models.Candle, len(symbols))
		availableSymbols := make([]string, 0, len(symbols))
		unavailableSymbols := make([]string, 0)
		partialSymbols := make([]string, 0)
		for index, symbol := range symbols {
			log.Printf("strategy long research: fetching %s (%d/%d)", symbol, index+1, len(symbols))
			candles, err := d.Research.KlinesRange(ctx, symbol, d.Cfg.KlineInterval, start, end)
			if err != nil || len(candles) == 0 {
				if err != nil {
					log.Printf("strategy long research: %s unavailable on Binance proxy: %v", symbol, err)
				}
				unavailableSymbols = append(unavailableSymbols, symbol)
				continue
			}
			candlesBySymbol[symbol] = candles
			availableSymbols = append(availableSymbols, symbol)
			availableDays := candles[len(candles)-1].Ts.Sub(candles[0].Ts).Hours() / 24
			if availableDays < 350 {
				partialSymbols = append(partialSymbols, symbol)
			}
		}
		if len(availableSymbols) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{
				"error": "the Binance proxy returned no one-year history for the selected BingX symbols",
			})
			return
		}

		// Use the proxy venue's own historical funding settlements. As with the
		// BingX run, a single incomplete series disables funding for the entire
		// report so every symbol is compared on the same cost basis.
		fundingBySymbol := make(map[string][]backtest.FundingEvent, len(availableSymbols))
		fundingComplete := true
		for _, symbol := range availableSymbols {
			candles := candlesBySymbol[symbol]
			rates, err := d.Research.FundingRatesRange(ctx, symbol, candles[0].Ts, candles[len(candles)-1].Ts)
			covered := err == nil && len(rates) > 0 &&
				!rates[0].Time.After(candles[0].Ts.Add(24*time.Hour)) &&
				!rates[len(rates)-1].Time.Before(candles[len(candles)-1].Ts.Add(-24*time.Hour))
			if !covered {
				if err != nil {
					log.Printf("strategy long research: funding unavailable for %s; omitting proxy funding: %v", symbol, err)
				} else {
					log.Printf("strategy long research: incomplete funding for %s; omitting proxy funding", symbol)
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
			log.Printf("strategy long research: BingX account commission unavailable; using configured %.4f%%: %v", takerFeePct, err)
		} else {
			takerFeePct = commission.TakerCommissionRate * 100
			feeSource = "bingx_account"
		}
		costs := backtest.CostModel{
			TakerFeePctPerSide:          takerFeePct,
			EstimatedSlippagePctPerSide: d.Cfg.BacktestSlippagePct,
			FundingIncluded:             fundingComplete,
			FundingSource:               "binance_proxy_history",
			FeeSource:                   feeSource,
		}
		if !fundingComplete {
			costs.FundingSource = "unavailable"
		}

		report := backtest.Optimize(
			candlesBySymbol, fundingBySymbol, availableSymbols,
			optimizeTrainFraction, optimizeValidationFraction, costs,
		)
		report.ReportKind = "long_range_proxy"
		report.DataSource = "binance_usdt_perpetual_proxy"
		report.ResearchOnly = true
		report.HistoryDays = int(end.Sub(start).Hours() / 24)
		report.SymbolsTested = availableSymbols
		report.SymbolsUnavailable = unavailableSymbols
		report.SymbolsPartial = partialSymbols
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

		// Optimize marks a passing report as applicable. Preserve that verdict
		// separately, then hard-disable application for all proxy studies.
		report.RobustnessPassed = report.ParamsApplied
		report.ParamsApplied = false
		reportJSON, _ := json.Marshal(report)
		if err := strategy.SaveLongOptimizationReport(ctx, d.Pool, reportJSON); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, report)
	})
}
