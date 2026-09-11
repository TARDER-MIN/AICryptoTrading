package bingx

import (
	"context"
	"sync"
	"time"
)

// clockSync tracks the offset between local time and Binance server time so
// signed requests don't get rejected with -1021 "Timestamp for this request
// is outside of the recvWindow" due to local clock drift.
type clockSync struct {
	mu     sync.RWMutex
	offset time.Duration
}

func newClockSync() *clockSync {
	return &clockSync{}
}

func (c *clockSync) Now() time.Time {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return time.Now().Add(c.offset)
}

// Sync fetches Binance's server time and updates the offset. Call once at
// startup and then periodically (e.g. hourly) from a background loop.
func (c *clockSync) Set(serverTimeMS int64) {
	serverTime := time.UnixMilli(serverTimeMS)
	c.mu.Lock()
	c.offset = time.Until(serverTime)
	c.mu.Unlock()
}

// SyncClock exposes the client's clock sync for the startup/periodic loop in cmd/server.
func (c *Client) SyncClock(ctx context.Context) error {
	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := c.do(ctx, "GET", "/openApi/swap/v2/server/time", nil, false, false, &out); err != nil {
		return err
	}
	c.clock.Set(out.ServerTime)
	return nil
}
