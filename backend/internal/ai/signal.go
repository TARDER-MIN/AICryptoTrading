package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"

	"cryptotrading/internal/models"
	"cryptotrading/internal/strategy"
)

const emitSignalTool = "emit_trade_signal"

// PreviousSignal carries the last stored ai_signals row for this symbol into
// the next prompt, so consecutive calls reason with continuity (did the
// prior thesis play out, does it still hold) instead of each being a
// stateless one-off judgment.
type PreviousSignal struct {
	Action     models.SignalAction
	Confidence float64
	Rationale  string
	CreatedAt  time.Time
}

type SignalRequest struct {
	Symbol  string
	Candles []models.Candle // recent window, chronological, most recent last
	// Setup is the Silver Bullet sweep+FVG pattern that triggered this
	// evaluation (internal/strategy.DecideSilverBullet's output) - Action
	// here is always BUY or SELL, never HOLD (the caller only invokes AI on
	// a genuine new setup; see internal/autotrader/watcher.go).
	Setup       strategy.SBSignal
	FundingRate float64
	Position    *models.Position // nil if flat
	Previous    *PreviousSignal  // nil if this is the first signal for the symbol
}

type SignalResult struct {
	Action     models.SignalAction `json:"action"`
	Confidence float64             `json:"confidence"`
	EntryHint  *float64            `json:"entry_hint,omitempty"`
	StopLoss   *float64            `json:"stop_loss,omitempty"`
	TakeProfit *float64            `json:"take_profit,omitempty"`
	Rationale  string              `json:"rationale"`
	RawJSON    []byte              `json:"-"`
}

// GenerateSignal asks Claude to confirm or reject an ICT-2026 Silver Bullet
// setup (internal/strategy.DecideSilverBullet already mechanically found a
// liquidity sweep, a displacement-quality Fair Value Gap, an Optimal Trade
// Entry retracement, a Breaker Block confluence, and SMT divergence
// confirmation against a correlated anchor symbol - no time-of-day session
// gating, by explicit user choice, since crypto trades 24/7; see that
// package's doc comment). This call is the second, human-judgment-shaped
// opinion the rule doesn't have: given that every mechanical filter already
// passed, does the setup actually look tradeable - is the sweep a genuine
// stop-hunt or just trending through, does the broader structure support
// the reversal, is anything about the specific numbers (thin OTE overlap,
// weak SMT divergence margin) borderline rather than clean. The response is
// forced through a tool call so the result is always parseable JSON. The
// judgment framework (mandatory counter-thesis, confidence calibrated to
// how convincing the setup actually looks, required nonzero entry/stop/
// target with a minimum risk:reward) carries over from this project's
// original MA/RSI/VWAP-based prompt (itself adapted from
// github.com/tradermonty/claude-trading-skills's technical-analysis
// methodology - a Python skill framework for US-equity swing trading, not
// directly installed or called, only its transferable reasoning principles
// rewritten as this project's own prompt) - only the specific setup being
// judged has changed.
func (c *Client) GenerateSignal(ctx context.Context, req SignalRequest) (*SignalResult, error) {
	if !c.enabled {
		return nil, ErrNotConfigured
	}

	tool := anthropic.ToolParam{
		Name:        emitSignalTool,
		Description: anthropic.String("Emit the trading decision for this Silver Bullet setup. Always call this exactly once."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"BUY", "SELL", "HOLD"},
					"description": "確認規則偵測到的方向進場、或判斷這組ICT2026設定不夠可信而選擇觀望(HOLD，不進場)。規則引擎已經機械式驗證sweep+位移FVG+OTE進場+Breaker重疊+SMT背離五項條件皆成立，這裡的方向只能跟規則一致或選擇HOLD——不能反向操作。",
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "0 到 1 之間的信心程度。五項機械條件（sweep、位移FVG、OTE進場、Breaker重疊、SMT背離）都已經通過規則引擎驗證，這裡要判斷的是「通過門檻」跟「真正乾淨、值得下注」之間的差距：sweep的wick插破幅度是勉強擦過還是明顯插破、位移K棒的實體比例是壓線過關還是遠高於門檻、OTE進場點是剛好壓線觸及還是深深回撤、Breaker重疊區間是些微交集還是大面積重疊、SMT背離的幅度是微小還是明顯、資金費率是否也支持這個方向、近期趨勢結構是否配合。多項條件都是「壓線勉強過關」時信心要低，多項條件都「遠優於門檻」時可以給高信心。",
				},
				"entry_hint": map[string]any{
					"type":        "number",
					"description": "具體進場價位。可以直接採用規則算出的entry，也可以根據FVG缺口位置或近期結構微調（例如等拉回到FVG缺口中點再進場），但不可以填0或跟分析無關的數字。",
				},
				"stop_loss": map[string]any{
					"type":        "number",
					"description": "具體停損價位，同時也是這次判斷的失效價。基準是sweep的極值（wick的最高/最低點）再留一點緩衝——如果停損設在sweep極值以內，代表這根本不承認這次sweep有效。禁止填0。",
				},
				"take_profit": map[string]any{
					"type":        "number",
					"description": "具體停利價位，必須讓風險報酬比 |take_profit-entry_hint| / |entry_hint-stop_loss| 至少達到1.5倍，理想上參考FVG缺口的另一側、近期高低點、或反向流動性池位置。禁止填0。",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "繁體中文，3-6句話說明：(1)sweep+位移FVG的品質是壓線過關還是明顯優於門檻 (2)OTE進場點與Breaker重疊區間的品質——重疊範圍大不大、進場點是否落在深度回撤處 (3)SMT背離的說服力——錨定幣種的背離幅度大不大 (4)近期趨勢/結構背景是否支持這個方向 (5)entry_hint/stop_loss/take_profit怎麼定出來的、資金費率是否支持這個方向 (6)至少一項讓你不完全放心的理由（例如某項條件壓線過關、逆勢、跟上一次訊號矛盾），不能只講對這次判斷有利的一面。",
				},
			},
			Required: []string{"action", "confidence", "entry_hint", "stop_loss", "take_profit", "rationale"},
		},
	}

	prompt := buildSignalPrompt(req)

	resp, err := c.anthropic.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(c.model),
		MaxTokens: 2048,
		System: []anthropic.TextBlockParam{
			{Text: c.signalSystemPrompt},
		},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(prompt)),
		},
		Tools: []anthropic.ToolUnionParam{{OfTool: &tool}},
		ToolChoice: anthropic.ToolChoiceUnionParam{
			OfTool: &anthropic.ToolChoiceToolParam{Name: emitSignalTool},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("ai: generate signal: %w", err)
	}

	for _, block := range resp.Content {
		if tu, ok := block.AsAny().(anthropic.ToolUseBlock); ok && tu.Name == emitSignalTool {
			raw := []byte(tu.JSON.Input.Raw())
			var parsed SignalResult
			if err := json.Unmarshal(raw, &parsed); err != nil {
				return nil, fmt.Errorf("ai: parse signal response: %w", err)
			}
			parsed.Rationale = stripTrailingTagArtifacts(parsed.Rationale)
			parsed.RawJSON = raw
			return &parsed, nil
		}
	}
	return nil, fmt.Errorf("ai: model did not call %s", emitSignalTool)
}

// trailingTagArtifact matches stray tool-call-transcript-looking closing
// tags the model has, on rare occasion, appended after otherwise-coherent
// rationale text (observed in the reference project despite tool_choice
// being forced) - harmless to the JSON parse since it's inside a string
// value, but ugly once rendered in the UI, so strip it defensively.
var trailingTagArtifact = regexp.MustCompile(`(?:\s*</\w+>)+\s*$`)

func stripTrailingTagArtifacts(s string) string {
	return strings.TrimSpace(trailingTagArtifact.ReplaceAllString(s, ""))
}

func buildSignalPrompt(req SignalRequest) string {
	var b strings.Builder
	fmt.Fprintf(&b, "合約代號：%s（BingX USDS-M永續合約）\n\n", req.Symbol)

	fmt.Fprintf(&b, "規則引擎偵測到的ICT2026 Silver Bullet設定（五項機械條件皆已通過）：\n")
	setup := req.Setup
	fmt.Fprintf(&b, "方向：%s\nSweep時間：%s，Sweep極值：%.6g\n位移FVG時間：%s，缺口：%.6g ~ %.6g，位移K棒實體佔比：%.0f%%\nOTE回撤帶：%.6g ~ %.6g\nBreaker Block重疊區間：%.6g ~ %.6g\nSMT背離錨定幣種：%s（背離確認：%v）\n規則計算的進場/停損/停利：%.6g / %.6g / %.6g\n規則理由：%s\n\n",
		setup.Action, setup.SweepTs.UTC().Format("01/02 15:04"), setup.SweepPrice,
		setup.FVGTs.UTC().Format("01/02 15:04"), setup.FVGLow, setup.FVGHigh, setup.DisplacementBodyPct*100,
		setup.OTELow, setup.OTEHigh,
		setup.BreakerLow, setup.BreakerHigh,
		setup.SMTAnchorSymbol, setup.SMTConfirmed,
		setup.Entry, setup.StopLoss, setup.TakeProfit, setup.Reason)

	fmt.Fprintf(&b, "最近K線 (最舊到最新，最多顯示最後30根)：\n")
	start := 0
	if len(req.Candles) > 30 {
		start = len(req.Candles) - 30
	}
	for _, c := range req.Candles[start:] {
		fmt.Fprintf(&b, "%s O:%.6g H:%.6g L:%.6g C:%.6g V:%.4f\n",
			c.Ts.UTC().Format("01/02 15:04"), c.Open, c.High, c.Low, c.Close, c.Volume)
	}

	fmt.Fprintf(&b, "\n目前資金費率：%.4f%%（正值代表多方付費給空方，數值越高代表多方部位越擁擠）\n", req.FundingRate*100)

	if req.Position != nil {
		p := req.Position
		fmt.Fprintf(&b, "\n目前持倉：%s %.6g 顆，槓桿%dx，進場均價%.6g，標記價%.6g，未實現損益%.4f USDT\n",
			p.Side, p.Qty, p.Leverage, p.EntryPrice, p.MarkPrice, p.UnrealizedPnL)
	} else {
		fmt.Fprintf(&b, "\n目前無持倉（空手）。\n")
	}

	if req.Previous != nil {
		p := req.Previous
		fmt.Fprintf(&b, "\n上一次AI訊號（%s UTC）：%s，信心%.0f%%\n理由：%s\n請說明這次判斷跟上次比起來是延續、轉向、還是修正，並解釋原因。\n",
			p.CreatedAt.UTC().Format("01/02 15:04"), p.Action, p.Confidence*100, p.Rationale)
	} else {
		fmt.Fprintf(&b, "\n這是這個合約的第一次AI訊號，沒有前一次紀錄可比較。\n")
	}

	return b.String()
}
