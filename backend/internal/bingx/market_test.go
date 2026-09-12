package bingx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"
)

func TestKlinesRangePaginatesAtBingXActualLimit(t *testing.T) {
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	const totalCandles = 1002
	rows := make([]klineRow, totalCandles)
	for i := range rows {
		ts := start.Add(time.Duration(i) * 5 * time.Minute)
		rows[i] = klineRow{
			Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10,
			Time: ts.UnixMilli(),
		}
	}

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if got := r.URL.Query().Get("limit"); got != "1000" {
			t.Errorf("limit = %q, want 1000", got)
		}
		endMillis, err := strconv.ParseInt(r.URL.Query().Get("endTime"), 10, 64)
		if err != nil {
			t.Errorf("invalid endTime: %v", err)
			http.Error(w, "bad endTime", http.StatusBadRequest)
			return
		}

		eligible := make([]klineRow, 0, len(rows))
		for i := len(rows) - 1; i >= 0; i-- {
			if rows[i].Time <= endMillis {
				eligible = append(eligible, rows[i])
			}
			if len(eligible) == 1000 {
				break
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"code": 0,
			"msg":  "",
			"data": eligible,
		})
	}))
	defer server.Close()

	client := NewClient("", "", server.URL, "")
	end := start.Add((totalCandles - 1) * 5 * time.Minute)
	got, err := client.KlinesRange(context.Background(), "BTC-USDT", "5m", start, end)
	if err != nil {
		t.Fatalf("KlinesRange() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("request count = %d, want 2", requests)
	}
	if len(got) != totalCandles {
		t.Fatalf("candle count = %d, want %d", len(got), totalCandles)
	}
	if !got[0].Ts.Equal(start) || !got[len(got)-1].Ts.Equal(end) {
		t.Fatalf("range = %s..%s, want %s..%s", got[0].Ts, got[len(got)-1].Ts, start, end)
	}
}
