package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/models"
	"cryptotrading/internal/signalengine"
	"cryptotrading/internal/strategy"
)

func registerAIRoutes(g *gin.RouterGroup, d Deps) {
	// POST /ai/signal manually triggers the same GenerateAndBroadcast path
	// the automatic watcher uses. It only generates/persists/broadcasts a
	// signal - it never places an order (that's a separate, explicit step
	// via the autotrader, either automatic on a rule transition or manual
	// via POST /orders).
	//
	// Like the automatic path (internal/autotrader.OnCandleClose), this
	// never calls the AI when the deterministic rule currently reads HOLD -
	// a manual click while the rule itself sees no setup would just spend
	// an API call to be told "wait", which the rule already said for free.
	g.POST("/ai/signal", func(c *gin.Context) {
		var body struct {
			Symbol string `json:"symbol" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		params := d.Trader.SBParams()
		candles, err := d.Market.GetCandles(c.Request.Context(), body.Symbol, 720)
		if err != nil || len(candles) < strategy.RequiredM5History(params) {
			c.JSON(http.StatusBadRequest, gin.H{"error": "not enough candle history yet for this symbol"})
			return
		}
		setup := strategy.DecideSilverBullet(candles, nil, params, candles[len(candles)-1].Ts)
		if setup.Action == models.SignalHold {
			c.JSON(http.StatusOK, gin.H{
				"configured": true, "signal": nil,
				"rule_action": setup.Action, "rule_reason": setup.Reason,
				"skipped_reason": "目前沒有符合條件的HTF 3+1設定，未呼叫AI",
			})
			return
		}

		signal, err := signalengine.GenerateAndBroadcast(c.Request.Context(), signalengine.Deps{
			Pool: d.Pool, Market: d.Market, Positions: d.Positions, AI: d.AI, Hub: d.Hub, Model: d.Cfg.AnthropicModel,
		}, body.Symbol, setup)
		if err != nil {
			if errors.Is(err, ai.ErrNotConfigured) {
				c.JSON(http.StatusOK, gin.H{"configured": false})
				return
			}
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, gin.H{"configured": true, "signal": signal})
	})

	g.GET("/ai/signals", func(c *gin.Context) {
		symbol := c.Query("symbol")
		limit := 30
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}

		query := `
			SELECT id, symbol, candle_ts, action, confidence, entry_hint, stop_loss, take_profit, funding_rate, rationale, model, created_at, sweep_ts, sweep_price, fvg_ts, fvg_low, fvg_high, ote_low, ote_high, breaker_low, breaker_high, smt_anchor_symbol, smt_confirmed
			FROM ai_signals`
		args := []any{}
		if symbol != "" {
			query += ` WHERE symbol = $1 ORDER BY created_at DESC LIMIT $2`
			args = []any{symbol, limit}
		} else {
			query += ` ORDER BY created_at DESC LIMIT $1`
			args = []any{limit}
		}

		result, err := d.Pool.Query(c.Request.Context(), query, args...)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer result.Close()

		out := []models.AISignal{}
		for result.Next() {
			var s models.AISignal
			if err := result.Scan(&s.ID, &s.Symbol, &s.CandleTs, &s.Action, &s.Confidence, &s.EntryHint, &s.StopLoss, &s.TakeProfit, &s.FundingRate, &s.Rationale, &s.Model, &s.CreatedAt, &s.SweepTs, &s.SweepPrice, &s.FVGTs, &s.FVGLow, &s.FVGHigh, &s.OTELow, &s.OTEHigh, &s.BreakerLow, &s.BreakerHigh, &s.SMTAnchorSymbol, &s.SMTConfirmed); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, s)
		}
		c.JSON(http.StatusOK, out)
	})
}
