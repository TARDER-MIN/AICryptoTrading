// Package autotrader is the safety-critical core: it watches for strategy
// signal transitions, asks the AI to confirm, and - only if every guard
// passes - places a REAL BingX order. Every order path (automatic and
// manual) funnels through execute.go's sizing/validation, so nothing in
// this codebase places an order without going through the same margin-based
// sizing and exchange-filter checks.
//
// Sizing is margin-based: every order commits a fixed margin amount
// (internal/settings.Settings.MarginUSD, default 5 USDT) and the resulting
// notional is MarginUSD * Leverage - there is deliberately no separate
// absolute notional ceiling, so raising leverage does proportionally
// increase position size (an explicit user choice, not an oversight).
//
// Auto-trading is paused by default and every strategy-research run forces
// it back to paused. If the user explicitly arms it in the dashboard,
// several independent guards still apply: a runtime kill
// switch (internal/settings), a per-symbol cooldown and a daily order-count
// circuit breaker (both defensive - belt-and-suspenders against a logic bug
// causing repeat orders), and the exchange-filter sizing check enforced in
// bingx.FilterCache.MaxQtyForCap.
package autotrader

import (
	"sync"
	"time"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/marketdata"
	"cryptotrading/internal/positionstore"
	"cryptotrading/internal/signalengine"
	"cryptotrading/internal/strategy"
	"cryptotrading/internal/ws"

	"github.com/jackc/pgx/v5/pgxpool"
)

type Trader struct {
	Pool      *pgxpool.Pool
	Market    *marketdata.Service
	BingX     *bingx.Client
	Filters   *bingx.FilterCache
	Positions *positionstore.Store
	AI        *ai.Client
	Hub       *ws.Hub
	Model     string

	MaxAutoOrdersPerDay int
	// MinOrderInterval is the per-symbol cooldown between auto orders,
	// independent of setup-dedup - a defensive floor in case a logic bug
	// causes the dedup check to misfire repeatedly.
	MinOrderInterval time.Duration

	mu sync.Mutex
	// sbParams is the currently-active HTF 3+1 parameter set, loaded
	// at startup via strategy.LoadParams and reloadable after a
	// POST /api/strategy/optimize run (see SetSBParams).
	sbParams strategy.SBParams
	// lastSignaledFVG dedups HTF 3+1 setups per symbol: a setup is
	// only acted on if its FVGTs is newer than the last one already acted
	// on for that symbol (the HTF 3+1 equivalent of the old MA-cross
	// rule's "action transitioned" gate).
	lastSignaledFVG map[string]time.Time
	lastOrderAt     map[string]time.Time
	leverageSetFor  map[string]bool
	dayKey          string
	dayOrderCount   int
}

func New(pool *pgxpool.Pool, market *marketdata.Service, bclient *bingx.Client, filters *bingx.FilterCache, positions *positionstore.Store, aiClient *ai.Client, hub *ws.Hub, model string, maxAutoOrdersPerDay int, sbParams strategy.SBParams) *Trader {
	return &Trader{
		Pool: pool, Market: market, BingX: bclient, Filters: filters, Positions: positions,
		AI: aiClient, Hub: hub, Model: model,
		MaxAutoOrdersPerDay: maxAutoOrdersPerDay,
		MinOrderInterval:    15 * time.Minute,
		sbParams:            sbParams,
		lastSignaledFVG:     make(map[string]time.Time),
		lastOrderAt:         make(map[string]time.Time),
		leverageSetFor:      make(map[string]bool),
	}
}

// SetSBParams atomically replaces the active HTF 3+1 parameters -
// called after POST /api/strategy/optimize saves a new tuned set, so the
// live pipeline picks it up without a process restart.
func (t *Trader) SetSBParams(p strategy.SBParams) {
	t.mu.Lock()
	t.sbParams = p
	t.mu.Unlock()
}

// SBParams returns the currently-active HTF 3+1 parameters.
func (t *Trader) SBParams() strategy.SBParams {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.sbParams
}

func (t *Trader) signalDeps() signalengine.Deps {
	return signalengine.Deps{
		Pool: t.Pool, Market: t.Market, Positions: t.Positions, AI: t.AI, Hub: t.Hub, Model: t.Model,
	}
}

// checkAndReserveDailyCap atomically checks the UTC-day order counter
// against MaxAutoOrdersPerDay and, if under the limit, increments it and
// returns true. Resets automatically at UTC midnight.
func (t *Trader) checkAndReserveDailyCap() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	today := time.Now().UTC().Format("2006-01-02")
	if t.dayKey != today {
		t.dayKey = today
		t.dayOrderCount = 0
	}
	if t.dayOrderCount >= t.MaxAutoOrdersPerDay {
		return false
	}
	t.dayOrderCount++
	return true
}

func (t *Trader) checkCooldown(symbol string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	last, ok := t.lastOrderAt[symbol]
	if !ok {
		return true
	}
	return time.Since(last) >= t.MinOrderInterval
}

func (t *Trader) markOrderPlaced(symbol string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.lastOrderAt[symbol] = time.Now()
}
