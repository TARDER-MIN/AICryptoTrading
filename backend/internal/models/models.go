// Package models holds the domain structs shared across the backend.
package models

import "time"

// Candle is one OHLCV bar for a BingX USDS-M perpetual futures symbol.
// Volume is fractional base-asset volume (not an integer share count).
type Candle struct {
	Symbol string    `json:"symbol"`
	Ts     time.Time `json:"ts"`
	Open   float64   `json:"open"`
	High   float64   `json:"high"`
	Low    float64   `json:"low"`
	Close  float64   `json:"close"`
	Volume float64   `json:"volume"`
}

type SignalAction string

const (
	SignalBuy  SignalAction = "BUY"
	SignalSell SignalAction = "SELL"
	SignalHold SignalAction = "HOLD"
)

type AISignal struct {
	ID          int64        `json:"id"`
	Symbol      string       `json:"symbol"`
	CandleTs    *time.Time   `json:"candle_ts,omitempty"`
	Action      SignalAction `json:"action"`
	Confidence  float64      `json:"confidence"`
	EntryHint   *float64     `json:"entry_hint,omitempty"`
	StopLoss    *float64     `json:"stop_loss,omitempty"`
	TakeProfit  *float64     `json:"take_profit,omitempty"`
	FundingRate *float64     `json:"funding_rate,omitempty"`
	// SweepTs/SweepPrice/FVGTs/FVGLow/FVGHigh describe the HTF 3+1
	// setup (internal/strategy.SBSignal) this AI signal evaluated - carried
	// through so the dashboard chart can mark the sweep/FVG even for
	// signals loaded from history, not just ones just received live.
	SweepTs    *time.Time `json:"sweep_ts,omitempty"`
	SweepPrice *float64   `json:"sweep_price,omitempty"`
	FVGTs      *time.Time `json:"fvg_ts,omitempty"`
	FVGLow     *float64   `json:"fvg_low,omitempty"`
	FVGHigh    *float64   `json:"fvg_high,omitempty"`
	// Legacy database/API fields remain readable for older signals. New HTF
	// 3+1 signals store the M5 order-block body in BreakerLow/BreakerHigh;
	// OTE/SMT are retired and left null.
	OTELow          *float64  `json:"ote_low,omitempty"`
	OTEHigh         *float64  `json:"ote_high,omitempty"`
	BreakerLow      *float64  `json:"breaker_low,omitempty"`
	BreakerHigh     *float64  `json:"breaker_high,omitempty"`
	SMTAnchorSymbol *string   `json:"smt_anchor_symbol,omitempty"`
	SMTConfirmed    *bool     `json:"smt_confirmed,omitempty"`
	Rationale       string    `json:"rationale"`
	Model           string    `json:"model"`
	CreatedAt       time.Time `json:"created_at"`
}

type OrderSide string

const (
	OrderSideBuy  OrderSide = "BUY"
	OrderSideSell OrderSide = "SELL"
)

type OrderStatus string

const (
	OrderStatusNew             OrderStatus = "NEW"
	OrderStatusPartiallyFilled OrderStatus = "PARTIALLY_FILLED"
	OrderStatusFilled          OrderStatus = "FILLED"
	OrderStatusCanceled        OrderStatus = "CANCELED"
	OrderStatusExpired         OrderStatus = "EXPIRED"
	OrderStatusRejected        OrderStatus = "REJECTED"
)

type OrderSource string

const (
	OrderSourceManual OrderSource = "manual"
	OrderSourceAuto   OrderSource = "auto"
)

// Order mirrors a real BingX futures order, kept locally for history/UI.
// BingX is the source of truth; this row is a read-optimized cache.
// Exactly one of BingXOrderID (a regular MARKET order) or AlgoID (a
// STOP_MARKET/TAKE_PROFIT_MARKET conditional order placed via BingX's
// Algo Order service) is set, never both.
type Order struct {
	ID            int64       `json:"id"`
	BingXOrderID  *int64      `json:"binance_order_id,omitempty"`
	AlgoID        *int64      `json:"algo_id,omitempty"`
	ClientOrderID string      `json:"client_order_id,omitempty"`
	Symbol        string      `json:"symbol"`
	Side          OrderSide   `json:"side"`
	OrderType     string      `json:"order_type"`
	ReduceOnly    bool        `json:"reduce_only"`
	Qty           float64     `json:"qty"`
	NotionalUSD   float64     `json:"notional_usd"`
	Leverage      int         `json:"leverage"`
	Status        OrderStatus `json:"status"`
	Source        OrderSource `json:"source"`
	FilledPrice   *float64    `json:"filled_price,omitempty"`
	FilledAt      *time.Time  `json:"filled_at,omitempty"`
	SubmittedAt   time.Time   `json:"submitted_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
}

// Position is the real, live futures position as reported by BingX
// (hydrated from REST at startup, kept current by the user-data stream).
type Position struct {
	Symbol           string    `json:"symbol"`
	Side             string    `json:"side"` // long | short
	Qty              float64   `json:"qty"`  // always positive; Side carries direction
	EntryPrice       float64   `json:"entry_price"`
	MarkPrice        float64   `json:"mark_price"`
	UnrealizedPnL    float64   `json:"unrealized_pnl"`
	Leverage         int       `json:"leverage"`
	LiquidationPrice float64   `json:"liquidation_price"`
	MarginType       string    `json:"margin_type"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// AccountSummary is the account-level balance snapshot.
type AccountSummary struct {
	WalletBalanceUSD    float64   `json:"wallet_balance_usd"`
	AvailableBalanceUSD float64   `json:"available_balance_usd"`
	TotalUnrealizedPnL  float64   `json:"total_unrealized_pnl"`
	UpdatedAt           time.Time `json:"updated_at"`
}

type AutoTradeDecision string

const (
	DecisionExecuted AutoTradeDecision = "executed"
	DecisionSkipped  AutoTradeDecision = "skipped"
	DecisionHold     AutoTradeDecision = "hold"
	DecisionError    AutoTradeDecision = "error"
)

// AutoTradeLogEntry records every strategy evaluation for a symbol at every
// candle close, whether or not it resulted in an order - the transparency
// trail for what the automated engine did and why.
type AutoTradeLogEntry struct {
	ID         int64             `json:"id"`
	Symbol     string            `json:"symbol"`
	Ts         time.Time         `json:"ts"`
	RuleAction SignalAction      `json:"rule_action"`
	RuleReason string            `json:"rule_reason"`
	AISignalID *int64            `json:"ai_signal_id,omitempty"`
	Decision   AutoTradeDecision `json:"decision"`
	SkipReason string            `json:"skip_reason,omitempty"`
	OrderID    *int64            `json:"order_id,omitempty"`
}

// Settings is the single-row runtime-mutable trading configuration.
type Settings struct {
	AutotradeEnabled bool `json:"autotrade_enabled"`
	Leverage         int  `json:"leverage"`
	// MarginUSD is the fixed margin committed per order (not the position's
	// notional value). Notional = MarginUSD * Leverage, so it scales with
	// whatever leverage is currently set - there is deliberately no
	// separate absolute notional ceiling (the user's explicit choice: risk
	// is bounded by margin, not by a fixed dollar position size).
	MarginUSD float64 `json:"margin_usd"`
	// EffectiveNotionalUSD is a read-only convenience value (MarginUSD *
	// Leverage) computed on every read, not stored - purely so callers
	// don't have to multiply it themselves to show "what will this actually
	// order in dollars".
	EffectiveNotionalUSD float64   `json:"effective_notional_usd"`
	MarginType           string    `json:"margin_type"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type WatchlistItem struct {
	ID      int       `json:"id"`
	Symbol  string    `json:"symbol"`
	Enabled bool      `json:"enabled"`
	AddedAt time.Time `json:"added_at"`

	// Live feasibility info, filled in by the handler from the cached
	// exchangeInfo filters - never persisted, always recomputed.
	Price          *float64 `json:"price,omitempty"`
	MinNotionalUSD *float64 `json:"min_notional_usd,omitempty"`
	StepSize       *float64 `json:"step_size,omitempty"`
	MaxQtyAtCap    *float64 `json:"max_qty_at_cap,omitempty"`
	TradableAtCap  bool     `json:"tradable_at_cap"`
	FundingRate    *float64 `json:"funding_rate,omitempty"`
}
