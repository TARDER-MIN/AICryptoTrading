package autotrader

import (
	"context"
	"log"

	"cryptotrading/internal/bingx"
	"cryptotrading/internal/models"
	"cryptotrading/internal/ws"
)

// HandleAccountUpdate applies a BingX user-data-stream ACCOUNT_UPDATE
// event to the in-memory position/balance cache and rebroadcasts the
// refreshed state to the dashboard.
func (t *Trader) HandleAccountUpdate(evt bingx.AccountUpdateEvent) {
	for _, p := range evt.Positions {
		t.Positions.UpsertFromAccountUpdate(p.Symbol, p.PositionAmt, p.EntryPrice, p.UnrealizedProfit, p.MarginType)
	}
	for _, b := range evt.Balances {
		if b.Asset == "USDT" {
			t.Positions.SetWalletBalance(b.WalletBalance, b.AvailableBalance)
		}
	}
	t.Positions.RecalculateUnrealized()

	t.Hub.Broadcast(ws.Message{Type: "position", Data: t.Positions.Positions()})
	t.Hub.Broadcast(ws.Message{Type: "balance", Data: t.Positions.Account()})
}

// HandleOrderUpdate applies a BingX user-data-stream ORDER_TRADE_UPDATE
// event to the local orders mirror table (status/fill price) and
// rebroadcasts it. Orders not placed by this backend (binance_order_id not
// found locally) are ignored - they're outside this dashboard's scope.
func (t *Trader) HandleOrderUpdate(ctx context.Context, evt bingx.OrderUpdateEvent) {
	var filledPrice *float64
	if evt.AvgPrice > 0 {
		fp := evt.AvgPrice
		filledPrice = &fp
	}
	isFilled := evt.Status == string(models.OrderStatusFilled)

	var id int64
	var err error
	if isFilled {
		err = t.Pool.QueryRow(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, filled_at = now(), updated_at = now()
			WHERE binance_order_id = $3
			RETURNING id
		`, evt.Status, filledPrice, evt.OrderID).Scan(&id)
	} else {
		err = t.Pool.QueryRow(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, updated_at = now()
			WHERE binance_order_id = $3
			RETURNING id
		`, evt.Status, filledPrice, evt.OrderID).Scan(&id)
	}
	if err != nil {
		return // not an order this backend placed, or not found yet - not an error worth logging
	}

	order, loadErr := t.loadOrder(ctx, id)
	if loadErr != nil {
		log.Printf("autotrader: order update: reload order %d failed: %v", id, loadErr)
		return
	}
	t.Hub.Broadcast(ws.Message{Type: "order", Data: order})
}

// SyncAccountState re-hydrates positions/balance from REST (GET
// /fapi/v2/positionRisk + /fapi/v2/account). This is a fallback/supplement
// to HandleAccountUpdate's WS path, run periodically (see cmd/server) so
// position/balance correctness doesn't depend entirely on the user-data
// WebSocket staying connected - some network environments (firewall/VPN/
// antivirus) complete the WS handshake but never deliver data frames
// afterward, which would otherwise leave the position table silently
// wrong (e.g. showing a symbol as flat when it's actually held) with no
// visible error, which matters a lot for a live-money auto-trading bot.
func (t *Trader) SyncAccountState(ctx context.Context) error {
	pos, err := t.BingX.PositionRisk(ctx)
	if err != nil {
		return err
	}
	t.Positions.SetPositions(pos)

	acct, err := t.BingX.Account(ctx)
	if err != nil {
		return err
	}
	t.Positions.SetAccount(*acct)
	t.Positions.RecalculateUnrealized()

	t.Hub.Broadcast(ws.Message{Type: "position", Data: t.Positions.Positions()})
	t.Hub.Broadcast(ws.Message{Type: "balance", Data: t.Positions.Account()})
	return nil
}

// ReconcilePendingOrders re-checks every locally-tracked order still in a
// non-terminal status against BingX via REST - the fallback counterpart
// to HandleOrderUpdate, for the same reason SyncAccountState exists. Covers
// both regular orders (GET /fapi/v1/order) and algo stop-loss/take-profit
// orders (GET /fapi/v1/algoOrder, a separate ID space and status set - see
// migration 0004). MARKET entry orders normally fill within milliseconds,
// so most reconciliation work here is actually tracking algo orders sitting
// in NEW/TRIGGERED for a while until price reaches them.
func (t *Trader) ReconcilePendingOrders(ctx context.Context) error {
	rows, err := t.Pool.Query(ctx, `
		SELECT id, binance_order_id, algo_id, symbol FROM orders
		WHERE status NOT IN ('FILLED','CANCELED','EXPIRED','REJECTED')
		  AND (binance_order_id IS NOT NULL OR algo_id IS NOT NULL)
	`)
	if err != nil {
		return err
	}
	type pendingOrder struct {
		id      int64
		orderID *int64
		algoID  *int64
		symbol  string
	}
	var pending []pendingOrder
	for rows.Next() {
		var p pendingOrder
		if err := rows.Scan(&p.id, &p.orderID, &p.algoID, &p.symbol); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	rows.Close()

	for _, p := range pending {
		if p.algoID != nil {
			t.reconcileAlgoOrder(ctx, p.id, *p.algoID, p.symbol)
			continue
		}
		if p.orderID != nil {
			t.reconcileRegularOrder(ctx, p.id, *p.orderID, p.symbol)
		}
	}
	return nil
}

func (t *Trader) reconcileRegularOrder(ctx context.Context, localID, orderID int64, symbol string) {
	resp, err := t.BingX.QueryOrder(ctx, symbol, orderID)
	if err != nil {
		log.Printf("autotrader: reconcile order %d (%s): %v", orderID, symbol, err)
		return
	}

	var filledPrice *float64
	if resp.AvgPrice.Float() > 0 {
		fp := resp.AvgPrice.Float()
		filledPrice = &fp
	}
	var updateErr error
	if resp.Status == string(models.OrderStatusFilled) {
		_, updateErr = t.Pool.Exec(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, filled_at = now(), updated_at = now() WHERE id = $3
		`, resp.Status, filledPrice, localID)
	} else {
		_, updateErr = t.Pool.Exec(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, updated_at = now() WHERE id = $3
		`, resp.Status, filledPrice, localID)
	}
	if updateErr != nil {
		log.Printf("autotrader: reconcile order %d: update failed: %v", orderID, updateErr)
		return
	}
	if order, loadErr := t.loadOrder(ctx, localID); loadErr == nil {
		t.Hub.Broadcast(ws.Message{Type: "order", Data: order})
	}
}

func (t *Trader) reconcileAlgoOrder(ctx context.Context, localID, algoID int64, symbol string) {
	resp, err := t.BingX.QueryAlgoOrder(ctx, symbol, algoID)
	if err != nil {
		log.Printf("autotrader: reconcile algo order %d: %v", algoID, err)
		return
	}

	var filledPrice *float64
	if resp.ActualPrice.Float() > 0 {
		fp := resp.ActualPrice.Float()
		filledPrice = &fp
	}
	var updateErr error
	if resp.AlgoStatus == "FILLED" {
		_, updateErr = t.Pool.Exec(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, filled_at = now(), updated_at = now() WHERE id = $3
		`, resp.AlgoStatus, filledPrice, localID)
	} else {
		_, updateErr = t.Pool.Exec(ctx, `
			UPDATE orders SET status = $1, filled_price = $2, updated_at = now() WHERE id = $3
		`, resp.AlgoStatus, filledPrice, localID)
	}
	if updateErr != nil {
		log.Printf("autotrader: reconcile algo order %d: update failed: %v", algoID, updateErr)
		return
	}
	if order, loadErr := t.loadOrder(ctx, localID); loadErr == nil {
		t.Hub.Broadcast(ws.Message{Type: "order", Data: order})
	}
}
