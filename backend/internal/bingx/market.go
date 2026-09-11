package bingx

import (
	"context"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"time"

	"cryptotrading/internal/models"
)

// ExchangeInfoSymbol is the subset of GET /fapi/v1/exchangeInfo's per-symbol
// fields this project needs: trading status and the LOT_SIZE/MIN_NOTIONAL/
// PRICE_FILTER filters used to size and validate orders.
type ExchangeInfoSymbol struct {
	Symbol            string           `json:"symbol"`
	Status            string           `json:"-"` // normalized to "TRADING" when live
	ContractType      string           `json:"contractType"`
	QuoteAsset        string           `json:"quoteAsset"`
	QuantityPrecision int              `json:"quantityPrecision"`
	PricePrecision    int              `json:"pricePrecision"`
	Filters           []map[string]any `json:"filters"`
	TradeMinQuantity  float64          `json:"tradeMinQuantity"`
	TradeMinUSDT      float64          `json:"tradeMinUSDT"`
	StatusCode        int              `json:"status"`
	APIStateOpen      string           `json:"apiStateOpen"`
	Currency          string           `json:"currency"`
	Asset             string           `json:"asset"`
}

// ExchangeInfo fetches the full symbol/filter table. Unauthenticated.
func (c *Client) ExchangeInfo(ctx context.Context) ([]ExchangeInfoSymbol, error) {
	var out []ExchangeInfoSymbol
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/quote/contracts", nil, false, false, &out); err != nil {
		return nil, err
	}
	for i := range out {
		if out[i].StatusCode == 1 && out[i].APIStateOpen == "true" {
			out[i].Status = "TRADING"
		}
		out[i].QuoteAsset = out[i].Currency
		out[i].ContractType = "PERPETUAL"
	}
	return out, nil
}

type klineRow struct {
	Open   numStr `json:"open"`
	Close  numStr `json:"close"`
	High   numStr `json:"high"`
	Low    numStr `json:"low"`
	Volume numStr `json:"volume"`
	Time   int64  `json:"time"`
}

// Klines returns historical candles for symbol, oldest first.
func (c *Client) Klines(ctx context.Context, symbol, interval string, limit int) ([]models.Candle, error) {
	params := url.Values{
		"symbol":   {symbol},
		"interval": {interval},
		"limit":    {strconv.Itoa(limit)},
	}
	var raw []klineRow
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v3/quote/klines", params, false, false, &raw); err != nil {
		return nil, err
	}

	out := make([]models.Candle, 0, len(raw))
	for _, k := range raw {
		out = append(out, models.Candle{
			Symbol: symbol,
			Ts:     time.UnixMilli(k.Time),
			Open:   k.Open.Float(),
			High:   k.High.Float(),
			Low:    k.Low.Float(),
			Close:  k.Close.Float(),
			Volume: k.Volume.Float(),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out, nil
}

func parseAny(v any) float64 {
	switch t := v.(type) {
	case string:
		f, _ := strconv.ParseFloat(t, 64)
		return f
	case float64:
		return t
	default:
		return 0
	}
}

// KlinesRange fetches every kline between start and end (inclusive),
// paginating past Binance's 1500-per-call limit via startTime/endTime -
// needed to pull enough history (weeks to months of 5m bars) for a
// meaningful backtest train/validation split, far more than the single
// SeedHistory call (limit-only, most-recent-N) used for live chart seeding.
func (c *Client) KlinesRange(ctx context.Context, symbol, interval string, start, end time.Time) ([]models.Candle, error) {
	const pageLimit = 1440
	var out []models.Candle
	cursorEnd := end

	for cursorEnd.After(start) {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		params := url.Values{
			"symbol":    {symbol},
			"interval":  {interval},
			"startTime": {strconv.FormatInt(start.UnixMilli(), 10)},
			"endTime":   {strconv.FormatInt(cursorEnd.UnixMilli(), 10)},
			"limit":     {strconv.Itoa(pageLimit)},
		}
		var raw []klineRow
		if err := c.do(ctx, http.MethodGet, "/openApi/swap/v3/quote/klines", params, false, false, &raw); err != nil {
			return nil, err
		}
		if len(raw) == 0 {
			break
		}

		earliest := cursorEnd
		for _, k := range raw {
			ts := time.UnixMilli(k.Time)
			if ts.Before(start) || ts.After(end) {
				continue
			}
			if ts.Before(earliest) {
				earliest = ts
			}
			out = append(out, models.Candle{
				Symbol: symbol,
				Ts:     ts,
				Open:   k.Open.Float(),
				High:   k.High.Float(),
				Low:    k.Low.Float(),
				Close:  k.Close.Float(),
				Volume: k.Volume.Float(),
			})
		}
		if !earliest.Before(cursorEnd) {
			break // safety valve against an infinite loop if the API ever stops advancing
		}
		cursorEnd = earliest.Add(-time.Millisecond)

		if len(raw) < pageLimit {
			break // reached the end of available data before `end`
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out, nil
}

// Ticker24hr is the subset of GET /fapi/v1/ticker/24hr this project needs
// for ranking candidates by liquidity in the AI daily watchlist selection
// (internal/watchlistai) - quote volume is the standard "how much money
// actually traded" liquidity signal, more meaningful across symbols of
// wildly different price/precision than raw base-asset volume.
type Ticker24hr struct {
	Symbol             string `json:"symbol"`
	LastPrice          numStr `json:"lastPrice"`
	PriceChangePercent numStr `json:"priceChangePercent"`
	QuoteVolume        numStr `json:"quoteVolume"`
}

// Ticker24hrAll fetches the rolling 24h stats for every symbol in one call
// (no `symbol` param). Unauthenticated.
func (c *Client) Ticker24hrAll(ctx context.Context) ([]Ticker24hr, error) {
	var out []Ticker24hr
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/quote/ticker", nil, false, false, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// PremiumIndexResult is the subset of GET /fapi/v1/premiumIndex used for
// funding-rate context - a crypto-perpetual-specific signal with no analog
// in the reference TWSE (equities) project.
type PremiumIndexResult struct {
	Symbol          string `json:"symbol"`
	MarkPrice       numStr `json:"markPrice"`
	LastFundingRate numStr `json:"lastFundingRate"`
	NextFundingTime int64  `json:"nextFundingTime"`
}

func (c *Client) PremiumIndex(ctx context.Context, symbol string) (*PremiumIndexResult, error) {
	params := url.Values{"symbol": {symbol}}
	var out PremiumIndexResult
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/quote/premiumIndex", params, false, false, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// PremiumIndexAll fetches funding rate/mark price for every symbol in one
// call (no `symbol` param) - used by internal/watchlistai to avoid one REST
// call per candidate when ranking dozens of symbols.
func (c *Client) PremiumIndexAll(ctx context.Context) ([]PremiumIndexResult, error) {
	var out []PremiumIndexResult
	if err := c.do(ctx, http.MethodGet, "/openApi/swap/v2/quote/premiumIndex", nil, false, false, &out); err != nil {
		return nil, err
	}
	return out, nil
}
