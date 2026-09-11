package bingx

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"cryptotrading/internal/models"
)

// AlgoOrderResponse preserves the application's existing protective-order model.
// On BingX these are normal STOP_MARKET/TAKE_PROFIT_MARKET orders, so AlgoID is
// the BingX orderId and QueryAlgoOrder uses the regular order-detail endpoint.
type AlgoOrderResponse struct {
	AlgoID       int64  `json:"orderId"`
	ClientAlgoID string `json:"clientOrderId"`
	Symbol       string `json:"symbol"`
	Side         string `json:"side"`
	OrderType    string `json:"type"`
	AlgoStatus   string `json:"status"`
	TriggerPrice numStr `json:"stopPrice"`
	ActualPrice  numStr `json:"avgPrice"`
}

func (c *Client) PlaceClosePositionAlgoOrder(ctx context.Context, symbol string, side models.OrderSide, qty float64, orderType string, triggerPrice float64, clientID string) (*AlgoOrderResponse, error) {
	p := url.Values{"symbol": {symbol}, "side": {string(side)}, "positionSide": {c.positionSide(string(side), true)}, "type": {orderType}, "quantity": {strconv.FormatFloat(qty, 'f', -1, 64)}, "stopPrice": {strconv.FormatFloat(triggerPrice, 'f', -1, 64)}, "closePosition": {"true"}, "workingType": {"MARK_PRICE"}}
	// BingX supports clientOrderId only for MARKET/LIMIT, not conditional orders.
	var out AlgoOrderResponse
	if err := c.do(ctx, http.MethodPost, "/openApi/swap/v2/trade/order", p, true, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) QueryAlgoOrder(ctx context.Context, symbol string, id int64) (*AlgoOrderResponse, error) {
	p := url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(id, 10)}}
	var out AlgoOrderResponse
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/trade/order", p, true, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *Client) CancelAlgoOrder(ctx context.Context, symbol string, id int64) error {
	p := url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(id, 10)}}
	return c.do(ctx, http.MethodDelete, "/openApi/swap/v2/trade/order", p, true, true, nil)
}
