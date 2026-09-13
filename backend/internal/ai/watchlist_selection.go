package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
)

const selectWatchlistTool = "select_daily_watchlist"

// WatchlistCandidate is one candidate perpetual contract's stats as shown
// to Claude when picking the daily watchlist. Pre-filtering/ranking by
// liquidity and $10-cap feasibility happens before this call (see
// internal/watchlistai) - this is the judgment layer on top of that
// mechanical shortlist.
type WatchlistCandidate struct {
	Symbol         string
	Price          float64
	ChangePct24h   float64
	QuoteVolume24h float64
	FundingRate    float64
	// RecentSignalCount is how many HTF 3+1 trades the currently-live rules
	// actually produced for this symbol over the last
	// internal/watchlistai.SignalScanLookbackDays - a real backtest count,
	// not a guess from volatility/change% (see internal/watchlistai.
	// AnnotateSignalFrequency). This is the primary ranking signal.
	RecentSignalCount int
}

type WatchlistPick struct {
	Symbol    string `json:"symbol"`
	Rationale string `json:"rationale"`
}

// SelectDailyWatchlist asks Claude to choose exactly n symbols from
// candidates - primarily ranked by RecentSignalCount, an actual backtest
// count of how often the currently-live HTF 3+1 rules fired
// on that symbol over the last lookbackDays (see internal/watchlistai.
// AnnotateSignalFrequency / SignalScanLookbackDays, the caller's source for
// this number) - with a rationale for each.
func (c *Client) SelectDailyWatchlist(ctx context.Context, candidates []WatchlistCandidate, n int, lookbackDays int) ([]WatchlistPick, error) {
	if !c.enabled {
		return nil, ErrNotConfigured
	}

	tool := anthropic.ToolParam{
		Name: selectWatchlistTool,
		Description: anthropic.String(fmt.Sprintf(
			"回傳選出的 %d 檔最容易在目前HTF 3+1規則下產生訊號的永續合約與各自理由。務必剛好回傳 %d 檔，且只能從提供的候選清單中選。", n, n)),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"picks": map[string]any{
					"type":        "array",
					"description": fmt.Sprintf("剛好 %d 檔選擇", n),
					"items": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"symbol":    map[string]any{"type": "string", "description": "合約代號，必須來自候選清單"},
							"rationale": map[string]any{"type": "string", "description": "繁體中文，2-3句話：(1)recent_signal_count（近期實際回測出的訊號次數）高不高，這是主要理由 (2)24h成交金額是否有基本流動性 (3)資金費率是否有極端值需要留意。不能只講優點，至少點出一項需要留意的風險（例如訊號次數雖高但樣本期間短、或資金費率偏極端）。"},
						},
						"required": []string{"symbol", "rationale"},
					},
				},
			},
			Required: []string{"picks"},
		},
	}

	prompt := buildWatchlistPrompt(candidates, n, lookbackDays)

	resp, err := c.anthropic.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 4096,
		System: []anthropic.TextBlockParam{
			{Text: c.watchlistSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: selectWatchlistTool},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ai: select daily watchlist: %w", err)
	}

	for _, block := range resp.Content {
		if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok && tu.Name == selectWatchlistTool {
			var parsed struct {
				Picks []WatchlistPick `json:"picks"`
			}
			if err := json.Unmarshal([]byte(tu.JSON.Input.Raw()), &parsed); err != nil {
				return nil, fmt.Errorf("ai: parse watchlist selection: %w", err)
			}
			return parsed.Picks, nil
		}
	}
	return nil, fmt.Errorf("ai: model did not call %s", selectWatchlistTool)
}

func buildWatchlistPrompt(candidates []WatchlistCandidate, n int, lookbackDays int) string {
	sorted := make([]WatchlistCandidate, len(candidates))
	copy(sorted, candidates)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].RecentSignalCount > sorted[j].RecentSignalCount })

	var b strings.Builder
	fmt.Fprintf(&b, "以下是候選永續合約清單（依recent_signal_count由高到低排序，共%d檔，皆為目前保證金×槓桿設定下可下單的合約）。recent_signal_count是拿目前上線中的完整HTF 3+1規則（已收線H1掃流動性定向，M5 CHOCH＋位移後Fresh FVG／OB首次回踩拒絕）對這檔合約最近%d天真實歷史K線回測出的訊號次數。請選出剛好%d檔最容易產生訊號的合約：\n\n", len(sorted), lookbackDays, n)
	fmt.Fprintf(&b, "%-14s %20s %12s %10s %16s %10s\n", "合約", "recent_signal_count", "價格", "24h漲跌%", "24h成交金額(USDT)", "資金費率%")
	for _, c := range sorted {
		fmt.Fprintf(&b, "%-14s %20d %12.6g %9.2f%% %16.0f %9.4f%%\n",
			c.Symbol, c.RecentSignalCount, c.Price, c.ChangePct24h, c.QuoteVolume24h, c.FundingRate*100)
	}
	fmt.Fprintf(&b, "\n只能從以上清單選，剛好選%d檔，並各給2-3句理由，以recent_signal_count為主要依據。\n", n)
	return b.String()
}
