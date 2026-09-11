package httpapi

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/settings"
	"cryptotrading/internal/strategy"
	"cryptotrading/internal/watchlistai"
)

// registerWatchlistAIRoutes wires the "AI daily pick" feature: POST
// triggers a fresh selection (replacing the live watchlist with the
// picks), GET returns the most recent run for display. Mirrors the
// sibling TWSEDailyTrading project's watchlistai endpoints, adapted for a
// 24/7 perpetuals market (no weekday/session skip logic).
func registerWatchlistAIRoutes(g *gin.RouterGroup, d Deps) {
	g.POST("/watchlist/ai-refresh", func(c *gin.Context) {
		ctx := c.Request.Context()

		st, err := settings.Get(ctx, d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		// Loaded fresh (not from the running Trader's cached copy) so a
		// refresh always ranks candidates against whatever rules are
		// actually live right now, including one just saved by a
		// POST /strategy/optimize this process hasn't been restarted since.
		sbParams, err := strategy.LoadParams(ctx, d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}

		result, err := watchlistai.Refresh(ctx, d.Pool, d.AI, d.BingX, d.Filters,
			st.EffectiveNotionalUSD, watchlistai.DefaultCandidatePool, watchlistai.DefaultPickCount,
			sbParams, d.Cfg.KlineInterval)
		if err != nil {
			c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
			return
		}

		if d.RestartMarketStream != nil {
			d.RestartMarketStream()
		}
		c.JSON(http.StatusOK, result)
	})

	g.GET("/watchlist/ai-picks/latest", func(c *gin.Context) {
		result, err := watchlistai.LatestResult(c.Request.Context(), d.Pool)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		if result == nil {
			c.JSON(http.StatusOK, gin.H{"run_date": nil, "picks": []any{}})
			return
		}
		c.JSON(http.StatusOK, result)
	})
}
