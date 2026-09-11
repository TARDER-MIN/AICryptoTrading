package httpapi

import (
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/backtest"
	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

const (
	optimizeHistoryDays   = 60
	optimizeTrainFraction = 0.7
)

func registerStrategyRoutes(g *gin.RouterGroup, d Deps) {
	g.GET("/strategy/params", func(c *gin.Context) {
		params, err := strategy.LoadParams(c.Request.Context(), d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		var trainMetrics, validationMetrics json.RawMessage
		var optimizedAt *time.Time
		_ = d.Pool.QueryRow(c.Request.Context(), `
			SELECT train_metrics, validation_metrics, optimized_at FROM strategy_params WHERE id = 1
		`).Scan(&trainMetrics, &validationMetrics, &optimizedAt)

		c.JSON(http.StatusOK, gin.H{
			"params": params, "train_metrics": trainMetrics, "validation_metrics": validationMetrics, "optimized_at": optimizedAt,
		})
	})

	// POST /strategy/optimize pulls deep history for every enabled
	// watchlist symbol, grid-searches Silver Bullet parameters against it
	// (train/validation split, ranked by VALIDATION Sharpe - see
	// internal/backtest.Optimize for why), saves the winning set, and
	// live-reloads the running autotrader.Trader so it takes effect
	// immediately without a restart. Manually triggered and can take a
	// while (deep history fetch + a few hundred backtest runs per symbol).
	g.POST("/strategy/optimize", func(c *gin.Context) {
		ctx := c.Request.Context()

		rows, err := d.Pool.Query(ctx, `SELECT symbol FROM watchlist WHERE enabled`)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		var symbols []string
		for rows.Next() {
			var s string
			if err := rows.Scan(&s); err != nil {
				rows.Close()
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			symbols = append(symbols, s)
		}
		rows.Close()
		if len(symbols) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "watchlist is empty"})
			return
		}

		end := time.Now()
		start := end.AddDate(0, 0, -optimizeHistoryDays)

		// Always fetch the SMT anchor symbols (strategy.AnchorSymbols) too,
		// even if the AI daily watchlist selection dropped them from the
		// tradable set - Optimize needs their history to backtest SMT
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
				c.JSON(http.StatusBadGateway, gin.H{"error": "fetch history for " + symbol + ": " + err.Error()})
				return
			}
			if len(candles) > 0 {
				candlesBySymbol[symbol] = candles
			}
		}
		if len(candlesBySymbol) == 0 {
			c.JSON(http.StatusBadGateway, gin.H{"error": "no historical data returned for any watchlist symbol"})
			return
		}

		report := backtest.Optimize(candlesBySymbol, symbols, optimizeTrainFraction)

		trainJSON, _ := json.Marshal(report.Train)
		validJSON, _ := json.Marshal(report.Validation)
		if err := strategy.SaveParams(ctx, d.Pool, report.Best, trainJSON, validJSON); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		d.Trader.SetSBParams(report.Best)

		c.JSON(http.StatusOK, report)
	})
}
