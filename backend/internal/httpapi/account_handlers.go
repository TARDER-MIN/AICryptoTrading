package httpapi

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"cryptotrading/internal/models"
)

func registerAccountRoutes(g *gin.RouterGroup, d Deps) {
	g.GET("/account/balance", func(c *gin.Context) {
		c.JSON(http.StatusOK, d.Positions.Account())
	})

	g.GET("/account/positions", func(c *gin.Context) {
		c.JSON(http.StatusOK, d.Positions.Positions())
	})

	g.GET("/account/orders", func(c *gin.Context) {
		limit := 50
		if v := c.Query("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := d.Pool.Query(c.Request.Context(), `
			SELECT id, binance_order_id, algo_id, client_order_id, symbol, side, order_type, reduce_only, qty, notional_usd, leverage, status, source, filled_price, filled_at, submitted_at, updated_at
			FROM orders ORDER BY submitted_at DESC LIMIT $1
		`, limit)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
			return
		}
		defer rows.Close()

		out := []models.Order{}
		for rows.Next() {
			var o models.Order
			if err := rows.Scan(&o.ID, &o.BingXOrderID, &o.AlgoID, &o.ClientOrderID, &o.Symbol, &o.Side, &o.OrderType, &o.ReduceOnly, &o.Qty, &o.NotionalUSD, &o.Leverage, &o.Status, &o.Source, &o.FilledPrice, &o.FilledAt, &o.SubmittedAt, &o.UpdatedAt); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
				return
			}
			out = append(out, o)
		}
		c.JSON(http.StatusOK, out)
	})
}
