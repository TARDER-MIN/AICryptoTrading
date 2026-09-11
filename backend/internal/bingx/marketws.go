package bingx

import (
	"context"
	"time"

	"cryptotrading/internal/models"
)

type (
	KlineEvent struct {
		Candle models.Candle
		Closed bool
	}
	MarkPriceEvent struct {
		Symbol                 string
		MarkPrice, FundingRate float64
		NextFundingTime        time.Time
	}
)

// RunMarketStream intentionally leaves live delivery to marketdata's REST
// pollers. They run every 8/10 seconds and are more reliable on networks that
// accept HTTPS but silently block long-lived WebSockets. Keeping this method
// blocking preserves the service lifecycle without creating a fake WS feed.
func (c *Client) RunMarketStream(ctx context.Context, symbols []string, interval string, onKline func(KlineEvent), onMarkPrice func(MarkPriceEvent)) error {
	<-ctx.Done()
	return ctx.Err()
}
