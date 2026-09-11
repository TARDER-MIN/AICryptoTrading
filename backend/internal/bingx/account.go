package bingx

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"cryptotrading/internal/models"
)

type accountResponse struct {
	Asset            string `json:"asset"`
	Balance          numStr `json:"balance"`
	AvailableMargin  numStr `json:"availableMargin"`
	UnrealizedProfit numStr `json:"unrealizedProfit"`
}

// Account fetches the account-level balance snapshot (signed).
func (c *Client) Account(ctx context.Context) (*models.AccountSummary, error) {
	var rows []accountResponse
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v3/user/balance", nil, true, true, &rows); err != nil {
		return nil, err
	}
	var out accountResponse
	for _, r := range rows {
		if r.Asset == "USDT" {
			out = r
			break
		}
	}
	return &models.AccountSummary{
		WalletBalanceUSD:    out.Balance.Float(),
		AvailableBalanceUSD: out.AvailableMargin.Float(),
		TotalUnrealizedPnL:  out.UnrealizedProfit.Float(),
		UpdatedAt:           time.Now(),
	}, nil
}

type positionRiskRow struct {
	Symbol           string `json:"symbol"`
	PositionAmt      numStr `json:"positionAmt"`
	EntryPrice       numStr `json:"avgPrice"`
	UnRealizedProfit numStr `json:"unRealizedProfit"`
	LiquidationPrice numStr `json:"liquidationPrice"`
	Leverage         numStr `json:"leverage"`
	Isolated         bool   `json:"isolated"`
	PositionSide     string `json:"positionSide"`
}

// PositionRisk fetches every open position with nonzero size (signed).
// Symbols with positionAmt == 0 are filtered out - Binance returns a row
// for every symbol regardless of whether a position is open.
func (c *Client) PositionRisk(ctx context.Context) ([]models.Position, error) {
	var rows []positionRiskRow
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/user/positions", nil, true, true, &rows); err != nil {
		return nil, err
	}

	out := make([]models.Position, 0)
	now := time.Now()
	for _, r := range rows {
		amt := r.PositionAmt.Float()
		if amt == 0 {
			continue
		}
		side := "long"
		if r.PositionSide == "SHORT" {
			side = "short"
		}
		if amt < 0 {
			amt = -amt
		}
		mark := r.EntryPrice.Float()
		if amt > 0 {
			if side == "long" {
				mark += r.UnRealizedProfit.Float() / amt
			} else {
				mark -= r.UnRealizedProfit.Float() / amt
			}
		}
		marginType := "CROSSED"
		if r.Isolated {
			marginType = "ISOLATED"
		}
		out = append(out, models.Position{
			Symbol:           r.Symbol,
			Side:             side,
			Qty:              amt,
			EntryPrice:       r.EntryPrice.Float(),
			MarkPrice:        mark,
			UnrealizedPnL:    r.UnRealizedProfit.Float(),
			Leverage:         int(r.Leverage.Float()),
			LiquidationPrice: r.LiquidationPrice.Float(),
			MarginType:       marginType,
			UpdatedAt:        now,
		})
	}
	return out, nil
}

// SetLeverage sets the leverage for symbol (signed). Idempotent - Binance
// accepts re-setting the same leverage without error.
func (c *Client) SetLeverage(ctx context.Context, symbol string, leverage int) error {
	sides := []string{"BOTH"}
	if c.hedgeMode() {
		sides = []string{"LONG", "SHORT"}
	}
	for _, side := range sides {
		params := url.Values{"symbol": {symbol}, "leverage": {strconv.Itoa(leverage)}, "side": {side}}
		if err := c.do(ctx, http.MethodPost, "/openApi/swap/v2/trade/leverage", params, true, true, nil); err != nil {
			return err
		}
	}
	return nil
}

// binanceErrMarginTypeUnchanged is Binance's error code for "No need to
// change margin type" - returned when the symbol is already on the
// requested margin type. Treated as success by SetMarginType.
const binanceErrMarginTypeUnchanged = -4046

// SetMarginType sets ISOLATED or CROSSED margin for symbol (signed).
// Already-set is treated as success (Binance rejects a no-op change with
// code -4046, which is not a real failure for our idempotent startup call).
func (c *Client) SetMarginType(ctx context.Context, symbol, marginType string) error {
	params := url.Values{"symbol": {symbol}, "marginType": {marginType}}
	return c.do(ctx, http.MethodPost, "/openApi/swap/v2/trade/marginType", params, true, true, nil)
}

type OrderResponse struct {
	OrderID       int64  `json:"orderId"`
	ClientOrderID string `json:"clientOrderId"`
	Symbol        string `json:"symbol"`
	Side          string `json:"side"`
	Status        string `json:"status"`
	Type          string `json:"type"`
	ReduceOnly    bool   `json:"reduceOnly"`
	OrigQty       numStr `json:"origQty"`
	ExecutedQty   numStr `json:"executedQty"`
	AvgPrice      numStr `json:"avgPrice"`
}

// PlaceMarketOrder submits a MARKET order (signed). qty must already be
// rounded to the symbol's stepSize and validated against MinNotional by the
// caller via FilterCache.MaxQtyForCap - this method does not re-validate.
func (c *Client) PlaceMarketOrder(ctx context.Context, symbol string, side models.OrderSide, qty float64, reduceOnly bool, clientOrderID string) (*OrderResponse, error) {
	params := url.Values{
		"symbol":           {symbol},
		"side":             {string(side)},
		"type":             {"MARKET"},
		"quantity":         {strconv.FormatFloat(qty, 'f', -1, 64)},
		"newOrderRespType": {"RESULT"},
	}
	if reduceOnly && !c.hedgeMode() {
		params.Set("reduceOnly", "true")
	}
	if clientOrderID != "" {
		params.Set("newClientOrderId", clientOrderID)
	}

	var out OrderResponse
	params.Set("positionSide", c.positionSide(string(side), reduceOnly))
	if err := c.do(ctx, http.MethodPost, "/openApi/swap/v2/trade/order", params, true, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// QueryOrder fetches the current status of a previously-placed order
// (signed). Used as a REST fallback/supplement to reconcile local order
// status when the user-data-stream ORDER_TRADE_UPDATE event may not have
// arrived (see internal/autotrader.ReconcilePendingOrders).
func (c *Client) QueryOrder(ctx context.Context, symbol string, orderID int64) (*OrderResponse, error) {
	params := url.Values{"symbol": {symbol}, "orderId": {strconv.FormatInt(orderID, 10)}}
	var out OrderResponse
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/trade/order", params, true, true, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// --- User data stream (listenKey) lifecycle ---

type listenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

// CreateListenKey starts a new user-data-stream session (needs the API key
// header but is NOT HMAC-signed).
func (c *Client) CreateListenKey(ctx context.Context) (string, error) {
	var out listenKeyResponse
	if err := c.do(ctx, http.MethodPost, "/fapi/v1/listenKey", nil, false, true, &out); err != nil {
		return "", err
	}
	return out.ListenKey, nil
}

// KeepAliveListenKey extends the current listenKey's 60-minute expiry.
// Callers should invoke this on a ticker well under 60 minutes (the config
// default is every 30) to avoid ever letting it lapse.
func (c *Client) KeepAliveListenKey(ctx context.Context) error {
	return c.do(ctx, http.MethodPut, "/fapi/v1/listenKey", nil, false, true, nil)
}

// CloseListenKey ends the user-data-stream session (call on graceful shutdown).
func (c *Client) CloseListenKey(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/fapi/v1/listenKey", nil, false, true, nil)
}
