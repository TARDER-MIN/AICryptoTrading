package main

import (
	"context"
	"log"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/autotrader"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/marketdata"
	"cryptotrading/internal/strategy"
)

// withAnchors returns tradable with strategy.AnchorSymbols (BTC/ETH) merged
// in, deduped - so their candles/funding keep streaming for SMT divergence
// lookups even on a day the AI daily watchlist selection drops both of them
// from the tradable set. autotrader.Trader.OnCandleClose separately guards
// against ever auto-trading a symbol that isn't actually on the tradable
// watchlist, so streaming an anchor-only symbol here is safe.
func withAnchors(tradable []string) []string {
	out := append([]string{}, tradable...)
	for _, a := range strategy.AnchorSymbols {
		if !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	return out
}

func watchlistSymbols(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	rows, err := pool.Query(ctx, `SELECT symbol FROM watchlist WHERE enabled ORDER BY added_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// seedHistory backfills each symbol's candle table with recent history via
// REST so the chart and indicator windows (MACD needs ~26+9 bars to be
// meaningful) aren't empty on first load. Safe to call repeatedly -
// persistence is an upsert.
func seedHistory(ctx context.Context, market *marketdata.Service, symbols []string, interval string) {
	for _, symbol := range symbols {
		if err := market.SeedHistory(ctx, symbol, interval, 200); err != nil {
			log.Printf("seed history for %s: %v", symbol, err)
		}
	}
}

// streamMarketData runs the live kline stream forever (until ctx is
// cancelled), re-reading the watchlist and reseeding history every time it
// (re)starts - on the initial call, after a transient error, and whenever
// restartCh fires (a watchlist add/remove) - so new symbols start streaming
// without a process restart.
func streamMarketData(ctx context.Context, pool *pgxpool.Pool, market *marketdata.Service, interval string, restartCh <-chan struct{}) {
	for {
		if ctx.Err() != nil {
			return
		}

		symbols, err := watchlistSymbols(ctx, pool)
		if err != nil {
			log.Printf("marketdata: load watchlist: %v; retrying in 3s", err)
			select {
			case <-time.After(3 * time.Second):
				continue
			case <-ctx.Done():
				return
			}
		}
		if len(symbols) == 0 {
			log.Println("watchlist is empty - add symbols via POST /api/watchlist to start streaming candles")
			select {
			case <-restartCh:
				continue
			case <-ctx.Done():
				return
			}
		}

		streamSet := withAnchors(symbols)
		seedHistory(ctx, market, streamSet, interval)

		runCtx, cancelRun := context.WithCancel(ctx)
		restarted := make(chan struct{})
		go func() {
			select {
			case <-restartCh:
				close(restarted)
				cancelRun()
			case <-runCtx.Done():
			}
		}()

		// REST polling fallback, running alongside the WS stream for the
		// lifetime of this symbol set - see marketdata.PollCandles/
		// PollFunding for why this exists (some networks silently drop WS
		// data frames post-handshake; REST keeps working regardless).
		go market.PollCandles(runCtx, streamSet, interval, 8*time.Second)
		go market.PollFunding(runCtx, streamSet, 10*time.Second)

		err = market.Run(runCtx, streamSet, interval)
		cancelRun()

		select {
		case <-restarted:
			continue // watchlist changed - loop immediately picks up the new symbol set
		default:
		}
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("marketdata run stopped: %v; retrying in 3s", err)
			select {
			case <-time.After(3 * time.Second):
			case <-ctx.Done():
				return
			}
		}
	}
}

// runUserDataStream owns the full listenKey lifecycle: create, keepalive on
// a ticker, reconnect with backoff on any drop, and mint a fresh listenKey
// (rather than reconnecting to a dead one) whenever the stream reports
// listenKeyExpired. Requires BingX API credentials - logs and returns
// early if they're not configured, leaving position/balance data at
// whatever the initial REST hydration produced.
func runUserDataStream(ctx context.Context, bclient *bingx.Client, trader *autotrader.Trader, keepaliveInterval time.Duration) {
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		listenKey, err := bclient.CreateListenKey(ctx)
		if err != nil {
			log.Printf("binance: create listenKey failed (account/order updates unavailable until this succeeds): %v; retrying in %s", err, backoff)
			select {
			case <-time.After(backoff):
			case <-ctx.Done():
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second

		keepaliveCtx, stopKeepalive := context.WithCancel(ctx)
		go bclient.KeepAliveLoop(keepaliveCtx, keepaliveInterval)

		err = bclient.RunUserStream(ctx, listenKey, trader.HandleAccountUpdate, func(evt bingx.OrderUpdateEvent) {
			trader.HandleOrderUpdate(ctx, evt)
		})
		stopKeepalive()

		if ctx.Err() != nil {
			return
		}
		if bingx.IsListenKeyExpired(err) {
			log.Println("binance: listenKey expired, minting a fresh one")
			continue
		}
		log.Printf("binance: user data stream disconnected (%v), reconnecting in %s", err, backoff)
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
}
