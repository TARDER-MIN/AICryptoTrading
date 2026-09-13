package main

import (
	"context"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/autotrader"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/config"
	"cryptotrading/internal/db"
	"cryptotrading/internal/httpapi"
	"cryptotrading/internal/marketdata"
	"cryptotrading/internal/models"
	"cryptotrading/internal/positionstore"
	"cryptotrading/internal/researchdata"
	"cryptotrading/internal/strategy"
	"cryptotrading/internal/ws"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	if cfg.BingXAPIKey == "" || cfg.BingXAPISecret == "" {
		log.Println("WARNING: BINGX_API_KEY/BINGX_API_SECRET not set - account/order endpoints will fail; market data and charting still work")
	}

	pool, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("db: %v", err)
	}
	defer pool.Close()

	bclient := bingx.NewClient(cfg.BingXAPIKey, cfg.BingXAPISecret, cfg.BingXRESTBaseURL, cfg.BingXWSBaseURL)
	researchClient := researchdata.NewClient(cfg.BinanceResearchBaseURL)
	if err := bclient.SyncClock(ctx); err != nil {
		log.Printf("binance: initial clock sync failed (signed requests may be rejected until this succeeds): %v", err)
	}

	filters := bingx.NewFilterCache(bclient)
	if err := filters.Refresh(ctx); err != nil {
		// Not fatal: MaxQtyForCap fails safe (infeasible) for any symbol
		// with no cached filters, so no order can be placed until this
		// succeeds - the rest of the dashboard (charts, AI signals) still works.
		log.Printf("binance: initial exchangeInfo fetch failed (auto/manual orders will be rejected until this succeeds): %v", err)
	}

	hub := ws.NewHub()
	market := marketdata.NewService(pool, bclient, hub)

	positions := positionstore.New()
	hydrateAccountState(ctx, bclient, positions)

	aiClient := ai.New(cfg.AnthropicAPIKey, cfg.AnthropicModel, cfg.AISignalSystemPrompt, cfg.AIWatchlistSystemPrompt)
	if !aiClient.Enabled() {
		log.Println("ai: ANTHROPIC_API_KEY not set - AI endpoints report {configured:false}, and auto-trading is effectively a no-op (it never trades off the bare rule signal alone)")
	}

	sbParams, err := strategy.LoadParams(ctx, pool)
	if err != nil {
		log.Printf("strategy: load params failed, using defaults: %v", err)
		sbParams = strategy.DefaultSBParams()
	}
	trader := autotrader.New(pool, market, bclient, filters, positions, aiClient, hub, cfg.AnthropicModel, cfg.MaxAutoOrdersPerDay, sbParams)
	if d, err := time.ParseDuration(cfg.KlineInterval); err == nil {
		trader.MinOrderInterval = d
	}

	// Runs in its own goroutine: OnCandleClose may call the AI API and place
	// a real order (both real network calls) and must not block candle
	// ingestion for every other symbol while in flight.
	market.OnCandleClose = func(ctx context.Context, c models.Candle) {
		go trader.OnCandleClose(ctx, c)
	}

	restartCh := make(chan struct{}, 1)
	triggerRestart := func() {
		select {
		case restartCh <- struct{}{}:
		default:
		}
	}

	go streamMarketData(ctx, pool, market, cfg.KlineInterval, restartCh)
	// BingX account/order state is reconciled by the 15-second REST loop below.
	go periodicRefresh(ctx, bclient, filters)
	go periodicAccountSync(ctx, trader)
	go runWatchlistAIScheduler(ctx, pool, aiClient, bclient, filters, cfg.WatchlistAIRefreshHourUTC, cfg.KlineInterval, triggerRestart)

	router := httpapi.NewRouter(httpapi.Deps{
		Cfg: cfg, Pool: pool, Market: market, BingX: bclient, Filters: filters,
		Positions: positions, Trader: trader, AI: aiClient, Research: researchClient, Hub: hub,
		RestartMarketStream: triggerRestart,
	})

	srv := &http.Server{Addr: ":" + cfg.ServerPort, Handler: router}
	go func() {
		log.Printf("listening on :%s", cfg.ServerPort)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down...")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = srv.Shutdown(shutdownCtx)
}

// hydrateAccountState loads the real starting position/balance snapshot via
// REST before anything else runs, so the dashboard isn't empty (or wrong)
// until the first user-data-stream event arrives.
func hydrateAccountState(ctx context.Context, bclient *bingx.Client, positions *positionstore.Store) {
	if pos, err := bclient.PositionRisk(ctx); err != nil {
		log.Printf("binance: initial PositionRisk fetch failed: %v", err)
	} else {
		positions.SetPositions(pos)
	}
	if acct, err := bclient.Account(ctx); err != nil {
		log.Printf("binance: initial Account fetch failed: %v", err)
	} else {
		positions.SetAccount(*acct)
	}
	positions.RecalculateUnrealized()
}

// periodicRefresh keeps the clock offset and exchangeInfo filter cache
// current for the life of the process. Filters rarely change, so an hourly
// cadence is plenty; it also opportunistically re-syncs the clock at the
// same interval to guard against slow local drift.
func periodicRefresh(ctx context.Context, bclient *bingx.Client, filters *bingx.FilterCache) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := bclient.SyncClock(ctx); err != nil {
				log.Printf("binance: periodic clock sync failed: %v", err)
			}
			if err := filters.Refresh(ctx); err != nil {
				log.Printf("binance: periodic exchangeInfo refresh failed: %v", err)
			}
		}
	}
}

// periodicAccountSync re-hydrates positions/balance and reconciles pending
// order status from REST on a short interval - the fallback/supplement to
// the user-data-stream WS path (see autotrader.SyncAccountState/
// ReconcilePendingOrders for why this matters: some network environments
// silently never deliver WS data frames after a successful handshake).
// 15s keeps position/fill correctness reasonably current even if the WS
// path never works at all in a given environment.
func periodicAccountSync(ctx context.Context, trader *autotrader.Trader) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := trader.SyncAccountState(ctx); err != nil {
				log.Printf("autotrader: periodic account sync failed: %v", err)
			}
			if err := trader.ReconcilePendingOrders(ctx); err != nil {
				log.Printf("autotrader: periodic order reconcile failed: %v", err)
			}
		}
	}
}
