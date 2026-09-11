// Package marketdata streams live BingX klines into Postgres and the
// websocket hub. Unlike the reference TWSE project, BingX's kline stream
// already tracks a fully-formed OHLCV bar server-side on every update (the
// exchange, not this client, is doing the tick aggregation) - so there's no
// local Aggregator, just a straight upsert of whatever BingX last sent.
package marketdata

import (
	"context"
	"log"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/bingx"
	"cryptotrading/internal/models"
	"cryptotrading/internal/ws"
)

type Service struct {
	pool   *pgxpool.Pool
	client *bingx.Client
	hub    *ws.Hub

	fundingMu sync.RWMutex
	funding   map[string]fundingState

	// OnCandleClose, if set, fires whenever a bar finalizes (whatever
	// interval KLINE_INTERVAL is configured to - BingX's kline "x"
	// field). Called synchronously from the stream
	// read loop - implementations that do network calls (AI, order
	// placement) must hand off to their own goroutine, not block here.
	OnCandleClose func(ctx context.Context, c models.Candle)
}

type fundingState struct {
	MarkPrice       float64
	FundingRate     float64
	NextFundingTime time.Time
}

func NewService(pool *pgxpool.Pool, client *bingx.Client, hub *ws.Hub) *Service {
	return &Service{pool: pool, client: client, hub: hub, funding: make(map[string]fundingState)}
}

// Run subscribes to the given symbols' kline+markPrice streams and blocks
// until ctx is cancelled or the underlying stream gives up reconnecting.
func (s *Service) Run(ctx context.Context, symbols []string, interval string) error {
	return s.client.RunMarketStream(ctx, symbols, interval, func(evt bingx.KlineEvent) {
		s.persist(ctx, evt.Candle)
		s.hub.Broadcast(ws.Message{Type: "candle", Data: evt.Candle})
		if evt.Closed && s.OnCandleClose != nil {
			s.OnCandleClose(ctx, evt.Candle)
		}
	}, func(evt bingx.MarkPriceEvent) {
		s.fundingMu.Lock()
		s.funding[evt.Symbol] = fundingState{MarkPrice: evt.MarkPrice, FundingRate: evt.FundingRate, NextFundingTime: evt.NextFundingTime}
		s.fundingMu.Unlock()
		s.hub.Broadcast(ws.Message{Type: "funding", Data: map[string]any{
			"symbol": evt.Symbol, "mark_price": evt.MarkPrice, "funding_rate": evt.FundingRate, "next_funding_time": evt.NextFundingTime,
		}})
	})
}

func (s *Service) persist(ctx context.Context, c models.Candle) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO candles (symbol, ts, open, high, low, close, volume)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (symbol, ts) DO UPDATE SET
			open = EXCLUDED.open, high = EXCLUDED.high, low = EXCLUDED.low,
			close = EXCLUDED.close, volume = EXCLUDED.volume
	`, c.Symbol, c.Ts, c.Open, c.High, c.Low, c.Close, c.Volume)
	if err != nil {
		log.Printf("marketdata: persist candle failed: %v", err)
	}
}

// GetCandles returns the most recent `limit` candles for symbol, oldest first.
func (s *Service) GetCandles(ctx context.Context, symbol string, limit int) ([]models.Candle, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT symbol, ts, open, high, low, close, volume
		FROM candles
		WHERE symbol = $1
		ORDER BY ts DESC
		LIMIT $2
	`, symbol, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []models.Candle{}
	for rows.Next() {
		var c models.Candle
		if err := rows.Scan(&c.Symbol, &c.Ts, &c.Open, &c.High, &c.Low, &c.Close, &c.Volume); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// PollCandles is a REST-based fallback/supplement to the live kline
// WebSocket stream. Some network environments (firewall/VPN/antivirus)
// complete the WS handshake but never actually deliver data frames
// afterward, while plain HTTPS REST keeps working fine - in that case the
// WS path silently produces nothing and the chart (and, critically, the
// OnCandleClose-driven strategy/AI/autotrader pipeline, which ONLY fires
// from the WS path) would otherwise go dead with no visible error. This
// polls the last 2 klines (the most recent CLOSED bar plus the
// currently-forming one) per symbol on a fixed interval, persists+
// broadcasts both exactly like a live kline event would, and fires
// OnCandleClose the first time a given symbol's closed-bar timestamp is
// newly observed - REST always returns closed bars deterministically, so
// this is a reliable close-detection signal independent of whether the WS
// "x":true flag ever reaches us. Runs concurrently with Run(); if the WS
// path IS working in a given environment, both paths may occasionally
// observe the same close and fire OnCandleClose twice for it - harmless
// (the rule-transition gate in autotrader just re-evaluates to the same
// non-transitioning result), not a double order (the cooldown guard there
// prevents that regardless).
func (s *Service) PollCandles(ctx context.Context, symbols []string, interval string, every time.Duration) {
	lastClosed := make(map[string]time.Time)
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, symbol := range symbols {
				candles, err := s.client.Klines(ctx, symbol, interval, 2)
				if err != nil {
					log.Printf("marketdata: poll klines for %s failed: %v", symbol, err)
					continue
				}
				if len(candles) == 0 {
					continue
				}
				forming := candles[len(candles)-1]
				for _, c := range candles[:len(candles)-1] {
					s.persist(ctx, c)
					s.hub.Broadcast(ws.Message{Type: "candle", Data: c})
					if c.Ts.After(lastClosed[symbol]) {
						lastClosed[symbol] = c.Ts
						if s.OnCandleClose != nil {
							go s.OnCandleClose(ctx, c)
						}
					}
				}
				s.persist(ctx, forming)
				s.hub.Broadcast(ws.Message{Type: "candle", Data: forming})
			}
		}
	}
}

// PollFunding is the REST-based fallback/supplement for funding-rate data,
// for the same reason PollCandles exists - populates the same cache the
// live @markPrice WS stream would, so Funding()/the AI prompt's funding
// context isn't silently stuck at zero when the WS path isn't delivering.
func (s *Service) PollFunding(ctx context.Context, symbols []string, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			for _, symbol := range symbols {
				pi, err := s.client.PremiumIndex(ctx, symbol)
				if err != nil {
					log.Printf("marketdata: poll premiumIndex for %s failed: %v", symbol, err)
					continue
				}
				markPrice, fundingRate := pi.MarkPrice.Float(), pi.LastFundingRate.Float()
				nextFundingTime := time.UnixMilli(pi.NextFundingTime)
				s.fundingMu.Lock()
				s.funding[symbol] = fundingState{MarkPrice: markPrice, FundingRate: fundingRate, NextFundingTime: nextFundingTime}
				s.fundingMu.Unlock()
				s.hub.Broadcast(ws.Message{Type: "funding", Data: map[string]any{
					"symbol": symbol, "mark_price": markPrice, "funding_rate": fundingRate, "next_funding_time": nextFundingTime,
				}})
			}
		}
	}
}

// SeedHistory backfills the candles table from BingX's REST klines
// endpoint so a fresh chart/indicator window isn't empty on first load or
// after downtime.
func (s *Service) SeedHistory(ctx context.Context, symbol, interval string, limit int) error {
	candles, err := s.client.Klines(ctx, symbol, interval, limit)
	if err != nil {
		return err
	}
	for _, c := range candles {
		s.persist(ctx, c)
	}
	return nil
}

// Funding returns the latest cached mark price / funding rate for symbol,
// populated by the @markPrice stream. ok is false until the first update
// arrives (e.g. briefly after startup) - callers should fall back to a REST
// PremiumIndex call in that case.
func (s *Service) Funding(symbol string) (markPrice, fundingRate float64, nextFundingTime time.Time, ok bool) {
	s.fundingMu.RLock()
	defer s.fundingMu.RUnlock()
	st, found := s.funding[symbol]
	if !found {
		return 0, 0, time.Time{}, false
	}
	return st.MarkPrice, st.FundingRate, st.NextFundingTime, true
}
