package main

import (
	"context"
	"log"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/settings"
	"cryptotrading/internal/strategy"
	"cryptotrading/internal/watchlistai"
)

// runWatchlistAIScheduler fires one AI daily-watchlist refresh per UTC
// calendar day at refreshHourUTC, skipping the wait if today doesn't have a
// run yet (covers a restart after the scheduled hour already passed).
// Unlike the sibling TWSEDailyTrading project's scheduler, there is no
// weekday/holiday skip logic - BingX perpetuals trade every day.
func runWatchlistAIScheduler(ctx context.Context, pool *pgxpool.Pool, aiClient *ai.Client, bclient *bingx.Client, filters *bingx.FilterCache, refreshHourUTC int, interval string, restart func()) {
	if !aiClient.Enabled() {
		log.Println("watchlistai: ANTHROPIC_API_KEY not set - daily watchlist scheduler disabled")
		return
	}

	runIfDue := func() {
		hasRun, err := watchlistai.HasRunForToday(ctx, pool)
		if err != nil {
			log.Printf("watchlistai: check today's run failed: %v", err)
			return
		}
		if hasRun {
			return
		}
		if time.Now().UTC().Hour() < refreshHourUTC {
			return
		}
		runWatchlistAIRefresh(ctx, pool, aiClient, bclient, filters, interval, restart)
	}

	runIfDue()

	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			runIfDue()
		}
	}
}

func runWatchlistAIRefresh(ctx context.Context, pool *pgxpool.Pool, aiClient *ai.Client, bclient *bingx.Client, filters *bingx.FilterCache, interval string, restart func()) {
	st, err := settings.Get(ctx, pool)
	if err != nil {
		log.Printf("watchlistai: load settings failed: %v", err)
		return
	}
	sbParams, err := strategy.LoadParams(ctx, pool)
	if err != nil {
		log.Printf("watchlistai: load strategy params failed: %v", err)
		return
	}
	result, err := watchlistai.Refresh(ctx, pool, aiClient, bclient, filters,
		st.EffectiveNotionalUSD, watchlistai.DefaultCandidatePool, watchlistai.DefaultPickCount,
		sbParams, interval)
	if err != nil {
		log.Printf("watchlistai: scheduled refresh failed: %v", err)
		return
	}
	log.Printf("watchlistai: scheduled refresh picked %d symbols for %s", len(result.Picks), result.RunDate)
	if restart != nil {
		restart()
	}
}
