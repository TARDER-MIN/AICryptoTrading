// Package positionstore holds the live, in-memory view of the real BingX
// account state (positions + balance) that the rest of the backend reads
// from. It is hydrated once at startup via REST (GET /fapi/v2/account,
// GET /fapi/v2/positionRisk) and kept current by the user-data-stream
// ACCOUNT_UPDATE events - there is no Postgres table for this; BingX
// itself is the source of truth, this is just a read-optimized cache so
// every handler/decision path doesn't have to hit the exchange.
package positionstore

import (
	"sync"
	"time"

	"cryptotrading/internal/models"
)

type Store struct {
	mu        sync.RWMutex
	positions map[string]models.Position
	account   models.AccountSummary
}

func New() *Store {
	return &Store{positions: make(map[string]models.Position)}
}

// SetPositions replaces the entire position table (used for REST hydration
// at startup, or periodic reconciliation).
func (s *Store) SetPositions(list []models.Position) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions = make(map[string]models.Position, len(list))
	for _, p := range list {
		s.positions[p.Symbol] = p
	}
}

// UpsertFromAccountUpdate applies one position's worth of an ACCOUNT_UPDATE
// event. amt == 0 means the position was closed and is removed. Fields not
// carried by ACCOUNT_UPDATE (markPrice, liquidationPrice, leverage) are
// preserved from the prior cached value when present.
func (s *Store) UpsertFromAccountUpdate(symbol string, amt, entryPrice, unrealizedPnL float64, marginType string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if amt == 0 {
		delete(s.positions, symbol)
		return
	}
	side := "long"
	if amt < 0 {
		side = "short"
		amt = -amt
	}
	prev := s.positions[symbol]
	s.positions[symbol] = models.Position{
		Symbol: symbol, Side: side, Qty: amt, EntryPrice: entryPrice,
		MarkPrice: prev.MarkPrice, UnrealizedPnL: unrealizedPnL,
		Leverage: prev.Leverage, LiquidationPrice: prev.LiquidationPrice,
		MarginType: marginType, UpdatedAt: time.Now(),
	}
}

// UpdateMarkPrice refreshes just the mark price / unrealized-PnL-relevant
// display fields for an already-open position from the markPrice stream,
// without waiting for the next ACCOUNT_UPDATE.
func (s *Store) UpdateMarkPrice(symbol string, markPrice float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.positions[symbol]
	if !ok {
		return
	}
	p.MarkPrice = markPrice
	if p.Side == "long" {
		p.UnrealizedPnL = (markPrice - p.EntryPrice) * p.Qty
	} else {
		p.UnrealizedPnL = (p.EntryPrice - markPrice) * p.Qty
	}
	s.positions[symbol] = p
}

func (s *Store) Positions() []models.Position {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]models.Position, 0, len(s.positions))
	for _, p := range s.positions {
		out = append(out, p)
	}
	return out
}

func (s *Store) Get(symbol string) (models.Position, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.positions[symbol]
	return p, ok
}

func (s *Store) SetAccount(a models.AccountSummary) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account = a
}

func (s *Store) Account() models.AccountSummary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.account
}

// SetWalletBalance updates just the wallet/available balance fields (from a
// user-data-stream ACCOUNT_UPDATE), leaving TotalUnrealizedPnL for a
// separate RecalculateUnrealized call since that's derived from positions.
func (s *Store) SetWalletBalance(wallet, available float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.account.WalletBalanceUSD = wallet
	s.account.AvailableBalanceUSD = available
	s.account.UpdatedAt = time.Now()
}

// RecalculateUnrealized recomputes TotalUnrealizedPnL as the sum of every
// open position's UnrealizedPnL. Call after any position or mark-price update.
func (s *Store) RecalculateUnrealized() {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total float64
	for _, p := range s.positions {
		total += p.UnrealizedPnL
	}
	s.account.TotalUnrealizedPnL = total
	s.account.UpdatedAt = time.Now()
}
