package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

func registerMarketRoutes(g *gin.RouterGroup, d Deps) {
	g.GET("/market/candles", func(c *gin.Context) {
		symbol := c.Query("symbol")
		if symbol == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol is required"})
			return
		}
		limit := 200
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		candles, err := d.Market.GetCandles(c.Request.Context(), symbol, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusOK, candles)
	})

	g.GET("/market/funding", func(c *gin.Context) {
		symbol := c.Query("symbol")
		if symbol == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "symbol is required"})
			return
		}
		markPrice, fundingRate, nextFundingTime, ok := d.Market.Funding(symbol)
		if !ok {
			// Stream hasn't produced anything yet - fall back to REST.
			pi, err := d.BingX.PremiumIndex(c.Request.Context(), symbol)
			if err != nil {
				c.JSON(http.StatusServiceUnavailable, gin.H{"error": "funding data not available yet"})
				return
			}
			c.JSON(http.StatusOK, gin.H{
				"symbol": symbol, "mark_price": pi.MarkPrice.Float(), "funding_rate": pi.LastFundingRate.Float(),
				"next_funding_time": pi.NextFundingTime,
			})
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"symbol": symbol, "mark_price": markPrice, "funding_rate": fundingRate, "next_funding_time": nextFundingTime,
		})
	})
}
