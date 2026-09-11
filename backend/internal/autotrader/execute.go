package autotrader

import (
	"context"
	"fmt"
	"log"
	"strconv"
	"time"

	"cryptotrading/internal/bingx"
	"cryptotrading/internal/models"
	"cryptotrading/internal/settings"
	"cryptotrading/internal/ws"
)

// execute is the ONLY path that turns an AI signal into a real automatic
// order. Every guard here is deliberate and ordered cheapest-first: kill
// switch, cooldown, daily cap, existing-position direction, then the
// exchange-filter sizing check (bingx.FilterCache.MaxQtyForCap) that can
// never be bypassed regardless of what the AI/strategy suggested.
func (t *Trader) execute(ctx context.Context, signal *models.AISignal) (decision models.AutoTradeDecision, reason string, orderID *int64) {
	st, err := settings.Get(ctx, t.Pool)
	if err != nil {
		return models.DecisionError, fmt.Sprintf("load settings: %v", err), nil
	}
	if !st.AutotradeEnabled {
		return models.DecisionSkipped, "kill_switch_off", nil
	}

	symbol := signal.Symbol
	if !t.checkCooldown(symbol) {
		return models.DecisionSkipped, fmt.Sprintf("cooldown_active (min %s between auto orders per symbol)", t.MinOrderInterval), nil
	}

	side := models.OrderSideBuy
	if signal.Action == models.SignalSell {
		side = models.OrderSideSell
	}

	pos, hasPosition := t.Positions.Get(symbol)
	if hasPosition {
		wantLong := side == models.OrderSideBuy
		haveLong := pos.Side == "long"
		if wantLong == haveLong {
			return models.DecisionSkipped, "already_holding_direction", nil
		}
		// Opposite-direction signal: the capped order below will partially
		// reduce the existing position (BingX nets against it), not
		// aggressively flip it - the margin-based sizing keeps this bounded
		// even for a larger existing position. Its stop-loss/take-profit
		// (placed when it was first opened, see isFreshEntry below) uses
		// closePosition=true, so it stays correctly sized to protect
		// whatever remains after this reduce - no need to replace it.
	}
	isFreshEntry := !hasPosition

	if !t.checkAndReserveDailyCap() {
		return models.DecisionSkipped, fmt.Sprintf("daily_order_cap_reached (%d/day)", t.MaxAutoOrdersPerDay), nil
	}

	price, ok := t.latestPrice(ctx, symbol)
	if !ok {
		return models.DecisionError, "no price available for sizing", nil
	}

	sizing := t.Filters.MaxQtyForCap(symbol, price, st.EffectiveNotionalUSD)
	if !sizing.Feasible {
		return models.DecisionSkipped, sizing.Reason, nil
	}

	if err := t.ensureLeverage(ctx, symbol, st.Leverage, st.MarginType); err != nil {
		return models.DecisionError, fmt.Sprintf("set leverage/margin type: %v", err), nil
	}

	clientOrderID := "auto" + strconv.FormatInt(time.Now().UnixNano(), 36)
	resp, err := t.BingX.PlaceMarketOrder(ctx, symbol, side, sizing.Qty, false, clientOrderID)
	if err != nil {
		return models.DecisionError, fmt.Sprintf("place order: %v", err), nil
	}

	id, err := t.recordOrder(ctx, resp, symbol, side, sizing, st.Leverage, false, models.OrderSourceAuto)
	if err != nil {
		log.Printf("autotrader: order %d placed on BingX but failed to record locally: %v", resp.OrderID, err)
	}
	t.markOrderPlaced(symbol)

	// Attach stop-loss/take-profit immediately on a fresh entry, using the
	// AI's own computed levels - real protection resting on the exchange
	// from the moment the position opens, not something that depends on
	// this process staying up to enforce later. Skipped on a reduce of an
	// existing opposite position (see isFreshEntry above) since that
	// position's own SL/TP, placed when IT was opened, remains valid.
	if isFreshEntry && signal.StopLoss != nil && signal.TakeProfit != nil {
		// Errors are already logged inside placeProtectiveOrders; the
		// automatic path never rolls back or fails the entry over this -
		// see its doc comment. AttachProtectiveOrders is the manual
		// recovery path for when this does fail.
		_, _ = t.placeProtectiveOrders(ctx, symbol, side, sizing.Qty, st.Leverage, *signal.StopLoss, *signal.TakeProfit)
	}

	return models.DecisionExecuted, "", &id
}

// placeProtectiveOrders attaches STOP_MARKET/TAKE_PROFIT_MARKET
// closePosition algo orders right after a fresh entry fills. A failure here
// is logged but does not roll back or fail the entry - the position is
// already open on BingX regardless; losing the protective order just
// means it's temporarily unprotected until this is noticed (visible via the
// missing rows in GET /api/account/orders, or BingX's own order history,
// or via AttachProtectiveOrders below). Returns the two placement errors
// (nil on success) so a caller that needs to know - like AttachProtectiveOrders
// - can react; the automatic entry-fill path above ignores them by design.
func (t *Trader) placeProtectiveOrders(ctx context.Context, symbol string, entrySide models.OrderSide, qty float64, leverage int, stopLoss, takeProfit float64) (stopErr, takeProfitErr error) {
	closeSide := models.OrderSideSell
	if entrySide == models.OrderSideSell {
		closeSide = models.OrderSideBuy
	}

	place := func(orderType, clientPrefix string, triggerPrice float64) error {
		// AI-computed stop/target prices (from strategy.SBSignal's OTE/
		// Fibonacci arithmetic) are not naturally aligned to the exchange's
		// PRICE_FILTER tickSize the way a real traded price is - sending one
		// unrounded gets the whole algo order rejected with code=-1111
		// "Precision is over the maximum defined for this asset", silently
		// leaving the position unprotected (see the log line below).
		triggerPrice = t.Filters.RoundPrice(symbol, triggerPrice)
		clientAlgoID := clientPrefix + strconv.FormatInt(time.Now().UnixNano(), 36)
		resp, err := t.BingX.PlaceClosePositionAlgoOrder(ctx, symbol, closeSide, qty, orderType, triggerPrice, clientAlgoID)
		if err != nil {
			log.Printf("autotrader: place %s for %s at %.8f failed (position is OPEN without this protection): %v", orderType, symbol, triggerPrice, err)
			return err
		}
		if err := t.recordAlgoOrder(ctx, resp, symbol, closeSide, orderType, leverage); err != nil {
			log.Printf("autotrader: record %s algo order %d for %s failed: %v", orderType, resp.AlgoID, symbol, err)
			return err
		}
		return nil
	}

	stopErr = place("STOP_MARKET", "sl", stopLoss)
	takeProfitErr = place("TAKE_PROFIT_MARKET", "tp", takeProfit)
	return stopErr, takeProfitErr
}

// AttachProtectiveOrders is the manual recovery path for a position that
// ended up open without stop-loss/take-profit protection (e.g. the
// entry-time attempt in execute() failed - see placeProtectiveOrders).
// Reuses the exact same placement logic, keyed off the position's CURRENT
// live side/leverage rather than trusting a caller-supplied direction.
// Errors if there's no open position for symbol right now.
func (t *Trader) AttachProtectiveOrders(ctx context.Context, symbol string, stopLoss, takeProfit float64) error {
	pos, ok := t.Positions.Get(symbol)
	if !ok || pos.Qty == 0 {
		return fmt.Errorf("no open position for %s", symbol)
	}
	entrySide := models.OrderSideBuy
	if pos.Side == "short" {
		entrySide = models.OrderSideSell
	}
	stopErr, takeProfitErr := t.placeProtectiveOrders(ctx, symbol, entrySide, pos.Qty, pos.Leverage, stopLoss, takeProfit)
	if stopErr != nil || takeProfitErr != nil {
		return fmt.Errorf("stop_loss error: %v, take_profit error: %v", stopErr, takeProfitErr)
	}
	return nil
}

// PlaceManualOrder routes a user-initiated order through the exact same
// sizing/validation path as the automatic engine. It bypasses the kill
// switch (a manual action is an explicit override by definition) but NOT
// the margin-based sizing - manual orders are sized exactly like auto orders.
func (t *Trader) PlaceManualOrder(ctx context.Context, symbol string, side models.OrderSide) (*models.Order, error) {
	st, err := settings.Get(ctx, t.Pool)
	if err != nil {
		return nil, err
	}

	price, ok := t.latestPrice(ctx, symbol)
	if !ok {
		return nil, fmt.Errorf("no price available for %s", symbol)
	}
	sizing := t.Filters.MaxQtyForCap(symbol, price, st.EffectiveNotionalUSD)
	if !sizing.Feasible {
		return nil, fmt.Errorf("order rejected: %s", sizing.Reason)
	}

	if err := t.ensureLeverage(ctx, symbol, st.Leverage, st.MarginType); err != nil {
		return nil, fmt.Errorf("set leverage/margin type: %w", err)
	}

	clientOrderID := "manual" + strconv.FormatInt(time.Now().UnixNano(), 36)
	resp, err := t.BingX.PlaceMarketOrder(ctx, symbol, side, sizing.Qty, false, clientOrderID)
	if err != nil {
		return nil, fmt.Errorf("place order: %w", err)
	}

	id, err := t.recordOrder(ctx, resp, symbol, side, sizing, st.Leverage, false, models.OrderSourceManual)
	if err != nil {
		return nil, err
	}
	return t.loadOrder(ctx, id)
}

// Flatten closes an open position at market with a reduceOnly order. Always
// allowed regardless of the kill switch, and exempt from the notional cap -
// a pure risk-reduction action should never be blocked by a sizing rule.
func (t *Trader) Flatten(ctx context.Context, symbol string) (*models.Order, error) {
	pos, ok := t.Positions.Get(symbol)
	if !ok {
		return nil, fmt.Errorf("no open position on %s", symbol)
	}

	side := models.OrderSideSell
	if pos.Side == "short" {
		side = models.OrderSideBuy
	}

	clientOrderID := "flatten" + strconv.FormatInt(time.Now().UnixNano(), 36)
	resp, err := t.BingX.PlaceMarketOrder(ctx, symbol, side, pos.Qty, true, clientOrderID)
	if err != nil {
		return nil, fmt.Errorf("place flatten order: %w", err)
	}

	sizing := bingx.SizingResult{Feasible: true, Qty: pos.Qty, NotionalUSD: pos.Qty * pos.MarkPrice}
	id, err := t.recordOrder(ctx, resp, symbol, side, sizing, pos.Leverage, true, models.OrderSourceManual)
	if err != nil {
		return nil, err
	}
	return t.loadOrder(ctx, id)
}

func (t *Trader) ensureLeverage(ctx context.Context, symbol string, leverage int, marginType string) error {
	t.mu.Lock()
	already := t.leverageSetFor[symbol]
	t.mu.Unlock()
	if already {
		return nil
	}

	if err := t.BingX.SetMarginType(ctx, symbol, marginType); err != nil {
		return err
	}
	if err := t.BingX.SetLeverage(ctx, symbol, leverage); err != nil {
		return err
	}

	t.mu.Lock()
	t.leverageSetFor[symbol] = true
	t.mu.Unlock()
	return nil
}

// latestPrice prefers the live-streamed mark price (updates ~every second);
// falls back to the most recent candle close if the mark-price stream
// hasn't produced anything yet (e.g. briefly after startup).
func (t *Trader) latestPrice(ctx context.Context, symbol string) (float64, bool) {
	if mark, _, _, ok := t.Market.Funding(symbol); ok && mark > 0 {
		return mark, true
	}
	candles, err := t.Market.GetCandles(ctx, symbol, 1)
	if err != nil || len(candles) == 0 {
		return 0, false
	}
	return candles[len(candles)-1].Close, true
}

func (t *Trader) recordOrder(ctx context.Context, resp *bingx.OrderResponse, symbol string, side models.OrderSide, sizing bingx.SizingResult, leverage int, reduceOnly bool, source models.OrderSource) (int64, error) {
	var id int64
	err := t.Pool.QueryRow(ctx, `
		INSERT INTO orders (binance_order_id, client_order_id, symbol, side, order_type, reduce_only, qty, notional_usd, leverage, status, source)
		VALUES ($1,$2,$3,$4,'MARKET',$5,$6,$7,$8,$9,$10)
		RETURNING id
	`, resp.OrderID, resp.ClientOrderID, symbol, side, reduceOnly, sizing.Qty, sizing.NotionalUSD, leverage, resp.Status, source).Scan(&id)
	if err != nil {
		return 0, err
	}
	order, loadErr := t.loadOrder(ctx, id)
	if loadErr == nil {
		t.Hub.Broadcast(ws.Message{Type: "order", Data: order})
	}
	return id, nil
}

// recordAlgoOrder mirrors a placed STOP_MARKET/TAKE_PROFIT_MARKET algo
// order locally (algo_id set, binance_order_id left NULL - see migration
// 0004). qty/notional are 0: closePosition orders carry no fixed quantity,
// it's whatever the position size is at trigger time.
func (t *Trader) recordAlgoOrder(ctx context.Context, resp *bingx.AlgoOrderResponse, symbol string, side models.OrderSide, orderType string, leverage int) error {
	var id int64
	err := t.Pool.QueryRow(ctx, `
		INSERT INTO orders (algo_id, client_order_id, symbol, side, order_type, reduce_only, qty, notional_usd, leverage, status, source)
		VALUES ($1,$2,$3,$4,$5,true,0,0,$6,$7,'auto')
		RETURNING id
	`, resp.AlgoID, resp.ClientAlgoID, symbol, side, orderType, leverage, resp.AlgoStatus).Scan(&id)
	if err != nil {
		return err
	}
	order, loadErr := t.loadOrder(ctx, id)
	if loadErr == nil {
		t.Hub.Broadcast(ws.Message{Type: "order", Data: order})
	}
	return nil
}

func (t *Trader) loadOrder(ctx context.Context, id int64) (*models.Order, error) {
	var o models.Order
	err := t.Pool.QueryRow(ctx, `
		SELECT id, binance_order_id, algo_id, client_order_id, symbol, side, order_type, reduce_only, qty, notional_usd, leverage, status, source, filled_price, filled_at, submitted_at, updated_at
		FROM orders WHERE id = $1
	`, id).Scan(&o.ID, &o.BingXOrderID, &o.AlgoID, &o.ClientOrderID, &o.Symbol, &o.Side, &o.OrderType, &o.ReduceOnly, &o.Qty, &o.NotionalUSD, &o.Leverage, &o.Status, &o.Source, &o.FilledPrice, &o.FilledAt, &o.SubmittedAt, &o.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &o, nil
}
