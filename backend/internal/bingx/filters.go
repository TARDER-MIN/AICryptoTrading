package bingx

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"sync"
)

// SymbolFilters holds the per-symbol trading rules needed to size and
// validate an order. Binance MARKET orders are constrained by the
// MARKET_LOT_SIZE filter specifically (not the plain LOT_SIZE filter, which
// governs LIMIT orders) - this project only ever places MARKET orders, so
// MARKET_LOT_SIZE is preferred when present, falling back to LOT_SIZE.
type SymbolFilters struct {
	Symbol            string
	Status            string
	StepSize          float64
	MinQty            float64
	MaxQty            float64
	TickSize          float64
	MinNotionalUSD    float64
	QuantityPrecision int
	PricePrecision    int
}

// FilterCache holds the latest exchangeInfo filters for every symbol,
// refreshed periodically. This is the single source of truth every order
// path (auto and manual) must consult before sizing a trade.
type FilterCache struct {
	client *Client

	mu   sync.RWMutex
	byID map[string]SymbolFilters
}

func NewFilterCache(client *Client) *FilterCache {
	return &FilterCache{client: client, byID: make(map[string]SymbolFilters)}
}

// Refresh re-fetches exchangeInfo and replaces the cached filter table.
func (fc *FilterCache) Refresh(ctx context.Context) error {
	symbols, err := fc.client.ExchangeInfo(ctx)
	if err != nil {
		return fmt.Errorf("filters: refresh: %w", err)
	}

	next := make(map[string]SymbolFilters, len(symbols))
	for _, s := range symbols {
		f := SymbolFilters{Symbol: s.Symbol, Status: s.Status, QuantityPrecision: s.QuantityPrecision, PricePrecision: s.PricePrecision}
		f.StepSize = math.Pow10(-s.QuantityPrecision)
		f.TickSize = math.Pow10(-s.PricePrecision)
		f.MinQty = s.TradeMinQuantity
		f.MinNotionalUSD = s.TradeMinUSDT

		var lotStep, lotMin, lotMax float64
		haveMarketLot := false
		for _, raw := range s.Filters {
			ftype, _ := raw["filterType"].(string)
			switch ftype {
			case "MARKET_LOT_SIZE":
				f.StepSize = parseFilterFloat(raw["stepSize"])
				f.MinQty = parseFilterFloat(raw["minQty"])
				f.MaxQty = parseFilterFloat(raw["maxQty"])
				haveMarketLot = true
			case "LOT_SIZE":
				lotStep = parseFilterFloat(raw["stepSize"])
				lotMin = parseFilterFloat(raw["minQty"])
				lotMax = parseFilterFloat(raw["maxQty"])
			case "PRICE_FILTER":
				f.TickSize = parseFilterFloat(raw["tickSize"])
			case "MIN_NOTIONAL":
				f.MinNotionalUSD = parseFilterFloat(raw["notional"])
			case "NOTIONAL":
				// Some symbols expose the min-notional filter as "NOTIONAL"
				// with a "minNotional" field instead of "MIN_NOTIONAL"/"notional".
				if v, ok := raw["minNotional"]; ok {
					f.MinNotionalUSD = parseFilterFloat(v)
				}
			}
		}
		if !haveMarketLot && lotStep > 0 {
			f.StepSize, f.MinQty, f.MaxQty = lotStep, lotMin, lotMax
		}
		next[s.Symbol] = f
	}

	fc.mu.Lock()
	fc.byID = next
	fc.mu.Unlock()
	return nil
}

func parseFilterFloat(v any) float64 {
	s, ok := v.(string)
	if !ok {
		return 0
	}
	f, _ := strconv.ParseFloat(s, 64)
	return f
}

// Get returns the cached filters for symbol, if known.
func (fc *FilterCache) Get(symbol string) (SymbolFilters, bool) {
	fc.mu.RLock()
	defer fc.mu.RUnlock()
	f, ok := fc.byID[symbol]
	return f, ok
}

// SizingResult is the outcome of validating a target notional against a
// symbol's real exchange constraints.
type SizingResult struct {
	Feasible       bool
	Qty            float64 // rounded down to StepSize; 0 if infeasible
	NotionalUSD    float64 // Qty * price
	MinTradableUSD float64 // the smallest possible order's notional, for the "why infeasible" message
	Reason         string  // set when !Feasible
}

// MaxQtyForCap computes the largest quantity of symbol tradable at `price`
// without exceeding capUSD notional, rounded down to the exchange's
// stepSize, and validated against MinQty/MinNotional. This is the ONE
// function every order-placement path (autotrader and manual) must call -
// never trust a caller-supplied quantity. Returns Feasible=false (never a
// bigger-than-requested order) when even the smallest tradable unit would
// exceed capUSD - the real-world case for BTCUSDT/ETHUSDT at a $10 cap.
func (fc *FilterCache) MaxQtyForCap(symbol string, price, capUSD float64) SizingResult {
	f, ok := fc.Get(symbol)
	if !ok {
		return SizingResult{Reason: fmt.Sprintf("no cached exchange filters for %s (exchangeInfo not yet loaded or unknown symbol)", symbol)}
	}
	if price <= 0 {
		return SizingResult{Reason: fmt.Sprintf("invalid price %.8f for %s", price, symbol)}
	}
	if f.StepSize <= 0 {
		return SizingResult{Reason: fmt.Sprintf("no LOT_SIZE/MARKET_LOT_SIZE stepSize found for %s", symbol)}
	}

	minTradableUSD := f.MinQty * price
	if f.MinNotionalUSD > minTradableUSD {
		minTradableUSD = f.MinNotionalUSD
	}

	if minTradableUSD > capUSD {
		return SizingResult{
			MinTradableUSD: minTradableUSD,
			Reason:         fmt.Sprintf("min tradable size ($%.2f) exceeds $%.2f cap", minTradableUSD, capUSD),
		}
	}

	rawQty := capUSD / price
	steppedQty := math.Floor(rawQty/f.StepSize) * f.StepSize
	steppedQty = roundToPrecision(steppedQty, f.QuantityPrecision)

	if steppedQty < f.MinQty || steppedQty <= 0 {
		return SizingResult{
			MinTradableUSD: minTradableUSD,
			Reason:         fmt.Sprintf("rounded quantity %.8f is below minQty %.8f for %s", steppedQty, f.MinQty, symbol),
		}
	}
	notional := steppedQty * price
	if notional < f.MinNotionalUSD {
		return SizingResult{
			MinTradableUSD: minTradableUSD,
			Reason:         fmt.Sprintf("rounded notional $%.2f is below exchange minNotional $%.2f for %s", notional, f.MinNotionalUSD, symbol),
		}
	}
	if f.MaxQty > 0 && steppedQty > f.MaxQty {
		steppedQty = f.MaxQty
		notional = steppedQty * price
	}

	return SizingResult{Feasible: true, Qty: steppedQty, NotionalUSD: notional, MinTradableUSD: minTradableUSD}
}

// RoundPrice rounds price to the symbol's PRICE_FILTER tickSize (the exact
// bug this guards against: Binance rejects any order/algo-order price that
// isn't a multiple of tickSize with code=-1111 "Precision is over the
// maximum defined for this asset" - hit in production because computed
// stop-loss/take-profit prices, e.g. from strategy.SBSignal's OTE/Fibonacci
// arithmetic, are NOT naturally tick-aligned the way a real traded price
// from a candle close is). Falls back to PricePrecision decimal rounding
// when tickSize is unknown, and returns price unchanged if neither is
// available for symbol (fails open rather than silently mangling an
// unrecognized symbol's price).
func (fc *FilterCache) RoundPrice(symbol string, price float64) float64 {
	f, ok := fc.Get(symbol)
	if !ok {
		return price
	}
	if f.TickSize > 0 {
		// Round-to-nearest-tick, then a nearest-decimal cleanup pass (NOT
		// roundToPrecision's floor) - floor here would risk knocking an
		// already-correct value down a full tick on the common case where
		// float64 division leaves it a hair below the true multiple (e.g.
		// 2.4359999999999999 instead of 2.436).
		ticks := math.Round(price / f.TickSize)
		price = ticks * f.TickSize
		if f.PricePrecision > 0 {
			price = roundHalfUp(price, f.PricePrecision)
		}
		return price
	}
	if f.PricePrecision > 0 {
		return roundHalfUp(price, f.PricePrecision)
	}
	return price
}

func roundHalfUp(v float64, precision int) float64 {
	if precision < 0 {
		return v
	}
	scale := math.Pow(10, float64(precision))
	return math.Round(v*scale) / scale
}

func roundToPrecision(v float64, precision int) float64 {
	if precision < 0 {
		return v
	}
	scale := math.Pow(10, float64(precision))
	return math.Floor(v*scale) / scale
}
