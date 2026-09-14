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
	// Setup is the closed-H1-to-M5 HTF 3+1 pattern that triggered this
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

// GenerateSignal asks Claude to confirm or reject an HTF 3+1 setup. The rule
// engine has already found a fully-closed H1 external-liquidity sweep/reclaim,
// M5 CHOCH with ATR/volume-confirmed displacement, a fresh FVG or order block,
// its first-retest rejection, and a target large enough relative to execution
// costs. There is no time-of-day session gate. This call is the second,
// human-judgment-shaped
// opinion the rule doesn't have: given that every mechanical filter already
// passed, does the setup actually look tradeable - is the sweep a genuine
// stop-hunt or just trending through, does the broader structure support
// the reversal, and is the M5 rejection decisive rather than marginal. The response is
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
		Description: anthropic.String("Emit the trading decision for this HTF 3+1 setup. Always call this exactly once."),
		InputSchema: anthropic.ToolInputSchemaParam{
			Properties: map[string]any{
				"action": map[string]any{
					"type":        "string",
					"enum":        []string{"BUY", "SELL", "HOLD"},
					"description": "確認規則偵測到的方向進場，或判斷這組HTF 3+1設定不夠可信而選擇HOLD。規則引擎已驗證：已收線H1掃PDH/PDL或H4外部流動性並收回；掃上方只允許SELL、掃下方只允許BUY；M5 CHOCH與ATR/量能位移、Fresh FVG或OB第一次回踩拒絕及成本距離門檻皆已通過。只能跟規則方向一致或HOLD，禁止反向。",
				},
				"confidence": map[string]any{
					"type":        "number",
					"description": "0到1的信心。評估H1外部流動性掃掠與收回是否明確、M5 CHOCH突破幅度、位移K實體/ATR/量能品質、FVG/OB是否乾淨、第一次回踩拒絕是否有力、近期結構與資金費率是否配合。多項只壓線通過時信心應低。",
				},
				"entry_hint": map[string]any{
					"type":        "number",
					"description": "填規則引擎提供的固定進場價；系統會強制使用該值，不允許AI改價。禁止填0。",
				},
				"stop_loss": map[string]any{
					"type":        "number",
					"description": "填規則引擎提供的固定停損；位置在觸發用FVG/OB失效邊界外加緩衝，系統會強制使用該值。禁止填0。",
				},
				"take_profit": map[string]any{
					"type":        "number",
					"description": "填規則引擎提供的固定停利；系統強制完整1:1.5風險報酬比，不允許AI改價。禁止填0。",
				},
				"rationale": map[string]any{
					"type":        "string",
					"description": "繁體中文3-6句：(1)H1掃掠收回品質 (2)M5 CHOCH與位移品質 (3)Fresh FVG/OB第一次回踩拒絕品質 (4)近期結構與資金費率 (5)固定進場、失效停損與1:1.5停利 (6)至少一項反方風險。",
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
			if parsed.Action != models.SignalHold && parsed.Action != req.Setup.Action {
				parsed.Action = models.SignalHold
				parsed.Rationale = "AI回傳方向與規則方向不一致，系統已強制改為HOLD。" + parsed.Rationale
			}
			if parsed.Action == req.Setup.Action {
				entry, stop, target := req.Setup.Entry, req.Setup.StopLoss, req.Setup.TakeProfit
				parsed.EntryHint, parsed.StopLoss, parsed.TakeProfit = &entry, &stop, &target
			}
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

	fmt.Fprintf(&b, "規則引擎偵測到的HTF 3+1設定（機械條件皆已通過）：\n")
	setup := req.Setup
	fmt.Fprintf(&b, "方向：%s\nH1 Sweep時間：%s，掃掠極值：%.6g\nM5 CHOCH時間：%s，突破結構價：%.6g\nM5 FVG形成時間：%s，缺口：%.6g ~ %.6g，位移K實體佔比：%.0f%%，實體/ATR：%.2f倍，量能/均量：%.2f倍\n實際觸發區：%s；M5 OB body：%.6g ~ %.6g\n規則固定進場/停損/停利（完整1:1.5）：%.6g / %.6g / %.6g\n規則理由：%s\n\n",
		setup.Action, setup.SweepTs.UTC().Format("01/02 15:04"), setup.SweepPrice,
		setup.CHOCHTs.UTC().Format("01/02 15:04"), setup.CHOCHLevel,
		setup.FVGTs.UTC().Format("01/02 15:04"), setup.FVGLow, setup.FVGHigh, setup.DisplacementBodyPct*100, setup.DisplacementATR, setup.DisplacementVolume,
		setup.EntryZoneType, setup.BreakerLow, setup.BreakerHigh,
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
