// Package signalengine is the shared path for turning "should we look at
// this symbol right now" into a persisted, broadcast AI trading signal. It
// backs both the manual POST /api/ai/signal endpoint and the automatic
// rule-triggered watcher in internal/autotrader - both just call
// GenerateAndBroadcast, so a manually-requested signal and an
// auto-triggered one are built and stored identically. This package never
// places an order; internal/autotrader decides what to do with the result.
package signalengine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/marketdata"
	"cryptotrading/internal/models"
	"cryptotrading/internal/positionstore"
	"cryptotrading/internal/strategy"
	"cryptotrading/internal/ws"
)

type Deps struct {
	Pool      *pgxpool.Pool
	Market    *marketdata.Service
	Positions *positionstore.Store
	AI        *ai.Client
	Hub       *ws.Hub
	Model     string
}

// LoadPreviousSignal loads the most recent ai_signals row for symbol, so a
// new signal call can be given the previous one as context. Returns nil
// (not an error) when the symbol has no prior signal yet.
func LoadPreviousSignal(ctx context.Context, pool *pgxpool.Pool, symbol string) (*ai.PreviousSignal, error) {
	var p ai.PreviousSignal
	err := pool.QueryRow(ctx, `
		SELECT action, confidence, rationale, created_at
		FROM ai_signals WHERE symbol = $1 ORDER BY created_at DESC LIMIT 1
	`, symbol).Scan(&p.Action, &p.Confidence, &p.Rationale, &p.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// GenerateAndBroadcast asks Claude to confirm or reject an HTF 3+1
// setup (setup.Action must be BUY or SELL - the caller only invokes this on
// a genuine new setup, see internal/autotrader/watcher.go), persists the
// result to ai_signals (including the setup's sweep/FVG fields, so the
// dashboard chart can mark them even after a page reload), and broadcasts
// it over the WS hub as a "signal" message. Returns ai.ErrNotConfigured if
// no ANTHROPIC_API_KEY is set.
func GenerateAndBroadcast(ctx context.Context, d Deps, symbol string, setup strategy.SBSignal) (*models.AISignal, error) {
	if !d.AI.Enabled() {
		return nil, ai.ErrNotConfigured
	}

	candles, err := d.Market.GetCandles(ctx, symbol, 100)
	if err != nil {
		return nil, err
	}
	if len(candles) < 20 {
		return nil, fmt.Errorf("signalengine: not enough candle history for %s yet", symbol)
	}

	var fundingRate float64
	if _, fr, _, ok := d.Market.Funding(symbol); ok {
		fundingRate = fr
	}

	var position *models.Position
	if p, ok := d.Positions.Get(symbol); ok {
		position = &p
	}

	previous, err := LoadPreviousSignal(ctx, d.Pool, symbol)
	if err != nil {
		return nil, err
	}

	result, err := d.AI.GenerateSignal(ctx, ai.SignalRequest{
		Symbol: symbol, Candles: candles, Setup: setup, FundingRate: fundingRate, Position: position, Previous: previous,
	})
	if err != nil {
		return nil, err
	}

	candleTs := candles[len(candles)-1].Ts
	var fundingRatePtr *float64
	if fundingRate != 0 {
		fundingRatePtr = &fundingRate
	}
	sweepTs, fvgTs := setup.SweepTs, setup.FVGTs
	sweepPrice, fvgLow, fvgHigh := setup.SweepPrice, setup.FVGLow, setup.FVGHigh
	var orderBlockLowPtr, orderBlockHighPtr *float64
	if setup.BreakerHigh > setup.BreakerLow && setup.BreakerLow > 0 {
		orderBlockLow, orderBlockHigh := setup.BreakerLow, setup.BreakerHigh
		orderBlockLowPtr, orderBlockHighPtr = &orderBlockLow, &orderBlockHigh
	}

	var signalID int64
	var createdAt time.Time
	err = d.Pool.QueryRow(ctx, `
		INSERT INTO ai_signals (symbol, candle_ts, action, confidence, entry_hint, stop_loss, take_profit, funding_rate, rationale, model, raw_response, sweep_ts, sweep_price, fvg_ts, fvg_low, fvg_high, ote_low, ote_high, breaker_low, breaker_high, smt_anchor_symbol, smt_confirmed)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22) RETURNING id, created_at
	`, symbol, candleTs, result.Action, result.Confidence, result.EntryHint, result.StopLoss, result.TakeProfit, fundingRatePtr, result.Rationale, d.Model, json.RawMessage(result.RawJSON), sweepTs, sweepPrice, fvgTs, fvgLow, fvgHigh, nil, nil, orderBlockLowPtr, orderBlockHighPtr, nil, nil).Scan(&signalID, &createdAt)
	if err != nil {
		return nil, err
	}

	signal := models.AISignal{
		ID: signalID, Symbol: symbol, CandleTs: &candleTs,
		Action: result.Action, Confidence: result.Confidence,
		EntryHint: result.EntryHint, StopLoss: result.StopLoss, TakeProfit: result.TakeProfit,
		FundingRate: fundingRatePtr,
		Rationale:   result.Rationale, Model: d.Model, CreatedAt: createdAt,
		SweepTs: &sweepTs, SweepPrice: &sweepPrice, FVGTs: &fvgTs, FVGLow: &fvgLow, FVGHigh: &fvgHigh,
		BreakerLow: orderBlockLowPtr, BreakerHigh: orderBlockHighPtr,
	}
	d.Hub.Broadcast(ws.Message{Type: "signal", Data: signal})
	return &signal, nil
}
