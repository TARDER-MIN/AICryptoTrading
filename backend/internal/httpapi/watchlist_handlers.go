package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/models"
	"cryptotrading/internal/settings"
)

func registerWatchlistRoutes(g *gin.RouterGroup, d Deps) {
	// GET /watchlist always recomputes live feasibility (price, min
	// notional, step size, tradable-at-cap) from the cached exchangeInfo
	// filters rather than trusting anything hardcoded or stale - this is
	// how the dashboard shows e.g. BTCUSDT/ETHUSDT as chart-only.
	g.GET("/watchlist", func(c *gin.Context) {
		rows, err := d.Pool.Query(c.Request.Context(), `SELECT id, symbol, enabled, added_at FROM watchlist WHERE enabled ORDER BY symbol`)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()

		st, err := settings.Get(c.Request.Context(), d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		out := []models.WatchlistItem{}
		for rows.Next() {
			var item models.WatchlistItem
			if err := rows.Scan(&item.ID, &item.Symbol, &item.Enabled, &item.AddedAt); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}

			if candles, err := d.Market.GetCandles(c.Request.Context(), item.Symbol, 1); err == nil && len(candles) > 0 {
				price := candles[len(candles)-1].Close
				item.Price = &price
			}
			if f, ok := d.Filters.Get(item.Symbol); ok {
				item.MinNotionalUSD = &f.MinNotionalUSD
				item.StepSize = &f.StepSize
			}
			if item.Price != nil {
				sizing := d.Filters.MaxQtyForCap(item.Symbol, *item.Price, st.EffectiveNotionalUSD)
				item.TradableAtCap = sizing.Feasible
				if sizing.Feasible {
					item.MaxQtyAtCap = &sizing.Qty
				}
			}
			if _, fundingRate, _, ok := d.Market.Funding(item.Symbol); ok {
				item.FundingRate = &fundingRate
			}

			out = append(out, item)
		}
		c.JSON(http.StatusOK, out)
	})

	g.POST("/watchlist", func(c *gin.Context) {
		var body struct {
			Symbol string `json:"symbol" binding:"required"`
		}
		if err := c.ShouldBindJSON(&body); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if _, ok := d.Filters.Get(body.Symbol); !ok {
			c.JSON(http.StatusBadRequest, gin.H{"error": "unknown symbol (not found in BingX exchangeInfo)"})
			return
		}

		_, err := d.Pool.Exec(c.Request.Context(), `
			INSERT INTO watchlist (symbol) VALUES ($1)
			ON CONFLICT (symbol) DO UPDATE SET enabled = true
		`, body.Symbol)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		if d.RestartMarketStream != nil {
			d.RestartMarketStream()
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	g.DELETE("/watchlist/:symbol", func(c *gin.Context) {
		symbol := c.Param("symbol")
		_, err := d.Pool.Exec(c.Request.Context(), `UPDATE watchlist SET enabled = false WHERE symbol = $1`, symbol)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if d.RestartMarketStream != nil {
			d.RestartMarketStream()
		}
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})
}
