// Package researchdata reads public, unsigned market history used only for
// long-range strategy research. It never places orders and never reads an
// account. Live execution and the short apples-to-apples backtest remain on
// BingX; Binance USDT perpetual history is an explicitly labelled proxy when
// BingX cannot supply a full year of M5 candles.
package researchdata

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"cryptotrading/internal/models"
)

const (
	defaultBaseURL = "https://fapi.binance.com"
	pageLimit      = 1000
)

// Client deliberately rate-limits itself below Binance Futures' public
// request-weight ceiling. A 1000-row kline page has request weight 5; 150 ms
// spacing stays near 2,000 weight/minute rather than riding the 2,400 limit.
type Client struct {
	baseURL     string
	httpClient  *http.Client
	spacing     time.Duration
	requestMu   sync.Mutex
	nextRequest time.Time
}

func NewClient(baseURL string) *Client {
	if strings.TrimSpace(baseURL) == "" {
		baseURL = defaultBaseURL
	}
	return &Client{
		baseURL:    strings.TrimRight(baseURL, "/"),
		httpClient: &http.Client{Timeout: 30 * time.Second},
		spacing:    150 * time.Millisecond,
	}
}

// BinanceSymbol maps BingX's BTC-USDT spelling to Binance's BTCUSDT.
func BinanceSymbol(bingxSymbol string) (string, error) {
	symbol := strings.ToUpper(strings.TrimSpace(bingxSymbol))
	if !strings.HasSuffix(symbol, "-USDT") || strings.Count(symbol, "-") != 1 {
		return "", fmt.Errorf("research data: unsupported BingX symbol %q", bingxSymbol)
	}
	base := strings.TrimSuffix(symbol, "-USDT")
	if base == "" {
		return "", fmt.Errorf("research data: unsupported BingX symbol %q", bingxSymbol)
	}
	return base + "USDT", nil
}

// KlinesRange fetches all public Binance USDT-perpetual candles in ascending
// time order. Returned Candle.Symbol remains in BingX spelling so the same
// strategy/backtest code and the same symbol universe can be reused.
func (c *Client) KlinesRange(ctx context.Context, bingxSymbol, interval string, start, end time.Time) ([]models.Candle, error) {
	binanceSymbol, err := BinanceSymbol(bingxSymbol)
	if err != nil {
		return nil, err
	}
	if !end.After(start) {
		return nil, fmt.Errorf("research data: end must be after start")
	}

	var out []models.Candle
	cursor := start.UnixMilli()
	endMillis := end.UnixMilli()
	lastSeen := int64(-1)
	for cursor <= endMillis {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		query := url.Values{
			"symbol":    {binanceSymbol},
			"interval":  {interval},
			"startTime": {strconv.FormatInt(cursor, 10)},
			"endTime":   {strconv.FormatInt(endMillis, 10)},
			"limit":     {strconv.Itoa(pageLimit)},
		}
		var rows [][]json.RawMessage
		if err := c.getJSON(ctx, "/fapi/v1/klines", query, &rows); err != nil {
			return nil, fmt.Errorf("research data: %s klines: %w", bingxSymbol, err)
		}
		if len(rows) == 0 {
			break
		}

		pageLast := lastSeen
		for _, row := range rows {
			if len(row) < 6 {
				return nil, fmt.Errorf("research data: malformed kline row for %s", bingxSymbol)
			}
			tsMillis, err := parseInt64(row[0])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s kline timestamp: %w", bingxSymbol, err)
			}
			if tsMillis < start.UnixMilli() || tsMillis > endMillis || tsMillis <= lastSeen {
				continue
			}
			open, err := parseFloat(row[1])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s open: %w", bingxSymbol, err)
			}
			high, err := parseFloat(row[2])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s high: %w", bingxSymbol, err)
			}
			low, err := parseFloat(row[3])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s low: %w", bingxSymbol, err)
			}
			closePrice, err := parseFloat(row[4])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s close: %w", bingxSymbol, err)
			}
			volume, err := parseFloat(row[5])
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s volume: %w", bingxSymbol, err)
			}
			out = append(out, models.Candle{
				Symbol: bingxSymbol, Ts: time.UnixMilli(tsMillis).UTC(),
				Open: open, High: high, Low: low, Close: closePrice, Volume: volume,
			})
			pageLast = tsMillis
		}
		if pageLast <= lastSeen {
			break
		}
		lastSeen = pageLast
		cursor = pageLast + 1
		if len(rows) < pageLimit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ts.Before(out[j].Ts) })
	return out, nil
}

type FundingRateEvent struct {
	Symbol    string
	Rate      float64
	Time      time.Time
	MarkPrice float64
}

type fundingRow struct {
	FundingRate json.RawMessage `json:"fundingRate"`
	FundingTime json.RawMessage `json:"fundingTime"`
	MarkPrice   json.RawMessage `json:"markPrice"`
}

// FundingRatesRange fetches the proxy venue's actual settlement history so
// the one-year report does not mix Binance prices with BingX funding.
func (c *Client) FundingRatesRange(ctx context.Context, bingxSymbol string, start, end time.Time) ([]FundingRateEvent, error) {
	binanceSymbol, err := BinanceSymbol(bingxSymbol)
	if err != nil {
		return nil, err
	}
	var out []FundingRateEvent
	cursor := start.UnixMilli()
	endMillis := end.UnixMilli()
	lastSeen := int64(-1)
	for cursor <= endMillis {
		query := url.Values{
			"symbol":    {binanceSymbol},
			"startTime": {strconv.FormatInt(cursor, 10)},
			"endTime":   {strconv.FormatInt(endMillis, 10)},
			"limit":     {strconv.Itoa(pageLimit)},
		}
		var rows []fundingRow
		if err := c.getJSON(ctx, "/fapi/v1/fundingRate", query, &rows); err != nil {
			return nil, fmt.Errorf("research data: %s funding: %w", bingxSymbol, err)
		}
		if len(rows) == 0 {
			break
		}
		pageLast := lastSeen
		for _, row := range rows {
			tsMillis, err := parseInt64(row.FundingTime)
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s funding time: %w", bingxSymbol, err)
			}
			if tsMillis < start.UnixMilli() || tsMillis > endMillis || tsMillis <= lastSeen {
				continue
			}
			rate, err := parseFloat(row.FundingRate)
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s funding rate: %w", bingxSymbol, err)
			}
			markPrice, err := parseOptionalFloat(row.MarkPrice)
			if err != nil {
				return nil, fmt.Errorf("research data: parse %s mark price: %w", bingxSymbol, err)
			}
			out = append(out, FundingRateEvent{
				Symbol: bingxSymbol, Rate: rate, Time: time.UnixMilli(tsMillis).UTC(), MarkPrice: markPrice,
			})
			pageLast = tsMillis
		}
		if pageLast <= lastSeen {
			break
		}
		lastSeen = pageLast
		cursor = pageLast + 1
		if len(rows) < pageLimit {
			break
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Time.Before(out[j].Time) })
	return out, nil
}

func (c *Client) getJSON(ctx context.Context, path string, query url.Values, out any) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := c.wait(ctx); err != nil {
			return err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path+"?"+query.Encode(), nil)
		if err != nil {
			return err
		}
		resp, err := c.httpClient.Do(req)
		if err == nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
			decodeErr := json.NewDecoder(resp.Body).Decode(out)
			resp.Body.Close()
			if decodeErr != nil {
				return fmt.Errorf("decode %s: %w", path, decodeErr)
			}
			return nil
		}

		if err != nil {
			lastErr = err
		} else {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 1000))
			resp.Body.Close()
			lastErr = fmt.Errorf("HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
			if resp.StatusCode != http.StatusTooManyRequests && resp.StatusCode < 500 {
				break
			}
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(attempt+1) * time.Second):
		}
	}
	return lastErr
}

func (c *Client) wait(ctx context.Context) error {
	c.requestMu.Lock()
	defer c.requestMu.Unlock()
	if delay := time.Until(c.nextRequest); delay > 0 {
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
	c.nextRequest = time.Now().Add(c.spacing)
	return nil
}

func parseInt64(raw json.RawMessage) (int64, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return strconv.ParseInt(value, 10, 64)
}

func parseFloat(raw json.RawMessage) (float64, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	return strconv.ParseFloat(value, 64)
}

func parseOptionalFloat(raw json.RawMessage) (float64, error) {
	value := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if value == "" || value == "null" {
		return 0, nil
	}
	return strconv.ParseFloat(value, 64)
}
