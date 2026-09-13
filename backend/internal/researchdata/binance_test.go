package researchdata

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestBinanceSymbol(t *testing.T) {
	got, err := BinanceSymbol("1000PEPE-USDT")
	if err != nil || got != "1000PEPEUSDT" {
		t.Fatalf("BinanceSymbol() = %q, %v; want 1000PEPEUSDT", got, err)
	}
	if _, err := BinanceSymbol("BTCUSD"); err == nil {
		t.Fatal("BinanceSymbol() accepted a non-USDT BingX symbol")
	}
}

func TestKlinesRangePaginatesAndPreservesBingXSymbol(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.URL.Path != "/fapi/v1/klines" || r.URL.Query().Get("symbol") != "BTCUSDT" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		cursor, _ := strconv.ParseInt(r.URL.Query().Get("startTime"), 10, 64)
		if requests > 1 {
			step := int64(5 * time.Minute / time.Millisecond)
			cursor = ((cursor + step - 1) / step) * step
		}
		count := pageLimit
		if requests == 2 {
			count = 2
		}
		_, _ = fmt.Fprint(w, "[")
		for i := 0; i < count; i++ {
			if i > 0 {
				_, _ = fmt.Fprint(w, ",")
			}
			ts := cursor + int64(i)*int64(5*time.Minute/time.Millisecond)
			_, _ = fmt.Fprintf(w, `[%d,"100","101","99","100.5","12"]`, ts)
		}
		_, _ = fmt.Fprint(w, "]")
	}))
	defer server.Close()

	client := NewClient(server.URL)
	client.spacing = 0
	end := start.Add(1002 * 5 * time.Minute)
	got, err := client.KlinesRange(context.Background(), "BTC-USDT", "5m", start, end)
	if err != nil {
		t.Fatalf("KlinesRange() error = %v", err)
	}
	if requests != 2 || len(got) != 1002 {
		t.Fatalf("requests/candles = %d/%d, want 2/1002", requests, len(got))
	}
	if got[0].Symbol != "BTC-USDT" || got[len(got)-1].Ts != start.Add(1001*5*time.Minute) {
		t.Fatalf("unexpected first/last candle: %#v / %#v", got[0], got[len(got)-1])
	}
}

func TestKlinesRangeStopsWhenServerDoesNotAdvance(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		_, _ = fmt.Fprintf(w, `[[%d,"1","1","1","1","1"]]`, start.UnixMilli())
	}))
	defer server.Close()

	client := NewClient(server.URL)
	client.spacing = 0
	got, err := client.KlinesRange(context.Background(), "BTC-USDT", "5m", start, start.Add(time.Hour))
	if err != nil {
		t.Fatalf("KlinesRange() error = %v", err)
	}
	if requests != 1 || len(got) != 1 {
		t.Fatalf("requests/candles = %d/%d, want 1/1", requests, len(got))
	}
}

func TestFundingRatesRange(t *testing.T) {
	start := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/fapi/v1/fundingRate" || r.URL.Query().Get("symbol") != "ETHUSDT" {
			t.Fatalf("unexpected request %s?%s", r.URL.Path, r.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(w, `[{"fundingRate":"0.0001","fundingTime":%d,"markPrice":"2000"}]`, start.Add(8*time.Hour).UnixMilli())
	}))
	defer server.Close()

	client := NewClient(server.URL)
	client.spacing = 0
	got, err := client.FundingRatesRange(context.Background(), "ETH-USDT", start, start.Add(24*time.Hour))
	if err != nil {
		t.Fatalf("FundingRatesRange() error = %v", err)
	}
	if len(got) != 1 || got[0].Rate != 0.0001 || got[0].MarkPrice != 2000 {
		t.Fatalf("unexpected funding rows: %#v", got)
	}
}
