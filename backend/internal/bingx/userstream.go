package bingx

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/gorilla/websocket"
)

// AccountUpdateEvent mirrors the fields this project reads from a Binance
// ACCOUNT_UPDATE user-data event: updated balances and positions.
type AccountUpdateEvent struct {
	Balances  []AccountUpdateBalance
	Positions []AccountUpdatePosition
}

type AccountUpdateBalance struct {
	Asset            string
	WalletBalance    float64
	AvailableBalance float64
}

type AccountUpdatePosition struct {
	Symbol           string
	PositionAmt      float64
	EntryPrice       float64
	UnrealizedProfit float64
	MarginType       string
}

// OrderUpdateEvent mirrors an ORDER_TRADE_UPDATE user-data event.
type OrderUpdateEvent struct {
	Symbol        string
	OrderID       int64
	ClientOrderID string
	Side          string
	Status        string
	ReduceOnly    bool
	OrigQty       float64
	FilledQty     float64
	AvgPrice      float64
	LastFillPrice float64
}

type userStreamEnvelope struct {
	EventType string          `json:"e"`
	EventTime int64           `json:"E"`
	Account   json.RawMessage `json:"a"`
	Order     json.RawMessage `json:"o"`
}

type accountUpdatePayload struct {
	Balances []struct {
		Asset  string `json:"a"`
		Wallet numStr `json:"wb"`
		Avail  numStr `json:"cw"`
	} `json:"B"`
	Positions []struct {
		Symbol     string `json:"s"`
		Amt        numStr `json:"pa"`
		EntryPrice numStr `json:"ep"`
		UnPnL      numStr `json:"up"`
		MarginType string `json:"mt"`
	} `json:"P"`
}

type orderUpdatePayload struct {
	Symbol        string `json:"s"`
	ClientOrderID string `json:"c"`
	Side          string `json:"S"`
	OrderID       int64  `json:"i"`
	OrigQty       numStr `json:"q"`
	FilledQty     numStr `json:"z"`
	LastFillPrice numStr `json:"L"`
	AvgPrice      numStr `json:"ap"`
	Status        string `json:"X"`
	ReduceOnly    bool   `json:"R"`
}

// RunUserStream connects to the user-data WebSocket for the given listenKey
// and dispatches ACCOUNT_UPDATE / ORDER_TRADE_UPDATE events until ctx is
// cancelled or the stream reports listenKeyExpired (in which case it
// returns errListenKeyExpired so the caller can create a fresh key).
func (c *Client) RunUserStream(ctx context.Context, listenKey string, onAccount func(AccountUpdateEvent), onOrder func(OrderUpdateEvent)) error {
	wsURL := c.wsBase + "/ws/" + listenKey
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if err != nil {
		return fmt.Errorf("dial user stream: %w", err)
	}
	defer conn.Close()

	go func() {
		<-ctx.Done()
		conn.Close()
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}

		var env userStreamEnvelope
		if err := json.Unmarshal(msg, &env); err != nil {
			log.Printf("binance: user stream: bad envelope: %v", err)
			continue
		}

		switch env.EventType {
		case "ACCOUNT_UPDATE":
			var p struct {
				A accountUpdatePayload `json:"a"`
			}
			if err := json.Unmarshal(msg, &p); err != nil {
				log.Printf("binance: user stream: bad ACCOUNT_UPDATE: %v", err)
				continue
			}
			evt := AccountUpdateEvent{}
			for _, b := range p.A.Balances {
				evt.Balances = append(evt.Balances, AccountUpdateBalance{
					Asset: b.Asset, WalletBalance: b.Wallet.Float(), AvailableBalance: b.Avail.Float(),
				})
			}
			for _, pos := range p.A.Positions {
				evt.Positions = append(evt.Positions, AccountUpdatePosition{
					Symbol: pos.Symbol, PositionAmt: pos.Amt.Float(), EntryPrice: pos.EntryPrice.Float(),
					UnrealizedProfit: pos.UnPnL.Float(), MarginType: pos.MarginType,
				})
			}
			onAccount(evt)
		case "ORDER_TRADE_UPDATE":
			var p struct {
				O orderUpdatePayload `json:"o"`
			}
			if err := json.Unmarshal(msg, &p); err != nil {
				log.Printf("binance: user stream: bad ORDER_TRADE_UPDATE: %v", err)
				continue
			}
			onOrder(OrderUpdateEvent{
				Symbol: p.O.Symbol, OrderID: p.O.OrderID, ClientOrderID: p.O.ClientOrderID,
				Side: p.O.Side, Status: p.O.Status, ReduceOnly: p.O.ReduceOnly,
				OrigQty: p.O.OrigQty.Float(), FilledQty: p.O.FilledQty.Float(),
				AvgPrice: p.O.AvgPrice.Float(), LastFillPrice: p.O.LastFillPrice.Float(),
			})
		case "listenKeyExpired":
			return errListenKeyExpired
		}
	}
}

var errListenKeyExpired = fmt.Errorf("binance: listenKey expired")

// IsListenKeyExpired reports whether err is the listenKeyExpired sentinel
// RunUserStream returns, so the caller knows to mint a fresh listenKey
// (rather than just reconnecting to the now-dead one) before retrying.
func IsListenKeyExpired(err error) bool {
	return err == errListenKeyExpired
}

// KeepAliveLoop calls KeepAliveListenKey on the given interval until ctx is
// cancelled, logging (not panicking) on transient failures.
func (c *Client) KeepAliveLoop(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := c.KeepAliveListenKey(ctx); err != nil {
				log.Printf("binance: listenKey keepalive failed: %v", err)
			}
		}
	}
}
