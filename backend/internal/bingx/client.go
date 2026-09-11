// Package bingx implements the BingX USDT-M perpetual REST transport.
package bingx

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ErrCode struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

func (e *ErrCode) Error() string { return fmt.Sprintf("bingx: code=%d msg=%s", e.Code, e.Msg) }

func AsErrCode(err error) (*ErrCode, bool) { var e *ErrCode; ok := errors.As(err, &e); return e, ok }

type Client struct {
	apiKey, apiSecret, restBase, wsBase string
	httpClient                          *http.Client
	clock                               *clockSync
}

func NewClient(key, secret, restBase, wsBase string) *Client {
	return &Client{apiKey: key, apiSecret: secret, restBase: strings.TrimRight(restBase, "/"), wsBase: strings.TrimRight(wsBase, "/"), httpClient: &http.Client{Timeout: 15 * time.Second}, clock: newClockSync()}
}

func (c *Client) hedgeMode() bool {
	return !strings.EqualFold(os.Getenv("BINGX_POSITION_MODE"), "ONEWAY")
}

func (c *Client) positionSide(side string, closing bool) string {
	if !c.hedgeMode() {
		return "BOTH"
	}
	if closing {
		if side == "BUY" {
			return "SHORT"
		}
		return "LONG"
	}
	if side == "BUY" {
		return "LONG"
	}
	return "SHORT"
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func (c *Client) do(ctx context.Context, method, path string, params url.Values, signed, needsKey bool, out any) error {
	if params == nil {
		params = url.Values{}
	}
	if signed {
		needsKey = true
		params.Set("timestamp", strconv.FormatInt(c.clock.Now().UnixMilli(), 10))
		if params.Get("recvWindow") == "" {
			params.Set("recvWindow", "5000")
		}
		mac := hmac.New(sha256.New, []byte(c.apiSecret))
		_, _ = mac.Write([]byte(encodeSorted(params)))
		params.Set("signature", hex.EncodeToString(mac.Sum(nil)))
	}
	query := encodeSorted(params)
	reqURL := c.restBase + path
	if query != "" {
		reqURL += "?" + query
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, nil)
	if err != nil {
		return err
	}
	if needsKey {
		req.Header.Set("X-BX-APIKEY", c.apiKey)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("bingx: request %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("bingx: %s %s HTTP %d: %s", method, path, resp.StatusCode, truncate(body, 500))
	}
	var env envelope
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("bingx: parse envelope: %w (%s)", err, truncate(body, 500))
	}
	if env.Code != 0 {
		return &ErrCode{Code: env.Code, Msg: env.Msg}
	}
	if out != nil && len(env.Data) > 0 && string(env.Data) != "null" {
		if err := json.Unmarshal(env.Data, out); err != nil {
			return fmt.Errorf("bingx: parse data %s: %w (%s)", path, err, truncate(env.Data, 500))
		}
	}
	return nil
}

func encodeSorted(v url.Values) string {
	keys := make([]string, 0, len(v))
	for k := range v {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for i, k := range keys {
		if i > 0 {
			b.WriteByte('&')
		}
		b.WriteString(url.QueryEscape(k))
		b.WriteByte('=')
		b.WriteString(url.QueryEscape(v.Get(k)))
	}
	return b.String()
}

func truncate(b []byte, n int) string {
	s := string(b)
	if len(s) > n {
		return s[:n] + "..."
	}
	return s
}

type numStr float64

func (n *numStr) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*n = 0
		return nil
	}
	f, e := strconv.ParseFloat(s, 64)
	if e != nil {
		return e
	}
	*n = numStr(f)
	return nil
}
func (n numStr) Float() float64 { return float64(n) }
