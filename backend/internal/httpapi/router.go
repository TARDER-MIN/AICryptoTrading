// Package httpapi wires the Gin routes for the dashboard. Handlers are thin:
// they parse the request, call into internal/{marketdata,binance,autotrader,
// signalengine,settings}, and serialize the result.
package httpapi

import (
	"github.com/gin-gonic/gin"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/autotrader"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/config"
	"cryptotrading/internal/marketdata"
	"cryptotrading/internal/positionstore"
	"cryptotrading/internal/ws"
)

type Deps struct {
	Cfg       config.Config
	Pool      *pgxpool.Pool
	Market    *marketdata.Service
	BingX     *bingx.Client
	Filters   *bingx.FilterCache
	Positions *positionstore.Store
	Trader    *autotrader.Trader
	AI        *ai.Client
	Hub       *ws.Hub

	// RestartMarketStream tells the live kline-streaming loop to re-read the
	// watchlist and resubscribe - called after a watchlist add/remove so new
	// symbols start streaming without a backend restart.
	RestartMarketStream func()
}

func NewRouter(d Deps) *gin.Engine {
	r := gin.Default()
	r.Use(corsMiddleware())

	r.GET("/ws/market", func(c *gin.Context) { d.Hub.ServeWS(c.Writer, c.Request) })
	r.GET("/healthz", func(c *gin.Context) { c.JSON(200, gin.H{"status": "ok"}) })

	api := r.Group("/api")
	if d.Cfg.APIKey != "" {
		api.Use(apiKeyMiddleware(d.Cfg.APIKey))
	}

	registerMarketRoutes(api, d)
	registerAccountRoutes(api, d)
	registerOrderRoutes(api, d)
	registerAIRoutes(api, d)
	registerAutotradeRoutes(api, d)
	registerSettingsRoutes(api, d)
	registerWatchlistRoutes(api, d)
	registerWatchlistAIRoutes(api, d)
	registerStrategyRoutes(api, d)

	return r
}
