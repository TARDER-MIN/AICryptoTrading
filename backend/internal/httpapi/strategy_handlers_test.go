package httpapi

import (
	"encoding/json"
	"slices"
	"testing"

	"cryptotrading/internal/bingx"
)

func TestSelectOptimizeSymbolsRanksCryptoAndExcludesTradFi(t *testing.T) {
	contracts := []bingx.ExchangeInfoSymbol{
		{Symbol: "BTC-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "ETH-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "SOL-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "NCCOGOLD2USD-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "NCFXEUR2USD-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "NCSINASDAQ2USD-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "NCSKTSLA2USD-USDT", Status: "TRADING", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
		{Symbol: "OFFLINE-USDT", Status: "BREAK", ContractType: "PERPETUAL", QuoteAsset: "USDT"},
	}

	var tickers []bingx.Ticker24hr
	if err := json.Unmarshal([]byte(`[
		{"symbol":"NCCOGOLD2USD-USDT","lastPrice":"2600","quoteVolume":"9999"},
		{"symbol":"NCFXEUR2USD-USDT","lastPrice":"1.1","quoteVolume":"9000"},
		{"symbol":"NCSINASDAQ2USD-USDT","lastPrice":"20000","quoteVolume":"8000"},
		{"symbol":"NCSKTSLA2USD-USDT","lastPrice":"300","quoteVolume":"7000"},
		{"symbol":"SOL-USDT","lastPrice":"150","quoteVolume":"300"},
		{"symbol":"ETH-USDT","lastPrice":"4000","quoteVolume":"200"},
		{"symbol":"BTC-USDT","lastPrice":"100000","quoteVolume":"100"},
		{"symbol":"OFFLINE-USDT","lastPrice":"1","quoteVolume":"5000"}
	]`), &tickers); err != nil {
		t.Fatalf("decode test tickers: %v", err)
	}

	got := selectOptimizeSymbols(contracts, tickers, 2)
	want := []string{"SOL-USDT", "ETH-USDT"}
	if !slices.Equal(got, want) {
		t.Fatalf("selectOptimizeSymbols() = %v, want %v", got, want)
	}
}
